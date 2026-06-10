package repl

import (
	"fmt"
	"io"
	"os"
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
}

// newLineEditor creates an editor that will print the given prompt.
func newLineEditor(prompt string) *lineEditor {
	return &lineEditor{prompt: prompt}
}

// ReadLine reads one line of input from os.Stdin.
//
// The editor puts stdin in raw mode so it can read individual
// keystrokes (including ANSI escape sequences for arrow keys).
// The terminal is restored before ReadLine returns.
//
// It returns the line (without trailing newline) or io.EOF when
// the user presses Ctrl+C or Ctrl+D.
func (e *lineEditor) ReadLine() (string, error) {
	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return "", fmt.Errorf("line editor: enter raw mode: %w", err)
	}
	defer term.Restore(os.Stdin.Fd(), state)

	e.content = e.content[:0]
	e.pos = 0
	e.draw()

	for {
		buf := make([]byte, 64)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		b := buf[:n]

		// Escape sequence (arrow keys, etc.)
		if b[0] == '\x1b' && n > 1 {
			e.handleEscape(b)
			continue
		}

		// Single byte or multi-byte UTF-8 input
		for i := 0; i < n; {
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size == 1 {
				// Skip invalid bytes
				i++
				continue
			}
			switch {
			case r == '\r' || r == '\n':
				// Enter – submit line
				fmt.Fprint(os.Stdout, "\r\n")
				return string(e.content), nil
			case r == 0x03 || r == 0x04:
				// Ctrl+C / Ctrl+D – quit
				fmt.Fprint(os.Stdout, "\r\n")
				return "", io.EOF
			case r == 0x7f || r == 0x08:
				// Backspace (DEL or BS)
				e.deleteRuneBefore()
			case r == 0x15:
				// Ctrl+U – delete whole line
				e.content = e.content[:0]
				e.pos = 0
			default:
				e.insertRune(r)
			}
			i += size
		}
		e.draw()
	}
}

// handleEscape processes ANSI escape sequences for cursor keys.
func (e *lineEditor) handleEscape(b []byte) {
	if len(b) < 3 {
		return
	}
	// Arrow sequences: \x1b[A, \x1b[B, \x1b[C, \x1b[D
	if b[0] != '\x1b' || b[1] != '[' {
		return
	}
	switch b[2] {
	case 'C': // Right
		e.moveCursorRight()
	case 'D': // Left
		e.moveCursorLeft()
	}
}

// draw redraws the current line (prompt + content) on stdout.
// It uses \r to return to column 0, reprints everything, then
// positions the cursor at the correct display column.
func (e *lineEditor) draw() {
	// Build the full line string from the rune buffer.
	line := string(e.content)

	// Calculate the display column of the cursor.
	// Prompt + display width of content[:pos] in columns.
	before := string(e.content[:e.pos])
	cursorCol := uniseg.StringWidth(e.prompt) + uniseg.StringWidth(before)

	// Redraw the line: carriage return, prompt + content, clear to EOL.
	fmt.Fprintf(os.Stdout, "\r%s%s\x1b[K", e.prompt, line)
	// Move cursor to the correct column.
	if cursorCol > 0 {
		fmt.Fprintf(os.Stdout, "\x1b[%dG", cursorCol+1)
	}
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

// moveCursorLeft moves the cursor one grapheme cluster to the left.
func (e *lineEditor) moveCursorLeft() {
	if e.pos <= 0 {
		return
	}
	// Convert rune-buffer slice to string for the grapheme primitive.
	line := string(e.content[:e.pos])
	bytePos := runeOffsetToByteOffset(e.content[:e.pos], e.pos)
	newBytePos := graphemeCursorLeft(line, bytePos)
	e.pos = byteOffsetToRuneOffset(e.content, newBytePos)
	e.draw()
}

// moveCursorRight moves the cursor one grapheme cluster to the right.
func (e *lineEditor) moveCursorRight() {
	if e.pos >= len(e.content) {
		return
	}
	line := string(e.content)
	bytePos := runeOffsetToByteOffset(e.content, e.pos)
	newBytePos := graphemeCursorRight(line, bytePos)
	e.pos = byteOffsetToRuneOffset(e.content, newBytePos)
	e.draw()
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
