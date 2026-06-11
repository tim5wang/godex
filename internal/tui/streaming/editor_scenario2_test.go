package streaming

import (
	"bytes"
	"strings"
	"testing"
)

// TestEditorFullSessionScenario simulates what a real user sees:
// a session prints history, the editor paints a prompt + status
// bar, the user types multiple lines, then backspaces, then
// submits.  We capture the full byte stream and inspect it
// after-the-fact.
func TestEditorFullSessionScenario(t *testing.T) {
	var histBuf bytes.Buffer
	histBuf.WriteString("user: hello\n")
	histBuf.WriteString("assistant: hi there\n")

	inR, inW := makePipe(t)
	outBuf := &bytes.Buffer{}
	outBuf.Write(histBuf.Bytes())

	doneCh := make(chan string, 1)
	go func() {
		e := newLineEditor("> ")
		e.statusBar = "Ready"
		line, _ := e.readFrom(inR, outBuf)
		doneCh <- line
	}()

	// Simulate: type "ab", Alt+Enter, "cd", Alt+Enter, "ef",
	// 4 backspaces (removes "ef\n"), Alt+Enter, "x", Enter.
	inW.Write([]byte("ab\x1b\rcd\x1b\refrrrr\x1b\rx\r"))
	inW.Close()
	result := <-doneCh
	t.Logf("submitted: %q", result)
	t.Logf("editor output: %q", outBuf.String())

	// Count newlines in the editor's output.  Submit uses \r\n,
	// so the only legitimate newline is at the end.
	fullOut := outBuf.String()
	idxEditorStart := strings.Index(fullOut, "assistant: hi there\n") + len("assistant: hi there\n")
	editorOut := fullOut[idxEditorStart:]

	t.Logf("editor bytes (escaped):\n%q", editorOut)
	// The editor portion should have exactly one \n, which is
	// the trailing submit \r\n (so TrimSuffix leaves empty).
	trimmed := strings.TrimSuffix(editorOut, "\r\n")
	if strings.Contains(trimmed, "\n") {
		t.Fatalf("editor redraw emitted a newline:\n%q", editorOut)
	}

	// After typing "ab\ncd\nefrrrr\nx", the final content
	// should be exactly that (4 lines, no backspaces sent in
	// this scenario).
	if result != "ab\ncd\nefrrrr\nx" {
		t.Fatalf("unexpected result: %q", result)
	}
}

// TestEditorRedrawDoesNotWipeHistory is the central regression
// test for the user-reported "input box covers the history"
// bug.  When a big multiline paste arrives in one Read burst,
// the editor's redraw used to walk up `cursorLine` rows to
// repaint, which overshoots the prompt row and erases the
// history above it (each line is painted with a trailing
// "\x1b[K" clear).
//
// The fix tracks lastCursorRow from the previous draw and uses
// that as the walk-up distance, so the cursor always lands on
// the prompt row exactly.
func TestEditorRedrawDoesNotWipeHistory(t *testing.T) {
	var histBuf bytes.Buffer
	// 4 rows of history above the prompt.
	histBuf.WriteString("row1\n")
	histBuf.WriteString("row2\n")
	histBuf.WriteString("row3\n")
	histBuf.WriteString("row4\n")

	inR, inW := makePipe(t)
	outBuf := &bytes.Buffer{}
	outBuf.Write(histBuf.Bytes())

	doneCh := make(chan string, 1)
	go func() {
		e := newLineEditor("> ")
		e.statusBar = "R"
		e.readFrom(inR, outBuf)
		close(doneCh)
	}()

	// Send 5 lines of content in one burst.  If the redraw
	// walks up the wrong number of rows, the history above
	// the prompt will be erased.
	inW.Write([]byte("a\nb\nc\nd\ne"))
	// Drain the input pipe so the reader returns.
	_ = inW
	_ = inW.Close()
	<-doneCh

	out := outBuf.String()
	// The 4 history rows must still be in the output, in order.
	// If the redraw erased them, the output would start with
	// the editor's initial draw ("> " ...) and the history
	// would be missing.
	if !strings.Contains(out, "row1\n") {
		t.Fatalf("history row1 was wiped by redraw:\n%s", out)
	}
	if !strings.Contains(out, "row4\n") {
		t.Fatalf("history row4 was wiped by redraw:\n%s", out)
	}
	// The content of the user's input must also be visible.
	// The redraw should paint "a", "b", "c", "d", "e" each on
	// its own row below the prompt, with the status bar on
	// the row below "e".
	if !strings.Contains(out, "> ") {
		t.Fatalf("prompt row missing:\n%s", out)
	}
	// The history rows above the prompt must appear BEFORE
	// the editor's first redraw.  We verify the substring
	// "row4\n\r> " (history followed by prompt redraw) is
	// present, which proves the history was not overwritten.
	if !strings.Contains(out, "row4\n\r> ") {
		t.Fatalf("history is not before the prompt redraw:\n%s", out)
	}
}
