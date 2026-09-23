package agent

import (
	"context"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// TestStepFlowRunAdvancesOneNodePerCall verifies the single-step debug API:
// each StepFlowRun advances exactly one ready node (function nodes complete
// synchronously with outputs visible), and the view turns terminal at the end.
func TestStepFlowRunAdvancesOneNodePerCall(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	def := &flow.Definition{
		FlowID:  "fl_step",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{ID: "fn1", Kind: "function", Title: "one",
				Function: &flow.FunctionSpec{Runtime: "js", Handler: "handle",
					Source: "function handle(ctx, event) { return { v: 1 }; }"}},
			{ID: "fn2", Kind: "function", Title: "two",
				Function: &flow.FunctionSpec{Runtime: "js", Handler: "handle",
					Source: "function handle(ctx, event) { return { v: 2 }; }"}},
		},
		Edges: []flow.Edge{{From: "fn1", To: "fn2"}},
	}
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_step", "", map[string]any{"task": "x"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Step 1: fn1 runs and completes with outputs {v:1}; fn2 still pending.
	view, err := a.StepFlowRun(ctx, "fl_step", run.RunID)
	if err != nil {
		t.Fatalf("step 1: %v", err)
	}
	if view.Terminal {
		t.Fatalf("step 1 should not be terminal: %+v", view)
	}
	fn1 := stepNode(view, "fn1")
	fn2 := stepNode(view, "fn2")
	if fn1 == nil || fn1.Status != workflowStatusCompleted {
		t.Fatalf("expected fn1 completed after step 1, got %+v", view.Nodes)
	}
	if fn1.Outputs["v"] != float64(1) {
		t.Fatalf("expected fn1 outputs v=1, got %+v", fn1.Outputs)
	}
	if fn2 == nil || fn2.Status != workflowStatusPending {
		t.Fatalf("expected fn2 still pending after step 1, got %+v", view.Nodes)
	}

	// Step 2: fn2 runs and completes.
	view, err = a.StepFlowRun(ctx, "fl_step", run.RunID)
	if err != nil {
		t.Fatalf("step 2: %v", err)
	}
	fn2 = stepNode(view, "fn2")
	if fn2 == nil || fn2.Status != workflowStatusCompleted {
		t.Fatalf("expected fn2 completed after step 2, got %+v", view.Nodes)
	}
	if fn2.Outputs["v"] != float64(2) {
		t.Fatalf("expected fn2 outputs v=2, got %+v", fn2.Outputs)
	}

	// Step 3: nothing left to run → terminal.
	view, err = a.StepFlowRun(ctx, "fl_step", run.RunID)
	if err != nil {
		t.Fatalf("step 3: %v", err)
	}
	if !view.Terminal || view.Status != workflowStatusCompleted {
		t.Fatalf("expected terminal completed after step 3, got %+v", view)
	}
}

func stepNode(view *StepFlowView, id string) *NodeStepView {
	for i := range view.Nodes {
		if view.Nodes[i].ID == id {
			return &view.Nodes[i]
		}
	}
	return nil
}
