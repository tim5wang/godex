package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/decision"
)

// TestWorkflowDecisionInjectsKindDecisionUsageContext verifies P1.6: decision
// node calls carry a UsageContext tagged kind=decision so the usage system can
// meter them independently of ordinary LLM calls.
func TestWorkflowDecisionInjectsKindDecisionUsageContext(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	caller := &scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.93, Model: "jev-test"}}
	a.SetDecisionCaller(caller)

	// Carry a caller-side UsageContext (attribution) into the run; the
	// decision node must preserve it and add kind=decision.
	ctx := conversation.WithUsageContext(context.Background(), conversation.UsageContext{
		SourceChannel: "test",
		SessionID:     "sess-flow",
	})
	decisionTestWorkflow(t, a, "wf_decision_kind", branchEdges())
	started := runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_kind",
	})
	if nodeStatus(started.Nodes, "decide") != workflowStatusCompleted {
		t.Fatalf("expected decision node completed, got %+v", started.Nodes)
	}

	caller.mu.Lock()
	defer caller.mu.Unlock()
	if caller.calls != 1 {
		t.Fatalf("expected one decision call, got %d", caller.calls)
	}
	if caller.lastCtx == nil {
		t.Fatal("expected decision ctx recorded")
	}
	uc, ok := conversation.UsageContextFromContext(caller.lastCtx)
	if !ok {
		t.Fatal("expected UsageContext injected into decision call")
	}
	if uc.Kind != "decision" {
		t.Fatalf("expected kind=decision in usage context, got %q", uc.Kind)
	}
	if uc.SessionID != "sess-flow" {
		t.Fatalf("expected caller session attribution preserved, got %q", uc.SessionID)
	}
}

// TestWorkflowDecisionUsageEventMetersKind captures the actual usage event
// emitted by the conversation layer when a decision call goes through the
// real LLM adapter, and asserts it carries kind=decision.
func TestWorkflowDecisionUsageEventMetersKind(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	// Wire a usage hook that records emitted events.
	var mu sync.Mutex
	var got []conversation.UsageEvent
	unsub := conversation.AddUsageHook(func(_ context.Context, ev conversation.UsageEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	defer unsub()

	// Route decision calls through the real LLM adapter backed by a fake
	// chat caller that returns a valid structured verdict.
	fakeChat := &fakeConversationCaller{reply: `{"choice":"auto","confidence":0.91}`}
	a.SetDecisionCaller(decision.NewLLMCaller(decision.LLMCallerOptions{
		Provider: "llm",
		Caller:   fakeChat,
	}))

	ctx := conversation.WithUsageContext(context.Background(), conversation.UsageContext{
		SourceChannel: "test",
		SessionID:     "sess-usage",
	})
	decisionTestWorkflow(t, a, "wf_decision_usage", branchEdges())
	started := runWorkflowTool(t, a, ctx, map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_usage",
	})
	if nodeStatus(started.Nodes, "decide") != workflowStatusCompleted {
		t.Fatalf("expected decision node completed, got %+v", started.Nodes)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one usage event from the decision call")
	}
	last := got[len(got)-1]
	if last.Context.Kind != "decision" {
		t.Fatalf("expected usage event kind=decision, got %q", last.Context.Kind)
	}
	if last.Context.SessionID == "" {
		t.Fatal("expected session attribution on decision usage event")
	}
}

// TestWorkflowDecisionDoesNotTouchBudgetOrTranscript asserts the decision
// node stays out of the session transcript and context budget (P1.6): the
// node completes synchronously with no subagent job and no transcript rows.
func TestWorkflowDecisionDoesNotTouchBudgetOrTranscript(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	caller := &scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.9, Model: "jev-test"}}
	a.SetDecisionCaller(caller)

	decisionTestWorkflow(t, a, "wf_decision_clean", branchEdges())
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_clean",
	})
	decide := nodeByID(started.Nodes, "decide")
	if decide == nil || decide.Status != workflowStatusCompleted {
		t.Fatalf("expected decision completed, got %+v", decide)
	}
	if decide.JobID != "" {
		t.Fatalf("decision node must not start a subagent job, got %q", decide.JobID)
	}
	// The decision verdict must land on node outputs (routing source), not
	// on the session transcript.
	if decide.Decision == nil || decide.Decision.Choice != "auto" {
		t.Fatalf("expected decision result persisted, got %+v", decide.Decision)
	}
	if _, ok := decide.Outputs["choice"]; !ok {
		t.Fatalf("expected choice in node outputs for routing, got %+v", decide.Outputs)
	}
}

// fakeConversationCaller is a minimal conversation.Caller for decision tests.
type fakeConversationCaller struct {
	reply string
}

func (f *fakeConversationCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	// The real conversation client emits a usage event for every Call
	// (deferred in client.Call), filling event.Context from the ctx usage
	// context. Emulate both so the kind=decision tag injected by the decision
	// node is observable to usage hooks.
	resp := &protocol.Response{
		Content: []protocol.Block{protocol.TextBlock(f.reply)},
		Usage:   &protocol.Usage{InputTokens: 8, OutputTokens: 4},
	}
	ev := conversation.UsageEvent{Request: req, Response: resp}
	if uc, ok := conversation.UsageContextFromContext(ctx); ok {
		ev.Context = uc
	}
	conversation.NotifyUsageHooksForTest(ctx, ev)
	return resp, nil
}
