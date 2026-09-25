package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/tim5wang/godex/internal/core/decision"
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
	normalized := normalizeWorkflowEdgeCondition(nested)
	if len(normalized.All) != 1 || normalized.All[0].Not == nil || normalized.All[0].Not.Verdict != "fail" {
		t.Fatalf("normalization must preserve nested Not predicates, got %+v", normalized)
	}
	if workflowConditionEmpty(normalizeWorkflowEdgeCondition(wf)) {
		t.Fatal("a negated predicate must remain non-empty after normalization")
	}
}

func TestWorkflowEdgeFromPrefixMatchesOnlyNumberedIterations(t *testing.T) {
	edge := workflowEdge{FromPrefix: "task_"}
	for _, tc := range []struct {
		id      string
		matches bool
	}{
		{id: "task_1", matches: true},
		{id: "task_23", matches: true},
		{id: "task_review_1", matches: false},
		{id: "task_", matches: false},
		{id: "task_0", matches: false},
	} {
		if got := workflowEdgeMatchesSource(edge, workflowNode{ID: tc.id}); got != tc.matches {
			t.Errorf("workflowEdgeMatchesSource(%q) = %v, want %v", tc.id, got, tc.matches)
		}
	}
}

func TestWorkflowStatusPreservesPartiallyCompletedCancellation(t *testing.T) {
	a := newTestAgent(t, 4096)
	state := workflowState{Nodes: []workflowNode{
		{ID: "completed", Status: workflowStatusCompleted},
		{ID: "canceled", Status: workflowStatusCanceled},
	}}
	a.refreshWorkflowStatus(&state)
	if state.Summary.Status != workflowStatusCanceled {
		t.Fatalf("completed+canceled terminal nodes should resolve canceled, got %q", state.Summary.Status)
	}
}

// TestWorkflowLoopEdgesCompileIdempotent verifies a loop compiles to an entry
// append edge plus a bounded continuation edge that matches repeated body IDs.
func TestWorkflowLoopEdgeCompilesIdempotent(t *testing.T) {
	a := newTestAgent(t, 4096)
	def := &flow.Definition{
		FlowID:  "fl_loop",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "start", Kind: flow.KindDecision, Prompt: "begin",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "go"}}}},
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
			{ID: "e1", From: "start", To: "loop1", EdgeType: flow.EdgeDataDependency},
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
	// The entry edge starts the first body iteration. The continuation edge
	// evaluates exit_when against each dynamic body result.
	entryFound, continueFound := false, false
	for _, e := range rec.Compiled.Edges {
		switch e.ID {
		case "loop1_entry":
			entryFound = true
			if e.From != "start" || e.When.Status != "completed" {
				t.Fatalf("unexpected loop entry edge: %+v", e)
			}
			if e.Append.ID != "decide_{iteration}" || len(e.Append.DependsOn) != 1 || e.Append.DependsOn[0] != "{source}" {
				t.Fatalf("loop entry must append a source-dependent body template: %+v", e.Append)
			}
		case "loop1_continue":
			continueFound = true
			if e.FromPrefix != "decide_" {
				t.Fatalf("expected dynamic body source prefix, got %q", e.FromPrefix)
			}
			if e.When.Not == nil || e.When.Not.Choice != "done" {
				t.Fatalf("expected Not(choice=done) on loop continuation, got %+v", e.When)
			}
			if e.MaxIterations != 5 {
				t.Fatalf("expected max_iterations 5, got %d", e.MaxIterations)
			}
			if e.IterationKey != "retry_loop" {
				t.Fatalf("expected iteration_key retry_loop, got %q", e.IterationKey)
			}
		}
	}
	if !entryFound || !continueFound {
		t.Fatalf("expected loop entry and continuation edges in compiled: %+v", rec.Compiled.Edges)
	}

	// Lower to engine edge inputs: the Not condition must survive the adapter.
	engineNodes, engineEdges, err := compileFlowToWorkflowInputs(rec.Compiled)
	if err != nil {
		t.Fatalf("compile to engine inputs: %v", err)
	}
	if len(engineNodes) != 1 || engineNodes[0].ID != "start" {
		t.Fatalf("loop body should be append-only, static nodes: %+v", engineNodes)
	}
	engineContinueFound := false
	for _, e := range engineEdges {
		if e.ID != "loop1_continue" {
			continue
		}
		engineContinueFound = true
		if e.FromPrefix != "decide_" {
			t.Fatalf("dynamic source prefix lost in adapter: %+v", e)
		}
		if e.When.Not == nil || e.When.Not.Choice != "done" {
			t.Fatalf("expected engine Not(choice=done), got %+v", e.When)
		}
		if e.MaxIterations != 5 || e.IterationKey != "retry_loop" {
			t.Fatalf("iteration bounds lost in adapter: %+v", e)
		}
	}
	if !engineContinueFound {
		t.Fatalf("expected loop continuation edge in engine inputs: %+v", engineEdges)
	}
}

type sequenceDecisionCaller struct {
	mu      sync.Mutex
	results []decision.Result
}

func (c *sequenceDecisionCaller) Decide(context.Context, decision.Request) (decision.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.results) == 0 {
		return decision.Result{}, nil
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result, nil
}

func TestWorkflowLoopExecutesBodyUntilExit(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.SetDecisionCaller(&sequenceDecisionCaller{results: []decision.Result{
		{Choice: "go"},
		{Choice: "retry"},
		{Choice: "done"},
		{Choice: "ok"},
	}})
	def := &flow.Definition{
		FlowID: "fl_loop_exec", Version: "1", Status: "draft",
		Nodes: []flow.Node{
			{ID: "start", Kind: flow.KindDecision, Prompt: "enter loop?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "go"}}}},
			{ID: "body", Kind: flow.KindDecision, Prompt: "done or retry?",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "done"}, {ID: "retry"}}}},
			{ID: "after", Kind: flow.KindDecision, Prompt: "confirm completion",
				Decision: &flow.DecisionSpec{DecisionType: "choice", Choices: []flow.Choice{{ID: "ok"}}}},
			{ID: "loop", Kind: flow.KindLoop, Loop: &flow.LoopSpec{
				Body: []string{"body"}, ExitWhen: flow.Condition{Choice: "done"}, MaxIterations: 4,
			}},
		},
		Edges: []flow.Edge{
			{ID: "enter", From: "start", To: "loop", EdgeType: flow.EdgeDataDependency},
			{ID: "leave", From: "loop", To: "after", EdgeType: flow.EdgeDataDependency},
		},
	}
	compiled, err := flow.Compile(def)
	if err != nil {
		t.Fatalf("compile loop: %v", err)
	}
	nodes, edges, err := compileFlowToWorkflowInputs(compiled)
	if err != nil {
		t.Fatalf("lower loop: %v", err)
	}
	nodeMaps, edgeMaps, err := flowInputsToMaps(nodes, edges)
	if err != nil {
		t.Fatalf("convert loop inputs: %v", err)
	}
	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "create", "workflow_id": "wf_loop_exec", "nodes": nodeMaps, "edges": edgeMaps,
	})
	started := runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action": "start", "workflow_id": "wf_loop_exec",
	})
	if started.Status != workflowStatusCompleted {
		t.Fatalf("expected loop to complete after exit condition, got %+v", started)
	}
	for _, id := range []string{"start", "body_1", "body_2", "after"} {
		node := workflowNodeViewByID(started.Nodes, id)
		if node == nil || node.Status != workflowStatusCompleted {
			t.Fatalf("expected %s to complete, got %+v", id, node)
		}
	}
	if workflowNodeViewByID(started.Nodes, "body_3") != nil {
		t.Fatalf("loop should stop when exit_when matches: %+v", started.Nodes)
	}
}

func TestWorkflowLoopRejectsMultiNodeBody(t *testing.T) {
	def := &flow.Definition{
		FlowID: "fl_loop_multi", Version: "1", Status: "draft",
		Nodes: []flow.Node{
			{ID: "start", Kind: flow.KindStep, Prompt: "start"},
			{ID: "one", Kind: flow.KindStep, Prompt: "one"},
			{ID: "two", Kind: flow.KindStep, Prompt: "two"},
			{ID: "loop", Kind: flow.KindLoop, Loop: &flow.LoopSpec{
				Body: []string{"one", "two"}, ExitWhen: flow.Condition{Status: "completed"}, MaxIterations: 2,
			}},
		},
		Edges: []flow.Edge{{ID: "enter", From: "start", To: "loop", EdgeType: flow.EdgeDataDependency}},
	}
	if _, err := flow.Compile(def); err == nil || !strings.Contains(err.Error(), "exactly one body node") {
		t.Fatalf("expected explicit multi-node loop error, got %v", err)
	}
}
