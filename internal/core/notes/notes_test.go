package notes

import (
	"path/filepath"
	"testing"
	"time"
)

func TestManagerSaveListGetDelete(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.now = func() time.Time { return time.Date(2026, 5, 4, 1, 2, 3, 0, time.UTC) }

	saved, err := manager.Save(SaveInput{
		Title:   "Architecture Notes",
		Summary: "review summary",
		Tags:    []string{"review", "architecture"},
		Content: "# Architecture Notes\n\nImportant context.",
	})
	if err != nil {
		t.Fatalf("save note: %v", err)
	}
	if saved.ID != "architecture-notes" || filepath.Base(saved.Path) != "architecture-notes.md" {
		t.Fatalf("unexpected saved note: %+v", saved)
	}

	items, err := manager.List(SearchOptions{Query: "important", Tag: "review"})
	if err != nil {
		t.Fatalf("list notes: %v", err)
	}
	if len(items) != 1 || items[0].ID != saved.ID {
		t.Fatalf("unexpected listed notes: %+v", items)
	}
	loaded, err := manager.Get(saved.ID)
	if err != nil {
		t.Fatalf("get note: %v", err)
	}
	if loaded.Title != saved.Title || loaded.Summary != "review summary" || len(loaded.Tags) != 2 {
		t.Fatalf("unexpected loaded note: %+v", loaded)
	}
	deleted, err := manager.Delete(saved.ID)
	if err != nil {
		t.Fatalf("delete note: %v", err)
	}
	if deleted.ID != saved.ID {
		t.Fatalf("unexpected deleted note: %+v", deleted)
	}
	if _, err := manager.Get(saved.ID); err == nil {
		t.Fatalf("expected deleted note to be gone")
	}
}

func TestManagerRejectsEscapingID(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.Get("../secret"); err == nil {
		t.Fatalf("expected escaping id to fail")
	}
}
