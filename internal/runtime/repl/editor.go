package repl

import (
	"fmt"
	"io"
	"strings"
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

	e.content = e.content[:0]
	e.pos = 0

	// isPasteLine is set when the first Read() call of a new input
	// cycle already contains one or more line breaks.  When true,
	// embedded newlines are inserted literally instead of submitted.
	isPasteLine := false

	for {
		buf := make([]byte, 64)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		b := buf[:n]

		// Detect paste: if this is the very first read and the input
		// contains more than just whitespace / a single newline, or
		// contains multiple lines, treat newlines as literal inserts.
		if !isPasteLine && n > 0 {
			newlineCount := 0
			nonSpaceCount := 0
			for _, bb := range b {
				if bb == '\r' || bb == '\n' {
					newlineCount++
				} else if bb > 0x20 {
					nonSpaceCount++
				}
			}
			if nonSpaceCount > 0 && newlineCount >= 1 {
				isPasteLine = true
			}
		}

		// Escape sequence (arrow keys, Alt+Enter, etc.)
		if b[0] == '\x1b' && n > 1 {
			e.handleEscape(b)
			continue
		}

		for i := 0; i < n; {
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			switch {
			case r == '\r' || r == '\n':
				if isPasteLine {
					e.insertRune('\n')
					i += size
					continue
				}
				// Backslash-Enter: swallow trailing backslash.
				if r == '\n' && e.pos > 0 && e.content[e.pos-1] == '\\' {
					e.pos--
					e.content = append(e.content[:e.pos], e.content[e.pos+1:]...)
					e.insertRune('\n')
					e.draw()
					break
				}
				fmt.Fprint(os.Stdout, "\r\n")
				return string(e.content), nil
			case r == 0x03 || r == 0x04:
				fmt.Fprint(os.Stdout, "\r\n")
				return "", io.EOF
			case r == 0x7f || r == 0x08:
				e.deleteRuneBefore()
			case r == 0x15:
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
		e.draw()
	}
}

// draw redraws the current line (prompt + content) on stdout.
// It uses \r to return to column 0, reprints everything, then
// positions the cursor at the correct display column.
func (e *lineEditor) draw() {
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

	fmt.Fprint(os.Stdout, "\x1b7")

	if cursorLine > 0 {
		fmt.Fprintf(os.Stdout, "\x1b[%dA", cursorLine)
	}

	prefix := e.prompt
	for i, line := range lines {
		fmt.Fprintf(os.Stdout, "\r%s%s\x1b[K", prefix, line)
		if i < len(lines)-1 {
			fmt.Fprint(os.Stdout, "\n")
			prefix = strings.Repeat(" ", uniseg.StringWidth(e.prompt))
		}
	}

	if cursorCol > 0 {
		fmt.Fprintf(os.Stdout, "\r> \x1b[%dG", cursorCol+3)
	} else {
		fmt.Fprint(os.Stdout, "\r> ")
	}

	fmt.Fprint(os.Stdout, "\x1b8")
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
		e.draw()
		return
	}
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
	if e.content[e.pos] == '\n' {
		e.pos++
		e.draw()
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
