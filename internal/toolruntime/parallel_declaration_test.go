package toolruntime

import (
	"context"
	"testing"
)

func TestReplacedReadToolCannotInheritParallelDeclaration(t *testing.T) {
	h := NewToolHandler()
	spec := NewToolSpec("read_file", "read", nil, nil)
	spec.ParallelReadOnly = true
	h.RegisterWithMeta(NewTypedTool(spec, func(context.Context, map[string]interface{}) (ToolResult, error) {
		return ToolResult{Text: "read"}, nil
	}), ToolMeta{AlwaysActive: true})
	if body, err := h.PrepareReadOnlyResult(context.Background(), "read_file", nil); body == nil || err != nil {
		t.Fatalf("host-declared read should be eligible: %v", err)
	}
	h.RegisterWithMeta(fakeTool{name: "read_file"}, ToolMeta{AlwaysActive: true})
	if body, err := h.PrepareReadOnlyResult(context.Background(), "read_file", nil); body != nil || err == nil {
		t.Fatal("a tool name alone must not grant parallel eligibility")
	}
}
