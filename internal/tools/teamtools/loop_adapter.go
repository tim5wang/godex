package teamtools

import (
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/toolruntime"
	"github.com/tim5wang/godex/internal/tools"
)

// NewLoopToolFactories adapts the concrete builtin tools to the narrow
// factory contract owned by core/teammate.
func NewLoopToolFactories() []teammate.LoopToolFactory {
	return []teammate.LoopToolFactory{
		func(ctx teammate.LoopToolContext) toolruntime.Tool {
			return tools.NewBashTool(ctx.WorkspaceDir)
		},
		func(ctx teammate.LoopToolContext) toolruntime.Tool {
			return tools.NewReadFileTool(ctx.WorkspaceDir)
		},
		func(ctx teammate.LoopToolContext) toolruntime.Tool {
			return tools.NewWriteFileTool(ctx.WorkspaceDir)
		},
		func(ctx teammate.LoopToolContext) toolruntime.Tool {
			return tools.NewEditFileTool(ctx.WorkspaceDir)
		},
		func(ctx teammate.LoopToolContext) toolruntime.Tool {
			return tools.NewTaskTool(ctx.TaskManager)
		},
		func(ctx teammate.LoopToolContext) toolruntime.Tool {
			return tools.NewIdleTool(ctx.IdleSignal)
		},
	}
}
