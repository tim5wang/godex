package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/flow"
)

// flowVarRunDef declares flow inputs, a node output, and a downstream prompt
// referencing both {{inputs.*}} and {{nodes.*.outputs.*}} (P2.3).
func flowVarRunDef() *flow.Definition {
	return &flow.Definition{
		FlowID:  "fl_var_run",
		Version: "1",
		Status:  "draft",
		Inputs:  []flow.VarDef{{Name: "order_id", Type: "string"}},
		Nodes: []flow.Node{
			{ID: "lookup", Kind: flow.KindStep, Prompt: "look up {{inputs.order_id}}",
				Outputs: []flow.VarDef{{Name: "found", Type: "boolean"}}},
			{ID: "done", Kind: flow.KindStep, Prompt: "order {{inputs.order_id}} found={{nodes.lookup.outputs.found}}"},
		},
		Edges: []flow.Edge{
			{ID: "e1", From: "lookup", To: "done", EdgeType: flow.EdgeDataDependency},
		},
	}
}

// TestWorkflowRunInputsPersisted verifies run inputs passed to CreateFlowRun
// are persisted on the workflow summary (source for {{inputs.*}}, P2.3).
func TestWorkflowRunInputsPersisted(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("found")

	def := flowVarRunDef()
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_var_run", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_var_run", "", map[string]any{"order_id": "o-77"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	state, err := a.workflowState(run.WorkflowID)
	if err != nil {
		t.Fatalf("workflow state: %v", err)
	}
	if state.Summary.RunInputs == nil || state.Summary.RunInputs["order_id"] != "o-77" {
		t.Fatalf("expected run inputs persisted, got %+v", state.Summary.RunInputs)
	}
}

// TestRenderWorkflowPromptVarsDirect exercises the renderer against a
// synthetic state with run inputs and a completed upstream node output.
func TestRenderWorkflowPromptVarsDirect(t *testing.T) {
	a := newTestAgent(t, 4096)
	state := workflowState{
		Summary: workflowSummary{RunInputs: map[string]any{"order_id": "o-1"}},
		Nodes: []workflowNode{
			{ID: "lookup", Outputs: map[string]any{"found": true}},
		},
	}
	prompt := "order {{inputs.order_id}} found={{nodes.lookup.outputs.found}} missing={{nodes.nope.outputs.x}}"
	got := a.renderWorkflowPromptVars(state, prompt)
	if !strings.Contains(got, "o-1") || !strings.Contains(got, "true") {
		t.Fatalf("expected vars rendered, got %q", got)
	}
	// Unresolved refs stay as-is.
	if !strings.Contains(got, "{{nodes.nope.outputs.x}}") {
		t.Fatalf("expected unresolved ref preserved, got %q", got)
	}
}
