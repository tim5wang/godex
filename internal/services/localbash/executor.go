// Package localbash provides a shared shell command executor used by both
// TUI and Web UI for local /bash (and /sh) slash commands.
//
// It runs the command via sh -c in the workspace directory and streams
// output chunks through a channel, with support for cancellation and timeout.
package localbash

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is the default maximum execution duration.
const DefaultTimeout = 30 * time.Second

// DefaultChunkInterval controls how often accumulated output is flushed.
const DefaultChunkInterval = 200 * time.Millisecond

// OutputChunk represents a snapshot of the running or finished command output.
type OutputChunk struct {
	Command  string
	Output   string
	Final    bool
	ExitCode int
	Err      error
}

// Result holds the aggregated final output from a completed command.
type Result struct {
	Command  string
	Output   string
	ExitCode int
	Err      error
}

// Run executes shellCommand via "sh -c" inside workspaceDir. Output chunks
// are sent to the returned channel at DefaultChunkInterval until the command
// finishes or ctx is cancelled. The caller MUST consume the channel to
// completion (until it is closed) to avoid leaking goroutines.
//
// If ctx is cancelled (e.g. by timeout or user interrupt), the process is
// killed and the final chunk will include whatever output was captured up to
// that point.
func Run(ctx context.Context, workspaceDir, shellCommand string) <-chan OutputChunk {
	return runWithShell(ctx, workspaceDir, "sh", shellCommand, DefaultChunkInterval)
}

// RunBash is like Run but uses "bash -c" instead of "sh -c".
func RunBash(ctx context.Context, workspaceDir, shellCommand string) <-chan OutputChunk {
	return runWithShell(ctx, workspaceDir, "bash", shellCommand, DefaultChunkInterval)
}

// RunWithTimeout is a convenience wrapper that applies a context timeout of
// DefaultTimeout and runs Run.
func RunWithTimeout(ctx context.Context, workspaceDir, shellCommand string) <-chan OutputChunk {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	_ = cancel
	return Run(ctx, workspaceDir, shellCommand)
}

// Collect runs the command and returns the aggregated final result.
// It is a convenience wrapper around Run that waits for all chunks.
func Collect(ctx context.Context, workspaceDir, shellCommand string) Result {
	ch := Run(ctx, workspaceDir, shellCommand)
	var last OutputChunk
	for chunk := range ch {
		last = chunk
	}
	return Result{
		Command:  last.Command,
		Output:   last.Output,
		ExitCode: last.ExitCode,
		Err:      last.Err,
	}
}

// CollectBash runs the command via bash -c and returns the aggregated result.
func CollectBash(ctx context.Context, workspaceDir, shellCommand string) Result {
	ch := RunBash(ctx, workspaceDir, shellCommand)
	var last OutputChunk
	for chunk := range ch {
		last = chunk
	}
	return Result{
		Command:  last.Command,
		Output:   last.Output,
		ExitCode: last.ExitCode,
		Err:      last.Err,
	}
}

// CollectBashWithTimeout runs the command via bash -c with DefaultTimeout.
func CollectBashWithTimeout(ctx context.Context, workspaceDir, shellCommand string) Result {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	return CollectBash(ctx, workspaceDir, shellCommand)
}

// CollectWithTimeout runs the command with a DefaultTimeout and returns the
// aggregated result.
func CollectWithTimeout(ctx context.Context, workspaceDir, shellCommand string) Result {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	return Collect(ctx, workspaceDir, shellCommand)
}

// ParseCommand extracts the shell command string from a raw slash-command input
// like "/bash git status" or "/sh ls -la".
func ParseCommand(rawLine string) (shellCommand string, ok bool) {
	trimmed := strings.TrimSpace(rawLine)
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return "", false
	}
	idx := strings.Index(trimmed, " ")
	if idx < 0 {
		return "", false
	}
	return strings.TrimSpace(trimmed[idx:]), true
}

// runWithShell is the internal implementation that runs shellCommand via the
// given shell (e.g. "sh" or "bash") inside workspaceDir, streaming OutputChunks
// at chunkInterval.
func runWithShell(ctx context.Context, workspaceDir, shell, shellCommand string, chunkInterval time.Duration) <-chan OutputChunk {
	ch := make(chan OutputChunk, 64)

	go func() {
		defer close(ch)

		cmd := exec.CommandContext(ctx, shell, "-c", shellCommand)
		cmd.Dir = workspaceDir

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- OutputChunk{Command: shellCommand, Final: true, ExitCode: -1, Err: err}
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			ch <- OutputChunk{Command: shellCommand, Final: true, ExitCode: -1, Err: err}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- OutputChunk{Command: shellCommand, Final: true, ExitCode: -1, Err: err}
			return
		}

		var (
			accumulated strings.Builder
			mu          sync.Mutex
		)

		// Read both pipes concurrently into the accumulator.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); copyTo(&accumulated, &mu, stdout) }()
		go func() { defer wg.Done(); copyTo(&accumulated, &mu, stderr) }()

		ticker := time.NewTicker(chunkInterval)
		defer ticker.Stop()

		readDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(readDone)
		}()

	loop:
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				if accumulated.Len() > 0 {
					ch <- OutputChunk{Command: shellCommand, Output: accumulated.String()}
				}
				mu.Unlock()
			case <-readDone:
				break loop
			case <-ctx.Done():
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				break loop
			}
		}

		// Drain any remaining output after loop exit.
		mu.Lock()
		finalOutput := accumulated.String()
		mu.Unlock()

		waitErr := cmd.Wait()
		code := exitCode(waitErr)
		if ctx.Err() != nil {
			if finalOutput == "" {
				finalOutput = "(cancelled)"
			}
			code = -1
		}

		ch <- OutputChunk{
			Command:  shellCommand,
			Output:   finalOutput,
			Final:    true,
			ExitCode: code,
			Err:      waitErr,
		}
	}()

	return ch
}

func copyTo(dst *strings.Builder, mu *sync.Mutex, src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			mu.Lock()
			dst.Write(buf[:n])
			mu.Unlock()
		}
		if readErr != nil {
			break
		}
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
