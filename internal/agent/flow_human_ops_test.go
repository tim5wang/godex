package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/flow"
)

// flowHumanTestDef returns a flow with a human fallback node between a
// decision and an end step (P1.1 vertical slice). The decision node is the
// entry so it completes synchronously in tests (no subagent worker needed);
// the human node then becomes ready and registers a task.
func flowHumanTestDef() *flow.Definition {
	return &flow.Definition{
		FlowID:      "fl_approval",
		Name:        "Approval",
		Description: "decision -> human approve -> finalize",
		Version:     "1",
		Status:      "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "needs approval?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "yes"}, {ID: "no"}}}},
			{ID: "approve", Kind: flow.KindHuman, Prompt: "Approve this refund?",
				Human: &flow.HumanSpec{Queue: "ops", ResultVar: "approved"}},
			{ID: "end", Kind: flow.KindStep, Prompt: "finalize the refund"},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "approve", EdgeType: flow.EdgeDataDependency},
			{ID: "e2", From: "approve", To: "end", EdgeType: flow.EdgeDataDependency},
		},
	}
}

func TestFlowHumanTaskLifecycle(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "yes", Confidence: 0.6}})
	// Subagent steps after the human reply run through the fake client so
	// the test is deterministic without a real LLM worker.
	a.client = repeatedTextCaller("finalized")

	if a.humanTasks == nil {
		t.Fatal("expected human task store to be wired")
	}

	// Create + publish the flow.
	if _, err := a.CreateFlow(FlowCreateArgs{Def: flowHumanTestDef()}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_approval", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Start a run. The human node must register a task and stay waiting.
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_approval", "", map[string]any{"order_id": "o-1"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	cleanupWorkflowAfterTest(t, a, run.WorkflowID)
	started, err := a.StartFlowRun(ctx, "fl_approval", run.RunID)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if started.Status == workflowStatusError {
		t.Fatalf("run errored: %+v", started)
	}

	// The human node compiled to user_input and registered a pending task.
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	approve := workflowNodeByID(state.Nodes, "approve")
	if approve == nil {
		t.Fatalf("missing approve node: %+v", state.Nodes)
	}
	if approve.Kind != workflowNodeKindUserInput {
		t.Fatalf("expected human node compiled to user_input, got %q", approve.Kind)
	}
	if approve.Status != workflowStatusWaitingHuman {
		t.Fatalf("expected waiting_human, got %q", approve.Status)
	}
	if approve.HumanTaskID == "" {
		t.Fatal("expected human task id registered on node")
	}

	tasks, err := a.ListHumanTasks("", "")
	if err != nil {
		t.Fatalf("list human tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].NodeID != "approve" || tasks[0].Status != humanTaskStatusPending {
		t.Fatalf("unexpected tasks: %+v", tasks)
	}

	// Reply completes the node, writes the result var, and continues the run.
	replied, err := a.ReplyFlowRunHuman(ctx, "fl_approval", run.RunID, "approve", map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("reply human task: %v", err)
	}
	if replied.Status == workflowStatusError {
		t.Fatalf("run errored after reply: %+v", replied)
	}

	state, _ = a.workflowState(run.WorkflowID)
	approve = workflowNodeByID(state.Nodes, "approve")
	if approve == nil || approve.Status != workflowStatusCompleted {
		t.Fatalf("expected approve completed after reply, got %+v", approve)
	}
	if approve.Outputs == nil || approve.Outputs["approved"] == nil {
		t.Fatalf("expected result var approved in outputs: %+v", approve.Outputs)
	}

	tasks, _ = a.ListHumanTasks("", "")
	if len(tasks) != 1 || tasks[0].Status != humanTaskStatusReplied {
		t.Fatalf("expected task replied, got %+v", tasks)
	}

	// Run now progresses: end node was a dependency of approve, so it should
	// have been started (fake client completes it deterministically).
	state, _ = a.workflowState(run.WorkflowID)
	end := workflowNodeByID(state.Nodes, "end")
	if end == nil || end.Status == workflowStatusPending || end.Status == workflowStatusError {
		t.Fatalf("expected end node started after reply, got %+v", end)
	}
}

func TestCancelFlowRunCancelsHumanWaitAndTask(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "yes", Confidence: 0.6}})
	if _, err := a.CreateFlow(FlowCreateArgs{Def: flowHumanTestDef()}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	run, err := a.CreateFlowRun(context.Background(), "fl_approval", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	cleanupWorkflowAfterTest(t, a, run.WorkflowID)
	if _, err := a.StartFlowRun(context.Background(), "fl_approval", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	canceled, err := a.CancelFlowRun(context.Background(), "fl_approval", run.RunID)
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if canceled.Status != workflowStatusCanceled {
		t.Fatalf("expected canceled run, got %+v", canceled)
	}
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	node := workflowNodeByID(state.Nodes, "approve")
	if node == nil || node.Status != workflowStatusCanceled {
		t.Fatalf("expected waiting human node to be canceled, got %+v", node)
	}
	tasks, err := a.humanTasks.listRunTasks(run.RunID)
	if err != nil {
		t.Fatalf("load human tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != humanTaskStatusCanceled {
		t.Fatalf("expected human task canceled with its run, got %+v", tasks)
	}
}

func TestFlowRunTimeoutCancelsHumanWaitAndTask(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "yes", Confidence: 0.6}})
	def := flowHumanTestDef()
	def.TimeoutSec = 60
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	run, err := a.CreateFlowRun(context.Background(), def.FlowID, "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	cleanupWorkflowAfterTest(t, a, run.WorkflowID)
	if _, err := a.StartFlowRun(context.Background(), def.FlowID, run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	state.Summary.RunTimeoutAt = time.Now().UTC().Add(-time.Second)
	if err := a.workflows.save(state); err != nil {
		t.Fatalf("persist expired deadline: %v", err)
	}

	expired, err := a.RefreshFlowRun(def.FlowID, run.RunID)
	if err != nil {
		t.Fatalf("refresh expired run: %v", err)
	}
	if expired.Status != workflowStatusError || !strings.Contains(expired.Error, "timed out") {
		t.Fatalf("expected timeout error, got %+v", expired)
	}
	state, err = a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	node := workflowNodeByID(state.Nodes, "approve")
	if node == nil || node.Status != workflowStatusCanceled {
		t.Fatalf("expected human wait canceled on timeout, got %+v", node)
	}
	tasks, err := a.humanTasks.listRunTasks(run.RunID)
	if err != nil {
		t.Fatalf("load human tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != humanTaskStatusCanceled {
		t.Fatalf("expected timed-out human task canceled, got %+v", tasks)
	}
}

func TestFlowHumanTaskQueueFilter(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	// Register a task directly through the store to test queue/status filters.
	now := time.Now().UTC()
	rec := humanTaskRecord{
		TaskID:    "fr_1:approve",
		FlowID:    "fl_approval",
		RunID:     "fr_1",
		NodeID:    "approve",
		Queue:     "ops",
		Prompt:    "Approve?",
		Status:    humanTaskStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := a.humanTasks.saveTask(rec); err != nil {
		t.Fatalf("save task: %v", err)
	}

	// queue filter
	tasks, err := a.ListHumanTasks("ops", "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("queue filter: %v %+v", err, tasks)
	}
	tasks, err = a.ListHumanTasks("other", "")
	if err != nil || len(tasks) != 0 {
		t.Fatalf("queue filter miss: %v %+v", err, tasks)
	}
	// status filter
	tasks, err = a.ListHumanTasks("", humanTaskStatusPending)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("status filter: %v %+v", err, tasks)
	}
	tasks, err = a.ListHumanTasks("", humanTaskStatusReplied)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("status filter miss: %v %+v", err, tasks)
	}
}

func TestFlowHumanTimeoutEscalatesToLLM(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "yes", Confidence: 0.6}})
	// llm escalation re-schedules the node as an llm_task subagent; drive it
	// with the fake client so the test does not need a real LLM worker.
	a.client = repeatedTextCaller("escalated")

	def := flowHumanTestDef()
	def.Nodes[1].Human.OnTimeout = "llm"
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_approval", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_approval", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	cleanupWorkflowAfterTest(t, a, run.WorkflowID)
	if _, err := a.StartFlowRun(ctx, "fl_approval", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	// Overdue: force the task past its deadline and run the timeout sweep.
	tasks, _ := a.humanTasks.listRunTasks(run.RunID)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	tasks[0].DueAt = time.Now().UTC().Add(-time.Minute)
	tasks[0].TimeoutMS = 1000
	tasks[0].OnTimeout = "llm"
	if err := a.humanTasks.saveTask(tasks[0]); err != nil {
		t.Fatalf("save overdue task: %v", err)
	}

	changed, err := a.CheckHumanTaskTimeouts(time.Now().UTC())
	if err != nil {
		t.Fatalf("check timeouts: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed task, got %+v", changed)
	}
	// llm escalation re-schedules the blocked node as an llm_task.
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	approve := workflowNodeByID(state.Nodes, "approve")
	if approve == nil {
		t.Fatalf("missing approve node")
	}
	if approve.Kind != agentGraphNodeLLMTask {
		t.Fatalf("expected node re-scheduled as llm_task, got %q", approve.Kind)
	}
	if approve.Status == workflowStatusError || approve.Status == workflowStatusPending {
		t.Fatalf("expected llm fallback node started, got %q", approve.Status)
	}
	if strings.TrimSpace(changed[0].Status) == "" {
		t.Fatalf("expected changed task status set: %+v", changed[0])
	}
}
