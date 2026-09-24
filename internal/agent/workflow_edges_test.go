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

// TestValidateWorkflowEdgesAllowsConvergeFromAppendTemplate reproduces the
// fl_ticket_auto failure: a branch edge appends template "autoreply" and a
// later converge edge references it as From (when.status=completed). The From
// node is not static, but it IS a valid dynamic source — validateWorkflowEdges
// must accept it (regression for "references unknown from node autoreply").
func TestValidateWorkflowEdgesAllowsConvergeFromAppendTemplate(t *testing.T) {
	nodes := []workflowNode{
		{ID: "route", Kind: workflowNodeKindBranch, Status: workflowStatusCompleted},
	}
	edges := []workflowEdge{
		// branch 边：append autoreply（动态追加模板）
		{ID: "route_case_0", From: "route", When: workflowEdgeCondition{Choice: "auto"},
			Append: workflowNodeInput{ID: "autoreply", Kind: workflowNodeKindFunction,
				Function: &workflowFunctionSpec{Runtime: "js", Source: "export function handle(ctx) { return []; }"}}},
		// 汇聚边：From 引用 append 模板 autoreply（静态节点里没有它）
		{ID: "e_autoreply_finalize", From: "autoreply", When: workflowEdgeCondition{Status: "completed"},
			Append: workflowNodeInput{ID: "finalize", Kind: "step", Prompt: "汇总结果"}},
	}
	if err := validateWorkflowEdges(edges, nodes); err != nil {
		t.Fatalf("expected converge edge from append template to be accepted, got %v", err)
	}
}

// TestValidateWorkflowEdgesRejectsUnknownFromStill verifies a From that is
// neither a static node nor any Append.ID is still rejected (guard against
// over-relaxing the check).
func TestValidateWorkflowEdgesRejectsUnknownFromStill(t *testing.T) {
	nodes := []workflowNode{{ID: "a", Kind: "step"}}
	edges := []workflowEdge{
		{ID: "e1", From: "ghost", When: workflowEdgeCondition{Status: "completed"},
			Append: workflowNodeInput{ID: "b", Kind: "step", Prompt: "do"}},
	}
	if err := validateWorkflowEdges(edges, nodes); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected unknown-from rejection, got %v", err)
	}
}
