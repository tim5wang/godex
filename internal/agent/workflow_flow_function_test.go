package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// startCreatedWorkflow starts a workflow created via the tool and returns the
// resulting view (mirrors the decision test pattern: create, then start).
func startCreatedWorkflow(t *testing.T, a *Agent, workflowID string) workflowView {
	t.Helper()
	return runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "start",
		"workflow_id": workflowID,
	})
}

// TestWorkflowFunctionNodeExecutesSynchronously verifies a js function node
// runs synchronously in the scheduler (no subagent job), writes its handler
// result into node outputs, and reaches completed.
func TestWorkflowFunctionNodeExecutesSynchronously(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_function_ok",
		"nodes": []map[string]interface{}{
			{
				"id":    "fn",
				"kind":  "function",
				"title": "double",
				"function": map[string]interface{}{
					"runtime": "js",
					"handler": "handle",
					"source":  "function handle(ctx, event) { return { doubled: 2, seen: Object.keys(ctx.outputs || {}).length }; }",
				},
			},
		},
		"edges": []map[string]interface{}{},
	})
	view := startCreatedWorkflow(t, a, "wf_function_ok")
	if st := nodeStatus(view.Nodes, "fn"); st != workflowStatusCompleted {
		t.Fatalf("expected function node completed, got %q", st)
	}
	preview := nodeResultPreview(view.Nodes, "fn")
	if !strings.Contains(preview, `"doubled":2`) {
		t.Fatalf("expected handler output in preview, got %q", preview)
	}
}

// TestWorkflowFunctionNodeErrorMarksNodeError verifies a failing js handler
// marks the function node error and surfaces the error text.
func TestWorkflowFunctionNodeErrorMarksNodeError(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runWorkflowTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"workflow_id": "wf_function_err",
		"nodes": []map[string]interface{}{
			{
				"id":    "fn",
				"kind":  "function",
				"title": "boom",
				"function": map[string]interface{}{
					"runtime": "js",
					"handler": "handle",
					"source":  "function handle(ctx, event) { throw new Error('kaboom'); }",
				},
			},
		},
		"edges": []map[string]interface{}{},
	})
	view := startCreatedWorkflow(t, a, "wf_function_err")
	if st := nodeStatus(view.Nodes, "fn"); st != workflowStatusError {
		t.Fatalf("expected function node error, got %q", st)
	}
	for _, n := range view.Nodes {
		if n.ID == "fn" && n.Error != "" {
			if !strings.Contains(n.Error, "kaboom") {
				t.Fatalf("expected handler error surfaced, got %q", n.Error)
			}
			return
		}
	}
	t.Fatalf("expected handler error surfaced on node, got nodes=%+v", view.Nodes)
}

// functionFlowDef builds a single-function-node flow definition.
func functionFlowDef() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_function",
		Version: "1",
		Status:  "draft",
		Nodes: []flow.Node{
			{
				ID:    "fn",
				Kind:  flow.KindFunction,
				Title: "echo-task",
				Function: &flow.FunctionSpec{
					Runtime: flow.FunctionRuntimeJS,
					Handler: "handle",
					Source:  "function handle(ctx, event) { return { task: ctx.inputs.task, doubled: (ctx.inputs.n || 0) * 2 }; }",
				},
				Outputs: []flow.VarDef{{Name: "task", Type: "string"}, {Name: "doubled", Type: "number"}},
			},
		},
		Edges: []flow.Edge{},
	}
}

// TestWorkflowFunctionNodeFlowRunCarriesInputs verifies the full Flow Spec
// path: create flow (compiles the function node) → create run with inputs →
// start run → the js handler sees {{inputs}} in the unified ctx and writes
// typed outputs (P3 + P2.3 inputs).
func TestWorkflowFunctionNodeFlowRunCarriesInputs(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	if _, err := a.CreateFlow(FlowCreateArgs{Def: functionFlowDef()}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_function", "", map[string]any{"task": "transcribe", "n": 5})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_function", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	fn := workflowNodeByID(state.Nodes, "fn")
	if fn == nil {
		t.Fatalf("expected function node, got %+v", state.Nodes)
	}
	if fn.Status != workflowStatusCompleted {
		t.Fatalf("expected function node completed, got %q (error=%s)", fn.Status, fn.Error)
	}
	if fn.Outputs["task"] != "transcribe" {
		t.Fatalf("expected ctx.inputs.task in outputs, got %+v", fn.Outputs)
	}
	if fn.Outputs["doubled"] != float64(10) {
		t.Fatalf("expected doubled=10, got %+v", fn.Outputs["doubled"])
	}
}
