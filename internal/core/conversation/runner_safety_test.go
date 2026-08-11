package conversation

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// Safety valve: an exhausted empty-response streak must surface as a hard
// error, not as a fabricated "diagnostic handoff" that ends the turn without
// doing work and invites the user to restart the same stalled loop.
func TestRunnerEmptyResponseStreakReturnsHardError(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "do the work")}
	caller := &fakeCaller{responses: []protocol.Response{
		{Content: []protocol.Block{}}, // empty, no tool use
		{Content: []protocol.Block{}},
		{Content: []protocol.Block{}},
		{Content: []protocol.Block{}},
	}}

	result, err := Runner{
		Caller: caller,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return NewRequest("model", 1024, "", "system", messages, nil), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{}, nil
		},
		MaxTurns: 10,
	}.Run(context.Background())

	if err == nil {
		t.Fatal("expected hard error after repeated empty responses")
	}
	if !strings.Contains(err.Error(), "empty responses") {
		t.Fatalf("expected error to name empty responses, got %v", err)
	}
	if result.Completed {
		t.Fatal("empty-response streak must not be reported as completed")
	}
	if !result.Stopped {
		t.Fatal("expected result.Stopped on failure")
	}
	// No finalization call: the old behavior made an extra model call with a
	// "diagnostic handoff" instruction. The safety valve sends the SAME
	// (non-finalizing) request and then errors — total calls = retries + the
	// exhausted call, never a fabricated handoff.
	if caller.calls != defaultMaxEmptyResponses+1 {
		t.Fatalf("expected %d model calls (retries + exhausted call), got %d", defaultMaxEmptyResponses+1, caller.calls)
	}
	if len(messages) != 1 {
		t.Fatalf("expected no assistant messages appended for the empty streak, got %d messages", len(messages))
	}
}

// One empty response followed by a real one still recovers (retries preserved).
func TestRunnerEmptyResponseRecoversBeforeExhaustion(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "do the work")}
	caller := &fakeCaller{responses: []protocol.Response{
		{Content: []protocol.Block{}}, // empty
		{Content: []protocol.Block{protocol.TextBlock("real answer")}},
	}}

	result, err := Runner{
		Caller: caller,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return NewRequest("model", 1024, "", "system", messages, nil), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{}, nil
		},
		MaxTurns: 10,
	}.Run(context.Background())

	if err != nil {
		t.Fatalf("expected recovery, got error: %v", err)
	}
	if !result.Completed || result.LastAssistantText != "real answer" {
		t.Fatalf("expected completed run with real answer, got %+v", result)
	}
}
