package streaming

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// runEditorWithInput drives the line editor's testable inner loop
// (readFrom) with the given raw bytes and captures everything
// written to a buffer.  This avoids the os.Stdin/os.Stdout dance
// that the production ReadLine does, so we can run in environments
// that are not attached to a TTY.
func runEditorWithInput(t *testing.T, in string) (string, string) {
	t.Helper()

	inR, inW := io.Pipe()
	outBuf := &bytes.Buffer{}

	// Feed input asynchronously and close the pipe when done.
	go func() {
		defer inW.Close()
		io.WriteString(inW, in)
	}()

	// Run the editor synchronously.
	type result struct {
		line string
		err  error
	}
	resCh := make(chan result,1)
	go func() {
		e := newLineEditor("> ")
		e.statusBar = "TEST_STATUS"
		line, err := e.readFrom(inR, outBuf)
		resCh <- result{line: line, err: err}
	}()

	var res result
	select {
	case res = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("editor timed out")
	}

	if res.err != nil && res.err != io.EOF {
		t.Logf("editor returned error: %v", res.err)
	}
	if res.line == "" && res.err == nil {
		t.Logf("editor returned empty line with no error – check test")
	}

	return outBuf.String(), res.line
}

// TestEditorEnterSubmits verifies that a plain CR submits.
func TestEditorEnterSubmits(t *testing.T) {
	_, line := runEditorWithInput(t, "hello\r")
	if line != "hello" {
		t.Fatalf("expected %q, got %q", "hello", line)
	}
}

// TestEditorAltEnterInsertsNewline verifies that the ESC+CR
// sequence (Alt+Enter on most terminals) inserts a newline into
// the content rather than submitting.
func TestEditorAltEnterInsertsNewline(t *testing.T) {
	out, line := runEditorWithInput(t, "line1\x1b\rline2\r")
	if line != "line1\nline2" {
		t.Fatalf("expected %q, got %q", "line1\nline2", line)
	}
	if !strings.Contains(out, "TEST_STATUS") {
		t.Fatalf("expected output to contain status bar, got %q", out)
	}
}

// TestEditorBackslashEnterInsertsNewline verifies that a backslash
// followed by LF (\\n) inserts a literal newline.
func TestEditorBackslashEnterInsertsNewline(t *testing.T) {
	// Single backslash-LF burst (no other printable content), then
	// "b" + final \\r.  The backslash-LF is the line-continuation
	// request, the final \\r submits.
	_, line := runEditorWithInput(t, "\\\nb\r")
	if line != "\nb" {
		t.Fatalf("expected %q, got %q", "\nb", line)
	}
}

// TestEditorStatusBarPinnedBelowMultilineContent verifies that
// when the content expands to multiple lines, the status bar
// remains visible below the last content line.  This is the
// core fix for issue #3.
func TestEditorStatusBarPinnedBelowContent(t *testing.T) {
	out, line := runEditorWithInput(t, "L1\x1b\rL2\x1b\rL3\r")
	if line != "L1\nL2\nL3" {
		t.Fatalf("expected %q, got %q", "L1\nL2\nL3", line)
	}
	if !strings.Contains(out, "TEST_STATUS") {
		t.Fatalf("expected status bar marker, got %q", out)
	}
	// The status bar gets painted twice in drawTo: once at startup
	// (initial draw) and again after the multiline content.  We
	// want to verify the SECOND paint (the one after content) is
	// rendered below the last content line, so compare the last
	// status bar position with the last content position.
	idxL3 := strings.LastIndex(out, "L3")
	idxStatus := strings.LastIndex(out, "TEST_STATUS")
	if idxL3 <0 || idxStatus <0 {
		t.Fatalf("output missing content or status bar:\n%s", out)
	}
	if idxL3 > idxStatus {
		t.Fatalf("status bar appeared BEFORE last content line:\n%s", out)
	}
}

// TestEditorEnterOnEmptyNewlinePreservesContent verifies that a
// sequence of newlines at the start of the buffer (right after the
// prompt opens) inserts the expected blank lines without submitting
// – this is the situation when a user wants to start a multi-line
// prompt with intentional leading blank lines.
func TestEditorLeadingNewlinesAreInserted(t *testing.T) {
	// Two Alt+Enter keystrokes, then "hi", then submit.  The result
	// should have two leading newlines so the user can compose a
	// prompt that starts with blank lines.
	_, line := runEditorWithInput(t, "\x1b\r\x1b\rhi\r")
	if !strings.HasPrefix(line, "\n\nhi") {
		t.Fatalf("expected line to start with two newlines, got %q", line)
	}
}

// TestEditorPasteIsAcceptedAsOneInput verifies that a multi-line
// paste (multiple \\r in one Read burst) is treated as one input
// with literal newlines rather than multiple submit events.
func TestEditorPasteAcceptedAsOneInput(t *testing.T) {
	// Single Read burst containing three lines + final submit.
	_, line := runEditorWithInput(t, "first line\rsecond line\rthird line\r")
	if line != "first line\nsecond line\nthird line\n" {
		t.Fatalf("expected %q, got %q", "first line\nsecond line\nthird line\n", line)
	}
}
