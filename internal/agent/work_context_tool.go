package agent

import "github.com/tim5wang/godex/internal/tools"

// bundleWorkContext groups the work desktop ledger capability. It is its own
// bundle (not always_on) so tool_exchange can load and unload it on demand.
const bundleWorkContext = "work_context"

// SetWorkContextTool installs the session-bound application capability. The
// backend supplies the implementation so the Agent does not import its service.
// It is only called at serialized session turn boundaries.
//
// The tool registers into its own bundle with DefaultActive instead of
// AlwaysActive, so tool_exchange can load/unload it on demand instead of it
// being pinned by the always_on bundle (which tool_exchange refuses to touch).
func (a *Agent) SetWorkContextTool(tool tools.Tool) {
	if a == nil || tool == nil {
		return
	}
	a.workContextTool = tool
	if a.toolHandler == nil {
		return
	}
	a.registerToolTo(a.toolHandler, tool, tools.ToolMeta{
		Bundle:        bundleWorkContext,
		Summary:       "work desktop ledger: stream checkpoint, plan items (owner/due) and evidence-backed facts",
		DefaultActive: true,
	})
	// registerWithMetaLocked only auto-activates AlwaysActive tools, and
	// RegisterTools already ran ActivateDefaults before this tool was mounted, so
	// activate the bundle explicitly here; otherwise a bound session would load
	// without the capability. A resumed session's persisted tool set, or a later
	// template/session-mode apply, still wins — so this only sets the default and
	// a user's tool_exchange disable is not clobbered.
	a.toolHandler.ActivateBundles(bundleWorkContext)
}
