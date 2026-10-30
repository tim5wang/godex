package workcontext

import (
	"context"
	"errors"
	"testing"
)

// TestTodosLifecycleAndEvents covers create → patch → delete and that each step
// records an event, which is what feeds the desktop's "what changed" list.
func TestTodosLifecycleAndEvents(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	stream := mustCreate(t, store, testScope(), "stream")

	item, err := store.CreateTodo(ctx, testScope(), stream.ID, TodoInput{Text: "write design", Owner: "me"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	if item.Status != "todo" || item.Version != 1 || item.Position != 1 {
		t.Fatalf("unexpected new todo: %+v", item)
	}

	updated, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Status: ptr("doing"), DueAt: ptr("2026-11-01T09:00:00Z"), IfVersion: 1})
	if err != nil {
		t.Fatalf("PatchTodo: %v", err)
	}
	if updated.Status != "doing" || updated.Version != 2 || updated.DueAt == "" {
		t.Fatalf("patch did not apply: %+v", updated)
	}

	// A stale version must conflict rather than silently overwrite.
	if _, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Text: ptr("nope"), IfVersion: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale patch: want ErrConflict, got %v", err)
	}
	// An unknown status is rejected.
	if _, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Status: ptr("nonsense"), IfVersion: 2}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: want ErrInvalid, got %v", err)
	}

	list, err := store.ListTodos(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(list) != 1 || list[0].Text != "write design" {
		t.Fatalf("unexpected list: %+v", list)
	}

	focus, err := store.Focus(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if len(focus.Changes) < 2 {
		t.Fatalf("expected todo events in focus changes, got %d", len(focus.Changes))
	}

	if err := store.DeleteTodo(ctx, testScope(), stream.ID, item.ID); err != nil {
		t.Fatalf("DeleteTodo: %v", err)
	}
	empty, err := store.ListTodos(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("ListTodos after delete: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("todo survived delete: %+v", empty)
	}
}

// TestTodosRejectMissingTextAndBadDueAt checks the trust-boundary validation.
func TestTodosRejectMissingTextAndBadDueAt(t *testing.T) {
	store := newTestStore(t)
	stream := mustCreate(t, store, testScope(), "stream")
	if _, err := store.CreateTodo(context.Background(), testScope(), stream.ID, TodoInput{Text: "   "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank text: want ErrInvalid, got %v", err)
	}
	if _, err := store.CreateTodo(context.Background(), testScope(), stream.ID, TodoInput{Text: "x", DueAt: "next tuesday"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad due_at: want ErrInvalid, got %v", err)
	}
}

// TestFocusTracksChangesSinceSeen is the core of "what changed since I last
// looked": a fresh stream reports everything, marking seen advances the
// watermark, and only later events reappear.
func TestFocusTracksChangesSinceSeen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	stream := mustCreate(t, store, testScope(), "stream")

	before, err := store.Focus(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if before.LastSeenAt != "" {
		t.Fatalf("fresh stream should have no seen watermark, got %q", before.LastSeenAt)
	}
	firstCount := len(before.Changes)
	if firstCount == 0 {
		t.Fatal("fresh stream should show all recorded events")
	}

	previous, err := store.MarkSeen(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if previous != "" {
		t.Fatalf("first MarkSeen should report an empty previous watermark, got %q", previous)
	}

	after, err := store.Focus(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("Focus after seen: %v", err)
	}
	if after.LastSeenAt == "" {
		t.Fatal("MarkSeen did not persist a watermark")
	}
	if len(after.Changes) != 0 {
		t.Fatalf("nothing changed since MarkSeen, want 0 changes, got %d", len(after.Changes))
	}
	// Marking seen must not bump the context version (viewing is not an edit).
	detail, err := store.Get(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Context.Version != stream.Version {
		t.Fatalf("MarkSeen bumped context version: %d -> %d", stream.Version, detail.Context.Version)
	}

	// A new plan item is a recordable change and must surface again. Since
	// created_at has nanosecond precision this is strictly later than the
	// watermark written a moment ago.
	if _, err := store.CreateTodo(ctx, testScope(), stream.ID, TodoInput{Text: "new work"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	later, err := store.Focus(ctx, testScope(), stream.ID)
	if err != nil {
		t.Fatalf("Focus after change: %v", err)
	}
	if len(later.Changes) == 0 {
		t.Fatal("a plan item added after MarkSeen should appear as a change")
	}
}

func ptr[T any](value T) *T { return &value }
