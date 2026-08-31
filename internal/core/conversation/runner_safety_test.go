package conversation

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
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
	if len(injected) != 1 || !strings.Contains(protocol.MessageText(injected[0]), "answer directly") {
		t.Fatalf("expected one brevity nudge, got %+v", injected)
	}
	// The recovery must shrink the output budget on the follow-up request so
	// the model cannot re-exhaust the full ceiling on reasoning again.
	lastReq := caller.requests[len(caller.requests)-1]
	if lastReq.MaxTokens != 512 {
		t.Fatalf("expected reduced max_tokens (1024/2=512) on recovery request, got %d", lastReq.MaxTokens)
	}
	// Two model calls: the overflow + the direct answer (NOT 3 blind retries).
	if caller.calls != 2 {
		t.Fatalf("expected 2 model calls, got %d", caller.calls)
	}
}

// No-mutation spiral: many consecutive tool rounds without touching a file
// (research spiral) must trip the loop guard even when the tools vary, but
// the no-mutation signal alone never aborts in strict mode: the model is
// nudged repeatedly and may always escape by writing a checkpoint or giving
// a final answer. Abort remains reserved for the identical-tool/polling/
// cycle detectors where repetition is unambiguous.
func TestLoopGuardNoMutationSpiralNeverAborts(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "investigate")}
	// Many rounds; vary the command so identical-tool detection stays silent.
	// Each response gets its own Content slice (no shared backing array).
	var responses []protocol.Response
	for i := 0; i < 30; i++ {
		responses = append(responses, protocol.Response{Content: []protocol.Block{
			protocol.TextBlock("checking"),
			protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "grep check-" + itoa(i)}),
		}})
	}
	// The model eventually answers; the loop must never be aborted by the
	// no-mutation detector alone, no matter how many nudges it received.
	responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("findings summarized")}})
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
		MaxRepeatedTools:    1000, // isolate the no-mutation detector (0 maps to the default 8 in Runner)
		MaxNoMutationRounds: 3,
		MaxTurns:            40,
	}.Run(context.Background())

	if len(feedbacks) == 0 || !strings.Contains(feedbacks[0], "no_mutation_spiral") {
		t.Fatalf("expected no_mutation_spiral recovery, got %+v", feedbacks)
	}
	// The nudge text blesses the research-notes escape hatch.
	if !strings.Contains(feedbacks[0], "DEEP RESEARCH") || !strings.Contains(feedbacks[0], "write your findings") {
		t.Fatalf("expected research-notes escape hatch in nudge, got %q", feedbacks[0])
	}
	// Repeated nudges are fine (research-safe), but the run must complete:
	// the no-mutation signal never escalates to abort in strict mode.
	if err != nil {
		t.Fatalf("expected no-mutation spiral to recover until completion, got %v", err)
	}
	if result == nil || !result.Completed || result.LastAssistantText != "findings summarized" {
		t.Fatalf("expected completed result, got %+v", result)
	}
	if len(feedbacks) < 3 {
		t.Fatalf("expected repeated nudges (research-safe), got %d", len(feedbacks))
	}
}

// Deep research that periodically checkpoints findings to a file must NOT be
// interrupted: the file write resets the detector, so no nudge fires at all.
func TestLoopGuardNoMutationResetsOnResearchNotesWrite(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "research the architecture")}
	var responses []protocol.Response
	// 2 read rounds, then a notes write, then 2 reads, then a write, then 1 read:
	// never 3 consecutive read rounds, so no nudge fires.
	rounds := []string{"read", "read", "write", "read", "read", "write", "read"}
	for i, kind := range rounds {
		var block protocol.Block
		if kind == "write" {
			block = protocol.ToolUseBlock("tool-1", "write_file", map[string]interface{}{"path": "notes.md"})
		} else {
			block = protocol.ToolUseBlock("tool-1", "read_file", map[string]interface{}{"path": "file-" + itoa(i) + ".go"})
		}
		responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("checking"), block}})
	}
	responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("research report complete")}})
	caller := &fakeCaller{responses: responses}

	var feedbacks []string
	_, err := Runner{
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
			return ToolExecutionResult{Output: "ok"}, nil
		},
		AppendRuntimeFeedback: func(msg protocol.Message) {
			feedbacks = append(feedbacks, protocol.MessageText(msg))
			messages = append(messages, msg)
		},
		MaxRepeatedTools:    0,
		MaxNoMutationRounds: 3,
		MaxTurns:            20,
	}.Run(context.Background())

	if err != nil {
		t.Fatalf("deep research with note checkpoints must not be aborted: %v", err)
	}
	if len(feedbacks) != 0 {
		t.Fatalf("expected no no-mutation nudge when research writes notes, got %+v", feedbacks)
	}
}
