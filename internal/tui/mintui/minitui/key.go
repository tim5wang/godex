package minitui

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	keyNone = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyBackspace
	keyForwardDelete
	keyTab
	keyPageUp
	keyPageDown
)

type keyEvent struct {
	r       rune
	enter   bool
	shift   bool
	ctrl    bool
	alt     bool
	special int
	pasted  string
}

func (t *TUI) readKey() (keyEvent, error) {
	n, _ := os.Stdin.Read(t.keyBuf)
	if n == 0 {
		return keyEvent{}, nil
	}
	b := t.keyBuf[:n]
	if len(b) == 1 {
		switch b[0] {
		case 0x0d:
			return keyEvent{enter: true}, nil
		case 0x0a: // Ctrl+J or bare linefeed
			return keyEvent{ctrl: true, r: 'j'}, nil
		case 0x7f:
			return keyEvent{special: keyBackspace}, nil
		case 0x09:
			return keyEvent{special: keyTab}, nil
		default:
			if b[0] >= 0x01 && b[0] <= 0x1a {
				return keyEvent{ctrl: true, r: rune(b[0] + 0x60)}, nil
			}
			r, _ := utf8.DecodeRune(b)
			return keyEvent{r: r}, nil
		}
	}
	if b[0] == 0x1b {
		return t.parseEscape(b)
	}
	r, sz := utf8.DecodeRune(b)
	if sz < n {
		return keyEvent{pasted: string(b)}, nil
	}
	return keyEvent{r: r}, nil
}

func (t *TUI) parseEscape(b []byte) (keyEvent, error) {
	if len(b) == 1 {
		return keyEvent{r: 27}, nil
	}
	rest := b[1:]

	// Alt+Enter / Option+Enter fallback: ESC CR
	if len(rest) == 1 && rest[0] == 0x0d {
		return keyEvent{enter: true, alt: true}, nil
	}

	// Bracketed paste: ESC [ 2 0 0 ~ ... ESC [ 2 0 1 ~
	if len(rest) >= 5 && string(rest[:5]) == "[200~" {
		return t.extractPaste(rest)
	}

	// Kitty keyboard protocol: ESC [ code ; mod u  /  ESC [ code ; mod ; ev u
	if len(rest) >= 3 && rest[0] == '[' {
		for i := 1; i < len(rest); i++ {
			if rest[i] == 'u' {
				return t.parseKitty(rest[1:i]), nil
			}
		}
	}

	// SS3: ESC O letter
	if len(rest) == 2 && rest[0] == 'O' {
		return keyEvent{special: ss3Map(rest[1])}, nil
	}

	// CSI: ESC [ ... letter
	if rest[0] == '[' {
		ev, _ := t.parseCSI(rest[1:])
		return ev, nil
	}

	// Unknown two-byte ESC sequence — treat as Alt+char (e.g. ESC a = Alt+A)
	if len(rest) == 1 && rest[0] >= 32 && rest[0] <= 126 {
		return keyEvent{alt: true, r: rune(rest[0])}, nil
	}

	return keyEvent{}, nil
}

func ss3Map(c byte) int {
	switch c {
	case 'A': return keyUp
	case 'B': return keyDown
	case 'C': return keyRight
	case 'D': return keyLeft
	case 'H': return keyHome
	case 'F': return keyEnd
	}
	return keyNone
}

func (t *TUI) parseCSI(rest []byte) (keyEvent, error) {
	if len(rest) == 0 {
		return keyEvent{}, nil
	}
	last := rest[len(rest)-1]
	params := parseNums(rest[:len(rest)-1])
	switch last {
	case 'A': return keyEvent{special: keyUp}, nil
	case 'B': return keyEvent{special: keyDown}, nil
	case 'C': return keyEvent{special: keyRight}, nil
	case 'D': return keyEvent{special: keyLeft}, nil
	case 'H': return keyEvent{special: keyHome}, nil
	case 'F': return keyEvent{special: keyEnd}, nil
	case 'Z': return keyEvent{special: keyTab, shift: true}, nil
	case '~':
		// XTerm modifyOtherKeys: ESC [ 2 7 ; mod ; key ~
		if len(params) >= 3 && params[0] == 27 {
			return t.parseModifyOther(params[1], params[2]), nil
		}
		if len(params) > 0 {
			switch params[0] {
			case 3: return keyEvent{special: keyForwardDelete}, nil
			case 1: return keyEvent{special: keyHome}, nil
			case 4: return keyEvent{special: keyEnd}, nil
			case 5: return keyEvent{special: keyPageUp}, nil
			case 6: return keyEvent{special: keyPageDown}, nil
			}
		}
	}
	return keyEvent{}, nil
}

func (t *TUI) parseKitty(data []byte) keyEvent {
	params := parseNums(data)
	if len(params) == 0 {
		return keyEvent{}
	}
	code, mod := params[0], 0
	if len(params) >= 2 {
		mod = params[1]
	}
	ev := keyEvent{shift: mod&1 != 0, alt: mod&2 != 0, ctrl: mod&4 != 0}
	switch code {
	case 13:
		ev.enter = true
	case 27:
		ev.r = 27 // Escape
	case 127, 57355:
		ev.special = keyBackspace
	case 9:
		ev.special = keyTab
	default:
		if code >= 32 && code <= 126 {
			ev.r = rune(code)
		}
	}
	return ev
}

// parseModifyOther handles XTerm modifyOtherKeys: ESC [ 2 7 ; mod ; key ~
// Modifier values: 2=Shift, 3=Alt, 4=Shift+Alt, 5=Ctrl, 6=Ctrl+Shift, 7=Ctrl+Alt, 8=Ctrl+Shift+Alt
func (t *TUI) parseModifyOther(mod, key int) keyEvent {
	ev := keyEvent{
		shift: mod == 2 || mod == 4 || mod == 6 || mod == 8,
		alt:   mod == 3 || mod == 4 || mod == 7 || mod == 8,
		ctrl:  mod == 5 || mod == 6 || mod == 7 || mod == 8,
	}
	switch key {
	case 13:
		ev.enter = true
	case 127:
		ev.special = keyBackspace
	case 9:
		ev.special = keyTab
	default:
		if key >= 32 && key <= 126 {
			ev.r = rune(key)
		}
	}
	return ev
}

func (t *TUI) extractPaste(rest []byte) (keyEvent, error) {
	// rest starts with "[200~". Check if end marker is already in buffer.
	if idx := indexBytes(rest[5:], []byte("\x1b[201~")); idx >= 0 {
		// Full paste was captured in one read — extract directly.
		return keyEvent{pasted: string(rest[5 : 5+idx])}, nil
	}
	// Paste spans multiple reads — fall back to streaming read.
	return t.readPaste(rest[5:])
}

func (t *TUI) readPaste(prefix []byte) (keyEvent, error) {
	buf := make([]byte, len(prefix))
	copy(buf, prefix)
	for {
		tmp := make([]byte, 1024)
		n, _ := os.Stdin.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if idx := indexBytes(buf, []byte("\x1b[201~")); idx >= 0 {
			return keyEvent{pasted: string(buf[:idx])}, nil
		}
	}
}

func parseNums(b []byte) []int {
	var nums []int
	cur, has := 0, false
	for _, c := range b {
		if c >= '0' && c <= '9' {
			cur = cur*10 + int(c-'0')
			has = true
		} else if c == ';' {
			if has { nums = append(nums, cur) }
			cur, has = 0, false
		}
	}
	if has { nums = append(nums, cur) }
	return nums
}

// Key constants for KeyEvent.Special.
const (
	KeyUp    = keyUp
	KeyDown  = keyDown
	KeyLeft  = keyLeft
	KeyRight = keyRight
	KeyHome  = keyHome
	KeyEnd   = keyEnd
)

// ── input editor ─────────────────────────────────────────────────

func (t *TUI) processKey(k keyEvent) {
	// ── popup mode ─────────────────────────────────────────
	if len(t.popups) > 0 && t.popups[len(t.popups)-1].focused {
		t.processPopupKey(k)
		return
	}
	// ── select mode ─────────────────────────────────────────
	if t.selectMode {
		t.processSelectKey(k)
		return
	}
	// ── slash mode ───────────────────────────────────────────
	if t.slashMode {
		t.processSlashKey(k)
		return
	}
	// ── normal mode ─────────────────────────────────────────
	switch {
	case k.pasted != "":
		t.paste(k.pasted)
	case k.enter && (k.shift || k.alt), k.ctrl && k.r == 'j':
		t.newline()
	case k.special == keyBackspace:
		t.backspace()
	case k.special == keyForwardDelete, k.ctrl && k.r == 'd':
		t.forwardDelete()
	case k.special == keyLeft, k.ctrl && k.r == 'b':
		t.moveCursor(-1, 0)
	case k.special == keyRight, k.ctrl && k.r == 'f':
		t.moveCursor(1, 0)
	case k.special == keyUp, k.ctrl && k.r == 'p':
		t.moveCursor(0, -1)
	case k.special == keyDown, k.ctrl && k.r == 'n':
		t.moveCursor(0, 1)
	case k.special == keyHome, k.ctrl && k.r == 'a':
		t.inCursorCol = 0
	case k.special == keyEnd, k.ctrl && k.r == 'e':
		t.inCursorCol = len(t.inLines[t.inCursorRow])
	case k.ctrl && k.r == 'k':
		t.inLines[t.inCursorRow] = t.inLines[t.inCursorRow][:t.inCursorCol]
	case k.ctrl && k.r == 'u':
		t.inLines[t.inCursorRow] = t.inLines[t.inCursorRow][t.inCursorCol:]
		t.inCursorCol = 0
	case k.ctrl && k.r == 'w':
		t.killWord()
	case k.special == keyTab:
		for i := 0; i < 4; i++ { t.insertRune(' ') }
	case k.r > 0:
		t.insertRune(k.r)
		t.checkSlashTrigger()
	}
	t.recalcInputHeight()
}

func (t *TUI) moveCursor(dx, dy int) {
	if dx < 0 && t.inCursorCol > 0 {
		t.inCursorCol--
	} else if dx < 0 && t.inCursorRow > 0 {
		t.inCursorRow--
		t.inCursorCol = len(t.inLines[t.inCursorRow])
	} else if dx > 0 && t.inCursorCol < len(t.inLines[t.inCursorRow]) {
		t.inCursorCol++
	} else if dx > 0 && t.inCursorRow < len(t.inLines)-1 {
		t.inCursorRow++; t.inCursorCol = 0
	}
	if dy < 0 && t.inCursorRow > 0 {
		t.inCursorRow--
		if t.inCursorCol > len(t.inLines[t.inCursorRow]) {
			t.inCursorCol = len(t.inLines[t.inCursorRow])
		}
	} else if dy > 0 && t.inCursorRow < len(t.inLines)-1 {
		t.inCursorRow++
		if t.inCursorCol > len(t.inLines[t.inCursorRow]) {
			t.inCursorCol = len(t.inLines[t.inCursorRow])
		}
	}
}

func (t *TUI) insertRune(r rune) {
	row, col := t.inCursorRow, t.inCursorCol
	line := t.inLines[row]
	n := make([]rune, 0, len(line)+1)
	n = append(n, line[:col]...)
	n = append(n, r)
	n = append(n, line[col:]...)
	t.inLines[row] = n
	t.inCursorCol++
}

func (t *TUI) newline() {
	row, col := t.inCursorRow, t.inCursorCol
	line := t.inLines[row]
	left := make([]rune, col); copy(left, line[:col])
	right := make([]rune, len(line)-col); copy(right, line[col:])
	t.inLines[row] = left
	t.inLines = append(t.inLines[:row+1], append([][]rune{right}, t.inLines[row+1:]...)...)
	t.inCursorRow++; t.inCursorCol = 0
}

func (t *TUI) paste(text string) {
	parts := splitLines(text)
	if len(parts) == 0 { return }
	row, col := t.inCursorRow, t.inCursorCol
	line := t.inLines[row]
	r0 := []rune(parts[0])
	fst := make([]rune, 0, col+len(r0))
	fst = append(fst, line[:col]...)
	fst = append(fst, r0...)
	if len(parts) == 1 {
		fst = append(fst, line[col:]...)
		t.inLines[row] = fst
		t.inCursorCol = col + len(r0)
		return
	}
	rl := []rune(parts[len(parts)-1])
	lst := make([]rune, 0, len(rl)+len(line)-col)
	lst = append(lst, rl...)
	lst = append(lst, line[col:]...)
	var nl [][]rune
	nl = append(nl, t.inLines[:row]...)
	nl = append(nl, fst)
	for i := 1; i < len(parts)-1; i++ { nl = append(nl, []rune(parts[i])) }
	nl = append(nl, lst)
	nl = append(nl, t.inLines[row+1:]...)
	t.inLines = nl
	t.inCursorRow = row + len(parts) - 1
	t.inCursorCol = len(rl)
}

func (t *TUI) backspace() {
	if t.inCursorCol > 0 {
		l := t.inLines[t.inCursorRow]
		t.inLines[t.inCursorRow] = append(l[:t.inCursorCol-1], l[t.inCursorCol:]...)
		t.inCursorCol--
	} else if t.inCursorRow > 0 {
		t.inCursorCol = len(t.inLines[t.inCursorRow-1])
		t.inLines[t.inCursorRow-1] = append(t.inLines[t.inCursorRow-1], t.inLines[t.inCursorRow]...)
		t.inLines = append(t.inLines[:t.inCursorRow], t.inLines[t.inCursorRow+1:]...)
		t.inCursorRow--
	}
}

func (t *TUI) forwardDelete() {
	l := t.inLines[t.inCursorRow]
	if t.inCursorCol < len(l) {
		t.inLines[t.inCursorRow] = append(l[:t.inCursorCol], l[t.inCursorCol+1:]...)
	} else if t.inCursorRow < len(t.inLines)-1 {
		t.inLines[t.inCursorRow] = append(l, t.inLines[t.inCursorRow+1]...)
		t.inLines = append(t.inLines[:t.inCursorRow+1], t.inLines[t.inCursorRow+2:]...)
	}
}

func (t *TUI) killWord() {
	l := t.inLines[t.inCursorRow]
	p := t.inCursorCol
	for p > 0 && (l[p-1] == ' ' || l[p-1] == '\t') { p-- }
	for p > 0 && l[p-1] != ' ' && l[p-1] != '\t' { p-- }
	t.inLines[t.inCursorRow] = append(l[:p], l[t.inCursorCol:]...)
	t.inCursorCol = p
}

func splitLines(s string) []string {
	// Normalize line endings: \r\n → \n, \r → \n
	s = stringsReplace(s, "\r\n", "\n")
	s = stringsReplace(s, "\r", "\n")
	var ls []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' { ls = append(ls, s[start:i]); start = i + 1 }
	}
	return append(ls, s[start:])
}

func stringsReplace(s, old, new string) string {
	// Simple replace to avoid importing "strings" twice.
	var b []byte
	for i := 0; i < len(s); i++ {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			b = append(b, new...)
			i += len(old) - 1
		} else {
			b = append(b, s[i])
		}
	}
	return string(b)
}

func indexByte(s []byte, b byte) int {
	for i, c := range s { if c == b { return i } }
	return -1
}

func indexBytes(haystack, needle []byte) int {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) { return i }
	}
	return -1
}

// ── slash commands ───────────────────────────────────────────────

const slashDropdownMax = 8

// checkSlashTrigger detects whether the input line starts with /.
func (t *TUI) checkSlashTrigger() {
	if t.slashMode || len(t.slashCmds) == 0 {
		return
	}
	line := t.inLines[t.inCursorRow]
	// Slash must be the first non-space character.
	for i, r := range line {
		if r == ' ' {
			continue
		}
		if r == '/' && i == t.inCursorCol-1 {
			t.enterSlashMode()
		}
		return
	}
}

func (t *TUI) enterSlashMode() {
	t.slashMode = true
	t.slashQuery = ""
	t.slashMatches = nil
	t.slashSelected = 0
	t.updateSlashMatches()
}

func (t *TUI) exitSlashMode() {
	t.slashMode = false
	t.slashDropdownH = 0
	t.slashMatches = nil
	t.renderOutputScreen()
	t.renderInputBox()
}

func (t *TUI) processSlashKey(k keyEvent) {
	switch {
	case k.r == 27: // Escape
		t.exitSlashMode()
		return
	case k.special == keyUp:
		if t.slashSelected > 0 {
			t.slashSelected--
		}
	case k.special == keyDown:
		if t.slashSelected < len(t.slashMatches)-1 {
			t.slashSelected++
		}
	case k.special == keyBackspace:
		t.backspace()
		if t.inCursorCol == 0 {
			// Backspaced past the / — exit slash mode.
			t.exitSlashMode()
			return
		}
		t.updateSlashQueryFromInput()
		t.updateSlashMatches()
	case k.r > 0:
		t.insertRune(k.r)
		t.updateSlashQueryFromInput()
		t.updateSlashMatches()
	}
	t.renderSlashDropdown()
	t.renderInputBox()
	t.renderStatus()
}

// updateSlashQueryFromInput extracts the query text after / from the input.
func (t *TUI) updateSlashQueryFromInput() {
	line := string(t.inLines[t.inCursorRow])
	if len(line) > 0 && line[0] == '/' {
		t.slashQuery = line[1:]
	} else {
		t.slashQuery = ""
	}
}

// updateSlashMatches filters slash commands by the current query.
func (t *TUI) updateSlashMatches() {
	t.slashMatches = t.slashMatches[:0]
	q := strings.ToLower(t.slashQuery)
	for i, c := range t.slashCmds {
		if strings.Contains(strings.ToLower(c.Name), q) {
			t.slashMatches = append(t.slashMatches, i)
		}
	}
	if len(t.slashMatches) == 0 {
		t.slashDropdownH = 0
		return
	}
	t.slashDropdownH = len(t.slashMatches)
	if t.slashDropdownH > slashDropdownMax {
		t.slashDropdownH = slashDropdownMax
	}
	if t.slashSelected >= len(t.slashMatches) {
		t.slashSelected = len(t.slashMatches) - 1
	}
	if t.slashSelected < 0 {
		t.slashSelected = 0
	}
}

// renderSlashDropdown draws the command suggestion list above the input box.
func (t *TUI) renderSlashDropdown() {
	if !t.slashMode || t.slashDropdownH == 0 {
		return
	}
	// Dropdown sits between output area and input box.
	// output area was already rendered by processSlashKey caller.
	startRow := t.outputRows() // first row of dropdown

	for i := 0; i < t.slashDropdownH; i++ {
		matchIdx := t.slashMatches[i]
		cmd := t.slashCmds[matchIdx]
		prefix := "  "
		if i == t.slashSelected {
			prefix = "\x1b[7m> " // inverted highlight
		} else {
			prefix = "  "
		}
		line := fmt.Sprintf("%s/%-20s %s", prefix, cmd.Name, cmd.Description)
		if i == t.slashSelected {
			line += "\x1b[0m"
		}
		t.writeRow(startRow+i, line)
	}
	// Clear any remaining dropdown rows from previous render.
	prevH := startRow + t.slashDropdownH
	maxPrev := t.inTopBorder() - 1
	for i := prevH; i <= maxPrev; i++ {
		t.writeRow(i, "")
	}
}

func (t *TUI) executeSelectedCommand() {
	if t.slashSelected < 0 || t.slashSelected >= len(t.slashMatches) {
		return
	}
	cmdIdx := t.slashMatches[t.slashSelected]
	cmd := t.slashCmds[cmdIdx]
	args := ""
	if len(t.slashQuery) > len(cmd.Name) {
		args = strings.TrimSpace(t.slashQuery[len(cmd.Name):])
	}
	ctx := &CommandContext{Args: args, tui: t}
	go cmd.Handler(ctx)
}

// ── select mode (secondary dropdown) ────────────────────────────

func (t *TUI) processSelectKey(k keyEvent) {
	switch {
	case k.r == 27: // Escape
		t.cancelSelect()
	case k.special == keyUp:
		if t.selectIdx > 0 {
			t.selectIdx--
		}
	case k.special == keyDown:
		if t.selectIdx < len(t.selectItems)-1 {
			t.selectIdx++
		}
	}
	t.renderSelectDropdown()
	t.renderInputBox()
	t.renderStatus()
}

func (t *TUI) cancelSelect() {
	ch := t.selectCh
	t.selectCh = nil
	t.mu.Unlock()
	ch <- -1
	t.mu.Lock()
}

func (t *TUI) renderSelectDropdown() {
	if !t.selectMode || len(t.selectItems) == 0 {
		return
	}
	startRow := t.outputRows()
	visible := t.slashDropdownH

	for i := 0; i < visible; i++ {
		if i >= len(t.selectItems) {
			t.writeRow(startRow+i, "")
			continue
		}
		item := t.selectItems[i]
		prefix := "  "
		suffix := ""
		if i == t.selectIdx {
			prefix = "\x1b[7m> "
			suffix = "\x1b[0m"
		}
		line := fmt.Sprintf("%s%-30s %s%s", prefix, item.Label, item.Description, suffix)
		t.writeRow(startRow+i, line)
	}
}
