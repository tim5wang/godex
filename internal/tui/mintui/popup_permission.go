package mintui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/tools"
	minitui "github.com/tim5wang/min-tui"
)

// permUI holds the state for the Ctrl+P permission approval popup.
type permUI struct {
	mu      sync.Mutex
	result  string
	loading bool
}

func (p *permUI) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.result = ""
	p.loading = false
}

// openPermissionPopup pushes the approval popup for the current
// permission blocker.  Called from the Ctrl+P global hotkey.
func (s *Session) openPermissionPopup(ctx context.Context) {
	if s.tui == nil {
		return
	}
	blocker := s.snapshot.ActivePermissionBlocker
	if blocker == nil {
		s.tui.SetStatus("No permission requests pending", minitui.StatusInfo)
		return
	}
	s.permUI.reset()
	s.tui.PushPopup(s.buildPermissionPopup(blocker))
}

func (s *Session) buildPermissionPopup(blocker *rtbackend.PermissionBlocker) minitui.Popup {
	return minitui.Popup{
		Title:  "Permission",
		Width:  50,
		Height: 15,
		Render: func(w, h int) []string {
			return s.renderPermissionPopup(blocker, w, h)
		},
		OnKey: func(k minitui.KeyEvent) minitui.PopupAction {
			return s.handlePermissionKey(k, blocker)
		},
	}
}

func (s *Session) renderPermissionPopup(blocker *rtbackend.PermissionBlocker, w, h int) []string {
	s.permUI.mu.Lock()
	result := s.permUI.result
	loading := s.permUI.loading
	s.permUI.mu.Unlock()

	if loading {
		lines := []string{"", "  Processing…", ""}
		return padPopupLines(lines, w, h)
	}
	if result != "" {
		lines := []string{
			"",
			"  " + result,
			"",
			"  Esc close",
		}
		return padPopupLines(lines, w, h)
	}

	tool := strings.TrimSpace(blocker.ToolName)
	action := strings.TrimSpace(blocker.Action)
	if action == "" {
		action = strings.TrimSpace(blocker.Command)
	}
	intent := strings.TrimSpace(blocker.Intent)
	paths := strings.Join(blocker.Paths, ", ")

	lines := []string{
		"",
		"  Tool:  " + tool,
	}
	if intent != "" {
		lines = append(lines, "  Intent: "+truncateForPopup(intent, w-10))
	}
	if action != "" {
		lines = append(lines, "  Action: "+truncateForPopup(action, w-10))
	}
	if paths != "" {
		lines = append(lines, "  Paths:  "+truncateForPopup(paths, w-10))
	}
	if blocker.RequestID != "" {
		lines = append(lines, "  ID:     "+blocker.RequestID)
	}
	lines = append(lines,
		"",
		"  [a] approve once       (本次放行)",
		"  [u] approve task       (本次任务)",
		"  [p] approve pattern    (匹配模式)",
		"  [t] timebox 10m        (10分钟内)",
		"  [s] approve session    (本次会话)",
		"  [x] deny",
		"",
		"  Esc close",
	)
	return padPopupLines(lines, w, h)
}

func (s *Session) handlePermissionKey(k minitui.KeyEvent, blocker *rtbackend.PermissionBlocker) minitui.PopupAction {
	s.permUI.mu.Lock()
	loading := s.permUI.loading
	s.permUI.mu.Unlock()
	if loading {
		return minitui.PopupPassthrough
	}

	var scope tools.PermissionGrantScope
	var deny bool

	switch {
	case k.Rune == 'a' || k.Rune == 'A':
		scope = tools.PermissionGrantOnce
	case k.Rune == 'u' || k.Rune == 'U':
		scope = tools.PermissionGrantTask
	case k.Rune == 'p' || k.Rune == 'P':
		scope = tools.PermissionGrantPattern
	case k.Rune == 't' || k.Rune == 'T':
		scope = tools.PermissionGrantScope("timebox:10m")
	case k.Rune == 's' || k.Rune == 'S':
		scope = tools.PermissionGrantSession
	case k.Rune == 'x' || k.Rune == 'X':
		deny = true
	default:
		return minitui.PopupPassthrough
	}

	s.permUI.mu.Lock()
	s.permUI.loading = true
	s.permUI.mu.Unlock()

	go s.runPermissionDecision(s.runCtx, blocker.RequestID, scope, deny)
	return minitui.PopupUpdate
}

func (s *Session) runPermissionDecision(ctx context.Context, requestID string, scope tools.PermissionGrantScope, deny bool) {
	if s.backend == nil {
		return
	}
	var err error
	if deny {
		_, err = s.backend.DenyPermission(ctx, s.sessionID, requestID, "User denied via Ctrl+P")
	} else {
		_, err = s.backend.ApprovePermission(ctx, s.sessionID, requestID, scope)
	}

	s.permUI.mu.Lock()
	defer s.permUI.mu.Unlock()
	s.permUI.loading = false
	if err != nil {
		s.permUI.result = fmt.Sprintf("✗ Failed: %s", err.Error())
	} else if deny {
		s.permUI.result = "✗ Denied. Esc to close."
	} else {
		s.permUI.result = fmt.Sprintf("✓ Approved (%s). Esc to close.", scope)
	}
	if s.tui != nil {
		_, _ = s.tui.WriteString("")
	}
}
