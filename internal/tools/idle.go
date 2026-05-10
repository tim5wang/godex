package tools

import "context"

// IdleSignal interface to avoid circular import.
type IdleSignal interface {
	SetIdle(idle bool)
}

type idleArgs struct{}

// NewIdleTool creates a new idle tool.
func NewIdleTool(signal IdleSignal) Tool {
	return NewTypedTool(NewToolSpec("idle", "Enter idle state, waiting for user input. Call when task is complete or needs clarification.", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args idleArgs) (ToolResult, error) {
		_ = ctx
		signal.SetIdle(true)
		return ToolResult{Text: "Entering idle state. Waiting for user input..."}, nil
	})
}
