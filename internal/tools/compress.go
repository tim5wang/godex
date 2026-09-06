package tools

import "context"

// ConversationManager exposes manual compaction.
type ConversationManager interface {
	CompactConversationContext(ctx context.Context) (string, error)
}

type compressArgs struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// NewCompressTool creates a new compress tool.
func NewCompressTool(manager ConversationManager) Tool {
	return NewTypedTool(NewToolSpec("compress", "Manually compress conversation context to reduce token usage", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Optional timeout for this compaction in seconds. Omit to use the global tool timeout.",
			},
		},
	}, nil), func(ctx context.Context, args compressArgs) (ToolResult, error) {
		compactCtx, cancel := withOptionalTimeout(ctx, args.TimeoutSeconds)
		defer cancel()
		output, err := manager.CompactConversationContext(compactCtx)
		return ToolResult{Text: output}, err
	})
}
