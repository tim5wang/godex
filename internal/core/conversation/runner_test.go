package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

type pendingStopError struct{}

func (pendingStopError) Error() string { return "pending approval" }
func (pendingStopError) StopConversationAfterTool() bool {
	return true
}
func (pendingStopError) PendingPermissionRequestID() string { return "perm-1" }

type fakeCaller struct {
	responses []protocol.Response
	calls     int
	requests  []protocol.Request
}

func (f *fakeCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	f.requests = append(f.requests, req)
	resp := f.responses[f.calls]
	f.calls++
	return &resp, nil
}

type fakeStreamingCaller struct {
	response protocol.Response
	deltas   []string
}

func (f *fakeStreamingCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	return &f.response, nil
}

func (f *fakeStreamingCaller) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	_ = ctx
	_ = req
	for _, delta := range f.deltas {
		if handler.OnTextDelta != nil {
			handler.OnTextDelta(delta)
		}
	}
	resp := f.response
	return &resp, nil
}

func TestRunnerExecutesSharedAssistantToolLoop(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	caller := &fakeCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.TextBlock("checking"),
			protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "pwd"}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
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
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			if name != "bash" {
				t.Fatalf("unexpected tool %q", name)
			}
			if input["command"] != "pwd" {
				t.Fatalf("unexpected tool input %+v", input)
			}
			return ToolExecutionResult{Output: "/workspace"}, nil
		},
		MaxTurns: 5,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("run runner: %v", err)
	}
	if !result.Completed {
		t.Fatal("expected runner to complete")
	}
	if result.LastAssistantText != "done" {
		t.Fatalf("expected last assistant text %q, got %q", "done", result.LastAssistantText)
	}
	if caller.calls != 2 {
		t.Fatalf("expected 2 model calls, got %d", caller.calls)
	}
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages in history, got %d", len(messages))
	}
	if messages[1].Content[1].Type != protocol.BlockToolUse {
		t.Fatalf("expected assistant message to preserve tool use, got %+v", messages[1].Content)
	}
	if messages[2].Content[0].Type != protocol.BlockToolResult {
		t.Fatalf("expected tool result message, got %+v", messages[2].Content)
	}
}

func TestRunnerUsesStreamingCallerForAssistantDeltas(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	caller := &fakeStreamingCaller{
		response: protocol.Response{Content: []protocol.Block{protocol.TextBlock("hello")}},
		deltas:   []string{"he", "llo"},
	}
	var deltas []string
	var completed string

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
			t.Fatalf("did not expect tool execution: %s %+v", name, input)
			return ToolExecutionResult{}, nil
		},
		OnAssistantTextDelta: func(delta string) {
			deltas = append(deltas, delta)
		},
		OnAssistantText: func(text string) {
			completed = text
		},
		MaxTurns: 1,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("run runner: %v", err)
	}
	if !result.Completed || result.LastAssistantText != "hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got := strings.Join(deltas, ""); got != "hello" {
		t.Fatalf("expected streamed deltas %q, got %q", "hello", got)
	}
	if completed != "hello" {
		t.Fatalf("expected completion text %q, got %q", "hello", completed)
	}
}

func TestRunnerStopsAfterToolsWhenRequested(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	caller := &fakeCaller{responses: []protocol.Response{{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "idle", map[string]interface{}{}),
	}}}}
	stop := false

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
			_ = input
			if name != "idle" {
				t.Fatalf("unexpected tool %q", name)
			}
			stop = true
			return ToolExecutionResult{Output: "Entering idle state."}, nil
		},
		StopAfterTools: func() bool {
			return stop
		},
		MaxTurns: 5,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("run runner: %v", err)
	}
	if !result.Stopped {
		t.Fatal("expected runner to stop after tools")
	}
	if caller.calls != 1 {
		t.Fatalf("expected 1 model call, got %d", caller.calls)
	}
}

func TestRunnerStopsRepeatedIdenticalToolCalls(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "tool_exchange", map[string]interface{}{"query": "ssh deploy"}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{repeated, repeated, repeated, repeated}}
	executions := 0

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
			executions++
			if name != "tool_exchange" {
				t.Fatalf("unexpected tool %q", name)
			}
			if input["query"] != "ssh deploy" {
				t.Fatalf("unexpected tool input %+v", input)
			}
			return ToolExecutionResult{Output: `{"recommended_bundles":[],"status":"no_match"}`}, nil
		},
		MaxTurns:         10,
		MaxRepeatedTools: 3,
	}.Run(context.Background())
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("expected repeated tool error, got %v", err)
	}
	if result == nil || !result.Stopped {
		t.Fatalf("expected stopped result, got %+v", result)
	}
	if caller.calls != 3 || executions != 3 {
		t.Fatalf("expected 3 calls/executions before guard, got calls=%d executions=%d", caller.calls, executions)
	}
}

func TestRunnerRecoversFromRepeatedIdenticalToolCalls(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "tool_exchange", map[string]interface{}{"query": "ssh deploy"}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{
		repeated,
		repeated,
		repeated,
		{Content: []protocol.Block{protocol.TextBlock("changed strategy")}},
	}}
	var phases []PhaseEvent

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
		AppendRuntimeFeedback: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{Output: `{"recommended_bundles":[],"status":"no_match"}`}, nil
		},
		OnPhase: func(event PhaseEvent) {
			phases = append(phases, event)
		},
		MaxTurns:         10,
		MaxRepeatedTools: 3,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected loop guard feedback to allow recovery, got %v", err)
	}
	if result == nil || !result.Completed || result.LastAssistantText != "changed strategy" {
		t.Fatalf("expected completed result, got %+v", result)
	}
	if caller.calls != 4 {
		t.Fatalf("expected 4 model calls, got %d", caller.calls)
	}
	foundFeedback := false
	for _, msg := range messages {
		if strings.Contains(protocol.MessageText(msg), "loop_guard_recovery") {
			foundFeedback = true
			break
		}
	}
	if !foundFeedback {
		t.Fatalf("expected loop guard runtime feedback in history, got %+v", messages)
	}
	foundPhase := false
	for _, phase := range phases {
		if phase.Phase == PhaseRecoveryAttempt && strings.Contains(phase.Message, "loop_guard_recovery") {
			foundPhase = true
			break
		}
	}
	if !foundPhase {
		t.Fatalf("expected loop guard recovery phase, got %+v", phases)
	}
}

func TestRunnerAbortsWhenSameLoopRepeatsAfterRecovery(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "tool_exchange", map[string]interface{}{"query": "ssh deploy"}),
	}}
	// 6 identical calls: the first 3 trip the guard (recover + feedback), the
	// repeated counter is reset after recovery, then re-accumulating 3 more
	// identical calls trips the guard again, and the second detection aborts.
	caller := &fakeCaller{responses: []protocol.Response{repeated, repeated, repeated, repeated, repeated, repeated}}
	feedbacks := 0

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
		AppendRuntimeFeedback: func(msg protocol.Message) {
			feedbacks++
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{Output: `{"recommended_bundles":[],"status":"no_match"}`}, nil
		},
		MaxTurns:         10,
		MaxRepeatedTools: 3,
	}.Run(context.Background())
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("expected repeated tool error, got %v", err)
	}
	if result == nil || !result.Stopped || !strings.Contains(result.RecoveryHint, "Loop guard") {
		t.Fatalf("expected stopped result with loop guard hint, got %+v", result)
	}
	if feedbacks != 1 {
		t.Fatalf("expected one feedback before abort, got %d", feedbacks)
	}
	if caller.calls != 6 {
		t.Fatalf("expected abort after re-accumulating to the limit post-recovery, got %d calls", caller.calls)
	}
}

func TestRunnerStopsRepeatingToolCycles(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	a := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-a", "tool_exchange", map[string]interface{}{"query": "weather API"}),
	}}
	b := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-b", "tool_exchange", map[string]interface{}{"query": "web search"}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{a, b, a, b, a, b}}

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
			return ToolExecutionResult{Output: `{"recommended_bundles":[],"status":"no_match"}`}, nil
		},
		MaxTurns:                10,
		MaxRepeatedTools:        10,
		MaxRepeatedPollingTools: 10,
	}.Run(context.Background())
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("expected repeated cycle error, got %v", err)
	}
	if result == nil || !result.Stopped {
		t.Fatalf("expected stopped result, got %+v", result)
	}
	if caller.calls != 6 {
		t.Fatalf("expected guard after 6 model calls, got %d", caller.calls)
	}
}

func TestRunnerRecoversFromRepeatingToolCycles(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	a := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-a", "tool_exchange", map[string]interface{}{"query": "weather API"}),
	}}
	b := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-b", "tool_exchange", map[string]interface{}{"query": "web search"}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{
		a,
		b,
		a,
		b,
		a,
		b,
		{Content: []protocol.Block{protocol.TextBlock("cycle avoided")}},
	}}
	feedbacks := 0

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
		AppendRuntimeFeedback: func(msg protocol.Message) {
			feedbacks++
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{Output: `{"recommended_bundles":[],"status":"no_match"}`}, nil
		},
		MaxTurns:                10,
		MaxRepeatedTools:        10,
		MaxRepeatedPollingTools: 10,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected loop guard feedback to recover from cycle, got %v", err)
	}
	if result == nil || !result.Completed || result.LastAssistantText != "cycle avoided" {
		t.Fatalf("expected completed result, got %+v", result)
	}
	if feedbacks != 1 {
		t.Fatalf("expected one cycle feedback, got %d", feedbacks)
	}
}

func TestRunnerAllowsRepeatedBrowserWaitsAcrossNavigation(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "inspect")}
	responses := []protocol.Response{}
	for i, ref := range []string{"e3", "e4", "e5", "e6"} {
		responses = append(responses,
			protocol.Response{Content: []protocol.Block{
				protocol.ToolUseBlock(fmt.Sprintf("click-%d", i), "browser", map[string]interface{}{
					"action": "click",
					"ref":    ref,
				}),
			}},
			protocol.Response{Content: []protocol.Block{
				protocol.ToolUseBlock(fmt.Sprintf("wait-%d", i), "browser", map[string]interface{}{
					"action":  "wait",
					"page_id": "page-1",
					"time_ms": 500,
				}),
			}},
		)
	}
	responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("done")}})
	caller := &fakeCaller{responses: responses}

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
			if name != "browser" {
				t.Fatalf("unexpected tool %q", name)
			}
			return ToolExecutionResult{Output: `{"status":"ok"}`}, nil
		},
		MaxTurns:         12,
		MaxRepeatedTools: 4,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected repeated navigation waits not to trigger guard, got %v", err)
	}
	if result == nil || !result.Completed || result.LastAssistantText != "done" {
		t.Fatalf("expected completed result, got %+v", result)
	}
	if caller.calls != len(responses) {
		t.Fatalf("expected %d model calls, got %d", len(responses), caller.calls)
	}
}

func TestRunnerStopsConsecutiveRepeatedBrowserWaits(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "wait")}
	wait := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("wait-1", "browser", map[string]interface{}{
			"action":  "wait",
			"page_id": "page-1",
			"time_ms": 500,
		}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{wait, wait, wait, wait, wait}}

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
			if name != "browser" {
				t.Fatalf("unexpected tool %q", name)
			}
			return ToolExecutionResult{Output: `{"status":"ok"}`}, nil
		},
		MaxTurns:         10,
		MaxRepeatedTools: 4,
	}.Run(context.Background())
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("expected repeated browser wait error, got %v", err)
	}
	if result == nil || !result.Stopped {
		t.Fatalf("expected stopped result, got %+v", result)
	}
	if caller.calls != 4 {
		t.Fatalf("expected guard after 4 model calls, got %d", caller.calls)
	}
}

func TestRunnerStopsRepeatedPollingToolInputs(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	repeated := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "subagent", map[string]interface{}{
			"action": "status",
			"job_id": "subagent_1",
		}),
	}}
	// 10 identical status calls: the first 5 trip the stalled-polling guard
	// (recover + feedback), the counter is reset after recovery, then
	// re-accumulating 5 more identical calls trips it again and aborts.
	caller := &fakeCaller{responses: []protocol.Response{repeated, repeated, repeated, repeated, repeated, repeated, repeated, repeated, repeated, repeated}}
	executions := 0

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
		AppendRuntimeFeedback: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			executions++
			return ToolExecutionResult{Output: fmt.Sprintf(`{"status":"running","updated_at":%d}`, executions)}, nil
		},
		MaxTurns:                   10,
		MaxRepeatedTools:           10,
		MaxStalledTaskPollingTools: 5,
		MaxLoopGuardRecoveries:     1,
	}.Run(context.Background())
	if !errors.Is(err, ErrRepeatedToolCalls) {
		t.Fatalf("expected repeated polling tool error, got %v", err)
	}
	if result == nil || !result.Stopped {
		t.Fatalf("expected stopped result, got %+v", result)
	}
	if caller.calls != 10 || executions != 10 {
		t.Fatalf("expected 10 calls/executions before guard aborts after recovery, got calls=%d executions=%d", caller.calls, executions)
	}
}

func TestRunnerAllowsRepeatedTaskStatusWithProgress(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	status := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "subagent", map[string]interface{}{
			"action": "status",
			"job_id": "subagent_1",
		}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{
		status,
		status,
		status,
		status,
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	executions := 0

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
			executions++
			return ToolExecutionResult{Output: fmt.Sprintf(`{"status":"running","last_phase":"tool_results","progress_count":%d,"updated_at":%d}`, executions, executions)}, nil
		},
		MaxTurns:                   10,
		MaxRepeatedTools:           10,
		MaxStalledTaskPollingTools: 3,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected progress polling to continue, got %v", err)
	}
	if result == nil || !result.Completed || result.LastAssistantText != "done" {
		t.Fatalf("expected completed result, got %+v", result)
	}
	if executions != 4 {
		t.Fatalf("expected 4 status executions, got %d", executions)
	}
}

func TestRunnerDoesNotStopTerminalTaskStatus(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	status := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "subagent", map[string]interface{}{
			"action": "status",
			"job_id": "subagent_1",
		}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{
		status,
		status,
		status,
		status,
		{Content: []protocol.Block{protocol.TextBlock("done")}},
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
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{Output: `{"status":"completed","result_preview":"handoff","updated_at":1}`}, nil
		},
		MaxTurns:                   10,
		MaxRepeatedTools:           10,
		MaxStalledTaskPollingTools: 2,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected terminal status polling not to trigger guard, got %v", err)
	}
	if result == nil || !result.Completed {
		t.Fatalf("expected completed result, got %+v", result)
	}
}

func TestRunnerDoesNotStopTerminalCheckBackground(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	check := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "background", map[string]interface{}{
			"action":  "check",
			"task_id": "bg_1",
		}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{
		check,
		check,
		check,
		{Content: []protocol.Block{protocol.TextBlock("handled failure")}},
	}}
	executions := 0

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
			executions++
			if executions < 3 {
				return ToolExecutionResult{Output: `{"task_id":"bg_1","status":"running"}`}, nil
			}
			return ToolExecutionResult{Output: `{"task_id":"bg_1","status":"error","error":"pip failed","exit_code":1}`}, nil
		},
		MaxTurns:         10,
		MaxRepeatedTools: 10,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected terminal background status not to trigger guard, got %v", err)
	}
	if result == nil || !result.Completed || result.LastAssistantText != "handled failure" {
		t.Fatalf("expected completed result, got %+v", result)
	}
}

func TestRunnerUsesPollingGuardForRepeatedRunningCheckBackground(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	check := protocol.Response{Content: []protocol.Block{
		protocol.ToolUseBlock("tool-1", "background", map[string]interface{}{
			"action":  "check",
			"task_id": "bg_1",
		}),
	}}
	caller := &fakeCaller{responses: []protocol.Response{
		check,
		check,
		check,
		check,
		{Content: []protocol.Block{protocol.TextBlock("still waiting")}},
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
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			return ToolExecutionResult{Output: `{"task_id":"bg_1","status":"running"}`}, nil
		},
		MaxTurns:                   10,
		MaxRepeatedTools:           4,
		MaxStalledTaskPollingTools: 5,
	}.Run(context.Background())
	if err != nil {
		t.Fatalf("expected background polling to use stalled polling guard, got %v", err)
	}
	if result == nil || !result.Completed {
		t.Fatalf("expected completed result, got %+v", result)
	}
}

func TestExecuteToolUsesEmitsPerToolLifecycle(t *testing.T) {
	blocks := []protocol.Block{
		protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "pwd"}),
		protocol.ToolUseBlock("tool-2", "read_file", map[string]interface{}{"path": "README.md"}),
	}
	started := make([]string, 0, len(blocks))
	finished := make([]string, 0, len(blocks))

	msg, executed, err := ExecuteToolUses(
		context.Background(),
		blocks,
		func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			switch name {
			case "bash":
				return ToolExecutionResult{Output: "/workspace"}, nil
			case "read_file":
				return ToolExecutionResult{Output: "content"}, nil
			default:
				t.Fatalf("unexpected tool %q", name)
				return ToolExecutionResult{}, nil
			}
		},
		func(block protocol.Block) {
			started = append(started, block.ID+":"+block.Name)
		},
		func(tool ExecutedTool) {
			finished = append(finished, tool.ID+":"+tool.Name)
		},
	)
	if err != nil {
		t.Fatalf("execute tool uses: %v", err)
	}

	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 tool result blocks, got %d", len(msg.Content))
	}
	if len(executed) != 2 {
		t.Fatalf("expected 2 executed tools, got %d", len(executed))
	}
	if got, want := started, []string{"tool-1:bash", "tool-2:read_file"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unexpected started lifecycle order: got %v want %v", got, want)
	}
	if got, want := finished, []string{"tool-1:bash", "tool-2:read_file"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unexpected finished lifecycle order: got %v want %v", got, want)
	}
	if executed[0].Input["command"] != "pwd" {
		t.Fatalf("expected first tool input to be preserved, got %+v", executed[0].Input)
	}
}

func TestExecuteToolUsesReturnsStructuredTimeoutResult(t *testing.T) {
	blocks := []protocol.Block{
		protocol.ToolUseBlock("tool-1", "browser", map[string]interface{}{"action": "open", "url": "https://example.com"}),
	}

	msg, executed, err := ExecuteToolUsesWithOptions(
		context.Background(),
		blocks,
		func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			<-ctx.Done()
			return ToolExecutionResult{}, ctx.Err()
		},
		nil,
		nil,
		nil,
		ExecuteToolOptions{Timeout: 5 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("timeout should be model-visible tool result, not runner error: %v", err)
	}
	if len(executed) != 1 || executed[0].Error == "" {
		t.Fatalf("expected executed timeout error, got %+v", executed)
	}
	if !strings.Contains(executed[0].Error, "timed out") {
		t.Fatalf("expected timeout error, got %+v", executed[0])
	}
	if len(msg.Content) != 1 || msg.Content[0].Type != protocol.BlockToolResult {
		t.Fatalf("expected one tool result block, got %+v", msg.Content)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Content[0].Content), &payload); err != nil {
		t.Fatalf("expected structured JSON timeout output, got %q: %v", msg.Content[0].Content, err)
	}
	if payload["status"] != "error" || payload["code"] != "tool_timeout" {
		t.Fatalf("expected tool_timeout payload, got %#v", payload)
	}
	if !strings.Contains(fmt.Sprint(payload["recovery_hint"]), "web_fetch") {
		t.Fatalf("expected recovery hint with alternatives, got %#v", payload)
	}
}

func TestExecuteToolUsesEmitsStuckWatchdogBeforeTimeout(t *testing.T) {
	blocks := []protocol.Block{
		protocol.ToolUseBlock("tool-1", "browser", map[string]interface{}{"action": "open", "url": "https://example.com"}),
	}
	stuck := make(chan ToolStuckEvent, 1)

	_, _, err := ExecuteToolUsesWithOptions(
		context.Background(),
		blocks,
		func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			<-ctx.Done()
			return ToolExecutionResult{}, ctx.Err()
		},
		nil,
		nil,
		nil,
		ExecuteToolOptions{
			Timeout:           25 * time.Millisecond,
			StuckWarningAfter: 5 * time.Millisecond,
			OnStuck: func(event ToolStuckEvent) {
				stuck <- event
			},
		},
	)
	if err != nil {
		t.Fatalf("timeout should remain model-visible, not abort runner: %v", err)
	}
	select {
	case event := <-stuck:
		if event.ID != "tool-1" || event.Name != "browser" || event.Threshold != 5*time.Millisecond || event.Timeout != 25*time.Millisecond {
			t.Fatalf("unexpected stuck event: %+v", event)
		}
	default:
		t.Fatal("expected stuck watchdog event before timeout")
	}
}

func TestRunnerStopsImmediatelyWhenToolRequestsApproval(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	caller := &fakeCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.TextBlock("checking"),
			protocol.ToolUseBlock("tool-1", "write_file", map[string]interface{}{"path": "notes/todo.txt", "content": "hello"}),
		}},
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
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			_ = input
			if name != "write_file" {
				t.Fatalf("unexpected tool %q", name)
			}
			return ToolExecutionResult{}, pendingStopError{}
		},
		MaxTurns: 5,
	}.Run(context.Background())
	if !errors.As(err, new(pendingStopError)) {
		t.Fatalf("expected pending-stop error, got %v", err)
	}
	if result == nil || !result.Stopped {
		t.Fatalf("expected runner to stop, got %+v", result)
	}
	if caller.calls != 1 {
		t.Fatalf("expected one model call before approval stop, got %d", caller.calls)
	}
	if len(messages) != 3 {
		t.Fatalf("expected assistant + tool result appended before stop, got %d messages", len(messages))
	}
	output := messages[2].Content[0].Content
	if !strings.Contains(output, `"status":"permission_pending"`) || !strings.Contains(output, `"request_id":"perm-1"`) {
		t.Fatalf("expected structured permission_pending tool result, got %q", output)
	}
}

func TestRunnerBackfillsSiblingToolResultsWhenApprovalStopsBatch(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "start")}
	caller := &fakeCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "xargs grep"}),
			protocol.ToolUseBlock("tool-2", "read_file", map[string]interface{}{"path": "internal/acp/server/agent.go"}),
		}},
	}}

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
			_ = input
			if name != "bash" {
				t.Fatalf("runner should stop before executing sibling tool, got %q", name)
			}
			return ToolExecutionResult{}, pendingStopError{}
		},
		MaxTurns: 5,
	}.Run(context.Background())
	if !errors.As(err, new(pendingStopError)) {
		t.Fatalf("expected pending-stop error, got %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected assistant + tool result appended before stop, got %d messages", len(messages))
	}
	results := messages[2].Content
	if len(results) != 2 {
		t.Fatalf("expected result plus skipped sibling backfill, got %+v", results)
	}
	if results[0].ToolUseID != "tool-1" || results[1].ToolUseID != "tool-2" {
		t.Fatalf("unexpected tool result ids: %+v", results)
	}
	if !strings.Contains(results[1].Content, "skipped_due_to_pending_approval") {
		t.Fatalf("expected skipped sibling result, got %q", results[1].Content)
	}
}

func TestSanitizeMessagesForProviderDropsUnresolvedToolUseInsteadOfBackfilling(t *testing.T) {
	messages := []protocol.APIMessage{
		{
			Role: protocol.RoleAssistant,
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "xargs grep"}),
				protocol.ToolUseBlock("tool-2", "read_file", map[string]interface{}{"path": "internal/acp/server/agent.go"}),
			},
		},
		{Role: protocol.RoleUser, Content: []protocol.Block{
			protocol.ToolResultBlock("tool-1", `{"status":"error"}`),
		}},
		{Role: protocol.RoleUser, Content: []protocol.Block{
			protocol.TextBlock("继续"),
		}},
	}

	sanitized := SanitizeMessagesForProvider(messages)
	// tool-2 has no matching tool_result: it is DROPPED from the assistant
	// message instead of inserting a synthetic backfill (which would shift the
	// shared prefix and break provider prefix caching).
	if len(sanitized) != 3 {
		t.Fatalf("expected assistant(resolved only), tool result, user text; got %+v", sanitized)
	}
	if len(sanitized[0].Content) != 1 || sanitized[0].Content[0].Type != protocol.BlockToolUse || sanitized[0].Content[0].ID != "tool-1" {
		t.Fatalf("expected only resolved tool-1 kept in assistant message, got %+v", sanitized[0].Content)
	}
	if sanitized[2].Content[0].Text != "继续" {
		t.Fatalf("expected user text last, got %+v", sanitized[2])
	}
}

func TestSanitizeMessagesForProviderDropsTrailingUnresolvedToolUse(t *testing.T) {
	messages := []protocol.APIMessage{
		{
			Role: protocol.RoleAssistant,
			Content: []protocol.Block{
				protocol.ToolUseBlock("tool-9", "bash", map[string]interface{}{"command": "ls"}),
			},
		},
	}

	sanitized := SanitizeMessagesForProvider(messages)
	// The dangling trailing tool_use is dropped rather than backfilled, so the
	// list stays append-only and provider-valid.
	if len(sanitized) != 0 {
		t.Fatalf("expected orphaned trailing tool_use dropped, got %+v", sanitized)
	}
}

func TestExecuteToolUsesPreservesArtifactPaths(t *testing.T) {
	blocks := []protocol.Block{
		protocol.ToolUseBlock("tool-1", "attach_file", map[string]interface{}{"path": ".godex/.tmp/report.pdf"}),
	}

	_, executed, err := ExecuteToolUses(
		context.Background(),
		blocks,
		func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			if name != "attach_file" {
				t.Fatalf("unexpected tool %q", name)
			}
			if input["path"] != ".godex/.tmp/report.pdf" {
				t.Fatalf("unexpected tool input %+v", input)
			}
			return ToolExecutionResult{
				Output:        `{"status":"attached"}`,
				ArtifactPaths: []string{"/tmp/report.pdf"},
			}, nil
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("execute tool uses: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected one executed tool, got %d", len(executed))
	}
	if len(executed[0].ArtifactPaths) != 1 || executed[0].ArtifactPaths[0] != "/tmp/report.pdf" {
		t.Fatalf("unexpected artifact paths: %+v", executed[0].ArtifactPaths)
	}
}

func TestExecuteToolUsesKeepsOutputWhenToolReturnsError(t *testing.T) {
	blocks := []protocol.Block{
		protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "go test ./..."}),
	}

	msg, executed, err := ExecuteToolUses(
		context.Background(),
		blocks,
		func(ctx context.Context, name string, input map[string]interface{}) (ToolExecutionResult, error) {
			_ = ctx
			_ = name
			_ = input
			return ToolExecutionResult{Output: "FAIL\t./calc\nexpected 5, got -1"}, errors.New("exit status 1")
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("non-stopping tool error should not abort runner, got %v", err)
	}
	if len(executed) != 1 || !strings.Contains(executed[0].Output, "expected 5") {
		t.Fatalf("expected executed tool output to keep diagnostics, got %+v", executed)
	}
	if len(msg.Content) != 1 || !strings.Contains(msg.Content[0].Content, "expected 5") {
		t.Fatalf("expected tool_result to keep diagnostics, got %+v", msg.Content)
	}
	if !msg.Content[0].IsError {
		t.Fatalf("expected failed tool_result to persist is_error, got %+v", msg.Content[0])
	}
}
