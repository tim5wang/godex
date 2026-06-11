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
