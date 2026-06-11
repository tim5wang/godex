package repl

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)
func runReplEditorWithInput(t *testing.T, in string) (string, string) {
	t.Helper()

	inR, inW := io.Pipe()
	outBuf := &bytes.Buffer{}

	go func() {
		defer inW.Close()
		io.WriteString(inW, in)
	}()

	type result struct {
		line string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		e := newLineEditor("> ")
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

	return outBuf.String(), res.line
}

// TestReplEditorEnterSubmits verifies plain CR submits.
func TestReplEditorEnterSubmits(t *testing.T) {
	_, line := runReplEditorWithInput(t, "hello\r")
	if line != "hello" {
		t.Fatalf("expected %q, got %q", "hello", line)
	}
}

// TestReplEditorAltEnterInsertsNewline verifies that the ESC+CR
// sequence inserts a newline rather than submitting.
func TestReplEditorAltEnterInsertsNewline(t *testing.T) {
	_, line := runReplEditorWithInput(t, "line1\x1b\rline2\r")
	if line != "line1\nline2\n" {
		t.Fatalf("expected %q, got %q", "line1\nline2\n", line)
	}
}

// TestReplEditorBackslashEnterInsertsNewline verifies that typing
// a backslash followed by LF inserts a literal newline.
func TestReplEditorBackslashEnterInsertsNewline(t *testing.T) {
	_, line := runReplEditorWithInput(t, "\\\nb\r")
	if line != "\nb" {
		t.Fatalf("expected %q, got %q", "\nb", line)
	}
}

// TestReplEditorPasteAcceptedAsOneInput verifies that a multi-line
// paste (multiple newlines in one Read burst) is treated as one
// input with literal newlines.
func TestReplEditorPasteAcceptedAsOneInput(t *testing.T) {
	_, line := runReplEditorWithInput(t, "first line\rsecond line\rthird line\r")
	if line != "first line\nsecond line\nthird line\n" {
		t.Fatalf("expected %q, got %q", "first line\nsecond line\nthird line\n", line)
	}
}

// TestReplEditorStatusBarDrawnForEachLine verifies that the
// editor paints a redraw after every Read burst, which the REPL
// caller uses to refresh the status bar.
func TestReplEditorStatusBarDrawnForEachLine(t *testing.T) {
	out, _ := runReplEditorWithInput(t, "abc\r")
	// The draw is invoked once after the first content burst; the
	// exact prompt prefix is the responsibility of the caller, but
	// the editor's redraw must contain the prompt at the start.
	if !strings.Contains(out, "> ") {
		t.Fatalf("expected output to contain prompt prefix, got %q", out)
	}
}

// TestReplEditorRedrawDoesNotPushUpHistory verifies that the
// editor's redraws do not emit newlines that would scroll the
// terminal and push the conversation history up.  The previous
// design printed a "\n" between content lines on every redraw,
// which scrolled the history by 1 row per keystroke.
func TestReplEditorRedrawDoesNotPushUpHistory(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()

	var histBuf bytes.Buffer
	histBuf.WriteString("user: hello\n")
	histBuf.WriteString("assistant: hi there\n")
	histBuf.WriteString("user: multi-line test\n")

	outBuf := &bytes.Buffer{}
	outBuf.Write(histBuf.Bytes())

	resCh := make(chan string, 1)
	go func() {
		e := newLineEditor("> ")
		line, _ := e.readFrom(inR, outBuf)
		resCh <- line
	}()

	go func() {
		inW.Write([]byte("abc"))
		inW.Close()
	}()

	<-resCh

	out := outBuf.String()
	idxEditorStart := strings.Index(out, histBuf.String()) + len(histBuf.String())
	editorOut := out[idxEditorStart:]

	// For a single-line buffer redrawn 3 times, we should see
	// 0 newlines (the redraw uses cursor-down to descend within
	// the same 1-line + status-bar layout).  The first character
	// of the first draw is the initial draw, which also has 0
	// newlines.  In a buggy implementation, every redraw emits
	// 1 newline.
	// Count newlines that come from REDRAWS, not from the
	// final submit.  We expect 0 redraw newlines: the
	// editor uses cursor-down to descend within the
	// existing 1-line layout.  The final "\r\n" is the
	// submit signal and is allowed (and necessary) but
	// is one newline at the very end.
	newlines := strings.Count(editorOut, "\n")
	// Strip the final submit newline before counting.
	trimmed := strings.TrimSuffix(editorOut, "\r\n")
	redrawNewlines := strings.Count(trimmed, "\n")
	if redrawNewlines > 0 {
		t.Fatalf("editor redraws emitted %d newlines; expected 0:\n%s",
			redrawNewlines, editorOut)
	}
	_ = newlines
}

// TestReplEditorMultilineRedrawDoesNotPushUpHistory verifies the
// multiline-content variant of TestReplEditorRedrawDoesNotPushUpHistory.
// With 2 lines of content, the redraw must descend using CUD
// (cursor down) instead of printing newlines that scroll the
// terminal.
func TestReplEditorMultilineRedrawDoesNotPushUpHistory(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()

	var histBuf bytes.Buffer
	histBuf.WriteString("user: hello\n")
	histBuf.WriteString("assistant: hi there\n")
	histBuf.WriteString("user: multi-line test\n")

	outBuf := &bytes.Buffer{}
	outBuf.Write(histBuf.Bytes())

	resCh := make(chan string, 1)
	go func() {
		e := newLineEditor("> ")
		line, _ := e.readFrom(inR, outBuf)
		resCh <- line
	}()

	// Type "ab" + Alt+Enter + "cd" + 1 char.  This forces the
	// editor through 2-line redraws.
	go func() {
		inW.Write([]byte("ab\x1b\rcdx"))
		inW.Close()
	}()

	<-resCh

	out := outBuf.String()
	idxEditorStart := strings.Index(out, histBuf.String()) + len(histBuf.String())
	editorOut := out[idxEditorStart:]
	trimmed := strings.TrimSuffix(editorOut, "\r\n")
	redrawNewlines := strings.Count(trimmed, "\n")
	if redrawNewlines > 0 {
		t.Fatalf("multiline redraws emitted %d newlines; expected 0:\n%s",
			redrawNewlines, editorOut)
	}
}
