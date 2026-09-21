package agent

import (
	"context"
	"encoding/json"
	"testing"

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
	// Exactly the auto branch appended; llm_run stays out.
	if !nodeExists(started.Nodes, "auto_run") {
		t.Fatalf("expected auto_run appended, got %+v", started.Nodes)
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
