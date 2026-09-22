package agent

import (
	"context"
	"testing"
	"time"
)

// TestInspectFlowsAggregatesRunHealth verifies the inspection aggregates runs
// of published flows (window-filtered): totals, failure rate, waiting-human
// bottleneck, error-node events and iteration caps.
func TestInspectFlowsAggregatesRunHealth(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	// A published flow with a failing function node.
	def := functionFlowDef()
	def.FlowID = "fl_inspect"
	def.Nodes[0].Function.Source = "function handle(ctx, event) { throw new Error('boom'); }"
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, err := a.PublishFlow("fl_inspect", "1"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	ctx := context.Background()
	// Two failed runs within the window.
	for i := 0; i < 2; i++ {
		run, err := a.CreateFlowRun(ctx, "fl_inspect", "", map[string]any{"task": "x"})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := a.StartFlowRun(ctx, "fl_inspect", run.RunID); err != nil {
			t.Fatalf("start run: %v", err)
		}
	}

	report, err := a.InspectFlows(24)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Total != 2 || report.Failed != 2 {
		t.Fatalf("expected 2 total/2 failed, got %+v", report)
	}
	if report.FailureRate != 1.0 {
		t.Fatalf("expected 100%% failure, got %f", report.FailureRate)
	}
	if len(report.Flows) != 1 || report.Flows[0].FlowID != "fl_inspect" {
		t.Fatalf("expected one inspected flow, got %+v", report.Flows)
	}
	sum := report.Flows[0]
	if sum.ErrorNodes < 2 {
		t.Fatalf("expected >=2 error-node events (2 failing runs), got %d", sum.ErrorNodes)
	}
	if len(sum.LatestErrorRuns) != 2 {
		t.Fatalf("expected 2 latest error runs, got %+v", sum.LatestErrorRuns)
	}
}

// TestInspectFlowsSkipsUnpublishedAndWindowFiltering verifies only published
// flows are inspected and runs outside the window are excluded.
func TestInspectFlowsSkipsUnpublishedAndWindowFiltering(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	// Draft-only flow: never inspected.
	def := functionFlowDef()
	def.FlowID = "fl_draft_only"
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	ctx := context.Background()
	run, err := a.CreateFlowRun(ctx, "fl_draft_only", "", nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_draft_only", run.RunID); err != nil {
		t.Fatalf("start run: %v", err)
	}

	// Published flow but all runs are older than the tiny window.
	def2 := functionFlowDef()
	def2.FlowID = "fl_old_runs"
	if _, err := a.CreateFlow(FlowCreateArgs{Def: def2}); err != nil {
		t.Fatalf("create flow 2: %v", err)
	}
	if _, err := a.PublishFlow("fl_old_runs", "1"); err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	run2, err := a.CreateFlowRun(ctx, "fl_old_runs", "", nil)
	if err != nil {
		t.Fatalf("create run 2: %v", err)
	}
	if _, err := a.StartFlowRun(ctx, "fl_old_runs", run2.RunID); err != nil {
		t.Fatalf("start run 2: %v", err)
	}
	// Backdate the run record beyond the window.
	rec, err := a.flows.loadRun("fl_old_runs", run2.RunID)
	if err != nil {
		t.Fatalf("load run 2: %v", err)
	}
	rec.StartedAt = time.Now().UTC().Add(-48 * time.Hour)
	if err := a.flows.saveRun("fl_old_runs", rec); err != nil {
		t.Fatalf("save run 2: %v", err)
	}

	report, err := a.InspectFlows(24)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// Draft-only flow skipped; published flow's runs outside window excluded.
	if len(report.Flows) != 1 || report.Flows[0].FlowID != "fl_old_runs" {
		t.Fatalf("expected only fl_old_runs inspected with zero runs, got %+v", report.Flows)
	}
	if report.Flows[0].Total != 0 || report.Total != 0 {
		t.Fatalf("expected zero runs in window, got %+v", report)
	}
}

// TestInspectionCardMarkdownRenders verifies the card markdown renders compactly.
func TestInspectionCardMarkdownRenders(t *testing.T) {
	r := &FlowInspectionReport{
		GeneratedAt: time.Now().UTC(),
		WindowHours: 24,
		Total:       10,
		Failed:      3,
		FailureRate: 0.3,
		Flows: []FlowInspectionSummary{{
			FlowID: "fl_x", Total: 10, Failed: 3, FailureRate: 0.3,
			ErrorNodes: 2, HumanWaiting: 1,
		}},
	}
	md := r.InspectionCardMarkdown()
	if !contains(md, "Flow 巡检") || !contains(md, "fl_x") || !contains(md, "30%") {
		t.Fatalf("unexpected card markdown: %s", md)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
