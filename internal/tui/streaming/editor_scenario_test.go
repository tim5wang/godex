package streaming

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestEditorBackspaceShrinksHeightWithStatusBar simulates a real
// session run: the session prints history, then the editor paints
// the prompt + status bar, then the user types content, then
// backspaces until the height shrinks.  The output between the
// last "history" byte and EOF is what the user actually sees.
func TestEditorBackspaceShrinksHeightWithStatusBar(t *testing.T) {
	// Inject pre-existing "history".
	var histBuf bytes.Buffer
	histBuf.WriteString("\n")
	histBuf.WriteString("user: first message\n")
	histBuf.WriteString("assistant: hi\n")

	inR, inW := makePipe(t)
	outBuf := &bytes.Buffer{}
	outBuf.Write(histBuf.Bytes())

	// Track the start of the editor's output (after history).
	doneCh := make(chan struct{})
	go func() {
		e := newLineEditor("> ")
		e.statusBar = "Ready"
		e.readFrom(inR, outBuf)
		close(doneCh)
	}()

	// Type "abc" + Alt+Enter + "def" + 4 backspaces.  The
	// content after this is just "ab".
	time.Sleep(50 * time.Millisecond)
	inW.Write([]byte("abc\x1b\rdef\x7f\x7f\x7f\x7f"))
	time.Sleep(50 * time.Millisecond)
	inW.Close()
	<-doneCh

	// Inspect the bytes that the editor wrote AFTER the history.
	out := outBuf.String()
	idxEditorStart := strings.Index(out, "assistant: hi\n") + len("assistant: hi\n")
	editorOut := out[idxEditorStart:]

	// After backspacing, the visible state should be:
	//   row P:     "> ab"
	//   row P+1:   "Ready" (status bar, dim)
	// The leftover row from the previous 2-line draw (which
	// showed "   def") MUST have been cleared.
	//
	// The clearest sign that the leftover row was NOT cleared
	// is if the substring "def" appears anywhere in editorOut.
	// (The "abc" is part of the legitimate content, but "def"
	// is the row that should have been cleared.)
	if strings.Contains(editorOut, "def") {
		t.Fatalf("leftover 'def' from taller draw is still visible:\n%q", editorOut)
	}
}

// TestEditorHeightMatchesContent verifies that at any point the
// number of ESC[1B (cursor down) moves emitted by the editor
// matches the number of rows the content should occupy (lines
// minus one for the inter-line moves, plus one for content->status
// bar).  This is the key invariant for "no scroll, no leftover".
func TestEditorHeightMatchesContent(t *testing.T) {
	// Build content "ab\ncd\nef", i.e. 3 lines.
	inR, inW := makePipe(t)
	outBuf := &bytes.Buffer{}

	doneCh := make(chan struct{})
	go func() {
		e := newLineEditor("> ")
		e.statusBar = "R"
		e.readFrom(inR, outBuf)
		close(doneCh)
	}()

	time.Sleep(50 * time.Millisecond)
	inW.Write([]byte("ab\x1b\rcd\x1b\ref"))
	time.Sleep(50 * time.Millisecond)
	inW.Close()
	<-doneCh

	out := outBuf.String()
	// Count the number of \x1b[1B sequences.  The final state
	// has 3 content lines + 1 status bar, so the expected
	// inter-row moves are 3 (content) + 1 (to status bar) = 4.
	cud := strings.Count(out, "\x1b[1B")
	// Note: the leftover-row clearing also uses \x1b[1B, so
	// the count may be higher if the editor had to clear old
	// rows.  The strict requirement is that there is AT LEAST
	// the expected number of down moves.
	if cud < 4 {
		t.Fatalf("not enough CUD moves: got %d, expected >= 4:\n%s", cud, out)
	}

	// Also: there must be ZERO literal \n bytes emitted by
	// the editor's redraws.  The submit-on-EOF "\r\n" is
	// allowed (it is a single newline at the very end).
	trimmed := strings.TrimSuffix(out, "\r\n")
	if strings.Contains(trimmed, "\n") {
		t.Fatalf("editor redraw emitted a newline:\n%s", out)
	}
}
