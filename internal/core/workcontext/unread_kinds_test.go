package workcontext

import (
	"context"
	"testing"
)

// TestUnreadCountsCountsEveryEventKind pins the behavior that AE2's storage choice
// depends on, and it is deliberately written as a CHARACTERIZATION test: it asserts
// what the code does today, so that changing it forces a deliberate revisit of
// docs/work-context-attention-economics.md §7 rather than a silent change of meaning.
//
// UnreadCounts and Focus.changes are both KIND-AGNOSTIC: they filter on the seen
// watermark only. That is why "store the verb state as an event" was rejected for the
// attention inbox — an action event would be counted as unread work and would appear
// in the stream's own "what changed" list, i.e. the user's own dismissal would be
// reported back to them as a change needing attention.
func TestUnreadCountsCountsEveryEventKind(t *testing.T) {
	store := newTestStore(t)
	scope := testScope()
	ctx := context.Background()
	stream := mustCreate(t, store, scope, "计数语义")

	if _, err := store.MarkSeen(ctx, scope, stream.ID); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	before, err := store.UnreadCounts(ctx, scope)
	if err != nil {
		t.Fatalf("UnreadCounts before: %v", err)
	}
	if before[stream.ID] != 0 {
		t.Fatalf("a just-seen stream must start at zero unread, got %d", before[stream.ID])
	}

	// A non-content kind, exactly what an action record would look like.
	if _, err := store.RecordEventIfNew(ctx, scope, stream.ID, "attention_action",
		"已忽略: 某条提醒", "attention-action", "action:dismiss:probe"); err != nil {
		t.Fatalf("RecordEventIfNew: %v", err)
	}

	after, err := store.UnreadCounts(ctx, scope)
	if err != nil {
		t.Fatalf("UnreadCounts after: %v", err)
	}
	if after[stream.ID] != before[stream.ID]+1 {
		t.Fatalf("unread counting is expected to be kind-agnostic: before=%d after=%d",
			before[stream.ID], after[stream.ID])
	}

	focus, err := store.Focus(ctx, scope, stream.ID)
	if err != nil {
		t.Fatalf("Focus: %v", err)
	}
	found := false
	for _, event := range focus.Changes {
		if event.Kind == "attention_action" {
			found = true
		}
	}
	if !found {
		t.Fatal("Focus.changes is expected to be kind-agnostic as well")
	}
}
