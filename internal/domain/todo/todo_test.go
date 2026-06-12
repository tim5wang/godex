package todo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestManagerResetClearsInMemoryItems asserts Reset() empties the in-memory
// list and next-id counter so future Add() calls start from id 1 again.
func TestManagerResetClearsInMemoryItems(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	if _, err := mgr.Add("first", ""); err != nil {
		t.Fatalf("add first: %v", err)
	}
	if _, err := mgr.Add("second", ""); err != nil {
		t.Fatalf("add second: %v", err)
	}
	if got := len(mgr.List()); got != 2 {
		t.Fatalf("expected 2 items before reset, got %d", got)
	}

	mgr.Reset()

	if got := len(mgr.List()); got != 0 {
		t.Fatalf("expected 0 items after reset, got %d", got)
	}
	if got := mgr.Render(); got != "No todos." {
		t.Fatalf("expected render to be %q after reset, got %q", "No todos.", got)
	}
	// Adding after reset must reuse id 1 so the on-disk representation is
	// stable across clear-and-rebuild cycles.
	item, err := mgr.Add("after-reset", "")
	if err != nil {
		t.Fatalf("add after reset: %v", err)
	}
	if item.ID != 1 {
		t.Fatalf("expected id=1 after reset, got %d", item.ID)
	}
}

// TestManagerResetPersistsEmptyFile asserts the on-disk todos.json becomes an
// empty JSON array after Reset so a fresh process loads zero items.
func TestManagerResetPersistsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	if _, err := mgr.Add("alpha", ""); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if _, err := mgr.Add("beta", ""); err != nil {
		t.Fatalf("add beta: %v", err)
	}

	mgr.Reset()

	path := filepath.Join(dir, "todos.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read todos.json: %v", err)
	}
	var items []Item
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("unmarshal todos.json: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty todos.json, got %d items: %s", len(items), string(data))
	}
	// Re-opening the manager should also report zero items, confirming
	// Reset() persisted the cleared state.
	other := NewManager(dir)
	if got := len(other.List()); got != 0 {
		t.Fatalf("expected re-opened manager to load 0 items, got %d", got)
	}
}

// TestManagerResetReturnsErrorOnDiskFailure asserts that Reset()
// surfaces persistence errors instead of silently swallowing them,
// so the caller (the /todos clear command) can warn the user that
// stale todos may still haunt the next session.
func TestManagerResetReturnsErrorOnDiskFailure(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	if _, err := mgr.Add("alpha", ""); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	// Make the data directory unwritable so persist() fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := mgr.Reset()
	if err == nil {
		t.Fatalf("expected Reset to surface persistence error, got nil")
	}
}

// TestNewManagerForSessionScopesToSubdirectory asserts that
// todos written through one per-session manager never appear in
// another per-session manager, even when both managers live
// under the same base sessions directory.
//
// Regression guard for the cross-session pollution bug: local
// session A writes todos, the user runs /todos clear, then a
// freshly-opened web session B must not see session A's todos.
func TestNewManagerForSessionScopesToSubdirectory(t *testing.T) {
	sessions := t.TempDir()
	a := NewManagerForSession(sessions, "session-A")
	b := NewManagerForSession(sessions, "session-B")

	if _, err := a.Add("from A", ""); err != nil {
		t.Fatalf("A.Add: %v", err)
	}
	if _, err := a.Add("also from A", ""); err != nil {
		t.Fatalf("A.Add: %v", err)
	}

	if got := len(b.List()); got != 0 {
		t.Fatalf("session B should see 0 todos while session A holds 2, got %d", got)
	}

	// Resetting session A must not touch session B.
	if err := a.Reset(); err != nil {
		t.Fatalf("A.Reset: %v", err)
	}
	if _, err := b.Add("from B", ""); err != nil {
		t.Fatalf("B.Add: %v", err)
	}

	if got := len(a.List()); got != 0 {
		t.Fatalf("session A should still be empty after Reset, got %d", got)
	}
	if got := len(b.List()); got != 1 {
		t.Fatalf("session B should have 1 todo, got %d", got)
	}

	// A fresh manager reopened for session A still reads empty;
	// a fresh manager for session B reads the one B wrote.
	a2 := NewManagerForSession(sessions, "session-A")
	b2 := NewManagerForSession(sessions, "session-B")
	if got := len(a2.List()); got != 0 {
		t.Fatalf("reopened session A should be empty, got %d", got)
	}
	if got := len(b2.List()); got != 1 {
		t.Fatalf("reopened session B should have 1 todo, got %d", got)
	}
}

// TestNewManagerForSessionEmptyIDFallsBackToBase asserts that
// passing an empty sessionID yields the legacy single-file
// manager rooted directly at <baseDir>/todos.json.  This is
// the safety net for unit tests and tooling that don't know
// which session they belong to.
func TestNewManagerForSessionEmptyIDFallsBackToBase(t *testing.T) {
	base := t.TempDir()
	mgr := NewManagerForSession(base, "")
	if _, err := mgr.Add("global", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// The file should live at <base>/todos.json (not nested).
	if _, err := os.Stat(filepath.Join(base, "todos.json")); err != nil {
		t.Fatalf("expected todos.json directly under base dir, got %v", err)
	}
}
