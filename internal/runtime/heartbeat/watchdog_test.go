package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunWatchdogEmptyScript(t *testing.T) {
	res, err := runWatchdog(context.Background(), "", t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("empty script should not error, got %v", err)
	}
	if res.Skipped {
		t.Fatal("empty script must not skip")
	}
}

func TestRunWatchdogExitZeroRuns(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "wd.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ready\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	res, err := runWatchdog(context.Background(), script, root, time.Second)
	if err != nil {
		t.Fatalf("exit 0 should not error, got %v", err)
	}
	if res.Skipped {
		t.Fatal("exit 0 must not skip")
	}
	if !strings.Contains(res.Output, "ready") {
		t.Fatalf("expected script output captured, got %q", res.Output)
	}
}

func TestRunWatchdogExitNonZeroSkips(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "wd.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho skip-me\nexit 3\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	res, err := runWatchdog(context.Background(), script, root, time.Second)
	if err != nil {
		t.Fatalf("non-zero exit is a skip decision, not an error, got %v", err)
	}
	if !res.Skipped {
		t.Fatal("non-zero exit must skip")
	}
	if !strings.Contains(res.Output, "skip-me") {
		t.Fatalf("expected skip output captured, got %q", res.Output)
	}
}

func TestRunWatchdogRelativePathUsesWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wd.sh"), []byte("exit 1\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	res, err := runWatchdog(context.Background(), "wd.sh", root, time.Second)
	if err != nil {
		t.Fatalf("relative path resolved against workspace, got %v", err)
	}
	if !res.Skipped {
		t.Fatal("expected skip for exit 1")
	}
}

func TestRunWatchdogMissingScriptErrors(t *testing.T) {
	_, err := runWatchdog(context.Background(), filepath.Join(t.TempDir(), "nope.sh"), t.TempDir(), time.Second)
	if err == nil {
		t.Fatal("missing script must error")
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRunWatchdogTimeoutErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh is not available on windows")
	}
	root := t.TempDir()
	script := filepath.Join(root, "wd.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	start := time.Now()
	_, err := runWatchdog(context.Background(), script, root, 100*time.Millisecond)
	if err == nil {
		t.Fatal("timeout must error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout did not stop the script in time: %v", elapsed)
	}
}
