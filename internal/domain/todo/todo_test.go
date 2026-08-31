package todo

import (
	"errors"
	"testing"
)

type testRepository struct {
	items   []Item
	saveErr error
}

func (r *testRepository) Load() ([]Item, error) {
	return cloneItems(r.items), nil
}

func (r *testRepository) Save(items []Item) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.items = cloneItems(items)
	return nil
}

// TestManagerResetClearsInMemoryItems asserts Reset() empties the in-memory
// list and next-id counter so future Add() calls start from id 1 again.
func TestManagerResetClearsInMemoryItems(t *testing.T) {
	repository := &testRepository{}
	mgr := NewManager(repository)
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
	// Adding after reset must reuse id 1 so the persisted representation is
	// stable across clear-and-rebuild cycles.
	item, err := mgr.Add("after-reset", "")
	if err != nil {
		t.Fatalf("add after reset: %v", err)
	}
	if item.ID != 1 {
		t.Fatalf("expected id=1 after reset, got %d", item.ID)
	}
}

// TestManagerResetPersistsEmptySnapshot asserts Reset stores an empty
// repository snapshot so a fresh manager loads zero items.
func TestManagerResetPersistsEmptySnapshot(t *testing.T) {
	repository := &testRepository{}
	mgr := NewManager(repository)
	if _, err := mgr.Add("alpha", ""); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if _, err := mgr.Add("beta", ""); err != nil {
		t.Fatalf("add beta: %v", err)
	}

	mgr.Reset()

	if len(repository.items) != 0 {
		t.Fatalf("expected empty repository snapshot, got %d items", len(repository.items))
	}
	other := NewManager(repository)
	if got := len(other.List()); got != 0 {
		t.Fatalf("expected re-opened manager to load 0 items, got %d", got)
	}
}

// TestManagerResetReturnsRepositoryError asserts that Reset() surfaces
// persistence errors instead of silently swallowing them,
// so the caller (the /todos clear command) can warn the user that
// stale todos may still haunt the next session.
func TestManagerResetReturnsRepositoryError(t *testing.T) {
	repository := &testRepository{}
	mgr := NewManager(repository)
	if _, err := mgr.Add("alpha", ""); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	repository.saveErr = errors.New("save failed")

	err := mgr.Reset()
	if err == nil {
		t.Fatalf("expected Reset to surface persistence error, got nil")
	}
}

func TestNewManagerLoadsRepositoryItems(t *testing.T) {
	repository := &testRepository{items: []Item{{ID: 7, Content: "persisted", Status: StatusPending}}}
	mgr := NewManager(repository)
	items := mgr.List()
	if len(items) != 1 || items[0].ID != 7 {
		t.Fatalf("expected repository item to be loaded, got %#v", items)
	}
}
