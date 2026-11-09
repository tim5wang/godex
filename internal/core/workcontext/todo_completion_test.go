package workcontext

import (
	"context"
	"testing"
	"time"
)

// TestTodoCompletedAtIsStampedOnceOnTheDoneTransition pins the numerator's exactness.
//
// The attention-yield numerator ("work finished today") is what a self-reported metric
// could most easily flatter: if it were derived from updated_at, then any later edit of an
// already-done item would count that item as finished AGAIN, and the user's own yield would
// climb every time they touched a finished line. So the stamp must be written exactly once
// per transition into done, and must not be rewritten by unrelated edits.
func TestTodoCompletedAtIsStampedOnceOnTheDoneTransition(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	stream := mustCreate(t, store, testScope(), "完成时间")

	item, err := store.CreateTodo(ctx, testScope(), stream.ID, TodoInput{Text: "ship it"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	if item.CompletedAt != "" {
		t.Fatalf("a new todo must not carry a completion time, got %q", item.CompletedAt)
	}

	// Not done yet: "today's finished work" must stay 0 even though the item exists.
	if got := mustCompletedSince(t, store, time.Now().Add(-24*time.Hour)); got != 0 {
		t.Fatalf("an open item must not count as finished, got %d", got)
	}

	done, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Status: ptr("done")})
	if err != nil {
		t.Fatalf("PatchTodo done: %v", err)
	}
	if done.CompletedAt == "" {
		t.Fatal("the transition into done must stamp completed_at")
	}
	firstStamp := done.CompletedAt
	if got := mustCompletedSince(t, store, time.Now().Add(-24*time.Hour)); got != 1 {
		t.Fatalf("a finished item should count once, got %d", got)
	}

	// Touching the finished item again (its text, say) must NOT re-count it as finished
	// today. This is the exactness the dedicated column exists for.
	if _, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Text: ptr("ship it (renamed)")}); err != nil {
		t.Fatalf("PatchTodo text: %v", err)
	}
	again := mustCompletedSince(t, store, time.Now().Add(-24*time.Hour))
	if again != 1 {
		t.Fatalf("editing an already-done item must not count it as finished again, got %d", again)
	}

	// Re-asserting done is likewise not a new completion.
	reDone, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Status: ptr("done")})
	if err != nil {
		t.Fatalf("PatchTodo done again: %v", err)
	}
	if reDone.CompletedAt != firstStamp {
		t.Fatalf("re-asserting done must keep the original completion time, got %q want %q", reDone.CompletedAt, firstStamp)
	}
	if got := mustCompletedSince(t, store, time.Now().Add(-24*time.Hour)); got != 1 {
		t.Fatalf("re-asserting done must not double count, got %d", got)
	}

	// Reopening the item clears the stamp, so it no longer counts as finished...
	reopened, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Status: ptr("doing")})
	if err != nil {
		t.Fatalf("PatchTodo reopen: %v", err)
	}
	if reopened.CompletedAt != "" {
		t.Fatalf("leaving done must clear completed_at, got %q", reopened.CompletedAt)
	}
	if got := mustCompletedSince(t, store, time.Now().Add(-24*time.Hour)); got != 0 {
		t.Fatalf("a reopened item must not count as finished, got %d", got)
	}
}

// TestCountTodosCompletedSinceIsScopedAndBounded pins the two properties that make this
// count safe to show one user: it never sees another owner's work, and the day boundary is
// the caller's.
func TestCountTodosCompletedSinceIsScopedAndBounded(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mine := mustCreate(t, store, testScope(), "我的主线")

	item, err := store.CreateTodo(ctx, testScope(), mine.ID, TodoInput{Text: "mine"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	if _, err := store.PatchTodo(ctx, testScope(), mine.ID, item.ID, TodoPatch{Status: ptr("done")}); err != nil {
		t.Fatalf("PatchTodo: %v", err)
	}

	// A different owner's finished item, in their own tenant and stream.
	other := Scope{TenantID: "tenant-b", OwnerID: "owner-2"}
	otherStream := mustCreate(t, store, other, "别人的主线")
	otherItem, err := store.CreateTodo(ctx, other, otherStream.ID, TodoInput{Text: "theirs"})
	if err != nil {
		t.Fatalf("CreateTodo other: %v", err)
	}
	if _, err := store.PatchTodo(ctx, other, otherStream.ID, otherItem.ID, TodoPatch{Status: ptr("done")}); err != nil {
		t.Fatalf("PatchTodo other: %v", err)
	}

	// Both are finished, but each owner sees only their own: this is the isolation the
	// taskboard-based numerator could not provide (cards carry no owner).
	if got := mustCompletedSince(t, store, time.Now().Add(-24*time.Hour)); got != 1 {
		t.Fatalf("owner-1 must see only their own finished work, got %d", got)
	}
	if got, err := store.CountTodosCompletedSince(ctx, other, time.Now().Add(-24*time.Hour)); err != nil || got != 1 {
		t.Fatalf("owner-2 must see only their own finished work, got %d (err=%v)", got, err)
	}

	// The boundary is the caller's: a window that starts in the future counts nothing.
	if got := mustCompletedSince(t, store, time.Now().Add(time.Hour)); got != 0 {
		t.Fatalf("a future boundary must count nothing, got %d", got)
	}
}

func mustCompletedSince(t *testing.T, store *Store, since time.Time) int {
	t.Helper()
	count, err := store.CountTodosCompletedSince(context.Background(), testScope(), since)
	if err != nil {
		t.Fatalf("CountTodosCompletedSince: %v", err)
	}
	return count
}
