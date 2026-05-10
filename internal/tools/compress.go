package tools

import "context"

// ConversationManager exposes manual compaction.
type ConversationManager interface {
	CompactConversation() (string, error)
}

type compressArgs struct{}

// NewCompressTool creates a new compress tool.
func NewCompressTool(manager ConversationManager) Tool {
	return NewTypedTool(NewToolSpec("compress", "Manually compress conversation context to reduce token usage", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args compressArgs) (ToolResult, error) {
		_ = ctx
		output, err := manager.CompactConversation()
		return ToolResult{Text: output}, err
	})
}
