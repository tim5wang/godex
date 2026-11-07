package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/workcontext"
)

// mustOverdueTodos creates n overdue plan items in one stream and returns their ids,
// which is what forms a grouped row with addressable entries.
//
// Every item is placed strictly in the PAST: a due time at or after the service's clock
// would fall into the "due soon" window and form a second row of a different kind, which
// is a different scenario from the grouped-overdue row these tests are about.
func mustOverdueTodos(t *testing.T, service *Service, scope workcontext.Scope, contextID string, texts []string, at time.Time) []string {
	t.Helper()
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatalf("WorkContexts: %v", err)
	}
	ids := make([]string, 0, len(texts))
	for i, text := range texts {
		todo, err := store.CreateTodo(context.Background(), scope, contextID, workcontext.TodoInput{
			Text: text, DueAt: at.Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("CreateTodo %s: %v", text, err)
		}
		ids = append(ids, todo.ID)
	}
	return ids
}

// TestAttentionEntryVerdictCanDeferOneEntryOfAGroup covers the per-entry verb: a
// collapsed row's entries are individually addressable, so "ignore this one, not all
// three" is expressible. The row must SURVIVE carrying the entries that were not ruled
// on — dropping it would hide work the user never decided about.
func TestAttentionEntryVerdictCanDeferOneEntryOfAGroup(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	stream := mustStream(t, service, scope, "单条动作")
	ids := mustOverdueTodos(t, service, scope, stream.ID, []string{"过期项 A", "过期项 B", "过期项 C"}, base)

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Count != 3 {
		t.Fatalf("three overdue items should form one row of 3, got %+v", view.Items)
	}
	// Every entry carries an addressable key, because plan items have a stable id.
	if len(view.Items[0].Entries) != 3 {
		t.Fatalf("a grouped row must expose its entries, got %+v", view.Items[0].Entries)
	}
	for _, entry := range view.Items[0].Entries {
		if entry.Key == "" {
			t.Fatalf("a plan-item entry must have an action key, got %+v", entry)
		}
	}

	// Rule on ONE entry only.
	entryKey := workcontext.AttentionEntryKeyFor("todo_overdue", stream.ID, ids[1])
	if entryKey == "" {
		t.Fatal("expected an entry key for a plan item")
	}
	if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: entryKey, Verb: workcontext.AttentionVerbDismiss, Reason: "这一条已手工处理",
	}); err != nil {
		t.Fatalf("entry dismiss: %v", err)
	}

	view, err = service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox after: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("the row must survive one entry being ruled on, got %d rows", len(view.Items))
	}
	if view.Items[0].Count != 2 {
		t.Fatalf("the surviving row must report the 2 entries left, got count %d", view.Items[0].Count)
	}
	if len(view.Deferred) != 1 {
		t.Fatalf("one entry verdict must produce one deferred line, got %d", len(view.Deferred))
	}
	if view.Deferred[0].Item.ItemKey != entryKey {
		t.Fatalf("the deferred line must name the entry it came from, got %q", view.Deferred[0].Item.ItemKey)
	}
	if view.Deferred[0].Item.Title != "过期项 B" {
		t.Fatalf("the deferred line must carry that entry's own title, got %q", view.Deferred[0].Item.Title)
	}

	// The whole row goes away only when every entry has been ruled on: a row with a
	// surviving entry is still work.
	for _, id := range []string{ids[0], ids[2]} {
		if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
			ItemKey: workcontext.AttentionEntryKeyFor("todo_overdue", stream.ID, id),
			Verb:    workcontext.AttentionVerbDismiss, Reason: "也处理了",
		}); err != nil {
			t.Fatalf("entry dismiss %s: %v", id, err)
		}
	}
	view, err = service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox final: %v", err)
	}
	if len(view.Items) != 0 {
		t.Fatalf("a fully ruled-on group must leave the list, got %+v", view.Items)
	}
	if len(view.Deferred) != 3 {
		t.Fatalf("every ruled-on entry must stay reported, got %d", len(view.Deferred))
	}
}

// TestAttentionEntryVerdictBeatsGroupVerdict pins the precedence rule: the more
// specific verdict wins. Someone who singled out one entry has said more than someone
// who ruled on the row, so the narrow verdict must not be silently overridden.
func TestAttentionEntryVerdictBeatsGroupVerdict(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	stream := mustStream(t, service, scope, "优先级")
	ids := mustOverdueTodos(t, service, scope, stream.ID, []string{"条目一", "条目二"}, base)

	// The ROW is dismissed...
	if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: workcontext.AttentionItemKey("todo_overdue", stream.ID),
		Verb:    workcontext.AttentionVerbDismiss, Reason: "整组暂不处理",
	}); err != nil {
		t.Fatalf("group dismiss: %v", err)
	}
	// ...but ONE entry is explicitly claimed, which is the narrower statement.
	if _, err := service.RecordAttentionAction(context.Background(), scope, stream.ID, workcontext.AttentionActionInput{
		ItemKey: workcontext.AttentionEntryKeyFor("todo_overdue", stream.ID, ids[0]),
		Verb:    workcontext.AttentionVerbClaim,
	}); err != nil {
		t.Fatalf("entry claim: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("the claimed entry must keep the row listed, got %d rows", len(view.Items))
	}
	if view.Items[0].Title != "条目一" {
		t.Fatalf("the surviving row must be the CLAIMED entry, got %q", view.Items[0].Title)
	}
	// The other entry still follows the group verdict.
	if len(view.Deferred) != 1 || view.Deferred[0].Item.Title != "条目二" {
		t.Fatalf("the unclaimed entry must stay deferred by the group verdict, got %+v", view.Deferred)
	}
}

// TestAttentionCheckpointEntriesHaveNoPerEntryKey pins the honest limit of per-entry
// verbs: the stream's recorded blockers/open questions are free text inside the
// checkpoint, so they have no stable identity and can only be ruled on as a group. A
// title hash would look like a solution and silently detach the verdict the moment the
// text is edited.
func TestAttentionCheckpointEntriesHaveNoPerEntryKey(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	service, _, scope := newAttentionService(t, base)
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	stream := mustStream(t, service, scope, "检查点条目")
	if _, err := store.SaveCheckpoint(context.Background(), scope, stream.ID, stream.Version, workcontext.Checkpoint{
		OpenQuestions: []string{"问题一", "问题二"},
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	view, err := service.AttentionInbox(context.Background(), scope)
	if err != nil {
		t.Fatalf("AttentionInbox: %v", err)
	}
	if len(view.Items) != 1 || view.Items[0].Kind != "checkpoint_question" {
		t.Fatalf("expected one grouped question row, got %+v", view.Items)
	}
	if view.Items[0].Count != 2 {
		t.Fatalf("the two questions should be grouped, got count %d", view.Items[0].Count)
	}
	for _, entry := range view.Items[0].Entries {
		if entry.Key != "" {
			t.Fatalf("free-text checkpoint entries must NOT get an action key, got %q", entry.Key)
		}
	}
	// The GROUP is still addressable, so the row is not rendered as un-actionable.
	if view.Items[0].ItemKey == "" {
		t.Fatal("the group key must still exist for a free-text kind")
	}
}

// TestThresholdsFromStampsFollowsTheStreamsOwnPace covers the adaptive aging scale:
// the threshold is a multiple of the stream's own median stride, so a slow stream is
// not called stale on a fast stream's schedule and vice versa.
func TestThresholdsFromStampsFollowsTheStreamsOwnPace(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	// Newest first, like the store returns them.
	stamps := func(contextID string, stride time.Duration, count int) []workcontext.EventStamp {
		out := []workcontext.EventStamp{}
		for i := 0; i < count; i++ {
			out = append(out, workcontext.EventStamp{ContextID: contextID, Kind: "checkpoint", At: now.Add(-time.Duration(i) * stride)})
		}
		return out
	}

	// A stream that moves every 2 days: 2d * 3 = 6d, inside the clamp bounds.
	slow := thresholdsFromStamps(stamps("slow", 48*time.Hour, 6))
	if got := slow["slow"]; got != 6*24*time.Hour {
		t.Fatalf("a 2-day pace should age at 6 days, got %s", got)
	}
	// A stream that moves every 10 minutes: floored, so its badge is not muted before
	// the user could plausibly have seen it.
	fast := thresholdsFromStamps(stamps("fast", 10*time.Minute, 6))
	if got := fast["fast"]; got != unreadAgingFloor {
		t.Fatalf("a 10-minute pace should be floored at %s, got %s", unreadAgingFloor, got)
	}
	// A stream that moves every 10 days: capped, so a dormant stream is not treated as
	// active forever.
	dormant := thresholdsFromStamps(stamps("dormant", 10*24*time.Hour, 6))
	if got := dormant["dormant"]; got != unreadAgingCeiling {
		t.Fatalf("a 10-day pace should be capped at %s, got %s", unreadAgingCeiling, got)
	}
	// Too little history yields no threshold, so the caller falls back to the fixed one
	// rather than deriving a scale from noise.
	thin := thresholdsFromStamps(stamps("thin", 48*time.Hour, unreadAgingMinSamples))
	if _, ok := thin["thin"]; ok {
		t.Fatal("fewer than the minimum samples must not produce a threshold")
	}
}

// TestThresholdsFromStampsIgnoresTheSystemsOwnEvents is the subtle one: reminder and
// progress-probe events are written BY the attention system, so counting them as the
// stream's activity would reset the staleness that triggered the reminder — the stream
// would never age, which is exactly backwards.
func TestThresholdsFromStampsIgnoresTheSystemsOwnEvents(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	stamps := []workcontext.EventStamp{}
	// A genuinely slow stream: one human checkpoint 5 days ago...
	stamps = append(stamps, workcontext.EventStamp{ContextID: "s", Kind: "checkpoint", At: now.Add(-5 * 24 * time.Hour)})
	// ...plus the reminders the system has been writing since, which must not count.
	for i := 0; i < 10; i++ {
		stamps = append(stamps, workcontext.EventStamp{ContextID: "s", Kind: nudgeEventKind, At: now.Add(-time.Duration(i) * time.Hour)})
	}
	thresholds := thresholdsFromStamps(stamps)
	if _, ok := thresholds["s"]; ok {
		t.Fatalf("system-written events must not be read as the human's pace, got %+v", thresholds)
	}
}
