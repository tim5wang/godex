package agent

import "strings"

// Session creation modes selectable from the Web UI "new chat" dialog.
// A mode pins the session's initial active tool set and prompt complexity at
// creation time. It is stored in the locator metadata ("mode") so reloads
// restore the same preset, and it stays fixed for the session so the stable
// prompt prefix (and therefore provider prefix-cache hits) is not churned by
// mid-session changes.
const (
	// SessionModeDefault is the standard mode: full tool availability and the
	// regular dynamic prompt sections (repo map / skill catalog, active
	// skills, environment, tool availability).
	SessionModeDefault = "default"
	// SessionModeMinimal is a lean mode for quick file/shell work: only the
	// four core tools (read_file, write_file, edit_file, bash) are active
	// initially, and the heavyweight background sections (repo map / skill
	// catalog, active skills) are omitted from the prompt. Always-active
	// tools (memory, skill, compress, manage_session, tool_exchange, cron,
	// heartbeat, history_search) remain regardless.
	SessionModeMinimal = "minimal"
)

// minimalModeTools is the initial active tool set for the minimal mode.
// Always-active tools are preserved by SetActiveTools on top of this list.
var minimalModeTools = []string{"read_file", "write_file", "edit_file", "bash"}

// ApplySessionMode applies a session creation mode to the agent: it replaces
// the active tool set with the mode's preset (preserving always-active tools)
// and records the mode for prompt shaping. Unknown or empty modes keep the
// default behavior.
func (a *Agent) ApplySessionMode(mode string) {
	mode = strings.TrimSpace(mode)
	switch mode {
	case SessionModeMinimal:
		a.toolHandler.SetActiveTools(minimalModeTools...)
	default:
		// default mode: leave the registered default-active tools as-is.
	}
	a.mu.Lock()
	a.sessionMode = mode
	a.mu.Unlock()
}

// sessionModeIsMinimal reports whether the session was created in minimal
// mode, which trims heavyweight dynamic prompt sections.
func (a *Agent) sessionModeIsMinimal() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionMode == SessionModeMinimal
}
