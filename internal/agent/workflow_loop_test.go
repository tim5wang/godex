package agent

import (
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// TestWorkflowConditionNotNegates tests the engine-side Not predicate that
// loop exit_when compiles to (P1.5): Not(choice=done) matches a node whose
// choice is anything except "done".
func TestWorkflowConditionNotNegates(t *testing.T) {
	doneNode := workflowNode{ID: "body", Status: workflowStatusCompleted, Outputs: map[string]any{"choice": "done"}}
	againNode := workflowNode{ID: "body", Status: workflowStatusCompleted, Outputs: map[string]any{"choice": "retry"}}

	notDone := workflowEdgeCondition{Not: &workflowEdgeCondition{Choice: "done"}}
	if workflowConditionMatchesNode(notDone, doneNode) {
		t.Fatal("Not(choice=done) should NOT match a node with choice=done")
	}
	if !workflowConditionMatchesNode(notDone, againNode) {
		t.Fatal("Not(choice=done) should match a node with choice=retry")
	}

	// Nested Not inside All: both sub-predicates must hold.
	nested := workflowEdgeCondition{All: []workflowEdgeCondition{
		{Status: workflowStatusCompleted},
		{Not: &workflowEdgeCondition{Choice: "done"}},
	}}
	if nestedConditionMatches := workflowConditionMatchesNode(nested, againNode); !nestedConditionMatches {
		t.Fatal("All[completed, Not(choice=done)] should match retry node")
	}
	if workflowConditionMatchesNode(nested, doneNode) {
		t.Fatal("All[completed, Not(choice=done)] should NOT match done node")
	}
}

// TestFlowConditionNotCompilesToWorkflowNot verifies the compile adapter maps
// a flow.Condition{Not: ...} onto workflowEdgeCondition.Not.
func TestFlowConditionNotCompilesToWorkflowNot(t *testing.T) {
	exit := flow.Condition{Choice: "done"}
	wf := flowConditionToWorkflow(flow.Condition{Not: &exit})
	if wf.Not == nil {
		t.Fatal("expected workflowEdgeCondition.Not to be set")
	}
	if wf.Not.Choice != "done" {
		t.Fatalf("expected Not{choice: done}, got %+v", wf.Not)
	}

	// Not must also flow through All nesting.
	nested := flowConditionToWorkflow(flow.Condition{
		All: []flow.Condition{{Not: &flow.Condition{Verdict: "fail"}}},
	})
	if len(nested.All) != 1 || nested.All[0].Not == nil || nested.All[0].Not.Verdict != "fail" {
		t.Fatalf("expected nested Not verdict fail, got %+v", nested.All)
	}
}

// TestWorkflowLoopEdgeCompilesIdempotent verifies a loop compiles to a
// control_flow append edge with Not(exit_when), iteration key and cap — the
// engine-side idempotency contract (restart-safe, capped at max_iterations).
func TestWorkflowLoopEdgeCompilesIdempotent(t *testing.T) {
	a := newTestAgent(t, 4096)
	def := &flow.Definition{
		FlowID:  "fl_loop",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "start", Kind: flow.KindStep, Prompt: "begin"},
			{ID: "decide", Kind: flow.KindDecision, Prompt: "retry?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "done"}, {ID: "retry"}}}},
			{ID: "loop1", Kind: flow.KindLoop,
				Loop: &flow.LoopSpec{
					Body:          []string{"decide"},
					ExitWhen:      flow.Condition{Choice: "done"},
					MaxIterations: 5,
					IterationKey:  "retry_loop",
				}},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "start", To: "decide", EdgeType: flow.EdgeDataDependency},
			{ID: "e2", From: "decide", To: "loop1", EdgeType: flow.EdgeDataDependency},
		},
	}
	v, err := a.CreateFlow(FlowCreateArgs{Def: def})
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	rec, err := a.flows.loadVersion("fl_loop", v.Version)
	if err != nil {
		t.Fatalf("load version: %v", err)
	}
	if rec.Compiled == nil {
		t.Fatal("expected compiled artifact")
	}
	// The loop edge must exist with Not(exit_when), iteration key and cap.
	found := false
	for _, e := range rec.Compiled.Edges {
		if e.ID != "loop1_loop" {
			continue
		}
		found = true
		if e.When.Not == nil || e.When.Not.Choice != "done" {
			t.Fatalf("expected Not(choice=done) on loop edge, got %+v", e.When)
		}
		if e.MaxIterations != 5 {
			t.Fatalf("expected max_iterations 5, got %d", e.MaxIterations)
		}
		if e.IterationKey != "retry_loop" {
			t.Fatalf("expected iteration_key retry_loop, got %q", e.IterationKey)
		}
	}
	if !found {
		t.Fatalf("expected loop1_loop edge in compiled: %+v", rec.Compiled.Edges)
	}

	// Lower to engine edge inputs: the Not condition must survive the adapter.
	engineNodes, engineEdges, err := compileFlowToWorkflowInputs(rec.Compiled)
	if err != nil {
		t.Fatalf("compile to engine inputs: %v", err)
	}
	_ = engineNodes
	engineFound := false
	for _, e := range engineEdges {
		if e.ID != "loop1_loop" {
			continue
		}
		engineFound = true
		if e.When.Not == nil || e.When.Not.Choice != "done" {
			t.Fatalf("expected engine Not(choice=done), got %+v", e.When)
		}
		if e.MaxIterations != 5 || e.IterationKey != "retry_loop" {
			t.Fatalf("iteration bounds lost in adapter: %+v", e)
		}
	}
	if !engineFound {
		t.Fatalf("expected loop edge in engine inputs: %+v", engineEdges)
	}
}
