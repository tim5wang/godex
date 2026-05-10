package workspacefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceFSRejectsTraversalAndExternalSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	root, err := New(workspace)
	if err != nil {
		t.Fatalf("new workspace fs: %v", err)
	}
	defer root.Close()

	if _, err := root.ReadFile("link.txt"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected external symlink read to fail, got %v", err)
	}
	if err := root.WriteFile("../outside.txt", []byte("nope"), 0644); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected traversal write to fail, got %v", err)
	}
}

func TestWorkspaceFSAllowsNormalWorkspaceFiles(t *testing.T) {
	workspace := t.TempDir()
	root, err := New(workspace)
	if err != nil {
		t.Fatalf("new workspace fs: %v", err)
	}
	defer root.Close()

	if err := root.WriteFile("notes/todo.txt", []byte("hello"), 0644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	data, err := root.ReadFile("notes/todo.txt")
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected data %q", data)
	}
	abs, err := root.Abs("notes/todo.txt")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if abs != filepath.Join(workspace, "notes", "todo.txt") {
		t.Fatalf("unexpected absolute path %q", abs)
	}
}
