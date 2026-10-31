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
