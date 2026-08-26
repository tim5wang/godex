package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultWatchdogTimeout = 30 * time.Second

// watchdogResult describes the outcome of running a heartbeat watchdog script.
type watchdogResult struct {
	// Skipped is true when the script exited non-zero, meaning the agent run
	// should be skipped for this heartbeat tick.
	Skipped bool
	// Output captures the script's stdout+stderr (trimmed and capped) for logs.
	Output string
}

// runWatchdog executes the pre-run watchdog script with `sh`. A zero exit code
// means "run the agent"; a non-zero exit code means "skip this run". Errors
// (missing script, timeout, exec failure) are returned so the caller records
// the run as failed rather than silently skipped.
func runWatchdog(ctx context.Context, script, workspaceDir string, timeout time.Duration) (watchdogResult, error) {
	script = strings.TrimSpace(script)
	if script == "" {
		return watchdogResult{}, nil
	}
	if timeout <= 0 {
		timeout = defaultWatchdogTimeout
	}
	path := script
	if !filepath.IsAbs(path) && strings.TrimSpace(workspaceDir) != "" {
		path = filepath.Join(workspaceDir, script)
	}
	if _, err := os.Stat(path); err != nil {
		return watchdogResult{}, fmt.Errorf("watchdog script not accessible: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", path)
	cmd.Dir = workspaceDir
	// CommandContext kills only `sh`; a child it spawned (e.g. sleep) keeps
	// holding the stdout pipe, which would block CombinedOutput past the
	// timeout. WaitDelay forces the pipe closed once the deadline passes.
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if len(text) > 512 {
		text = text[:512] + "…"
	}
	if err != nil {
		// Timeout must win over ExitError: CommandContext kills the process
		// with a signal, which surfaces as an ExitError even on deadline.
		if runCtx.Err() == context.DeadlineExceeded {
			return watchdogResult{Output: text}, fmt.Errorf("watchdog script timed out after %s", timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit: the script decided to skip the agent run.
			return watchdogResult{Skipped: true, Output: text}, nil
		}
		return watchdogResult{Output: text}, fmt.Errorf("watchdog script failed to run: %w", err)
	}
	return watchdogResult{Output: text}, nil
}
