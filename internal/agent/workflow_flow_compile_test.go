package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/decision"
	"github.com/tim5wang/godex/internal/core/flow"
)

// flowInputsToMaps round-trips compiled engine inputs through JSON so the
// workflow tool (which consumes map[string]interface{}) can create them.
func flowInputsToMaps(nodes []workflowNodeInput, edges []workflowEdgeInput) ([]map[string]interface{}, []map[string]interface{}, error) {
	nm := make([]map[string]interface{}, 0, len(nodes))
	for _, n := range nodes {
		raw, err := json.Marshal(n)
		if err != nil {
			return nil, nil, err
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, nil, err
		}
		nm = append(nm, m)
	}
	em := make([]map[string]interface{}, 0, len(edges))
	for _, e := range edges {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, nil, err
		}
		em = append(em, m)
	}
	return nm, em, nil
}

// branchE2EFlow builds a "decision → branch → auto/llm" flow: the decision
// gate synchronously completes, the branch gateway reads its choice and
// routes to exactly one target (auto_run or llm_run) via when.choice edges.
func branchE2EFlow() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_branch_e2e",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "should we auto-refund?",
				Decision: &flow.DecisionSpec{DecisionType: "choice",
					Choices: []flow.Choice{{ID: "auto"}, {ID: "llm"}}}},
			{ID: "auto_run", Kind: flow.KindStep, Prompt: "auto-refund the order"},
			{ID: "llm_run", Kind: flow.KindStep, Prompt: "escalate to an agent"},
			{ID: "br", Kind: flow.KindBranch,
				Branch: &flow.BranchSpec{
					Cases: []flow.BranchCase{
						{Name: "auto", To: "auto_run", Condition: flow.Condition{Choice: "auto"}},
						{Name: "llm", To: "llm_run", Condition: flow.Condition{Choice: "llm"}},
					},
					DefaultTo: "llm_run",
				}},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "decide", To: "br", EdgeType: flow.EdgeDataDependency},
		},
	}
}

// TestFlowCompileBranchRoutesEndToEnd runs the full F1a path: Flow Definition
// → flow.Compile → engine inputs → durable workflow run, asserting the
// decision gate completes synchronously, the branch gateway writes
// outputs.choice and exactly one target is appended.
func TestFlowCompileBranchRoutesEndToEnd(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.93}})

	compiled, err := flow.Compile(branchE2EFlow())
	if err != nil {
		t.Fatalf("flow compile: %v", err)
	}
	nodes, edges, err := compileFlowToWorkflowInputs(compiled)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	nodeMaps, edgeMaps, err := flowInputsToMaps(nodes, edges)
	if err != nil {
		t.Fatalf("to maps: %v", err)
	}

	created := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "create", "workflow_id": "wf_flow_branch_e2e",
		"nodes": nodeMaps, "edges": edgeMaps,
	})
	cleanupWorkflowAfterTest(t, a, "wf_flow_branch_e2e")
	// Static nodes only: decide + br (branch targets are append templates).
	if len(created.Nodes) != 2 {
		t.Fatalf("expected 2 static nodes (decide, br), got %d: %+v", len(created.Nodes), created.Nodes)
	}

	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_flow_branch_e2e",
	})
	// Decision gate completed synchronously with the scripted choice.
	if nodeStatus(started.Nodes, "decide") != workflowStatusCompleted {
		t.Fatalf("expected decision completed synchronously, got %+v", started.Nodes)
	}
	decide := workflowNodeViewByID(started.Nodes, "decide")
	if decide == nil || decide.Decision == nil || decide.Decision.Choice != "auto" {
		t.Fatalf("decision result not persisted: %+v", decide)
	}
	if decide.JobID != "" {
		t.Fatalf("decision must not start a subagent job, got %q", decide.JobID)
	}
	// Branch gateway completed synchronously with outputs.choice=auto.
	if nodeStatus(started.Nodes, "br") != workflowStatusCompleted {
		t.Fatalf("expected branch completed synchronously, got %+v", started.Nodes)
	}
	br := workflowNodeViewByID(started.Nodes, "br")
	if br == nil || br.Outputs["choice"] != "auto" {
		t.Fatalf("branch outputs.choice not auto: %+v", br)
	}
	// The selected append target is not only durable; the scheduler starts it
	// in the same pass. The other branch remains absent.
	autoRun := workflowNodeViewByID(started.Nodes, "auto_run")
	if autoRun == nil {
		t.Fatalf("expected auto_run appended, got %+v", started.Nodes)
	}
	if autoRun.JobID == "" || autoRun.Status != workflowStatusRunning {
		t.Fatalf("expected auto_run to be scheduled immediately, got %+v", autoRun)
	}
	if nodeExists(started.Nodes, "llm_run") {
		t.Fatalf("llm_run must not be appended on choice=auto: %+v", started.Nodes)
	}
}

// TestFlowCompileBranchDefaultRoutes verifies the reserved default route when
// no case matches.
func TestFlowCompileBranchDefaultRoutes(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "unknown", Confidence: 0.2}})

	compiled, err := flow.Compile(branchE2EFlow())
	if err != nil {
		t.Fatalf("flow compile: %v", err)
	}
	nodes, edges, err := compileFlowToWorkflowInputs(compiled)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	nodeMaps, edgeMaps, err := flowInputsToMaps(nodes, edges)
	if err != nil {
		t.Fatalf("to maps: %v", err)
	}
	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "create", "workflow_id": "wf_flow_branch_default",
		"nodes": nodeMaps, "edges": edgeMaps,
	})
	cleanupWorkflowAfterTest(t, a, "wf_flow_branch_default")
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_flow_branch_default",
	})
	br := workflowNodeViewByID(started.Nodes, "br")
	if br == nil || br.Outputs["choice"] != "default" {
		t.Fatalf("expected default route, got %+v", br)
	}
	if !nodeExists(started.Nodes, "llm_run") {
		t.Fatalf("expected default target llm_run appended, got %+v", started.Nodes)
	}
	if nodeExists(started.Nodes, "auto_run") {
		t.Fatalf("auto_run must not be appended on default: %+v", started.Nodes)
	}
}

func workflowNodeViewByID(nodes []workflowNodeView, id string) *workflowNodeView {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

// decisionDirectConditionFlow builds the canvas model of E4: a decision node
// routes DIRECTLY through condition edges with when.choice (one labelled
// branch port per choice on the canvas) — no separate branch gateway node.
func decisionDirectConditionFlow() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_decision_direct",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "decide", Kind: flow.KindDecision, Prompt: "auto or human?",
				Decision: &flow.DecisionSpec{DecisionType: "choice",
					Choices: []flow.Choice{{ID: "auto"}, {ID: "human"}}}},
			{ID: "auto_run", Kind: flow.KindStep, Prompt: "handle automatically"},
			{ID: "human_run", Kind: flow.KindStep, Prompt: "escalate to human"},
		},
		Edges: []flow.Edge{
			{ID: "e_auto", From: "decide", To: "auto_run", EdgeType: flow.EdgeCondition,
				When: &flow.Condition{Choice: "auto"}},
			{ID: "e_human", From: "decide", To: "human_run", EdgeType: flow.EdgeCondition,
				When: &flow.Condition{Choice: "human"}},
		},
	}
}

// TestFlowCompileDecisionDirectConditionRoutes verifies the E4 canvas model:
// decision → condition edges (when.choice) — the engine writes outputs.choice
// and appends exactly ONE target, so the labelled branch ports on canvas and
// the runtime routing can never disagree.
func TestFlowCompileDecisionDirectConditionRoutes(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&scriptedDecisionCaller{result: decision.Result{Choice: "auto", Confidence: 0.9}})

	compiled, err := flow.Compile(decisionDirectConditionFlow())
	if err != nil {
		t.Fatalf("flow compile: %v", err)
	}
	nodes, edges, err := compileFlowToWorkflowInputs(compiled)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	nodeMaps, edgeMaps, err := flowInputsToMaps(nodes, edges)
	if err != nil {
		t.Fatalf("to maps: %v", err)
	}

	created := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "create", "workflow_id": "wf_decision_direct",
		"nodes": nodeMaps, "edges": edgeMaps,
	})
	cleanupWorkflowAfterTest(t, a, "wf_decision_direct")
	// Static nodes: only decide (targets are append templates via condition
	// edges). The decision itself is a static job node.
	if len(created.Nodes) != 1 {
		t.Fatalf("expected 1 static node (decide), got %d: %+v", len(created.Nodes), created.Nodes)
	}

	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_decision_direct",
	})
	decide := workflowNodeViewByID(started.Nodes, "decide")
	if decide == nil || decide.Status != workflowStatusCompleted {
		t.Fatalf("expected decision completed synchronously, got %+v", started.Nodes)
	}
	if decide.Decision == nil || decide.Decision.Choice != "auto" {
		t.Fatalf("decision choice not auto: %+v", decide)
	}
	// Exactly the auto branch appended; human_run stays out.
	if !nodeExists(started.Nodes, "auto_run") {
		t.Fatalf("expected auto_run appended, got %+v", started.Nodes)
	}
	if nodeExists(started.Nodes, "human_run") {
		t.Fatalf("human_run must not be appended on choice=auto: %+v", started.Nodes)
	}
}

func cleanupWorkflowAfterTest(t *testing.T, a *Agent, workflowID string) {
	t.Helper()
	t.Cleanup(func() {
		state, err := a.cancelWorkflowNode(context.Background(), workflowID, "")
		if err != nil {
			t.Errorf("cancel test workflow: %v", err)
			return
		}
		var jobIDs []string
		for _, node := range state.Nodes {
			if node.JobID != "" {
				jobIDs = append(jobIDs, node.JobID)
			}
		}
		if len(jobIDs) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := waitSubagents(ctx, a, subagentWaitRequest{
			JobIDs: jobIDs, Mode: "all", TimeoutMS: 3000,
		}); err != nil {
			t.Errorf("wait for test workflow jobs to stop: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for _, jobID := range jobIDs {
			for {
				a.subagentJobs.mu.Lock()
				_, active := a.subagentJobs.cancels[jobID]
				a.subagentJobs.mu.Unlock()
				if !active {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("timed out waiting for test workflow job %s goroutine to stop", jobID)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
}
