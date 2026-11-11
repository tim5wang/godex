package conversation

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestDeliveryBudgetRequiresAnswerDespiteFileWrites(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "investigate")}
	var responses []protocol.Response
	for i := 0; i < 4; i++ {
		responses = append(responses, protocol.Response{Content: []protocol.Block{
			protocol.ToolUseBlock(itoa(i), "write_file", map[string]interface{}{"path": "notes-" + itoa(i)}),
		}})
	}
	responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("Verified A; B is still unverified.")}})
	caller := &fakeCaller{responses: responses}
	executions := 0
	result, err := (Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			req := NewRequest("model", 1024, "xhigh", "system", messages, []protocol.ToolSchema{{Name: "write_file"}})
			req.RuntimeTail = "original runtime context"
			return req, nil
		},
		AppendAssistant:   func(msg protocol.Message) { messages = append(messages, msg) },
		AppendToolResults: func(msg protocol.Message) { messages = append(messages, msg) },
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			executions++
			return ToolExecutionResult{Output: "saved"}, nil
		},
		MaxSilentToolRounds: 2,
		MaxTurns:            10,
	}).Run(context.Background())
	if err != nil || result == nil || !result.Completed {
		t.Fatalf("expected an evidence-based handoff, got %+v, %v", result, err)
	}
	if executions != 4 || caller.calls != 5 {
		t.Fatalf("unexpected extra work: tools=%d model calls=%d", executions, caller.calls)
	}
	if !strings.Contains(caller.requests[2].RuntimeTail, "Delivery checkpoint") || len(caller.requests[2].Tools) == 0 {
		t.Fatal("soft budget should request a conclusion while allowing necessary tools")
	}
	last := caller.requests[4]
	if len(last.Tools) != 0 || !strings.Contains(last.RuntimeTail, "unfinished work") || !strings.Contains(last.RuntimeTail, "original runtime context") {
		t.Fatalf("expected a delivery-only request preserving runtime context, got %+v", last)
	}
	if last.ReasoningEffort != "xhigh" {
		t.Fatalf("delivery must respect explicit reasoning effort, got %q", last.ReasoningEffort)
	}
}

func TestDeliveryCheckpointTextIsRetainedAndResetsSilentBudget(t *testing.T) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "investigate")}
	var responses []protocol.Response
	for i := 0; i < 5; i++ {
		blocks := []protocol.Block{}
		if i == 2 {
			blocks = append(blocks, protocol.TextBlock("Verified A; checking the remaining boundary case."))
		}
		blocks = append(blocks, protocol.ToolUseBlock(itoa(i), "read_file", map[string]interface{}{"path": "source-" + itoa(i)}))
		responses = append(responses, protocol.Response{Content: blocks})
	}
	responses = append(responses, protocol.Response{Content: []protocol.Block{protocol.TextBlock("A and the boundary case are verified.")}})
	caller := &fakeCaller{responses: responses}
	_, err := (Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return NewRequest("model", 1024, "", "system", messages, []protocol.ToolSchema{{Name: "read_file"}}), nil
		},
		AppendAssistant:   func(msg protocol.Message) { messages = append(messages, msg) },
		AppendToolResults: func(msg protocol.Message) { messages = append(messages, msg) },
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{Output: "source"}, nil
		},
		MaxSilentToolRounds: 2,
		MaxTurns:            10,
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caller.calls != 6 || len(caller.requests[4].Tools) == 0 || strings.Contains(caller.requests[3].RuntimeTail, "Delivery checkpoint") {
		t.Fatal("visible conclusions should reset the silence counter without an extra summarization call")
	}
	found := false
	for _, msg := range caller.requests[3].Messages {
		for _, block := range msg.Content {
			found = found || block.Text == "Verified A; checking the remaining boundary case."
		}
	}
	if !found {
		t.Fatal("later model calls must retain the evidence checkpoint in assistant history")
	}
}

func TestDeliveryBudgetDoesNotExecuteUnexpectedToolCalls(t *testing.T) {
	caller := &fakeCaller{}
	for i := 0; i < 3; i++ {
		caller.responses = append(caller.responses, protocol.Response{Content: []protocol.Block{
			protocol.ToolUseBlock(itoa(i), "write_file", map[string]interface{}{"path": "notes-" + itoa(i)}),
		}})
	}
	executions := 0
	result, err := (Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return protocol.Request{Tools: []protocol.ToolSchema{{Name: "write_file"}}}, nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			executions++
			return ToolExecutionResult{Output: "saved"}, nil
		},
		MaxSilentToolRounds: 1,
		MaxTurns:            5,
	}).Run(context.Background())
	if err == nil || result == nil || !result.Stopped || executions != 2 {
		t.Fatalf("unexpected tools must not execute during a delivery-only request: result=%+v err=%v executions=%d", result, err, executions)
	}
}

type deliveryOverflowCaller struct {
	fakeCaller
	overflowed bool
}

func (c *deliveryOverflowCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	if c.calls == 2 && !c.overflowed {
		c.overflowed = true
		c.requests = append(c.requests, req)
		return nil, &apiStatusError{StatusCode: 400, Message: "maximum context length exceeded"}
	}
	return c.fakeCaller.Call(ctx, req)
}

func TestDeliveryBudgetSurvivesContextOverflowRecovery(t *testing.T) {
	caller := &deliveryOverflowCaller{fakeCaller: fakeCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("a", "read_file", map[string]interface{}{"path": "a"})}},
		{Content: []protocol.Block{protocol.ToolUseBlock("b", "read_file", map[string]interface{}{"path": "b"})}},
		{Content: []protocol.Block{protocol.TextBlock("Verified A; remaining work is unverified.")}},
	}}}
	result, err := (Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return protocol.Request{Tools: []protocol.ToolSchema{{Name: "read_file"}}}, nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{Output: "source"}, nil
		},
		OnContextOverflow:            func(context.Context) bool { return true },
		MaxContextOverflowRecoveries: 1,
		MaxSilentToolRounds:          1,
		MaxTurns:                     5,
	}).Run(context.Background())
	if err != nil || result == nil || !result.Completed || len(caller.requests) != 4 {
		t.Fatalf("expected a bounded delivery after compaction: %+v, %v", result, err)
	}
	for _, req := range caller.requests[2:] {
		if len(req.Tools) != 0 || !strings.Contains(req.RuntimeTail, "Delivery budget reached") {
			t.Fatal("context rebuilding must not restore tools or drop the delivery instruction")
		}
	}
}
