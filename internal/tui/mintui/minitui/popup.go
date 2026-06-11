package minitui

import (
	"strings"
)

// ── popup types ──────────────────────────────────────────────────

// KeyEvent is the public key type exposed to popup handlers.
type KeyEvent struct {
	Rune   rune
	Enter  bool
	Shift  bool
	Ctrl   bool
	Alt    bool
	Special int // keyUp, keyDown, etc. (same constants as internal key.go)
}

func keyEventFromInternal(k keyEvent) KeyEvent {
	return KeyEvent{
		Rune:    k.r,
		Enter:   k.enter,
		Shift:   k.shift,
		Ctrl:    k.ctrl,
		Alt:     k.alt,
		Special: k.special,
	}
}

// PopupAction controls what happens after OnKey processes a key.
type PopupAction int

const (
	PopupPassthrough PopupAction = iota // key not handled — normal input
	PopupUpdate                            // key handled — re-render popup
	PopupClose                             // close popup
)

// Popup configures an overlay window.  Popups are rendered on top of the
// output area and do not affect the underlying history buffer.
type Popup struct {
	Title string

	// Width and Height specify the popup size in cells.
	// 0 means auto: 80% of terminal width, 60% of output-area height.
	Width  int
	Height int

	// BorderColor and BgColor are the ANSI escapes used when the popup is focused.
	// Defaults: cyan border (\x1b[36m), white background (\x1b[47;30m).
	BorderColor string
	BgColor     string

	// BorderColorUnfocus and BgColorUnfocus are used when the popup is not
	// focused (Tab toggles focus). Defaults: dim cyan, gray background.
	BorderColorUnfocus string
	BgColorUnfocus     string

	// Render returns the lines to display inside the popup (excluding borders).
	// w and h are the available content width and height.
	Render func(w, h int) []string

	// OnKey is called when the popup is focused and a key is pressed.
	// Return PopupUpdate to re-render, PopupClose to dismiss, PopupPassthrough
	// to let the key fall through to the input editor.
	OnKey func(key KeyEvent) PopupAction

	// OnClose is called after the popup is removed from the stack.
	OnClose func()
}

// ── popup stack ──────────────────────────────────────────────────

// PushPopup opens a popup window on top of the current view.  The popup
// is rendered over the output area; when closed, the original output is
// restored.  Safe to call from any goroutine.
func (t *TUI) PushPopup(p Popup) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if p.Width <= 0 {
		p.Width = t.width * 8 / 10
	}
	if p.Width > t.width-2 {
		p.Width = t.width - 2
	}
	if p.Height <= 0 {
		p.Height = t.outputRows() * 6 / 10
	}
	if p.Height < 3 {
		p.Height = 3
	}
	if p.Height > t.outputRows()-1 {
		p.Height = t.outputRows() - 1
	}
	if p.BorderColor == "" {
		p.BorderColor = "\x1b[36m" // cyan
	}
	if p.BgColor == "" {
		p.BgColor = "\x1b[47;30m" // white bg, black fg
	}
	if p.BorderColorUnfocus == "" {
		p.BorderColorUnfocus = "\x1b[2;36m" // dim cyan
	}
	if p.BgColorUnfocus == "" {
		p.BgColorUnfocus = "\x1b[100;30m" // bright black (gray) bg
	}

	// New popup starts focused; unfocus any existing popups.
	for i := range t.popups {
		t.popups[i].focused = false
	}

	// Center popup in the output area.
	x := (t.width - p.Width) / 2
	y := (t.outputRows() - p.Height) / 2

	pp := &popupState{
		Popup:   p,
		x:       x, y: y,
		focused: true,
	}
	t.popups = append(t.popups, pp)
	t.renderPopup(pp)
}

// PopPopup closes the topmost popup.
func (t *TUI) PopPopup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.popPopup()
	t.renderAfterPopupClose()
}

// SetGlobalKeyHandler registers a function called for every keypress in
// normal mode.  Return true to consume the key (it will not be processed
// by the input editor).  Use this to detect trigger keys for popups.
func (t *TUI) SetGlobalKeyHandler(fn func(KeyEvent) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.globalKeyFn = fn
}

// ── popup state ──────────────────────────────────────────────────

type popupState struct {
	Popup
	x, y    int
	focused bool
}

// ── internal: popup rendering ────────────────────────────────────

func (t *TUI) renderPopup(p *popupState) {
	w, h := p.Width, p.Height
	bc, bg := p.BorderColor, p.BgColor
	if !p.focused {
		bc, bg = p.BorderColorUnfocus, p.BgColorUnfocus
	}
	rst := ansiReset

	// Top border with title.
	title := p.Title
	tw := displayWidth(title)
	if tw > w-5 {
		// Truncate title to fit.
		var b strings.Builder
		cur := 0
		for _, r := range title {
			rw := runeWidth(r)
			if cur+rw > w-5 { break }
			b.WriteRune(r); cur += rw
		}
		title = b.String()
		tw = cur
	}
	padW := w - tw - 5 // dashes between title and right border
	if padW < 0 {
		padW = 0
	}

	topBar := bc + "┌─ " + rst + title + bc + " " + strings.Repeat("─", padW) + "┐" + rst
	t.writeRow(p.y, topBar)

	// Content area.
	content := p.Render(w-2, h-2)
	for i := 0; i < h-2; i++ {
		var line string
		if i < len(content) {
			line = bc + "│" + rst + bg + padTo(content[i], w-2) + rst + bc + "│" + rst
		} else {
			line = bc + "│" + rst + bg + strings.Repeat(" ", w-2) + rst + bc + "│" + rst
		}
		t.writeRow(p.y+1+i, line)
	}

	// Bottom border.
	botBar := bc + "└" + strings.Repeat("─", w-2) + "┘" + rst
	t.writeRow(p.y+h-1, botBar)
}

// ── internal: popup key dispatch ─────────────────────────────────

// handlePopupKey returns true if the key was consumed.
func (t *TUI) handlePopupKey(k keyEvent) bool {
	// Esc always closes the top popup.
	if k.r == 27 {
		t.popPopup()
		t.renderAfterPopupClose()
		return true
	}
	// Ctrl+C always passes through.
	if k.ctrl && (k.r == 'c' || k.r == 'C') {
		return false
	}

	// Tab toggles focus between input and top popup.
	if k.special == keyTab && !k.shift {
		top := t.popups[len(t.popups)-1]
		top.focused = !top.focused
		t.renderPopup(top)
		if top.focused {
			t.showCursor()
		}
		return true
	}

	// If no popup has focus, all keys pass through to input.
	top := t.popups[len(t.popups)-1]
	if !top.focused {
		return false
	}

	// Focused popup: route keys to OnKey.
	if top.OnKey == nil {
		return false
	}
	switch top.OnKey(keyEventFromInternal(k)) {
	case PopupClose:
		t.popPopup()
		t.renderAfterPopupClose()
		return true
	case PopupUpdate:
		t.renderPopup(top)
		t.renderInputBox()
		t.renderStatus()
		return true
	default:
		return false
	}
}

// processPopupKey is called from processKey when a popup is active
// and the key wasn't consumed by the ReadLine dispatch.
func (t *TUI) processPopupKey(k keyEvent) {
	if k.r == 27 || (k.ctrl && (k.r == 'c' || k.r == 'C')) {
		return // Esc/Ctrl+C handled elsewhere
	}
	top := t.popups[len(t.popups)-1]
	if top.OnKey == nil {
		return
	}
	switch top.OnKey(keyEventFromInternal(k)) {
	case PopupClose:
		t.popPopup()
		t.renderAfterPopupClose()
	case PopupUpdate:
		t.renderPopup(top)
	}
}

func (t *TUI) popPopup() {
	if len(t.popups) == 0 {
		return
	}
	top := t.popups[len(t.popups)-1]
	t.popups = t.popups[:len(t.popups)-1]
	if top.OnClose != nil {
		top.OnClose()
	}
}

func (t *TUI) renderAfterPopupClose() {
	t.renderOutputScreen()
	t.renderInputBox()
	t.renderStatus()
	t.showCursor()
}

// reRenderPopups redraws all active popups (called after Write).
func (t *TUI) reRenderPopups() {
	for _, p := range t.popups {
		t.renderPopup(p)
	}
}

// padTo pads or truncates a string to exactly w display cells.
func padTo(s string, w int) string {
	dw := displayWidth(s)
	if dw >= w {
		var b strings.Builder
		cur := 0
		for _, r := range s {
			rw := runeWidth(r)
			if cur+rw > w {
				break
			}
			b.WriteRune(r)
			cur += rw
		}
		for cur < w {
			b.WriteByte(' ')
			cur++
		}
		return b.String()
	}
	return s + strings.Repeat(" ", w-dw)
}
