package agent

import "github.com/tim5wang/godex/internal/tools"

// SetWorkContextTool installs the session-bound application capability. The
// backend supplies the implementation so the Agent does not import its service.
// It is only called at serialized session turn boundaries.
func (a *Agent) SetWorkContextTool(tool tools.Tool) {
	if a == nil || tool == nil {
		return
	}
	a.workContextTool = tool
	if a.toolHandler != nil {
		a.registerToolTo(a.toolHandler, tool, tools.ToolMeta{AlwaysActive: true})
	}
}
