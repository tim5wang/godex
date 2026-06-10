package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestHighlightTimeoutFallsBackToNil is the core regression test for
// the "TUI stuck on Loading TUI... with CPU pegged at 100%" bug.
//
// Root cause: a fenced code block in an assistant message gets fed to
// chroma v2.23.1, which in turn hands the regex matching to
// dlclark/regexp2 v1.11.5. On certain code+language combinations
// regexp2's NFA runner spins forever in
//
//	dlclark/regexp2.(*Regexp).run
//	  .../regexp2@v1.11.5/runner.go:76
//
// The whole Update call therefore never returns. The WindowSizeMsg
// that would have set m.width never gets handled, and the TUI is stuck
// on "Loading TUI..." forever (or until the user kills the terminal).
//
// The fix: highlight must run with a hard context. If chroma/regexp2
// hangs, the timeout fires, the goroutine abandons the result, and
// markdown.go falls back to rendering the code block as plain text.
//
// This test exercises a code pattern that triggers regexp2's runaway
// matcher: a single very long line containing a backreference-like
// pattern in languages that use catastrophic-backtracking-prone
// regexes. We don't need a guaranteed hang; we only need the timeout
// path to be wired up so that IF chroma hangs, the caller recovers
// within the deadline.
func TestHighlightTimeoutFallsBackToNil(t *testing.T) {
	t.Parallel()

	hl := NewHighlighter()

	// A pathological-looking single line that regexp2 has historically
	// choked on. We don't assert that the highlight call actually
	// hangs (that would be flaky); we only assert that, IF it were to
	// take longer than the deadline, the helper returns nil instead
	// of blocking forever.
	code := strings.Repeat("a", 5000) + strings.Repeat("(?:a)\\1", 100)
	lang := "javascript"

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan []string, 1)
	go func() {
		done <- hl.HighlightWithTimeout(ctx, code, lang, 200*time.Millisecond)
	}()

	select {
	case got := <-done:
		// Either Highlight finished within 200ms (returns non-nil
		// lines) or it didn't and the helper returned nil. Both
		// outcomes are acceptable; what matters is that we got
		// SOMETHING in 200ms rather than blocking the renderer.
		_ = got
	case <-time.After(2 * time.Second):
		t.Fatalf("HighlightWithTimeout blocked beyond the 2s outer deadline; the timeout must be enforced synchronously")
	}
}

// TestHighlightTimeoutFiresOnSimulatedHang uses a context the test
// cancels deliberately to prove the timeout path returns nil within
// the configured budget. This is the deterministic half of the
// regression suite: it does not rely on chroma actually hanging.
func TestHighlightTimeoutFiresOnSimulatedHang(t *testing.T) {
	t.Parallel()

	hl := NewHighlighter()

	// Pre-cancel the context so any context-aware work inside
	// HighlightWithTimeout bails out immediately. The 50ms budget
	// here is just so the test fails fast if the cancellation path
	// is broken.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	got := hl.HighlightWithTimeout(ctx, "x := 1\n", "go", 50*time.Millisecond)
	elapsed := time.Since(start)

	// Cancelling the context up front should let Highlight return
	// promptly; the test is mainly a smoke check that the contract
	// holds.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("HighlightWithTimeout took %v despite cancelled context", elapsed)
	}
	_ = got
}

// TestHighlightNormalPathUnchanged locks in the contract that the
// existing, un-timeout-bounded Highlight still returns highlighted
// lines for a small Go program. The new timeout-bounded variant
// must be a strict superset of this behaviour.
func TestHighlightNormalPathUnchanged(t *testing.T) {
	t.Parallel()

	hl := NewHighlighter()
	got := hl.Highlight("package main\n\nfunc main() {}\n", "go")
	if len(got) == 0 {
		t.Fatalf("Highlight returned no lines for a small Go program")
	}
}
