package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

func writeRepoMapFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func repoMapTestAgent(workspace string) *Agent {
	return &Agent{cfg: &config.Config{WorkspaceDir: workspace}}
}

func TestCollectRepoMapEntriesDeterministic(t *testing.T) {
	root := t.TempDir()
	writeRepoMapFile(t, root, "zebra.go", "package z\nfunc Zebra() {}\n")
	writeRepoMapFile(t, root, "alpha/one.go", "package alpha\ntype One struct{}\n")
	writeRepoMapFile(t, root, "notes.md", "# notes\n")

	first := collectRepoMapEntries(root)
	second := collectRepoMapEntries(root)
	if len(first) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(first), first)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic walk: %v vs %v", first, second)
		}
	}
	// Paths are sorted lexically regardless of walk order or query.
	got := []string{first[0].path, first[1].path, first[2].path}
	want := []string{"alpha/one.go", "notes.md", "zebra.go"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected sorted paths %v, got %v", want, got)
		}
	}
	// Go entries carry the exported symbol summary.
	if !strings.Contains(first[0].symbols, "One") {
		t.Fatalf("expected symbol summary, got %q", first[0].symbols)
	}
	if first[1].symbols != "" {
		t.Fatalf("non-Go entry must not carry symbols, got %q", first[1].symbols)
	}
}

func TestRepoMapSnapshotStableAcrossFileEdits(t *testing.T) {
	root := t.TempDir()
	writeRepoMapFile(t, root, "main.go", "package main\nfunc Main() {}\n")
	a := repoMapTestAgent(root)

	text1, entries1 := a.repoMapSnapshot(false)
	if text1 == "" || len(entries1) != 1 {
		t.Fatalf("expected one entry, got text=%q entries=%d", text1, len(entries1))
	}

	// Creating a file must NOT change the snapshot text (stable prefix); the
	// change note reports it instead.
	writeRepoMapFile(t, root, "helper.go", "package main\nfunc Helper() {}\n")
	text2, entries2 := a.repoMapSnapshot(false)
	if text2 != text1 || len(entries2) != 1 {
		t.Fatalf("snapshot changed after file creation: text1=%q text2=%q entries=%d", text1, text2, len(entries2))
	}
	if note := renderRepoMapChangeNote(entries1, collectRepoMapEntries(root)); !strings.Contains(note, "added helper.go") {
		t.Fatalf("expected added note, got %q", note)
	}
}

func TestRepoMapInvalidateRebuilds(t *testing.T) {
	root := t.TempDir()
	writeRepoMapFile(t, root, "main.go", "package main\nfunc Main() {}\n")
	a := repoMapTestAgent(root)

	text1, _ := a.repoMapSnapshot(false)
	writeRepoMapFile(t, root, "new.go", "package main\nfunc New() {}\n")
	a.repoMapInvalidate()
	text2, entries := a.repoMapSnapshot(false)
	if text2 == text1 {
		t.Fatal("expected rebuild after invalidate")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after rebuild, got %d", len(entries))
	}
	if !strings.Contains(text2, "new.go") {
		t.Fatalf("expected rebuilt map to include new file, got %q", text2)
	}
}

func TestRepoMapChangeNoteListsEdits(t *testing.T) {
	root := t.TempDir()
	writeRepoMapFile(t, root, "a.go", "package a\nfunc A() {}\n")
	writeRepoMapFile(t, root, "b.go", "package b\nfunc B() {}\n")
	snapshot := collectRepoMapEntries(root)

	// Add, update (size change), remove.
	writeRepoMapFile(t, root, "c.go", "package c\nfunc C() {}\n")
	writeRepoMapFile(t, root, "a.go", "package a\nfunc A() {}\nfunc A2() {}\n// padding to change size\n")
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	note := renderRepoMapChangeNote(snapshot, collectRepoMapEntries(root))
	for _, want := range []string{"added c.go", "updated a.go", "removed b.go"} {
		if !strings.Contains(note, want) {
			t.Fatalf("expected %q in note %q", want, note)
		}
	}
}

func TestRepoMapChangeNoteBounded(t *testing.T) {
	root := t.TempDir()
	var snapshot []repoMapEntry
	for i := 0; i < repoMapChangeNoteLimit+10; i++ {
		writeRepoMapFile(t, root, "f"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+".go", "package f\n")
	}
	snapshot = collectRepoMapEntries(root)
	for i := 0; i < repoMapChangeNoteLimit+10; i++ {
		writeRepoMapFile(t, root, "g"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+".go", "package g\n")
	}
	note := renderRepoMapChangeNote(snapshot, collectRepoMapEntries(root))
	lines := strings.Count(note, "\n- ")
	if lines > repoMapChangeNoteLimit {
		t.Fatalf("change note exceeded limit: %d lines\n%s", lines, note)
	}
	if !strings.Contains(note, "more changes") {
		t.Fatalf("expected overflow marker, got %q", note)
	}
}

func TestRepoMapQueryFocus(t *testing.T) {
	root := t.TempDir()
	writeRepoMapFile(t, root, "internal/http/client.go", "package http\n")
	writeRepoMapFile(t, root, "internal/notes/store.go", "package notes\n")
	entries := collectRepoMapEntries(root)

	focus := renderRepoMapQueryFocus(entries, "how does the http client work")
	if !strings.Contains(focus, "internal/http/client.go") {
		t.Fatalf("expected http client in focus, got %q", focus)
	}
	if strings.Contains(focus, "notes/store.go") {
		t.Fatalf("expected unrelated file excluded from focus, got %q", focus)
	}
	if empty := renderRepoMapQueryFocus(entries, ""); empty != "" {
		t.Fatalf("expected empty focus for empty query, got %q", empty)
	}
}
