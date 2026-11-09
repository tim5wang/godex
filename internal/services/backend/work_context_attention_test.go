package backend

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// recordingNotifier captures nudges so the discipline can be asserted without a
// browser. It also counts deliveries per owner, which is what the scoping tests
// care about.
type recordingNotifier struct {
	mu     sync.Mutex
	owners []string
	bodies []string
}

func (n *recordingNotifier) NotifyOwner(_ context.Context, owner, _, body string) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.owners = append(n.owners, owner)
	n.bodies = append(n.bodies, body)
	return 1, nil
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.bodies)
}

func (n *recordingNotifier) allBodies() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.bodies, "\n")
}

// newAttentionService builds a service with a fixed clock outside quiet hours, so
// tests are not time-of-day dependent.
func newAttentionService(t *testing.T, now time.Time) (*Service, *recordingNotifier, workcontext.Scope) {
	t.Helper()
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	service.now = fixedNow(now)
	notifier := &recordingNotifier{}
	service.SetNudgeNotifier(notifier)
	t.Cleanup(func() { service.CloseWorkContexts() })
	return service, notifier, workcontext.Scope{OwnerID: "local-operator"}
}

// mustStream creates a stream owned by scope.
func mustStream(t *testing.T, service *Service, scope workcontext.Scope, title string) workcontext.Context {
	t.Helper()
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatalf("WorkContexts: %v", err)
	}
	stream, err := store.Create(context.Background(), scope, workcontext.ContextInput{Title: title, Goal: "goal"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return stream
}

// TestAttentionSweepRecordsProgressWithoutOpeningTheDesktop is the core
// regression for "progress only existed once you visited the desktop": the sweep
// must record a card's transition on its own, and the recorded event must be the
// one the desktop read path would have written (same dedup key), so reading
// afterwards does not duplicate it.
func TestAttentionSweepRecordsProgressWithoutOpeningTheDesktop(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, notifier, scope := newAttentionService(t, base)
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	stream := mustStream(t, service, scope, "并行主线之一")
	ledger := service.TaskboardLedger()
	project := ledger.ListProjects()[0]

	delegated, err := service.DelegateWork(context.Background(), scope, stream.ID, WorkDelegationInput{
		RequestID: "req-sweep", Title: "完成一件委派", ProjectID: project.ID,
		Acceptance: []string{"产物可验证"},
	})
	if err != nil {
		t.Fatalf("DelegateWork: %v", err)
	}

	// Move the card to in_review: this is the "it has progress, and now it needs
	// me" transition.
	card, err := ledger.MoveCard(delegated.Card.ID, delegated.Card.Version, taskboard.StatusInProgress, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MoveCard(card.ID, card.Version, taskboard.StatusInReview, "human"); err != nil {
		t.Fatal(err)
	}

	// Nobody has read the desktop. The sweep alone must record it.
	service.sweepAttentionOnce(context.Background())
	detail, err := store.Get(context.Background(), scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEventKind(detail.Events, "task_status") {
		t.Fatalf("sweep did not record card progress; events=%+v", detail.Events)
	}
	// in_review is decision-class, so it also pushes.
	if notifier.count() == 0 {
		t.Fatal("a card entering in_review should notify the owner")
	}
	if !strings.Contains(notifier.allBodies(), "等待验收") {
		t.Fatalf("nudge should say acceptance is pending, got %q", notifier.allBodies())
	}

	// Reading the desktop afterwards must NOT add a second copy: the read path and
	// the sweep share the dedup key.
	before := len(detail.Events)
	if _, err := service.WorkContextDetail(context.Background(), scope, stream.ID); err != nil {
		t.Fatal(err)
	}
	after, err := store.Get(context.Background(), scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != before {
		t.Fatalf("reading the desktop duplicated the sweep's event: %d -> %d", before, len(after.Events))
	}
}

// TestAttentionSweepDoesNotRenotify is the dedup guarantee: a sweep every minute
// must not become a nudge every minute.
func TestAttentionSweepDoesNotRenotify(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, notifier, scope := newAttentionService(t, base)
	stream := mustStream(t, service, scope, "重复扫描")
	if _, err := service.WorkContexts(); err != nil {
		t.Fatal(err)
	}
	store, _ := service.WorkContexts()
	// A plan item due in the past is the simplest nudge source.
	past := base.Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{
		Text: "早已过期的计划", DueAt: past,
	}); err != nil {
		t.Fatal(err)
	}

	service.sweepAttentionOnce(context.Background())
	first := notifier.count()
	if first == 0 {
		t.Fatal("an overdue plan item should nudge once")
	}
	for i := 0; i < 3; i++ {
		service.sweepAttentionOnce(context.Background())
	}
	if got := notifier.count(); got != first {
		t.Fatalf("repeated sweeps re-notified: %d -> %d", first, got)
	}
}

// TestPushDisciplineQuietHoursAndCap pins the noise bounds. Without them six
// parallel streams become six naggers, which is the failure mode this whole
// discipline exists to prevent.
func TestPushDisciplineQuietHoursAndCap(t *testing.T) {
	// Quiet hours: 02:00 local is inside the 22:00-08:00 window.
	if !inQuietHours(time.Date(2026, 10, 31, 2, 0, 0, 0, time.Local)) {
		t.Fatal("02:00 should be quiet hours")
	}
	if inQuietHours(time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)) {
		t.Fatal("12:00 should not be quiet hours")
	}
	// The window wraps midnight, so both ends must be considered.
	if !inQuietHours(time.Date(2026, 10, 31, 23, 30, 0, 0, time.Local)) {
		t.Fatal("23:30 should be quiet hours")
	}

	base := time.Date(2026, 10, 31, 2, 0, 0, 0, time.Local) // inside quiet hours
	service, notifier, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "安静时段")

	// More overdue items than the daily cap.
	for i := 0; i < attentionDailyCap+5; i++ {
		if _, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{
			Text: "过期项", DueAt: base.Add(-time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	service.sweepAttentionOnce(context.Background())
	if notifier.count() != 0 {
		t.Fatalf("quiet hours must suppress pushes, got %d", notifier.count())
	}

	// A quiet-hours nudge must NOT be burnt: moving to daytime should deliver it.
	service.now = fixedNow(time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local))
	service.sweepAttentionOnce(context.Background())
	got := notifier.count()
	if got == 0 {
		t.Fatal("a nudge suppressed by quiet hours must still be delivered later")
	}
	if got > attentionDailyCap {
		t.Fatalf("daily cap exceeded: %d > %d", got, attentionDailyCap)
	}
}

// TestAttentionInboxSurfacesDecisionsAndDues checks the cross-stream list, which
// is what lets six streams be triaged without opening each one.
func TestAttentionInboxSurfacesDecisionsAndDues(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()

	withCard := mustStream(t, service, scope, "有待验收委派")
	plain := mustStream(t, service, scope, "只有计划项")
	ledger := service.TaskboardLedger()
	project := ledger.ListProjects()[0]
	delegated, err := service.DelegateWork(context.Background(), scope, withCard.ID, WorkDelegationInput{
		RequestID: "req-inbox", Title: "等待验收的任务", ProjectID: project.ID,
		Acceptance: []string{"产物可验证"},
	})
	if err != nil {
		t.Fatal(err)
	}
	card, err := ledger.MoveCard(delegated.Card.ID, delegated.Card.Version, taskboard.StatusInProgress, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MoveCard(card.ID, card.Version, taskboard.StatusInReview, "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTodo(context.Background(), scope, plain.ID, workcontext.TodoInput{
		Text: "过期的计划项", DueAt: base.Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("expected a review + an overdue item, got %+v", view.Items)
	}
	// A decision outranks a deadline: it is the entry that blocks someone else.
	if view.Items[0].Kind != "delegation_review" || view.Items[0].Priority != AttentionHigh {
		t.Fatalf("decision should sort first and be high priority: %+v", view.Items[0])
	}
	if view.Items[0].Tab != "tasks" || view.Items[0].ContextID != withCard.ID {
		t.Fatalf("item must route to where the work is done: %+v", view.Items[0])
	}
	if view.Items[1].Kind != "todo_overdue" || view.Items[1].Tab != "plan" {
		t.Fatalf("unexpected second item: %+v", view.Items[1])
	}
	if view.Counts["delegation_review"] != 1 || view.Counts["todo_overdue"] != 1 {
		t.Fatalf("counts should summarise the list: %+v", view.Counts)
	}
}

// TestUnreadCountsArePerStreamAndRespectSeeWatermark covers the badge source for
// the desktop wall.
func TestUnreadCountsArePerStreamAndRespectSeeWatermark(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "未读计数")

	// Creation itself is an event, so a never-seen stream starts non-zero.
	counts, err := store.UnreadCounts(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if counts[stream.ID] == 0 {
		t.Fatal("a never-seen stream with events should report unread")
	}

	if _, err := store.MarkSeen(context.Background(), scope, stream.ID); err != nil {
		t.Fatal(err)
	}
	counts, err = store.UnreadCounts(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if counts[stream.ID] != 0 {
		t.Fatalf("marking seen should zero the badge, got %d", counts[stream.ID])
	}

	if _, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{Text: "新计划"}); err != nil {
		t.Fatal(err)
	}
	counts, err = store.UnreadCounts(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if counts[stream.ID] == 0 {
		t.Fatal("a change after seeing should raise the badge again")
	}
}

// TestAttentionSweepNeverCrossesOwners is the scoping guarantee on the delivery
// side: two owners' subscriptions must not see each other's work, even though
// both are swept by the same background pass.
func TestAttentionSweepNeverCrossesOwners(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, notifier, alice := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	bob := workcontext.Scope{OwnerID: "bob"}

	aliceStream := mustStream(t, service, alice, "Alice 的主线")
	bobStream := mustStream(t, service, bob, "Bob 的主线")
	past := base.Add(-time.Hour).Format(time.RFC3339)
	if _, err := store.CreateTodo(context.Background(), alice, aliceStream.ID, workcontext.TodoInput{Text: "Alice 的过期项", DueAt: past}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTodo(context.Background(), bob, bobStream.ID, workcontext.TodoInput{Text: "Bob 的过期项", DueAt: past}); err != nil {
		t.Fatal(err)
	}

	service.sweepAttentionOnce(context.Background())

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	wantAlice := workcontext.ScopeKey(alice)
	wantBob := workcontext.ScopeKey(bob)
	if len(notifier.owners) == 0 {
		t.Fatal("expected nudges for both owners")
	}
	for i, owner := range notifier.owners {
		if owner != wantAlice && owner != wantBob {
			t.Fatalf("nudge %d addressed an unknown scope %q", i, owner)
		}
		if strings.Contains(notifier.bodies[i], "Alice") && owner != wantAlice {
			t.Fatalf("Alice's work was delivered to %q", owner)
		}
		if strings.Contains(notifier.bodies[i], "Bob") && owner != wantBob {
			t.Fatalf("Bob's work was delivered to %q", owner)
		}
	}
}

func hasEventKind(events []workcontext.Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

// mustOverdueTodo creates one overdue plan item, which is what the sweep turns into
// a nudge. Overdue (rather than due-soon) is used because it exercises the same
// path with a clearer dedup key.
func mustOverdueTodo(t *testing.T, service *Service, scope workcontext.Scope, contextID, text string, at time.Time) {
	t.Helper()
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatalf("WorkContexts: %v", err)
	}
	if _, err := store.CreateTodo(context.Background(), scope, contextID, workcontext.TodoInput{
		Text: text, DueAt: at.Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
}

// TestAttentionSnoozeSuppressesBeforeTheEventIsRecorded is the ordering test that
// matters most: a snoozed reminder must not be recorded as delivered, because the
// event's dedup key would then suppress it FOREVER — the reminder would be silently
// swallowed rather than delivered later. It is the attention equivalent of the
// quiet-hours ordering pin.
func TestAttentionSnoozeSuppressesBeforeTheEventIsRecorded(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, notifier, scope := newAttentionService(t, base)
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	stream := mustStream(t, service, scope, "被推迟的主线")
	mustOverdueTodo(t, service, scope, stream.ID, "过期项", base)

	// The owner snoozes the overdue row from the inbox.
	key := workcontext.AttentionItemKey(AttentionKindTodoOverdue, stream.ID)
	if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: key, Verb: workcontext.AttentionVerbSnooze,
		UntilAt: base.Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	// A sweep inside the snooze window must deliver NOTHING.
	service.sweepAttentionOnce(context.Background())
	if notifier.count() != 0 {
		t.Fatalf("a snoozed row must not notify, got %q", notifier.allBodies())
	}
	// And crucially it must not have written the nudge event either: recording it
	// would occupy the dedup key and swallow the reminder permanently.
	detail, err := store.Get(context.Background(), scope, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasEventKind(detail.Events, nudgeEventKind) {
		t.Fatal("a snoozed nudge must NOT be recorded as delivered (its dedup key would swallow it forever)")
	}

	// Once the snooze expires, the reminder is delivered — nothing was lost.
	service.now = fixedNow(base.Add(49 * time.Hour))
	service.sweepAttentionOnce(context.Background())
	if notifier.count() != 1 {
		t.Fatalf("after the snooze expires the reminder must be delivered, got %d", notifier.count())
	}
}

// TestAttentionSnoozeExpiresOnReadWithoutASweep pins that expiry is decided while
// READING: no scheduled job has to fire, so a snooze cannot be missed because a
// background pass did not run.
func TestAttentionSnoozeExpiresOnReadWithoutASweep(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	if _, err := service.WorkContexts(); err != nil {
		t.Fatal(err)
	}
	stream := mustStream(t, service, scope, "读时到期")
	mustOverdueTodo(t, service, scope, stream.ID, "过期项", base)
	key := workcontext.AttentionItemKey(AttentionKindTodoOverdue, stream.ID)
	if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: key, Verb: workcontext.AttentionVerbSnooze,
		UntilAt: base.Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Deferred) != 1 || len(view.Items) != 0 {
		t.Fatalf("inside the window the row is deferred: items=%d deferred=%d", len(view.Items), len(view.Deferred))
	}
	if view.Deferred[0].Action.Verb != workcontext.AttentionVerbSnooze {
		t.Fatalf("the deferred row must carry its verb, got %+v", view.Deferred[0].Action)
	}

	// Only the clock moves. No sweep runs.
	service.now = fixedNow(base.Add(25 * time.Hour))
	view, err = service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox after expiry: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("the row must reappear once due, got %d items", len(view.Items))
	}
	if len(view.Deferred) != 0 {
		t.Fatalf("an expired snooze must not still be reported as deferred, got %d", len(view.Deferred))
	}
	// The row carries its group-scoped identity, so the UI acts on exactly what it renders.
	if view.Items[0].ItemKey != key {
		t.Fatalf("the row must carry its item key %q, got %q", key, view.Items[0].ItemKey)
	}
}

// TestAttentionClaimKeepsTheRowAndDismissHidesIt covers the two other verbs: a claim
// is still work and must stay on the list (carrying its state), while a dismiss
// takes the row off the list WITHOUT losing it.
func TestAttentionClaimKeepsTheRowAndDismissHidesIt(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	if _, err := service.WorkContexts(); err != nil {
		t.Fatal(err)
	}
	claimed := mustStream(t, service, scope, "我接手了")
	dismissed := mustStream(t, service, scope, "我不做了")
	mustOverdueTodo(t, service, scope, claimed.ID, "过期项 A", base)
	mustOverdueTodo(t, service, scope, dismissed.ID, "过期项 B", base)

	claimKey := workcontext.AttentionItemKey(AttentionKindTodoOverdue, claimed.ID)
	if _, err := service.RecordAttentionAction(context.Background(), scope, claimed.ID, workcontext.AttentionActionInput{
		ItemKey: claimKey, Verb: workcontext.AttentionVerbClaim, ActorID: "local-operator",
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	dismissKey := workcontext.AttentionItemKey(AttentionKindTodoOverdue, dismissed.ID)
	if _, err := service.RecordAttentionAction(context.Background(), scope, dismissed.ID, workcontext.AttentionActionInput{
		ItemKey: dismissKey, Verb: workcontext.AttentionVerbDismiss, Reason: "已手工处理",
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	// A claim stays visible: it is the opposite of deferral.
	var claimedItem *AttentionItem
	for i := range view.Items {
		if view.Items[i].ContextID == claimed.ID {
			claimedItem = &view.Items[i]
		}
		if view.Items[i].ContextID == dismissed.ID {
			t.Fatal("a dismissed row must not remain on the working list")
		}
	}
	if claimedItem == nil {
		t.Fatal("a claimed row must stay on the working list")
	}
	if claimedItem.Action == nil || claimedItem.Action.Verb != workcontext.AttentionVerbClaim {
		t.Fatalf("the claimed row must carry its state, got %+v", claimedItem.Action)
	}
	// The dismissal is reported rather than dropped, with the reason that makes it undoable.
	if len(view.Deferred) != 1 {
		t.Fatalf("the dismissed row must be reported as deferred, got %d", len(view.Deferred))
	}
	if view.Deferred[0].Item.ContextID != dismissed.ID || view.Deferred[0].Action.Reason != "已手工处理" {
		t.Fatalf("deferred row lost its identity or reason: %+v", view.Deferred[0])
	}
}

// TestAttentionActionCountsAsADecisionNotAsUnread is the end-to-end version of the
// probe that ruled out event-derivation: ruling on a row must not raise that
// stream's unread count, and it must show up as a human decision in the ledger.
func TestAttentionActionCountsAsADecisionNotAsUnread(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	stream := mustStream(t, service, scope, "不计未读")
	mustOverdueTodo(t, service, scope, stream.ID, "过期项", base)
	if _, err := store.MarkSeen(context.Background(), scope, stream.ID); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	before, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}

	key := workcontext.AttentionItemKey(AttentionKindTodoOverdue, stream.ID)
	if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: key, Verb: workcontext.AttentionVerbDismiss, Reason: "已处理",
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	after, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox after: %v", err)
	}
	// Ruling on a row is not a change to the stream, so the badge must not move.
	if after.Unread[stream.ID] != before.Unread[stream.ID] {
		t.Fatalf("acting on a row must not raise unread: before=%d after=%d",
			before.Unread[stream.ID], after.Unread[stream.ID])
	}
	if after.Unread[stream.ID] != 0 {
		t.Fatalf("a just-seen stream should stay at zero unread, got %d", after.Unread[stream.ID])
	}
	// ...but it IS a human decision, which is what the yield ratio divides by.
	if after.Metrics.ActionsToday != before.Metrics.ActionsToday+1 {
		t.Fatalf("the verb must count as one decision: before=%d after=%d",
			before.Metrics.ActionsToday, after.Metrics.ActionsToday)
	}
}

// TestAttentionInboxAgesStaleUnread covers aging: an unread badge that nobody has
// cleared for days stops being a call to action, so the inbox demotes it to a muted
// summary. It is NOT hidden — the count stays real and the events stay readable;
// only the demand for action goes away.
func TestAttentionInboxAgesStaleUnread(t *testing.T) {
	// Real "now", because events are stamped by the wall clock while the inbox reads
	// s.now(): only a service clock set relative to real time can age a fresh event.
	base := time.Now()
	service, _, scope := newAttentionService(t, base)
	mustStream(t, service, scope, "刚有变动")

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if view.Metrics.UnreadTotal == 0 {
		t.Fatal("a never-seen stream should report unread changes")
	}
	// A fresh change is not aged: the marker must still be a call to action.
	if len(view.Aged) != 0 {
		t.Fatalf("a just-written change must not be aged, got %+v", view.Aged)
	}

	// Move the service clock past the aging window. The events themselves are
	// unchanged, which is exactly the situation aging describes.
	service.now = fixedNow(base.Add(unreadAgingAfter + time.Hour))
	view, err = service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox after aging: %v", err)
	}
	if len(view.Aged) != 1 {
		t.Fatalf("the stream should be reported as aged, got %+v", view.Aged)
	}
	if view.Metrics.AgedStreams != 1 {
		t.Fatalf("the ledger should count one aged stream, got %d", view.Metrics.AgedStreams)
	}
	// Aged is a DEMOTION, not a deletion: the count must survive.
	if view.Metrics.UnreadTotal == 0 {
		t.Fatal("aging must not erase the unread count")
	}
}

// TestAttentionInboxNeverAgesAStreamWithNothingUnread is the other half of the
// rule: a quiet stream must not acquire a permanent grey marker.
func TestAttentionInboxNeverAgesAStreamWithNothingUnread(t *testing.T) {
	base := time.Now()
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "已读且安静")
	if _, err := store.MarkSeen(context.Background(), scope, stream.ID); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	service.now = fixedNow(base.Add(unreadAgingAfter * 10))
	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Aged) != 0 {
		t.Fatalf("a stream with nothing unread must never be aged, got %+v", view.Aged)
	}
}

// TestAttentionMetricsReportSpendAgainstCap pins the ledger figures. A bare count
// with no scale invites the wrong conclusion, so the caps ship with the numbers.
func TestAttentionMetricsReportSpendAgainstCap(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, notifier, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "度量")
	if _, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{
		Text: "过期项", DueAt: base.Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	service.sweepAttentionOnce(context.Background())
	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if view.Metrics.NudgesToday != notifier.count() {
		t.Fatalf("ledger nudges = %d, deliveries = %d", view.Metrics.NudgesToday, notifier.count())
	}
	if view.Metrics.OwnerCap != attentionDailyCap || view.Metrics.PerStreamCap != attentionPerStreamDailyCap {
		t.Fatalf("ledger must report the caps it is measured against, got %+v", view.Metrics)
	}
}

// TestPushPerStreamCapBoundsOneBusyStream is why a per-stream share exists at all:
// with only an owner-level budget, whichever stream happens to have the most due
// items eats it and the other streams never speak.
func TestPushPerStreamCapBoundsOneBusyStream(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, notifier, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	busy := mustStream(t, service, scope, "忙碌主线")
	quiet := mustStream(t, service, scope, "安静主线")

	past := base.Add(-time.Hour).Format(time.RFC3339)
	// Twice the per-stream share, in ONE stream.
	for i := 0; i < attentionPerStreamDailyCap*2; i++ {
		if _, err := store.CreateTodo(context.Background(), scope, busy.ID, workcontext.TodoInput{
			Text: "忙碌过期项", DueAt: past,
		}); err != nil {
			t.Fatalf("CreateTodo: %v", err)
		}
	}
	if _, err := store.CreateTodo(context.Background(), scope, quiet.ID, workcontext.TodoInput{
		Text: "安静过期项", DueAt: past,
	}); err != nil {
		t.Fatalf("CreateTodo quiet: %v", err)
	}

	service.sweepAttentionOnce(context.Background())
	// The busy stream is capped at its share, not at the owner's whole budget.
	if got := notifier.count(); got != attentionPerStreamDailyCap+1 {
		t.Fatalf("expected the per-stream share then the other stream, got %d deliveries", got)
	}
	// The quiet stream still got through, which is the fairness this cap buys.
	if !strings.Contains(notifier.allBodies(), "安静过期项") {
		t.Fatalf("the per-stream cap must leave room for other streams, got %q", notifier.allBodies())
	}
}

// TestAttentionInboxSurfacesFactsBlockersAndQuestions pins the coverage that made
// the inbox worth opening: the signals that used to live only inside one stream's
// boards (a fact awaiting review, a blocked plan item, the stream's own recorded
// blockers and open questions) must reach the single triage list, each routing to
// the tab that resolves it.
func TestAttentionInboxSurfacesFactsBlockersAndQuestions(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "多源信号")

	if _, err := store.CreateFact(context.Background(), scope, stream.ID, workcontext.FactInput{
		Title: "待审事实", Content: "内容", Scope: `{"service":"api"}`,
	}); err != nil {
		t.Fatalf("CreateFact: %v", err)
	}
	// A blocked item with NO due date: exactly the case the deadline query cannot
	// see, and the one a person most needs told about.
	if _, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{
		Text: "受阻计划", Status: "blocked",
	}); err != nil {
		t.Fatalf("CreateTodo blocked: %v", err)
	}
	if _, err := store.SaveCheckpoint(context.Background(), scope, stream.ID, stream.Version, workcontext.Checkpoint{
		Blockers: []string{"缺凭据"}, OpenQuestions: []string{"走哪条链路"},
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	tabs := map[string]string{}
	for _, item := range view.Items {
		tabs[item.Kind] = item.Tab
	}
	for kind, wantTab := range map[string]string{
		"fact_review":         "knowledge",
		"todo_blocked":        "plan",
		"checkpoint_blocker":  "overview",
		"checkpoint_question": "overview",
	} {
		got, ok := tabs[kind]
		if !ok {
			t.Fatalf("inbox is missing kind %q: %+v", kind, view.Items)
		}
		if got != wantTab {
			t.Fatalf("kind %q routes to tab %q, want %q", kind, got, wantTab)
		}
	}
}

// TestAttentionInboxGroupsRepeatedNoise is the "attention cost tracks rows, not
// facts" guarantee: several same-kind entries from one stream collapse into ONE
// row that says how many, still readable without opening the stream.
func TestAttentionInboxGroupsRepeatedNoise(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "重复噪音")

	first := base.Add(-5 * time.Hour)
	for i, offset := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		texts := []string{"过期项 A", "过期项 B", "过期项 C"}
		if _, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{
			Text: texts[i], DueAt: first.Add(offset).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("CreateTodo: %v", err)
		}
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("three overdue items in one stream must collapse to one row, got %d: %+v", len(view.Items), view.Items)
	}
	row := view.Items[0]
	if row.Kind != "todo_overdue" || row.Count != 3 {
		t.Fatalf("grouped row = %+v, want kind todo_overdue with count 3", row)
	}
	if len(row.Also) != 2 {
		t.Fatalf("grouped row should spell out the collapsed titles, got %+v", row.Also)
	}
	// The row must carry the most urgent due time of the group, not an arbitrary one.
	if parsed, ok := workcontext.ParseDueAt(row.DueAt); !ok || !parsed.Equal(first) {
		t.Fatalf("grouped row due = %q, want the earliest %s", row.DueAt, first.Format(time.RFC3339))
	}
	// The count is what the user reads, so it must not be inflated by blank entries.
	if view.Counts["todo_overdue"] != 1 {
		t.Fatalf("counts should count rows, got %+v", view.Counts)
	}
}

// TestAttentionInboxRanksLeverageAboveDueTime covers the leverage dimension: a
// stream other work is blocked on outranks an equally urgent stream nobody
// depends on. The dependent stream's item is deliberately MORE overdue, so only
// leverage can explain the order.
func TestAttentionInboxRanksLeverageAboveDueTime(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	upstream := mustStream(t, service, scope, "被依赖的主线")
	downstream := mustStream(t, service, scope, "依赖方")

	if _, err := store.CreateTodo(context.Background(), scope, upstream.ID, workcontext.TodoInput{
		Text: "上游过期", DueAt: base.Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("CreateTodo upstream: %v", err)
	}
	if _, err := store.CreateTodo(context.Background(), scope, downstream.ID, workcontext.TodoInput{
		Text: "下游过期", DueAt: base.Add(-5 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("CreateTodo downstream: %v", err)
	}
	// downstream depends_on upstream, so upstream is the one others wait for.
	if _, err := store.AddRelation(context.Background(), scope, downstream.ID, workcontext.RelationInput{
		ToID: upstream.ID, Kind: "depends_on", Condition: "上游不完成则无法开工",
	}); err != nil {
		t.Fatalf("AddRelation: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("expected two items, got %+v", view.Items)
	}
	if view.Items[0].ContextID != upstream.ID {
		t.Fatalf("the stream others depend on should sort first, got %+v", view.Items)
	}
	if view.Items[0].BlockedCount != 1 || view.Items[1].BlockedCount != 0 {
		t.Fatalf("leverage counts wrong: %+v", view.Items)
	}
}

// TestAttentionInboxReportsLostDelegationCard: a delegation whose card has
// vanished is an unresolvable link, which is itself something the user must
// decide about, so it appears as its own state instead of silently disappearing.
func TestAttentionInboxReportsLostDelegationCard(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	stream := mustStream(t, service, scope, "委派丢失")
	ledger := service.TaskboardLedger()
	project := ledger.ListProjects()[0]
	delegated, err := service.DelegateWork(context.Background(), scope, stream.ID, WorkDelegationInput{
		RequestID: "req-lost", Title: "会被删掉的卡", ProjectID: project.ID,
		Acceptance: []string{"产物可验证"},
	})
	if err != nil {
		t.Fatalf("DelegateWork: %v", err)
	}
	if _, err := ledger.SoftDeleteCard(delegated.Card.ID, delegated.Card.Version, "human"); err != nil {
		t.Fatalf("SoftDeleteCard: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Kind != "delegation_missing" {
		t.Fatalf("a lost card should surface as delegation_missing, got %+v", view.Items)
	}
	if view.Items[0].Tab != "delegations" {
		t.Fatalf("a lost card routes to the delegations board, got %q", view.Items[0].Tab)
	}
}

// TestAttentionInboxNeverCrossesOwnersOnNewSignals extends the isolation guarantee
// to the newly collected signals. Facts, blocked items and checkpoint text are all
// owner data, so a second owner's inbox must not see any of it.
func TestAttentionInboxNeverCrossesOwnersOnNewSignals(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, alice := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	bob := workcontext.Scope{OwnerID: "bob"}

	aliceStream := mustStream(t, service, alice, "Alice 的主线")
	bobStream := mustStream(t, service, bob, "Bob 的主线")
	if _, err := store.CreateFact(context.Background(), alice, aliceStream.ID, workcontext.FactInput{
		Title: "Alice 的待审事实", Content: "内容", Scope: `{"service":"api"}`,
	}); err != nil {
		t.Fatalf("CreateFact alice: %v", err)
	}
	if _, err := store.CreateFact(context.Background(), bob, bobStream.ID, workcontext.FactInput{
		Title: "Bob 的待审事实", Content: "内容", Scope: `{"service":"api"}`,
	}); err != nil {
		t.Fatalf("CreateFact bob: %v", err)
	}
	if _, err := store.CreateTodo(context.Background(), alice, aliceStream.ID, workcontext.TodoInput{
		Text: "Alice 的受阻项", Status: "blocked",
	}); err != nil {
		t.Fatalf("CreateTodo alice: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), alice)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("Alice should see exactly her own two items, got %+v", view.Items)
	}
	for _, item := range view.Items {
		if item.ContextID != aliceStream.ID {
			t.Fatalf("Alice's inbox leaked another owner's stream: %+v", item)
		}
	}
}

// TestAttentionMetricsCarryTheYieldNumerator pins the last piece of the ratio: the ledger
// must report finished work, not just decisions. The denominator alone is half a ratio, and
// "5 decisions" reads very differently depending on whether 5 items also got finished.
func TestAttentionMetricsCarryTheYieldNumerator(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, _ := service.WorkContexts()
	stream := mustStream(t, service, scope, "收益率")

	item, err := store.CreateTodo(context.Background(), scope, stream.ID, workcontext.TodoInput{Text: "完成它"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	before, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if before.Metrics.CompletedToday != 0 {
		t.Fatalf("an open plan item must not count as finished, got %d", before.Metrics.CompletedToday)
	}

	done := "done"
	if _, err := store.PatchTodo(context.Background(), scope, stream.ID, item.ID, workcontext.TodoPatch{Status: &done}); err != nil {
		t.Fatalf("PatchTodo: %v", err)
	}
	after, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if after.Metrics.CompletedToday != 1 {
		t.Fatalf("the ledger should report one finished item, got %d", after.Metrics.CompletedToday)
	}

	// Finishing work is NOT a decision, so the denominator must not move. This is what
	// keeps the ratio honest: the numerator is work done BY the agent, the denominator is
	// attention spent BY the human, and collapsing the two would make the ratio always 1.
	if after.Metrics.ActionsToday != before.Metrics.ActionsToday {
		t.Fatalf("finishing work is not a decision; actions changed %d -> %d",
			before.Metrics.ActionsToday, after.Metrics.ActionsToday)
	}
}
