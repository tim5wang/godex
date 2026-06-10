package repl

import (
	"bytes"
	"sync"
	"testing"
)

// TestSessionWritePrompt is the regression test for the
// "REPL history messages get their newlines eaten" bug.
//
// chzyer/readline wrote the prompt prefix ("> ") by itself, then
// took over the terminal's line-edit mode. When the REPL was
// switched to a bubbles/textarea + tea.Program editor in commit
// 52db327, the program took over the same writer (s.stdout) the
// REPL uses for history messages. tea.Program redraws the prompt
// line on every Update by emitting "\r\x1b[K" (carriage return +
// clear-line) before the View() output; that redraw overwrites
// any "\n" characters emitted concurrently by handleEvent, which
// is why the user sees their history squashed into a single
// ragged line.
//
// The fix splits the writer: tea.Program's renderer writes to
// io.Discard, and the REPL loop writes the "> " prompt itself
// via Session.writePrompt. This test pins the second half of
// that contract: writePrompt produces exactly "> " on the
// configured writer, with no trailing newline (chzyer/readline
// parity) and under the same printMu that guards handleEvent's
// printf calls (so the two cannot interleave).
func TestSessionWritePrompt(t *testing.T) {
	t.Parallel()

	var (
		buf bytes.Buffer
		mu  sync.Mutex // stand-in for Session.printMu (pointer to satisfy copylocks)
	)
	// The point of the test is the Session-side behaviour, not
	// concurrency. We construct a Session with only the fields
	// writePrompt touches (stdout + printMu) and exercise the
	// public contract: a clean "> " on the writer, with no
	// trailing newline.
	s := &Session{
		stdout:  &buf,
		printMu: &mu,
	}
	s.writePrompt(s.stdout)

	if got := buf.String(); got != "> " {
		t.Fatalf("writePrompt: got %q, want %q", got, "> ")
	}
}
