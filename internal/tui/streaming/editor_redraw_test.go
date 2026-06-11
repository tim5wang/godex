package streaming

import (
	"strings"
	"testing"
)

// TestEditorRedrawsInPlaceOnSingleLine verifies that when the user
// types a single character, the editor redraws the prompt in place
// without scrolling the terminal.  This is the central invariant
// the user complained about: every keystroke should not push the
// conversation history up by one row.
func TestEditorRedrawsInPlaceOnSingleLine(t *testing.T) {
	out, _ := runEditorWithInput(t, "a")
	// Count the number of "\n" (LF) bytes that appear in the
	// output.  Each newline forces a terminal scroll.  The only
	// legitimate newlines in drawTo are the ones between content
	// lines and the one between content and status bar.  For a
	// single line of content we expect exactly one newline (content
	// -> status bar).
	//
	// Crucially, the FIRST draw (initial draw with empty content)
	// already includes one newline because empty content still
	// needs the status bar below it.  Then a single character
	// should not add another newline beyond the "content to
	// status bar" separator.  Two newlines in the output is OK
	// (initial + one content draw).  Three is a bug.
	if strings.Count(out, "\n") > 2 {
		t.Fatalf("redraw scrolled the terminal: %d newlines in output:\n%s",
			strings.Count(out, "\n"), out)
	}
}

// TestEditorBackspaceShrinksHeight verifies that after pressing
// backspace the input box shrinks (the leftover line is cleared).
// The user's complaint: "del删除输入, 输入框不会变回来, 而是让
// 输入过的内容repeat成了多行".
func TestEditorBackspaceShrinksHeight(t *testing.T) {
	// Build a 3-line buffer, then send one backspace (0x7f).  The
	// backspace should delete the last character; the leftover
	// line (the trailing newline that was inserted with the
	// backspaced newline) should NOT remain on screen.
	out, _ := runEditorWithInput(t, "L1\x1b\rL2\x1b\rL3\x7f")
	// If the bug is present, the redraw leaves the leftover
	// content from a previous longer draw visible.  We check
	// that the leftover `L3` (now one char shorter) is NOT
	// followed by a stale `L3` (length 3) on a later line.
	// Concretely: count occurrences of the substring `L3` (the
	// post-backspace line is just `L`); a leftover would show
	// additional `L3` rendering on a different row.
	if strings.Count(out, "L3\x1b[K") != 0 {
		t.Fatalf("backspace left a stale line: %q", out)
	}
}

// TestEditorHeightShrinksAfterDeletingNewline verifies that when
// the user deletes the newline that joins two lines (so the
// buffer shrinks from 2 lines to 1), the on-screen input box
// shrinks too.  This is the exact failure the user reported:
// "输入框不会变回来, 而是让输入过的内容repeat成了多行".
func TestEditorHeightShrinksAfterDeletingNewline(t *testing.T) {
	// Build "ab\ncd" (3 lines on screen: prompt + 2 content),
	// then send 3 backspaces to remove the last 3 chars
	// ("cd\n" plus one of the LFs).  After deletion the buffer
	// is "ab" (1 line on screen) and the leftover rows from
	// the previous taller draw must be cleared.
	out, _ := runEditorWithInput(t, "ab\x1b\rcd\x7f\x7f\x7f")
	// The prompt row in the FINAL draw should show "ab" with no
	// leftover continuation row.  We confirm by checking that
	// the output does not contain the substring "cd" anywhere
	// (the old content was fully deleted).
	if strings.Contains(out, "cd") {
		t.Fatalf("stale content 'cd' remains on screen after backspace:\n%s", out)
	}
}
