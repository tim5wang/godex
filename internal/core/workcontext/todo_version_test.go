package workcontext

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestPatchTodoVersionSemantics covers the fix for the "todo_patch always
// conflicts" trap. if_version is overloaded across actions (the stream's
// version for checkpoint vs the item's own version here), so the two mandates
// are: (1) 0 must mean "no precondition" like SaveCheckpoint, and (2) a
// mismatch must report the entity and its real version, because a bare
// "version conflict" left the caller unable to tell which entity to re-read.
func TestPatchTodoVersionSemantics(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	stream := mustCreate(t, store, testScope(), "stream")
	item, err := store.CreateTodo(ctx, testScope(), stream.ID, TodoInput{Text: "ship fix"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	// if_version omitted (0) patches without a precondition, so a caller that
	// has not read the item is not forced to guess a version.
	patched, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Status: ptr("done")})
	if err != nil {
		t.Fatalf("PatchTodo without if_version: %v", err)
	}
	if patched.Status != "done" || patched.Version != item.Version+1 {
		t.Fatalf("unexpected patch result: %+v", patched)
	}

	// Passing the STREAM's version here is exactly the mistake the old error
	// hid. It must still conflict, and the message must be actionable.
	_, err = store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Text: ptr("nope"), IfVersion: stream.Version})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stream version on todo_patch: want ErrConflict, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, item.ID) {
		t.Fatalf("conflict must name the plan item, got %q", msg)
	}
	if !strings.Contains(msg, strconv.Itoa(patched.Version)) {
		t.Fatalf("conflict must report the item's current version %d, got %q", patched.Version, msg)
	}
	if !strings.Contains(msg, "plan") {
		t.Fatalf("conflict should say which entity the version belongs to, got %q", msg)
	}

	// The reported version is the retry value: patching with it must succeed.
	if _, err := store.PatchTodo(ctx, testScope(), stream.ID, item.ID, TodoPatch{Text: ptr("nope"), IfVersion: patched.Version}); err != nil {
		t.Fatalf("retry with the reported version should succeed: %v", err)
	}
}
