package repl

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
	"github.com/rivo/uniseg"
)

// lineEditor is a minimal grapheme-aware line editor that reads one
// line from stdin and writes the prompt/echo to stdout.
//
// Unlike the previous bubbles/textarea Editor, this implementation
// does NOT create a bubbletea Program.  It reads/writes the raw
// terminal directly using ANSI escape sequences for cursor
// positioning.  Arrow keys use the graphemeCursorLeft/Right
// primitives so CJK and emoji sequences move by full grapheme
// clusters instead of raw bytes.
//
// The editor manages its own rune buffer so insertion and deletion
// happen at rune boundaries regardless of the underlying UTF-8
// byte length of each character.
type lineEditor struct {
	prompt string
	// content is the text the user has typed so far, stored as runes.
	content []rune
	// pos is the cursor position in rune indices (not byte offsets).
	pos int
	// lastLines is the number of content lines the previous
	// drawTo() call painted.  When the new draw has fewer
	// lines (e.g. the user pressed backspace enough to merge
	// two lines), drawTo uses this to know how many rows to
	// walk up to reach the top of the editable area, and how
	// many leftover rows to clear.
	lastLines int
	// lastCursorRow is the row (counted from the prompt row,
	// 0-indexed) the cursor was placed on by the previous
	// drawTo() call.  We use this instead of lastLines-1 to
	// walk up to the prompt row in the new draw, so we never
	// overshoot into the history above.
	lastCursorRow int
}

// newLineEditor creates an editor that will print the given prompt.
func newLineEditor(prompt string) *lineEditor {
	return &lineEditor{prompt: prompt}
}

// ReadLine reads one line of input from os.Stdin.  It supports
// multiline input via two sequences:
//   - Alt+Enter (\x1b\r): most terminals send this in raw mode as
//     ESC followed by carriage return.
//   - Backslash-Enter (\ followed by Enter): a portable fall-back
//     when the terminal does not send Alt+Enter consistently.
//
// Paste detection: when a single Read() call carries more than one
// line of text the editor treats the embedded newlines as literal
// \n insertions rather than submit signals, so a multi-line paste
// is accepted as one input.
//
// Plain Enter (\r or \n submitted alone) submits the line.
// Ctrl+C / Ctrl+D quit.
func (e *lineEditor) ReadLine() (string, error) {
	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return "", fmt.Errorf("line editor: enter raw mode: %w", err)
	}
	defer term.Restore(os.Stdin.Fd(), state)
	return e.readFrom(os.Stdin, os.Stdout)
}

// readFrom is the test-friendly inner loop.  It tokenizes each
// Read burst so escape sequences embedded mid-burst (e.g. Alt+Enter
// arriving in the same Read as the preceding text) are handled
// correctly.
func (e *lineEditor) readFrom(r io.Reader, w io.Writer) (string, error) {
	e.content = e.content[:0]
	e.pos = 0
	e.drawTo(w)
	for {
		buf := make([]byte, 64)
		n, err := r.Read(buf)
		if err != nil {
			if err == io.EOF && len(e.content) > 0 {
				// Pipe closed mid-paste: submit whatever we have.
				fmt.Fprint(w, "\r\n")
				return string(e.content), nil
			}
			return "", err
		}
		b := buf[:n]

		// Paste detection: if this burst contains two-or-more
		// newlines together with a meaningful amount of non-whitespace
		// content (>=4 bytes), treat the newlines as literal inserts
		// so the entire paste becomes one input.  The 4-byte threshold
		// keeps single-line input (e.g. backslash-Enter) from being
		// misclassified as paste.
		isPaste := detectPaste(b)

		i := 0
		for i < n {
			c := b[i]
			switch {
			case c == '\x1b' && i+1 < n:
				// Escape sequence starting at b[i].  Find its end:
				// CSI ends at the first byte in 0x40..0x7E; SS3 is
				// two bytes.  For our needs (arrow keys, Alt+Enter)
				// we only need to look at b[i+1] and (sometimes) b[i+2].
				seqLen := 1
				switch b[i+1] {
				case '[':
					if i+2 < n {
						seqLen = 3
					}
				case '\r', '\n':
					seqLen = 2
				}
				e.handleEscape(b[i : i+seqLen])
				i += seqLen
				continue
			case c == '\r' || c == '\n':
				if isPaste {
					e.insertRune('\n')
					i++
					continue
				}
				// Backslash-Enter fall-back: swallow the backslash and
				// insert a newline so the user can break the line on
				// any terminal.  Otherwise submit the line.
				if c == '\n' && e.pos > 0 && e.content[e.pos-1] == '\\' {
					e.pos--
					e.content = append(e.content[:e.pos], e.content[e.pos+1:]...)
					e.insertRune('\n')
					i++
					continue
				}
				// Paint the final content state so the user
				// sees what they typed before we move the
				// cursor down to the submit line.  Without
				// this, the prompt row would still show the
				// previous draw's state (or nothing at all
				// if the entire input arrived in one burst).
				e.drawTo(w)
				fmt.Fprint(w, "\r\n")
				return string(e.content), nil
			case c == 0x03 || c == 0x04:
				fmt.Fprint(w, "\r\n")
				return "", io.EOF
			case c == 0x7f || c == 0x08:
				e.deleteRuneBefore()
				i++
				continue
			case c == 0x15:
				e.content = e.content[:0]
				e.pos = 0
				i++
				continue
			default:
				r, size := utf8.DecodeRune(b[i:])
				if r == utf8.RuneError && size == 1 {
					i++
					continue
				}
				e.insertRune(r)
				i += size
			}
		}

		e.drawTo(w)
	}
}

// detectPaste returns true when the input burst looks like a
// paste: at least TWO non-escape newlines together with at
// least 4 non-whitespace content bytes.  See
// streaming.detectPaste for the full rationale; the two stay
// in sync.
func detectPaste(b []byte) bool {
	var newlineCount, hasContent int
	for i, c := range b {
		switch c {
		case '\r', '\n':
			// An ESC immediately before this newline means
			// it's part of an Alt+Enter sequence, not a
			// paste-newline.
			if i > 0 && b[i-1] == '\x1b' {
				continue
			}
			newlineCount++
		case 0x20, '\t':
			// whitespace is not "content" by itself
		default:
			hasContent++
		}
	}
	return newlineCount >= 2 && hasContent >= 4
}

// handleEscape processes ANSI escape sequences for cursor keys.
func (e *lineEditor) handleEscape(b []byte) {
	if len(b) < 2 {
		return
	}
	if b[0] != '\x1b' {
		return
	}
	switch b[1] {
	case '[':
		if len(b) < 3 {
			return
		}
		switch b[2] {
		case 'C':
			e.moveCursorRight()
		case 'D':
			e.moveCursorLeft()
		}
	case '\r', '\n':
		e.insertRune('\n')
	}
}

// drawTo is the testable variant of draw: writes the editable
// region onto w without ever emitting a newline. Newlines in
// the terminal scroll the history above the prompt upward; we
// use explicit CUD/CUU moves to walk between rows instead.
//
// The caller is expected to have placed the cursor on the
// prompt row before calling drawTo.  After drawTo returns the
// cursor is on the content line and column indicated by e.pos.
//
// To handle shrinking (e.g. backspace merged two lines), we
// track the previous line count in e.lastLines and clear the
// leftover rows so they don't show stale characters.
func (e *lineEditor) drawTo(w io.Writer) {
	lines := strings.Split(string(e.content), "\n")

	// Find cursor visual position.
	cursorLine := 0
	cursorCol := 0
	pos := e.pos
	for li, line := range lines {
		runes := []rune(line)
		if pos <= len(runes) {
			cursorLine = li
			cursorCol = uniseg.StringWidth(string(runes[:pos]))
			break
		}
		pos -= len(runes) + 1
	}

	continuationCol := uniseg.StringWidth(e.prompt)
	newLines := len(lines)

	// Walk up to the top of the editable area.  The previous
	// draw left the cursor on row P + lastCursorRow (counted
	// from the prompt row P).  Walking up lastCursorRow rows
	// lands us exactly on P, no matter where the cursor was.
	// This is critical: walking up "lastLines-1" rows would
	// overshoot the prompt row when the new content is much
	// taller than the previous draw (e.g. a big paste arrives
	// in one burst), wiping the conversation history above
	// the prompt with paint+clear.
	if e.lastCursorRow > 0 {
		fmt.Fprintf(w, "\x1b[%dA", e.lastCursorRow)
	}

	// Paint every content line, top to bottom, using CUD
	// (cursor down) to descend without scrolling.
	for i, line := range lines {
		prefix := e.prompt
		if i > 0 {
			prefix = strings.Repeat(" ", continuationCol)
		}
		fmt.Fprintf(w, "\r%s%s\x1b[K", prefix, line)
		if i < newLines-1 {
			fmt.Fprint(w, "\x1b[1B")
		}
	}

	// If the new content is shorter than the previous draw,
	// clear the leftover rows so they don't show stale
	// characters.
	if e.lastLines > newLines {
		leftover := e.lastLines - newLines
		for j := 0; j < leftover; j++ {
			fmt.Fprint(w, "\x1b[1B\r\x1b[K")
		}
		fmt.Fprintf(w, "\x1b[%dA", leftover)
	}

	// Walk back up to the cursor's content line and position.
	walkUp := newLines - cursorLine
	if walkUp > 0 {
		fmt.Fprintf(w, "\x1b[%dA", walkUp)
	}
	fmt.Fprintf(w, "\x1b[%dG", cursorCol+continuationCol+1)

	e.lastLines = newLines
	e.lastCursorRow = cursorLine
}

// draw is the production wrapper around drawTo.
func (e *lineEditor) draw() {
	e.drawTo(os.Stdout)
}

// insertRune inserts r at the current cursor position.
func (e *lineEditor) insertRune(r rune) {
	// Extend or insert.
	e.content = append(e.content, 0) // make room
	copy(e.content[e.pos+1:], e.content[e.pos:])
	e.content[e.pos] = r
	e.pos++
}

// deleteRuneBefore removes the rune immediately before the cursor.
func (e *lineEditor) deleteRuneBefore() {
	if e.pos <= 0 {
		return
	}
	e.pos--
	e.content = append(e.content[:e.pos], e.content[e.pos+1:]...)
}

// moveCursorLeft moves the cursor one grapheme cluster to the left,
// wrapping from the start of a continuation line to the end of
// the previous line.
func (e *lineEditor) moveCursorLeft() {
	if e.pos <= 0 {
		return
	}
	// Multiline wraparound: if the character before the cursor is a
	// newline, jump to the end of the previous line.
	if e.pos > 0 && e.content[e.pos-1] == '\n' {
		if e.pos-2 >= 0 {
			e.pos -= 2
			for e.pos > 0 && e.content[e.pos] != '\n' {
				e.pos--
			}
			if e.content[e.pos] == '\n' {
				e.pos++
			} else {
				e.pos = 0
			}
			for e.pos < len(e.content) && e.content[e.pos] != '\n' {
				e.pos++
			}
		} else {
			e.pos = 0
		}
		return
	}
	line := string(e.content[:e.pos])
	bytePos := runeOffsetToByteOffset(e.content[:e.pos], e.pos)
	newBytePos := graphemeCursorLeft(line, bytePos)
	e.pos = byteOffsetToRuneOffset(e.content, newBytePos)
}

// moveCursorRight moves the cursor one grapheme cluster to the right,
// wrapping from the end of a line to the start of the next line.
func (e *lineEditor) moveCursorRight() {
	if e.pos >= len(e.content) {
		return
	}
	if e.content[e.pos] == '\n' {
		e.pos++
		return
	}
	line := string(e.content)
	bytePos := runeOffsetToByteOffset(e.content, e.pos)
	newBytePos := graphemeCursorRight(line, bytePos)
	e.pos = byteOffsetToRuneOffset(e.content, newBytePos)
}

// runeOffsetToByteOffset converts a rune index to a byte offset
// in the same content represented as a string.
func runeOffsetToByteOffset(runes []rune, pos int) int {
	if pos <= 0 {
		return 0
	}
	if pos >= len(runes) {
		pos = len(runes)
	}
	offset := 0
	for i := 0; i < pos; i++ {
		offset += utf8.RuneLen(runes[i])
	}
	return offset
}

// byteOffsetToRuneOffset converts a byte offset to a rune index.
func byteOffsetToRuneOffset(runes []rune, bytePos int) int {
	if bytePos <= 0 {
		return 0
	}
	offset := 0
	for i, r := range runes {
		offset += utf8.RuneLen(r)
		if offset > bytePos {
			return i
		}
	}
	return len(runes)
}
