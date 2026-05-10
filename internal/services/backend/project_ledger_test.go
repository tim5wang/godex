package backend

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/events"
)

func TestLedgerWorkflowSummariesFromTool(t *testing.T) {
	decisions, blockers := ledgerWorkflowSummariesFromTool(events.ToolCallPayload{
		Name: "workflow",
		Output: `{
			"workflow_id": "wf_review",
			"status": "completed",
			"completed": 2,
			"failed": 1,
			"nodes": [
				{"id": "plan", "status": "completed", "result_preview": "plan done"},
				{"id": "impl", "status": "error", "error": "build failed"}
			]
		}`,
	})
	joinedDecisions := strings.Join(decisions, "\n")
	joinedBlockers := strings.Join(blockers, "\n")
	if !strings.Contains(joinedDecisions, "workflow wf_review completed: 2 nodes") ||
		!strings.Contains(joinedDecisions, "workflow wf_review/plan: plan done") {
		t.Fatalf("expected workflow decisions, got %v", decisions)
	}
	if !strings.Contains(joinedBlockers, "workflow wf_review has 1 failed nodes") ||
		!strings.Contains(joinedBlockers, "workflow wf_review/impl failed: build failed") {
		t.Fatalf("expected workflow blockers, got %v", blockers)
	}
}
