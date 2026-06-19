package mintui

import (
	minitui "github.com/tim5wang/min-tui"
)

// pageHelp holds the content for each page of the help popup.
type pageHelp struct {
	title string
	lines []string
}

// helpPages defines the multi-page help content shown by Ctrl+H and /help.
func buildHelpPages() []pageHelp {
	return []pageHelp{
		{
			title: "Global Hotkeys",
			lines: []string{
				"",
				"  Ctrl+B    Background Tasks   longtask list + filter + cancel",
				"  Ctrl+W    Workbench          task center + subagent tracking",
				"  Ctrl+P    Permission         approve / deny requests (when blocked)",
				"  Ctrl+H    This Help          you are here",
				"",
				"  Ctrl+C    Cancel / Quit      cancel turn → cancel bash → quit",
				"",
			},
		},
		{
			title: "Input & Navigation",
			lines: []string{
				"",
				"  ↑/↓       Input History      recall previous inputs",
				"  Shift+Enter  Newline          multi-line input",
				"  Ctrl+J    Newline (alt)      same as Shift+Enter",
				"  /         Slash Commands     type / then filter + Enter",
				"  !         Local Bash         !ls, !git status, !make test",
				"",
				"  Tab       Switch Focus       input ↔ popup (when popup open)",
				"  Esc       Close Popup        or exit filter mode first",
				"",
			},
		},
		{
			title: "LongTask Popup (Ctrl+B)",
			lines: []string{
				"",
				"  ↑/↓       Navigate           move cursor",
				"  Enter     Detail             open task detail view",
				"  /         Filter             type to filter by name/desc",
				"  c         Cancel             cancel selected task",
				"  r         Refresh            re-fetch from backend",
				"  Esc       Close              close the popup",
				"",
				"  Filter mode:",
				"    Backspace  delete char",
				"    ESC / /    exit filter mode, keep text",
				"    ↑/↓        exit filter, navigate",
				"    Enter      open detail for first match",
				"",
			},
		},
		{
			title: "Detail Popup (Enter from list)",
			lines: []string{
				"",
				"  R         Rollback           enter story nodeID → reason",
				"  l         Lookup             enter commit hash → find task",
				"  g         GC                 cleanup old artifacts",
				"  Esc       Close              back to task list",
				"",
			},
		},
		{
			title: "Workbench (Ctrl+W)",
			lines: []string{
				"",
				"  1         Tasks Tab          plan / active / review sections",
				"  2         Workers Tab        subagent job list",
				"  ↑/↓       Navigate           move cursor",
				"  Enter     Detail             open longtask detail (Tasks tab)",
				"  r         Refresh            re-fetch data",
				"  Esc       Close              close the popup",
				"",
			},
		},
		{
			title: "Permission (Ctrl+P)",
			lines: []string{
				"",
				"  a         Approve Once       this request only",
				"  u         Approve Task       all requests in this task",
				"  p         Approve Pattern    matching file/tool patterns",
				"  t         Timebox 10m        approve for 10 minutes",
				"  s         Approve Session    all requests this session",
				"  x         Deny               reject request",
				"  Esc       Close              close the popup",
				"",
			},
		},
		{
			title: "Slash Commands",
			lines: []string{
				"",
				"  /help     Show this help     keyboard shortcut reference",
				"  /model    Switch model       interactive model picker",
				"  /sessions Session list       list + switch sessions",
				"  /approve  Approve            approve permission (CLI style)",
				"  /deny     Deny               deny permission (CLI style)",
				"",
				"  Type / to see all commands in the dropdown.",
				"  Arrow keys filter, Enter selects.",
				"",
			},
		},
	}
}

// openHelp pushes the multi-page help popup.  Called from Ctrl+H
// global hotkey or /help slash command.
func (s *Session) openHelp() {
	if s.tui == nil {
		return
	}
	pages := buildHelpPages()
	page := 0

	s.tui.PushPopup(minitui.Popup{
		Title:  "Help — ← → page  " + pages[0].title,
		Width:  56,
		Height: 22,
		Render: func(w, h int) []string {
			return padPopupLines(pages[page].lines, w, h)
		},
		OnKey: func(k minitui.KeyEvent) minitui.PopupAction {
			switch {
			case k.Special == minitui.KeyLeft:
				if page > 0 {
					page--
				}
				return minitui.PopupUpdate
			case k.Special == minitui.KeyRight:
				if page < len(pages)-1 {
					page++
				}
				return minitui.PopupUpdate
			case k.Rune == 'h' || k.Rune == 'H':
				// Quick access: H jumps to "Global Hotkeys" page.
				page = 0
				return minitui.PopupUpdate
			}
			return minitui.PopupPassthrough
		},
	})
}
