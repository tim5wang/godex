package repl

import (
	"testing"

	"github.com/rivo/uniseg"
)

// TestGraphemeCursorLeftSkipsFullChineseRune is the core regression
// test for the "REPL arrow-left moves half a Chinese character" bug.
//
// Old behaviour: chzyer/readline v1.5.1's left/right cursor moves by
// raw byte offset. A Chinese character is 3 UTF-8 bytes but occupies
// 2 display columns. So one left-arrow from the end of "你" left
// the cursor between the first and second bytes of that character,
// i.e. visually "half a character". This test pins the contract
// that the new grapheme-aware cursor moves by full grapheme cluster
// at a time, regardless of byte width.
func TestGraphemeCursorLeftSkipsFullChineseRune(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		from int
		want int
	}{
		{name: "ascii", line: "abc", from: 3, want: 2},
		{name: "single CJK", line: "你", from: len("你"), want: 0},
		{name: "two CJK at end", line: "你好", from: len("你好"), want: len("你")},
		{name: "ascii then CJK", line: "a你", from: len("a你"), want: len("a")},
		{name: "CJK then ascii", line: "你a", from: len("你a"), want: len("你")},
		{name: "mixed", line: "ab你cd", from: len("ab你cd"), want: len("ab你c")},
		{name: "from middle of CJK", line: "你", from: 1, want: 0},
		{name: "at start stays", line: "你", from: 0, want: 0},
		{name: "empty", line: "", from: 0, want: 0},
		{name: "past end clamps", line: "你", from: 99, want: 0},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := graphemeCursorLeft(c.line, c.from)
			if got != c.want {
				t.Fatalf("graphemeCursorLeft(%q, %d): got %d, want %d", c.line, c.from, got, c.want)
			}
		})
	}
}

// TestGraphemeCursorRightAdvancesFullChineseRune is the symmetric
// half of the regression suite. A right-arrow from position 0 in
// "你好" must land on len("你"), not on byte offset 1 (which would
// be the middle of 你).
func TestGraphemeCursorRightAdvancesFullChineseRune(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		from int
		want int
	}{
		{name: "ascii", line: "abc", from: 0, want: 1},
		{name: "single CJK", line: "你", from: 0, want: len("你")},
		{name: "two CJK from start", line: "你好", from: 0, want: len("你")},
		{name: "ascii then CJK", line: "a你", from: 1, want: len("a你")},
		{name: "CJK then ascii", line: "你a", from: 0, want: len("你")},
		{name: "from middle of CJK", line: "你", from: 1, want: len("你")},
		{name: "at end stays", line: "你", from: len("你"), want: len("你")},
		{name: "empty", line: "", from: 0, want: 0},
		{name: "past end clamps", line: "你", from: 99, want: len("你")},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := graphemeCursorRight(c.line, c.from)
			if got != c.want {
				t.Fatalf("graphemeCursorRight(%q, %d): got %d, want %d", c.line, c.from, got, c.want)
			}
		})
	}
}

// TestGraphemeCursorHandlesZeroWidthAndEmojiSequence exercises the
// grapheme cluster grouping rivo/uniseg provides. The ZWJ family
// emoji 👨‍👩‍👧 (man + ZWJ + woman + ZWJ + girl) is a single grapheme
// cluster: 7 codepoints, 25 UTF-8 bytes, display width 2. A naive
// rune-based cursor would treat each codepoint separately; a
// naive byte-based cursor would stop in the middle of a ZWJ
// sequence. The grapheme cursor must step over the whole cluster
// in one move.
func TestGraphemeCursorHandlesZeroWidthAndEmojiSequence(t *testing.T) {
	t.Parallel()

	emoji := "👨‍👩‍👧"
	cjkEmojiMix := "a" + emoji + "b"

	// Sanity: rivo/uniseg reports this whole sequence as one grapheme
	// cluster. If this assertion breaks, the test data is no longer
	// representative and should be re-derived.
	if n := uniseg.GraphemeClusterCount(cjkEmojiMix); n != 3 {
		t.Fatalf("expected 3 grapheme clusters in %q, got %d", cjkEmojiMix, n)
	}

	// From end of "a👨‍👩‍👧b" one left-arrow should land at
	// byte offset len("a" + emoji) (one cluster before b).
	want := len("a") + len(emoji)
	if got := graphemeCursorLeft(cjkEmojiMix, len(cjkEmojiMix)); got != want {
		t.Fatalf("graphemeCursorLeft full: got %d, want %d", got, want)
	}
	// From len("a") one right-arrow should land at len("a"+emoji).
	if got := graphemeCursorRight(cjkEmojiMix, len("a")); got != want {
		t.Fatalf("graphemeCursorRight to emoji: got %d, want %d", got, want)
	}
}
