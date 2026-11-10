package workcontext

import (
	"context"
	"testing"
)

// TestSessionIndexAndLastTurnProjections covers the two cross-stream reads the desktop
// list is built on: every stream's linked conversations, and the newest conversation turn
// event per stream. They exist so one list request can say "this stream has a conversation
// running / just finished" without an events read per stream, so the properties that
// matter are grouping by stream, newest-wins, archived excluded, and no cross-owner leak.
func TestSessionIndexAndLastTurnProjections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	scope := testScope()

	a := mustCreate(t, store, scope, "主线A")
	b := mustCreate(t, store, scope, "主线B")
	c := mustCreate(t, store, scope, "主线C")

	if _, err := store.LinkSession(ctx, scope, a.ID, SessionLink{SessionID: "sess-a1"}); err != nil {
		t.Fatalf("link a1: %v", err)
	}
	if _, err := store.LinkSession(ctx, scope, a.ID, SessionLink{SessionID: "sess-a2"}); err != nil {
		t.Fatalf("link a2: %v", err)
	}
	if _, err := store.LinkSession(ctx, scope, b.ID, SessionLink{SessionID: "sess-b1"}); err != nil {
		t.Fatalf("link b1: %v", err)
	}

	index, err := store.SessionIndex(ctx, scope)
	if err != nil {
		t.Fatalf("SessionIndex: %v", err)
	}
	if got := len(index[a.ID]); got != 2 {
		t.Fatalf("stream A should carry both links, got %d (%+v)", got, index[a.ID])
	}
	if got := len(index[b.ID]); got != 1 {
		t.Fatalf("stream B should carry one link, got %d", got)
	}
	if _, ok := index[c.ID]; ok {
		t.Fatalf("a stream with no links must not be keyed, got %+v", index[c.ID])
	}

	// Two turns for the same stream: the projection must return the NEWER one, because
	// the card shows current state rather than a log. Distinct dedup keys, so both rows
	// are actually written.
	if _, err := store.RecordEventIfNew(ctx, scope, a.ID, SessionTurnEventKind, "sess-a1 · completed", "session://sess-a1", "k1"); err != nil {
		t.Fatalf("record turn 1: %v", err)
	}
	if _, err := store.RecordEventIfNew(ctx, scope, a.ID, SessionTurnEventKind, "sess-a2 · error", "session://sess-a2", "k2"); err != nil {
		t.Fatalf("record turn 2: %v", err)
	}
	turns, err := store.NewestSessionTurnByContext(ctx, scope, SessionTurnEventKind)
	if err != nil {
		t.Fatalf("NewestSessionTurnByContext: %v", err)
	}
	newest, ok := turns[a.ID]
	if !ok {
		t.Fatalf("stream A should have a turn event, got %+v", turns)
	}
	if newest.Source != "session://sess-a2" {
		t.Fatalf("newest turn should win, got source %q (title %q)", newest.Source, newest.Title)
	}
	if _, ok := turns[b.ID]; ok {
		t.Fatalf("stream B never recorded a turn, it must not be keyed")
	}
	// The projection is scoped to ONE kind: a checkpoint edit must not be read as a
	// finished conversation, or every edited stream would claim its conversation moved.
	if _, err := store.RecordEventIfNew(ctx, scope, b.ID, "checkpoint_saved", "检查点 r2 已保存", "desktop", "cp-b"); err != nil {
		t.Fatalf("record checkpoint event: %v", err)
	}
	turns, err = store.NewestSessionTurnByContext(ctx, scope, SessionTurnEventKind)
	if err != nil {
		t.Fatalf("NewestSessionTurnByContext after checkpoint: %v", err)
	}
	if _, ok := turns[b.ID]; ok {
		t.Fatalf("a checkpoint event must not be reported as a conversation turn")
	}

	// Archived streams drop out of the projections: the list is active work, and a
	// read-only stream must not advertise a running conversation.
	archived := statusArchived
	if _, err := store.Patch(ctx, scope, b.ID, ContextPatch{Status: &archived, IfVersion: b.Version}); err != nil {
		t.Fatalf("archive b: %v", err)
	}
	index, err = store.SessionIndex(ctx, scope)
	if err != nil {
		t.Fatalf("SessionIndex after archive: %v", err)
	}
	if _, ok := index[b.ID]; ok {
		t.Fatalf("archived stream must not appear in the session index")
	}
	seen, err := store.LastSeenByContext(ctx, scope)
	if err != nil {
		t.Fatalf("LastSeenByContext: %v", err)
	}
	if _, ok := seen[b.ID]; ok {
		t.Fatalf("archived stream must not appear in the seen map")
	}
	// A never-marked-seen stream is present with an EMPTY watermark, which the UI reads
	// as "everything is new" rather than as an error.
	if got, ok := seen[a.ID]; !ok || got != "" {
		t.Fatalf("a never-seen stream should map to an empty watermark, got %q ok=%v", got, ok)
	}
	// Marking seen must surface the watermark, because the card's "finished" claim is
	// exactly a comparison against it.
	if _, err := store.MarkSeen(ctx, scope, a.ID); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	seen, err = store.LastSeenByContext(ctx, scope)
	if err != nil {
		t.Fatalf("LastSeenByContext after mark: %v", err)
	}
	if seen[a.ID] == "" {
		t.Fatalf("a marked-seen stream must report a non-empty watermark")
	}
}

// TestSessionProjectionsAreOwnerScoped proves the projections cannot leak another owner's
// streams: the desktop list is built entirely from them, so a leak here would render
// someone else's work in the rail.
func TestSessionProjectionsAreOwnerScoped(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mine := testScope()
	theirs := Scope{TenantID: "tenant-a", OwnerID: "owner-2"}

	mineStream := mustCreate(t, store, mine, "我的主线")
	theirStream := mustCreate(t, store, theirs, "别人的主线")
	if _, err := store.LinkSession(ctx, mine, mineStream.ID, SessionLink{SessionID: "sess-mine"}); err != nil {
		t.Fatalf("link mine: %v", err)
	}
	if _, err := store.LinkSession(ctx, theirs, theirStream.ID, SessionLink{SessionID: "sess-theirs"}); err != nil {
		t.Fatalf("link theirs: %v", err)
	}
	if _, err := store.RecordEventIfNew(ctx, theirs, theirStream.ID, SessionTurnEventKind, "别人的主线 · completed", "session://sess-theirs", "k"); err != nil {
		t.Fatalf("record their turn: %v", err)
	}

	index, err := store.SessionIndex(ctx, mine)
	if err != nil {
		t.Fatalf("SessionIndex: %v", err)
	}
	if _, ok := index[theirStream.ID]; ok {
		t.Fatalf("another owner's stream leaked into the session index: %+v", index)
	}
	if _, ok := index[mineStream.ID]; !ok {
		t.Fatalf("the caller's own stream is missing from the session index: %+v", index)
	}
	turns, err := store.NewestSessionTurnByContext(ctx, mine, SessionTurnEventKind)
	if err != nil {
		t.Fatalf("NewestSessionTurnByContext: %v", err)
	}
	if _, ok := turns[theirStream.ID]; ok {
		t.Fatalf("another owner's turn leaked into the projection: %+v", turns)
	}
	seen, err := store.LastSeenByContext(ctx, mine)
	if err != nil {
		t.Fatalf("LastSeenByContext: %v", err)
	}
	if _, ok := seen[theirStream.ID]; ok {
		t.Fatalf("another owner's stream leaked into the seen map: %+v", seen)
	}
}
