package workspacefs

import (
	"io"
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

func TestWorkspaceFSAllowsReadFromAllowlistDirs(t *testing.T) {
	workspace := t.TempDir()
	allowDir := t.TempDir()
	secretPath := filepath.Join(allowDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0644); err != nil {
		t.Fatalf("write allowlist file: %v", err)
	}

	root, err := New(workspace, allowDir)
	if err != nil {
		t.Fatalf("new workspace fs: %v", err)
	}
	defer root.Close()

	// ReadFile via absolute path should work.
	data, err := root.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read allowlist file: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("unexpected data %q", data)
	}

	// Abs and Open are used together by the line-oriented read_file tool.
	abs, err := root.Abs(secretPath)
	if err != nil || abs != secretPath {
		t.Fatalf("abs allowlist file: path=%q err=%v", abs, err)
	}
	file, err := root.Open(secretPath)
	if err != nil {
		t.Fatalf("open allowlist file: %v", err)
	}
	opened, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(opened) != "secret" {
		t.Fatalf("read opened allowlist file: data=%q readErr=%v closeErr=%v", opened, readErr, closeErr)
	}

	// Stat should work.
	info, err := root.Stat(secretPath)
	if err != nil {
		t.Fatalf("stat allowlist file: %v", err)
	}
	if info.Name() != "secret.txt" {
		t.Fatalf("unexpected name %q", info.Name())
	}

	// ReadDir should work.
	entries, err := root.ReadDir(allowDir)
	if err != nil {
		t.Fatalf("readdir allowlist dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "secret.txt" {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func TestWorkspaceFSRejectsWriteToAllowlistDirs(t *testing.T) {
	workspace := t.TempDir()
	allowDir := t.TempDir()

	root, err := New(workspace, allowDir)
	if err != nil {
		t.Fatalf("new workspace fs: %v", err)
	}
	defer root.Close()

	// Write to allowlist dir should fail — external writes are not allowed.
	err = root.WriteFile(filepath.Join(allowDir, "nope.txt"), []byte("nope"), 0644)
	if err == nil {
		t.Fatalf("expected write to allowlist dir to fail")
	}
}

func TestDefaultReadAllowlistMerged(t *testing.T) {
	workspace := t.TempDir()
	allowDir := t.TempDir()
	secretPath := filepath.Join(allowDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0644); err != nil {
		t.Fatalf("write allowlist file: %v", err)
	}

	// Set the default and verify it's merged even when the per-call list is nil.
	old := DefaultReadAllowlist
	DefaultReadAllowlist = []string{allowDir}
	defer func() { DefaultReadAllowlist = old }()

	root, err := New(workspace) // no per-call allowlist
	if err != nil {
		t.Fatalf("new workspace fs: %v", err)
	}
	defer root.Close()

	data, err := root.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read via default allowlist: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("unexpected data %q", data)
	}
}

func TestWorkspaceFSAllowlistDoesNotAllowOutsidePaths(t *testing.T) {
	workspace := t.TempDir()
	allowDir := t.TempDir()
	outsideDir := t.TempDir()
	evilPath := filepath.Join(outsideDir, "evil.txt")
	if err := os.WriteFile(evilPath, []byte("evil"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	root, err := New(workspace, allowDir)
	if err != nil {
		t.Fatalf("new workspace fs: %v", err)
	}
	defer root.Close()

	// Paths outside both workspace and allowlist should still fail.
	_, err = root.ReadFile(evilPath)
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected outside path to fail, got %v", err)
	}
}
