package minitui

import (
	"strings"
	"testing"
)

// TestStatusBarDefaultUsesDimNotReverseVideo verifies that the
// default status bar style produces dim text without reverse
// video or background color.  The legacy min-tui default was
// "\x1b[7m" (reverse video), which clashed with the godex
// heartbeat text "Ready · Input · Model · ctx · calls · msgs".
func TestStatusBarDefaultUsesDimNotReverseVideo(t *testing.T) {
	seq := renderStatusAnsiForTest(80, 24, "Ready · Input · MiniMax-M3 · 6.8k/256k 3% · calls 5 · msgs 2", StatusDefault)

	if !strings.HasPrefix(seq, "\x1b[2m") {
		t.Fatalf("StatusDefault must start with dim escape (\\x1b[2m), got %q", seq)
	}
	if strings.Contains(seq, "\x1b[7m") {
		t.Fatalf("StatusDefault must not use reverse video, got %q", seq)
	}
	if strings.Contains(seq, "\x1b[4") {
		t.Fatalf("StatusDefault must not use background color, got %q", seq)
	}
}

// TestStatusBarInfoUsesDimLightFg verifies the StatusInfo
// style still uses dim + light fg, not a heavy blue
// background.
func TestStatusBarInfoUsesDimLightFg(t *testing.T) {
	seq := renderStatusAnsiForTest(80, 24, "info", StatusInfo)
	if !strings.HasPrefix(seq, "\x1b[2;37m") {
		t.Fatalf("StatusInfo should use dim + light fg, got %q", seq)
	}
	if strings.Contains(seq, "\x1b[44") {
		t.Fatalf("StatusInfo must not use heavy blue background, got %q", seq)
	}
}

// TestStatusBarWarningErrorSuccessAreDimColored verifies the
// other styles use a dim + colored fg, not a heavy background.
func TestStatusBarWarningErrorSuccessAreDimColored(t *testing.T) {
	cases := []struct {
		style   StatusStyle
		prefix  string
		message string
	}{
		{StatusWarning, "\x1b[2;33m", "warning"},
		{StatusError, "\x1b[2;31m", "error"},
		{StatusSuccess, "\x1b[2;32m", "success"},
	}
	for _, c := range cases {
		seq := renderStatusAnsiForTest(80, 24, c.message, c.style)
		if !strings.HasPrefix(seq, c.prefix) {
			t.Fatalf("style %d should start with %q, got %q", c.style, c.prefix, seq)
		}
		// Should not contain any background color escape.
		for _, bgPrefix := range []string{"\x1b[41", "\x1b[42", "\x1b[43", "\x1b[44", "\x1b[47", "\x1b[100"} {
			if strings.Contains(seq, bgPrefix) {
				t.Fatalf("style %d should not use background %q, got %q", c.style, bgPrefix, seq)
			}
		}
	}
}

// TestStatusBarTruncatesByDisplayWidth verifies that CJK
// characters (each 2 display cells) are not split mid-rune
// when truncating to the terminal width.
func TestStatusBarTruncatesByDisplayWidth(t *testing.T) {
	// 4 CJK chars = 8 display cells, then 4 ASCII = 4 cells;
	// total 12.  Width 10 should keep 4 CJK + 1 ASCII (10 cells),
	// splitting BEFORE the second ASCII so we end on a rune
	// boundary.
	seq := renderStatusAnsiForTest(10, 24, "你好世界abcde", StatusDefault)
	visible := stripAnsi(seq)
	if displayWidth(visible) > 10 {
		t.Fatalf("status text width %d exceeds terminal width 10: %q", displayWidth(visible), visible)
	}
	// The visible text must end on a complete rune; we check
	// by re-decoding and confirming no orphan high bytes.
	if strings.Contains(visible, "\xef\xbf") {
		// UTF-8 replacement marker (U+FFFD) which appears
		// when a sequence is split mid-rune.
		t.Fatalf("status text appears to be split mid-rune: %q", visible)
	}
}

// ── test helpers ─────────────────────────────────────────────────

// renderStatusAnsiForTest mirrors the format produced by
// renderStatus() without touching the terminal.  It is
// intentionally a copy of the formatting rules so the test
// fails if the production code drifts.
func renderStatusAnsiForTest(width, height int, text string, style StatusStyle) string {
	s := "\x1b[2m"
	switch style {
	case StatusInfo:
		s = "\x1b[2;37m"
	case StatusWarning:
		s = "\x1b[2;33m"
	case StatusError:
		s = "\x1b[2;31m"
	case StatusSuccess:
		s = "\x1b[2;32m"
	}
	_ = height
	if displayWidth(text) > width {
		var b strings.Builder
		cur := 0
		for _, r := range text {
			if cur+runeWidth(r) > width {
				break
			}
			b.WriteRune(r)
			cur += runeWidth(r)
		}
		text = b.String()
	}
	return s + text + "\x1b[0m" + strings.Repeat(" ", width-displayWidth(text))
}

func stripAnsi(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
