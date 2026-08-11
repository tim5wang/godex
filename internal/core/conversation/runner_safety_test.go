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

// Reasoning-budget overflow (finish_reason=length + empty answer + reasoning
// content) must trigger a brevity-nudge recovery instead of a blind retry,
// and must not be counted as an "empty response" retry.
func TestRunnerReasoningOverflowRequestsDirectAnswer(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "do the work")}
	caller := &fakeCaller{responses: []protocol.Response{
		{
			StopReason:       "length",
			ReasoningContent: "thinking very deeply about the entire codebase...",
			Content:          []protocol.Block{}, // no answer: budget eaten by reasoning
		},
		{Content: []protocol.Block{protocol.TextBlock("the answer")}},
	}}

	var injected []protocol.Message
	result, err := Runner{
		Caller: caller,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return NewRequest("model", 1024, "", "system", messages, nil), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		AppendInjectedMessages: func(msgs []protocol.Message) {
			injected = append(injected, msgs...)
			messages = append(messages, msgs...)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{}, nil
		},
		MaxTurns: 10,
	}.Run(context.Background())

	if err != nil {
		t.Fatalf("expected recovery from reasoning overflow, got error: %v", err)
	}
	if !result.Completed || result.LastAssistantText != "the answer" {
		t.Fatalf("expected completed run, got %+v", result)
	}
	if len(injected) != 1 || !strings.Contains(protocol.MessageText(injected[0]), "Answer directly") {
		t.Fatalf("expected one brevity nudge, got %+v", injected)
	}
	// Two model calls: the overflow + the direct answer (NOT 3 blind retries).
	if caller.calls != 2 {
		t.Fatalf("expected 2 model calls, got %d", caller.calls)
	}
}

// No-mutation spiral: many consecutive tool rounds without touching a file
// (research spiral) must trip the loop guard even when the tools vary, and
// the run must abort if the model keeps looping after the nudge.
func TestLoopGuardNoMutationSpiralRecoversThenAborts(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "investigate")}
	toolRound := protocol.Response{Content: []protocol.Block{
		protocol.TextBlock("checking"),
		protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "grep something"}),
	}}
	// Many rounds; vary the command so identical-tool detection stays silent.
	var responses []protocol.Response
	for i := 0; i < 30; i++ {
		resp := toolRound
		resp.Content[1] = protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "grep check-" + itoa(i)})
		responses = append(responses, resp)
	}
	caller := &fakeCaller{responses: responses}

	var feedbacks []string
	result, err := Runner{
		Caller: caller,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return NewRequest("model", 1024, "", "system", messages, nil), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{Output: "match"}, nil
		},
		AppendRuntimeFeedback: func(msg protocol.Message) {
			feedbacks = append(feedbacks, protocol.MessageText(msg))
			messages = append(messages, msg)
		},
		MaxRepeatedTools:    0, // isolate the no-mutation detector
		MaxNoMutationRounds: 3,
		MaxTurns:            40,
	}.Run(context.Background())

	if len(feedbacks) == 0 || !strings.Contains(feedbacks[0], "no_mutation_spiral") {
		t.Fatalf("expected no_mutation_spiral recovery, got %+v", feedbacks)
	}
	// The model kept looping with the same pattern -> strict mode aborts.
	if err == nil {
		t.Fatal("expected loop guard abort after the no-mutation spiral persisted")
	}
	_ = result
}
