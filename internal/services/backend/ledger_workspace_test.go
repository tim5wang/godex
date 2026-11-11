package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/sandbox"
)

func ledgerTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestLedgerWorkspaceTracksActualChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ledgerTestGit(t, dir, "init", "-q")
	ledgerTestGit(t, dir, "config", "user.email", "test@example.invalid")
	ledgerTestGit(t, dir, "config", "user.name", "Test")
	write("read.go", "read")
	write("modified.go", "original")
	write("untouched-dirty.go", "original")
	write("deleted.go", "delete me")
	write("rename.go", "rename me")
	ledgerTestGit(t, dir, "add", ".")
	ledgerTestGit(t, dir, "commit", "-qm", "baseline")
	write("modified.go", "pre-existing edit")
	write("untouched-dirty.go", "pre-existing edit")
	write("preexisting-untracked.go", "preexisting")
	before := captureLedgerWorkspace(sandbox.ToolBinding{WorkspaceDir: dir, TempDir: filepath.Join(dir, ".godex", "tmp")})
	if before == nil {
		t.Fatal("snapshot unavailable")
	}
	if _, err := os.ReadFile(filepath.Join(dir, "read.go")); err != nil {
		t.Fatal(err)
	}
	write("modified.go", "edited during turn")
	write("internal/lsp_test.go", "new test via shell")
	write("spaced  name.go", "preserve filename whitespace")
	write(".godex/tmp/probe.py", "temporary")
	if err := os.Remove(filepath.Join(dir, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	ledgerTestGit(t, dir, "mv", "rename.go", "renamed.go")
	paths, known := ledgerWorkspaceChanges(before)
	want := []string{"deleted.go", "internal/lsp_test.go", "modified.go", "rename.go", "renamed.go", "spaced  name.go"}
	if !known || !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v known=%v want=%v", paths, known, want)
	}
	// Committed changes remain visible, and unchanged pre-existing edits do not.
	ledgerTestGit(t, dir, "add", ".")
	ledgerTestGit(t, dir, "commit", "-qm", "turn changes")
	paths, known = ledgerWorkspaceChanges(before)
	if !known || !reflect.DeepEqual(paths, want) {
		t.Fatalf("committed paths=%v known=%v", paths, known)
	}
}

func TestLedgerWorkspaceUnavailableIsNotAnEmptyVerifiedDiff(t *testing.T) {
	before := captureLedgerWorkspace(sandbox.ToolBinding{WorkspaceDir: t.TempDir()})
	if before != nil {
		t.Fatal("non-repository claimed as a valid snapshot")
	}
	if paths, known := ledgerWorkspaceChanges(before); known || len(paths) != 0 {
		t.Fatalf("paths=%v known=%v", paths, known)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := ledgerGit(ctx, t.TempDir(), "rev-parse", "--show-toplevel"); err == nil {
		t.Fatal("expected non-repo error")
	}
}

func TestLedgerWorkspaceHandlesSubdirectoryAndFirstCommit(t *testing.T) {
	dir := t.TempDir()
	ledgerTestGit(t, dir, "init", "-q")
	ledgerTestGit(t, dir, "config", "user.email", "test@example.invalid")
	ledgerTestGit(t, dir, "config", "user.name", "Test")
	workspace := filepath.Join(dir, "project")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	before := captureLedgerWorkspace(sandbox.ToolBinding{WorkspaceDir: workspace})
	if before == nil {
		t.Fatal("snapshot unavailable")
	}
	for _, name := range []string{"project/new_test.go", "sibling.go", "project/.godex/.sessions/events.jsonl"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("created"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ledgerTestGit(t, dir, "add", ".")
	ledgerTestGit(t, dir, "commit", "-qm", "first commit")
	paths, known := ledgerWorkspaceChanges(before)
	if !known || !reflect.DeepEqual(paths, []string{"new_test.go"}) {
		t.Fatalf("outside workspace/runtime state leaked: %v known=%v", paths, known)
	}
}
