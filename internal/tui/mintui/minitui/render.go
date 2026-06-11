package minitui

import (
	"fmt"
	"os"
	"strings"
)

// ── input box ────────────────────────────────────────────────────

func (t *TUI) renderInputBox() {
	bc := t.borderColor
	brd := bc + strings.Repeat("─", t.width) + ansiReset

	t.writeRow(t.inTopBorder(), brd)
	vs := t.inContentStart()
	for i := 0; i < t.inHeight; i++ {
		idx := t.inScrollRow + i
		if idx < len(t.inLines) {
			s := string(t.inLines[idx])
			if len(s) > t.width {
				s = s[:t.width]
			}
			t.writeRow(vs+i, pad(s, t.width))
		} else {
			t.writeRow(vs+i, "")
		}
	}
	t.writeRow(t.inBotBorder(), brd)
}

// ── status bar ───────────────────────────────────────────────────

// renderStatus paints the status bar on the last row.
//
// Visual style: dim text (no background, no reverse video) by
// default, matching the godex "Ready · Input · Model · ctx ·
// calls · msgs" heartbeat format.  Non-default StatusStyles add
// a subtle color tint to the dim text so warning/error states
// are still distinguishable at a glance, but the dominant case
// stays quiet.
//
// The status text is truncated to the terminal width (by
// display width, not byte count, so wide characters like CJK
// and emoji are not split mid-rune) and right-padded with
// spaces so the row is fully cleared when the text shrinks
// between redraws.
func (t *TUI) renderStatus() {
	s := ansiDim
	switch t.statusStyle {
	case StatusInfo:
		s = "\x1b[2;37m" // dim + light fg
	case StatusWarning:
		s = "\x1b[2;33m" // dim + yellow fg
	case StatusError:
		s = "\x1b[2;31m" // dim + red fg
	case StatusSuccess:
		s = "\x1b[2;32m" // dim + green fg
	}
	text := t.statusText
	if displayWidth(text) > t.width {
		var b strings.Builder
		cur := 0
		for _, r := range text {
			if cur+runeWidth(r) > t.width {
				break
			}
			b.WriteRune(r)
			cur += runeWidth(r)
		}
		text = b.String()
	}
	t.writeRow(t.statusRow(), s+text+ansiReset+strings.Repeat(" ", t.width-displayWidth(text)))
}

// ── row write (with cursor save/restore) ────────────────────────

func (t *TUI) writeRow(row int, s string) {
	if row < 0 || row >= t.height {
		return
	}
	fmt.Fprintf(os.Stdout, "\x1b[s\x1b[%d;1H\x1b[K%s\x1b[K\x1b[u", row+1, s)
}

// ── markdown → ANSI ─────────────────────────────────────────────

func (t *TUI) renderLine(raw string, tableBuf *[]string, forceDim bool) string {
	if t.customRender != nil {
		if tableBuf != nil && isTableLine(raw) {
			*tableBuf = append(*tableBuf, raw)
			return ""
		}
		r := t.customRender(raw)
		if forceDim {
			r = ansiDim + r + ansiReset
		}
		return r
	}
	if tableBuf != nil && isTableLine(raw) {
		*tableBuf = append(*tableBuf, raw)
		return ""
	}
	if forceDim {
		return ansiDim + raw + ansiReset
	}
	return renderInline(raw)
}

// ── table ────────────────────────────────────────────────────────

func renderTable(buf []string, width int) string {
	if len(buf) < 2 || !isTableSep(buf[1]) {
		return strings.Join(buf, "\n")
	}
	hdr := splitCols(buf[0])
	var rows [][]string
	for i := 2; i < len(buf); i++ {
		rows = append(rows, splitCols(buf[i]))
	}
	ncols := len(hdr)
	for _, r := range rows {
		if len(r) < ncols {
			ncols = len(r)
		}
	}
	widths := make([]int, ncols)
	for i := 0; i < ncols; i++ {
		w := displayWidth(strings.TrimSpace(hdr[i]))
		for _, r := range rows {
			if i < len(r) {
				if dw := displayWidth(strings.TrimSpace(r[i])); dw > w {
					w = dw
				}
			}
		}
		widths[i] = w + 2
	}
	var out []string
	out = append(out, tableRow(hdr, widths, true))
	sep := make([]string, ncols)
	for i := 0; i < ncols; i++ {
		sep[i] = strings.Repeat("─", widths[i])
	}
	out = append(out, tableRow(sep, widths, false))
	for _, r := range rows {
		out = append(out, tableRow(r, widths, false))
	}
	return strings.Join(out, "\n")
}

func tableRow(cols []string, widths []int, bold bool) string {
	var b strings.Builder
	b.WriteString("│ ")
	for i := 0; i < len(widths); i++ {
		txt := ""
		if i < len(cols) {
			txt = cols[i]
		}
		if bold {
			b.WriteString(ansiBold + pad(txt, widths[i]) + ansiReset)
		} else {
			b.WriteString(pad(txt, widths[i]))
		}
		b.WriteString(" │ ")
	}
	return b.String()
}

func splitCols(s string) []string {
	s = strings.Trim(s, "|")
	p := strings.Split(s, "|")
	for i := range p {
		p[i] = strings.TrimSpace(p[i])
	}
	return p
}

func isTableLine(s string) bool { return strings.HasPrefix(s, "|") && strings.HasSuffix(s, "|") }
func isTableSep(s string) bool {
	if !isTableLine(s) { return false }
	for _, p := range strings.Split(strings.Trim(s, "|"), "|") {
		if strings.Trim(p, "-: ") != "" { return false }
	}
	return true
}
func isCodeFence(s string) bool { return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") }

// ── inline markdown ─────────────────────────────────────────────

func renderInline(s string) string {
	if s == "" { return s }
	if s[0] == '#' {
		lvl := 0
		for lvl < len(s) && s[lvl] == '#' { lvl++ }
		if lvl <= 6 && (lvl == len(s) || s[lvl] == ' ') {
			return ansiBold + strings.TrimLeft(s[lvl:], " ") + ansiReset
		}
	}
	var b strings.Builder
	b.Grow(len(s) + 32)
	for i := 0; i < len(s); {
		if i+3 < len(s) && s[i] == '*' && s[i+1] == '*' {
			if e := strings.Index(s[i+2:], "**"); e >= 0 {
				b.WriteString(ansiBold); b.WriteString(s[i+2 : i+2+e])
				b.WriteString(ansiReset); i += 2 + e + 2; continue
			}
		}
		if i+2 < len(s) && s[i] == '*' && s[i+1] != '*' {
			if e := strings.Index(s[i+1:], "*"); e > 0 {
				b.WriteString(ansiItalic); b.WriteString(s[i+1 : i+1+e])
				b.WriteString(ansiReset); i += 1 + e + 1; continue
			}
		}
		if s[i] == '`' {
			if e := strings.Index(s[i+1:], "`"); e >= 0 {
				b.WriteString(ansiDim); b.WriteString(s[i+1 : i+1+e])
				b.WriteString(ansiReset); i += 1 + e + 1; continue
			}
		}
		b.WriteByte(s[i]); i++
	}
	return b.String()
}

// ── display width ────────────────────────────────────────────────

func displayWidth(s string) int {
	w := 0; for _, r := range s { w += runeWidth(r) }; return w
}
func runeDisplayWidth(s string) int { return displayWidth(s) }

func runeWidth(r rune) int {
	if r == 0 { return 0 }
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, r >= 0x3400 && r <= 0x4DBF,
		r >= 0xF900 && r <= 0xFAFF, r >= 0x3000 && r <= 0x303F,
		r >= 0xFF01 && r <= 0xFF60, r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0xAC00 && r <= 0xD7AF, r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0x2EFF, r >= 0x2F00 && r <= 0x2FDF,
		r >= 0xFE30 && r <= 0xFE4F, r >= 0x1F300 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x2FA1F:
		return 2
	}
	if r < 32 || (r >= 0x7F && r <= 0x9F) { return 0 }
	return 1
}

func pad(s string, w int) string {
	dw := displayWidth(s)
	if dw >= w {
		var b strings.Builder
		cur := 0
		for _, r := range s {
			if cur+runeWidth(r) > w { break }
			b.WriteRune(r); cur += runeWidth(r)
		}
		for cur < w { b.WriteByte(' '); cur++ }
		return b.String()
	}
	return s + strings.Repeat(" ", w-dw)
}
