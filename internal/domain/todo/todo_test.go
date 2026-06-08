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
