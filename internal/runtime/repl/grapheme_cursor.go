package repl

import (
	"github.com/rivo/uniseg"
)

// graphemeCursorLeft returns the byte offset of the grapheme cluster
// immediately before the one starting at from. If from sits in the
// middle of a cluster, the cluster's start is used as the anchor.
//
// This is the grapheme-aware counterpart of `from - 1` that a naive
// byte-based cursor would do. chzyer/readline v1.5.1 uses the byte
// form, which is why pressing left-arrow once on a Chinese character
// left the cursor "half a character" in: the character occupies 3
// bytes but a single grapheme cluster, and the user's mental model
// is the grapheme cluster.
//
// Algorithm: walk clusters from the start of the line; the answer
// is the start of the LAST cluster whose end is <= from. Equivalently:
// the first cluster whose end is > from replaces our running
// candidate, and the candidate before that substitution is the
// answer.
//
// Out-of-range inputs are clamped: from < 0 → 0; from > len(line) →
// len(line); the result is then snapped to a valid cluster boundary.
func graphemeCursorLeft(line string, from int) int {
	if len(line) == 0 {
		return 0
	}
	if from <= 0 {
		return 0
	}
	if from >= len(line) {
		from = len(line)
	}
	clusters := uniseg.NewGraphemes(line)
	candidate := 0
	for clusters.Next() {
		start, end := clusters.Positions()
		if end > from {
			return candidate
		}
		candidate = start
	}
	// Reached the end of the line; candidate holds the start of the
	// final cluster, which is what we want when from is at the end.
	return candidate
}

// graphemeCursorRight returns the byte offset of the grapheme cluster
// immediately after the one ending at from. If from sits in the
// middle of a cluster, the cluster's end is used as the anchor.
//
// Symmetric counterpart of graphemeCursorLeft.
func graphemeCursorRight(line string, from int) int {
	if len(line) == 0 {
		return 0
	}
	if from >= len(line) {
		return len(line)
	}
	if from <= 0 {
		from = 0
	}
	// Snap to the next rune boundary at or after from.
	boundary := nextRuneBoundary(line, from)
	clusters := uniseg.NewGraphemes(line[boundary:])
	for clusters.Next() {
		_, end := clusters.Positions()
		return boundary + end
	}
	return len(line)
}

// nextRuneBoundary returns the smallest index >= i that is a rune start.
// Walking forward over continuation bytes until we hit the byte that
// starts a new code point.
func nextRuneBoundary(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i < len(s) && utf8Continuation(s[i]) {
		i++
	}
	return i
}

// utf8Continuation reports whether b is a UTF-8 continuation byte
// (10xxxxxx, i.e. 0x80..0xBF). Defined locally to avoid pulling in
// the unicode/utf8 package for a single 2-line check.
func utf8Continuation(b byte) bool {
	return b&0xC0 == 0x80
}
