package agent

import (
	"strings"
	"testing"
)

// TestWorkflowEdgesFromInputsAppendFunctionNoPrompt verifies the F1a lowering
// path accepts condition-edge append templates whose node kind is function
// (or branch) WITHOUT a prompt — function nodes execute via their Function
// spec (js/wasm handler), mirroring the static-node rule in
// workflowNodesFromInputs. Regression for fl_ticket_auto's
// "route_case_0 append node missing prompt" failure (branch case → function
// node autoreply with empty prompt).
func TestWorkflowEdgesFromInputsAppendFunctionNoPrompt(t *testing.T) {
	edges, err := workflowEdgesFromInputs([]workflowEdgeInput{
		{
			ID:   "route_case_0",
			From: "route",
			When: workflowEdgeCondition{Choice: "auto"},
			Append: workflowNodeInput{
				ID:   "autoreply",
				Kind: workflowNodeKindFunction,
				Function: &workflowFunctionSpec{
					Runtime: "js",
					Source:  "export function handle(ctx) { return { resolution: 'auto-replied' }; }",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected function append node without prompt to be accepted, got %v", err)
	}
	if len(edges) != 1 || edges[0].Append.ID != "autoreply" {
		t.Fatalf("unexpected edges: %+v", edges)
	}
}

// TestWorkflowEdgesFromInputsAppendStepStillRequiresPrompt verifies that a
// step/llm append node without a prompt is still rejected — only branch/function
// kinds are exempt.
func TestWorkflowEdgesFromInputsAppendStepStillRequiresPrompt(t *testing.T) {
	_, err := workflowEdgesFromInputs([]workflowEdgeInput{
		{
			ID:   "e_step",
			From: "route",
			When: workflowEdgeCondition{Status: "completed"},
			Append: workflowNodeInput{
				ID:   "finalize",
				Kind: "step",
			},
		},
	})
	if err == nil {
		t.Fatal("expected step append node without prompt to be rejected")
	}
	if !strings.Contains(err.Error(), "missing prompt") {
		t.Fatalf("expected missing-prompt error, got %v", err)
	}
}
