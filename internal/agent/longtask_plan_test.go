package agent

import (
	"context"
	"strings"
	"testing"
)

func TestLongTaskPlanDecomposesDescriptionIntoStories(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller(`[
		{"id": "US-001", "title": "Checkout API", "description": "add checkout endpoint", "acceptance_criteria": ["POST /checkout returns 200"], "priority": 1, "agent_type": "coder", "write_scope": ["internal/api"], "depends_on": []},
		{"id": "US-002", "title": "Order persistence", "description": "persist orders", "acceptance_criteria": ["order row written"], "priority": 2, "depends_on": ["US-001"]}
	]`)

	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "plan",
		"longtask_id": "lt_plan_checkout",
		"description": "Add an order checkout flow with persistence and tests.",
	})

	if view.LongTaskID != "lt_plan_checkout" || view.Total != 2 || view.Pending != 2 {
		t.Fatalf("unexpected planned longtask: %+v", view)
	}
	if len(view.Stories) != 2 || view.Stories[0].ID != "US-001" || view.Stories[1].ID != "US-002" {
		t.Fatalf("expected stories from LLM, got %+v", view.Stories)
	}
	if got := view.Workflow.Nodes[1].DependsOn; len(got) != 1 || got[0] != "US-001" {
		t.Fatalf("expected US-002 to depend on US-001, got %+v", got)
	}
}

func TestLongTaskPlanToleratesMarkdownFence(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("```json\n[{\"id\": \"US-001\", \"title\": \"A\", \"description\": \"do a\"}]\n```")

	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "plan",
		"longtask_id": "lt_plan_fence",
		"description": "Do thing A.",
	})
	if view.Total != 1 || view.Stories[0].ID != "US-001" {
		t.Fatalf("expected 1 story from fenced JSON, got %+v", view)
	}
}

func TestLongTaskPlanMissingDescriptionFails(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller(`[{"id": "US-001", "title": "A"}]`)

	_, err := a.handleTool(context.Background(), "longtask", map[string]interface{}{
		"action": "plan",
	})
	if err == nil || !strings.Contains(err.Error(), "missing task description") {
		t.Fatalf("expected missing description error, got %v", err)
	}
}

func TestLongTaskPlanLLMReturnsNoStoriesFails(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller(`[]`)

	_, err := a.handleTool(context.Background(), "longtask", map[string]interface{}{
		"action":      "plan",
		"description": "Do nothing.",
	})
	if err == nil || !strings.Contains(err.Error(), "no stories") {
		t.Fatalf("expected no-stories error, got %v", err)
	}
}
