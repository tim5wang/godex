package streaming

import (
	"bytes"
	"strings"
	"testing"
)

// TestEditorRedrawDoesNotPushUpHistory simulates the full
// session flow: the session prints conversation history to
// stdout, then the user starts editing.  Every keystroke should
// redraw the editable region in place WITHOUT pushing the
// history upward (i.e. without emitting newlines that scroll
// the terminal).
//
// The previous design printed "\n + status bar + walk up" on
// every draw, which scrolled the terminal by 1 row per
// keystroke.  The fix is to never emit a newline during
// redraws that do not introduce new content rows.
func TestEditorRedrawDoesNotPushUpHistory(t *testing.T) {
	inR, inW := makePipe(t)
	defer inW.Close()

	// Pretend the session has already printed a few rows of
	// conversation history before the editor starts.
	var histBuf bytes.Buffer
	histBuf.WriteString("user: hello\n")
	histBuf.WriteString("assistant: hi there\n")
	histBuf.WriteString("user: multi-line test\n")

	outBuf := &bytes.Buffer{}
	// The session writes history to outBuf, then runs the editor.
	outBuf.Write(histBuf.Bytes())

	resCh := make(chan string, 1)
	go func() {
		e := newLineEditor("> ")
		e.statusBar = "STATUS"
		line, _ := e.readFrom(inR, outBuf)
		resCh <- line
	}()

	// Feed the editor one character at a time so we can count
	// newlines per keystroke precisely.
	inputs := []byte("abc")
	go func() {
		for _, b := range inputs {
			inW.Write([]byte{b})
		}
		inW.Close()
	}()

	<-resCh

	out := outBuf.String()
	// Find the start of the first editor draw: the session wrote
	// the history first, then the editor redraws.  The first
	// "redraw" begins after the LAST historical newline.  We
	// count the number of newlines emitted by the editor's
	// INITIAL draw (one newline to push the cursor down to the
	// status bar row) versus the number emitted by SUBSEQUENT
	// redraws (one per character typed).
	idxEditorStart := strings.Index(out, histBuf.String()) + len(histBuf.String())
	editorOut := out[idxEditorStart:]

	// The initial draw should have at most 1 newline (to put
	// the status bar below the empty prompt).  Each of the 3
	// character redraws should add 0 newlines (the prompt row
	// is just overwritten in place; the status bar is refreshed
	// without a fresh newline).
	//
	// Total expected newlines: 1 (initial) + 0*3 = 1.
	// Buggy behavior would produce 1 + 1*3 = 4 newlines, with
	// the conversation history pushed off screen.
	actualNewlines := strings.Count(editorOut, "\n")
	if actualNewlines > 1 {
		t.Fatalf("editor emitted %d newlines; expected at most 1:\n%s",
			actualNewlines, editorOut)
	}
}

// TestEditorBackspaceShrinksVisibleHeight verifies that when the
// user presses backspace enough times to remove a newline (so
// the content goes from 2 lines to 1 line), the on-screen
// input box shrinks to match.  Otherwise the leftover
// continuation row stays visible and the user's content
// "looks repeated" because the previous line content is still
// there in dim or partial form.
func TestEditorBackspaceShrinksVisibleHeight(t *testing.T) {
	// Build "ab\ncd" (2 content lines + prompt = 3 rows total).
	// Send 3 backspaces: remove "d", then "\n", then "c".
	// The buffer is now "a" (single content line).  The on-screen
	// continuation row that used to show "cd" must be cleared.
	out, _ := runEditorWithInput(t, "ab\x1b\rcd\x7f\x7f\x7f")
	// Find the LAST draw of the editor (the one that contains
	// the final content state).  After backspacing, the visible
	// input is "> a" (no continuation row).  We verify the
	// output does not contain a leftover continuation row
	// (which would be "\r  cd\x1b[K" or similar).
	if strings.Contains(out, "\r  cd\x1b[K") {
		t.Fatalf("backspace left a stale continuation row:\n%s", out)
	}
	if strings.Contains(out, "\r  c\x1b[K") {
		t.Fatalf("backspace left a stale partial continuation row:\n%s", out)
	}
}

// TestEditorBackspaceToEmptyClearsAllRows verifies that an empty
// content after backspace leaves the editable area with no
// visible residue.
func TestEditorBackspaceToEmptyClearsAllRows(t *testing.T) {
	// Type "ab" + Alt+Enter + "cd" + 4 backspaces.  Buffer is
	// empty.  No content lines should remain visible.
	out, _ := runEditorWithInput(t, "ab\x1b\rcd\x7f\x7f\x7f\x7f")
	if strings.Contains(out, "ab") && !strings.Contains(out, "> \x1b[K") {
		t.Fatalf("stale 'ab' still visible after backspacing to empty:\n%s", out)
	}
	// "cd" must be gone.
	if strings.Contains(out, "cd") {
		t.Fatalf("stale 'cd' still visible after backspacing to empty:\n%s", out)
	}
}
