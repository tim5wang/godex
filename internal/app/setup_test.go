package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSetupCommandInitializesWorkspace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspace")
	home := filepath.Join(root, "home")
	t.Setenv("GODEX_HOME", home)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := RunSetupCommand(context.Background(), []string{"--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("setup: %v\nstderr=%s", err, stderr.String())
	}
	for _, path := range []string{
		"godex.yaml",
		".env.example",
		"AGENT.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".godex")); !os.IsNotExist(err) {
		t.Fatalf("setup should not create workspace .godex by default, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills")); err != nil {
		t.Fatalf("expected home skills to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions")); err != nil {
		t.Fatalf("expected home sessions to exist: %v", err)
	}
	if !strings.Contains(stdout.String(), "Initialized GoDex workspace") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Doctor:") {
		t.Fatalf("expected doctor summary in stdout: %s", stdout.String())
	}
}

func TestRunnerInitAliasInitializesWorkspace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspace")
	t.Setenv("GODEX_HOME", filepath.Join(root, "home"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stderr}

	if err := runner.Run(context.Background(), []string{"init", "--dir", dir}); err != nil {
		t.Fatalf("init: %v\nstderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "godex.yaml")); err != nil {
		t.Fatalf("expected godex.yaml to exist: %v", err)
	}
	if !strings.Contains(stdout.String(), "Initialized GoDex workspace") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}
