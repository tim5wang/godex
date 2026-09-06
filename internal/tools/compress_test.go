package tools

import (
	"context"
	"testing"
)

type timeoutConversationManager struct {
	hadDeadline bool
}

func (m *timeoutConversationManager) CompactConversationContext(ctx context.Context) (string, error) {
	_, m.hadDeadline = ctx.Deadline()
	return "compacted", nil
}

func TestCompressToolAppliesTimeoutSeconds(t *testing.T) {
	manager := &timeoutConversationManager{}
	tool := NewCompressTool(manager)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"timeout_seconds": 1}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if !manager.hadDeadline {
		t.Fatal("expected compaction context to have a deadline")
	}
}
