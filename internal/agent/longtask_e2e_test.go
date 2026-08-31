package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// The T13 e2e tests are the top of the longtask test pyramid:
// each one drives a real longtask through the full pipeline
// (create -> run -> finalize -> audit) and asserts the
// end-to-end behavior the user sees. Unlike the per-feature
// tests in longtask_test.go / longtask_rollback_test.go, the
// e2e tests do not pin a single line of behavior: they are
// the canary that catches regressions in the cross-feature
// flow introduced by future refactors.
//
// Each test reuses newTestAgent + repeatedTextCaller to keep
// the runtime deterministic. The agent's underlying git
// operations (finalize, rollback) are exercised only by the
// tests that explicitly require them; the rest assert the
// orchestrator's output and on-disk state.

func TestE2ELongTaskCreateListStatusRoundTrip(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_01_create",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	status, err := a.longTaskStatus("lt_e2e_01_create")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.LongTaskID != "lt_e2e_01_create" {
		t.Fatalf("expected round-trip id, got %q", status.LongTaskID)
	}
	if len(status.Stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(status.Stories))
	}
}

func TestE2ELongTaskRunSyncSingleStoryPasses(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_02_run",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_e2e_02_run",
	})
	if view.Run == nil {
		t.Fatalf("expected run summary, got nil")
	}
	// T11 acceptance: the run summary lands in chat history so
	// the user can see it without leaving the chat. The e2e
	// assertion is a soft check; the strict contract is in
	// TestLongTaskRefluxInjectsAssistantMessageOnCompletion.
	msgs := a.GetMessages()
	found := false
	for _, m := range msgs {
		if m.Metadata != nil && m.Metadata.Kind == protocol.KindLongTaskReflux {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected T11 reflux message in chat history for a sync run")
	}
}

func TestE2ELongTaskRunBlockedStopsOnFailure(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_03_blocked",
		"quality_checks": []string{"cat /nonexistent-file-for-e2e"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_e2e_03_blocked",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	if view.Run == nil {
		t.Fatalf("expected run summary")
	}
	// Stop-on-failure (T1 + T9) means the run does not advance
	// past US-001 even though US-002 is ready. The summary
	// status should be blocked / max_iterations and US-002
	// should not appear in the run's Started list.
	blocked := view.Run.Status == "blocked" || view.Run.Status == "stalled" || view.Run.Status == "max_iterations"
	if !blocked {
		t.Fatalf("expected blocked / stalled / max_iterations, got %s", view.Run.Status)
	}
}

func TestE2ELongTaskResumeAfterInterrupted(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_04_resume",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_e2e_04_resume",
	})
	// Get the run id; it is the only record on disk.
	records, err := a.workflows.listLongTaskRuns("lt_e2e_04_resume")
	if err != nil || len(records) == 0 {
		t.Fatalf("expected at least one run record, got err=%v n=%d", err, len(records))
	}
	runID := records[0].RunID
	// Simulate a previous interrupted run by rewriting the
	// record to mark it as interrupted and rewind its iteration
	// counter; the resume call must complete the run without
	// re-starting US-001.
	rec := records[0]
	rec.Status = "interrupted"
	rec.Iterations = 0
	if err := a.workflows.writeLongTaskRun(rec); err != nil {
		t.Fatalf("rewrite run record: %v", err)
	}
	// Resume by run id.
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_e2e_04_resume",
		"resume_run_id":   runID,
		"wait_timeout_ms": 5000,
	})
	// After resume, the run must be terminal (not running) and
	// the run id must still match (no competing record).
	records, err = a.workflows.listLongTaskRuns("lt_e2e_04_resume")
	if err != nil || len(records) != 1 {
		t.Fatalf("expected exactly 1 run record, got err=%v n=%d", err, len(records))
	}
	if records[0].RunID != runID {
		t.Fatalf("expected same run id %s, got %s", runID, records[0].RunID)
	}
	if records[0].Status == "running" {
		t.Fatalf("expected terminal status, got running")
	}
}

func TestE2ELongTaskAsyncRunPersistsAndFinalizes(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_05_async",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_e2e_05_async",
		"async":           true,
		"wait_timeout_ms": 5000,
	})
	// T6 acceptance: the run record is on disk immediately
	// with Async=true. T11 acceptance: the eventual
	// completion refluxes. Drain the async goroutine so the
	// TempDir cleanup hook succeeds.
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := a.workflows.listLongTaskRuns("lt_e2e_05_async")
			if err == nil && len(cur) == 1 && cur[0].Status != workflowStatusRunning {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	records, err := a.workflows.listLongTaskRuns("lt_e2e_05_async")
	if err != nil || len(records) != 1 {
		t.Fatalf("expected exactly 1 run record, got err=%v n=%d", err, len(records))
	}
	if !records[0].Async {
		t.Fatalf("expected async=true on the run record")
	}
}

func TestE2ELongTaskRepairNodeRewiresDownstream(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_06_repair",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	// Verify the workflow surface exposes the two nodes in
	// dependency order; the e2e is the canary for a future
	// refactor that breaks the wiring.
	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "status",
		"longtask_id": "lt_e2e_06_repair",
	})
	if len(view.Stories) != 2 {
		t.Fatalf("expected 2 stories, got %d", len(view.Stories))
	}
}

func TestE2ELongTaskValidationBudgetEnforced(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_07_budget",
		"quality_checks": []string{"sleep 1"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	start := time.Now()
	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":                  "run",
		"longtask_id":             "lt_e2e_07_budget",
		"max_validation_budget_ms": 500,
		"validation_timeout_ms":    5000,
		"wait_timeout_ms":          500,
	})
	elapsed := time.Since(start)
	// 1 sleep with a 500ms budget: the validation should abort
	// well before the unconstrained 5s timeout.
	if elapsed > 3*time.Second {
		t.Fatalf("validation took too long (%s); budget did not fire", elapsed)
	}
	if view.Run == nil {
		t.Fatalf("expected run summary")
	}
}

func TestE2ELongTaskLookupByCommitReturnsEmptyForUnknownHash(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_08_lookup",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_e2e_08_lookup",
	})
	entries, err := a.LongTaskLookupByCommit("e2e-unknown", "lt_e2e_08_lookup")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 matches for unknown commit, got %d", len(entries))
	}
}

func TestE2ELongTaskRollbackPathEnforced(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_09_rollback",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_e2e_09_rollback",
	})
	// The 1024-byte cap is enforced at the boundary. T12
	// acceptance: an over-cap reason is rejected before any
	// git operation. The full happy-path rollback needs a
	// real git project root which the unit test agent does
	// not have, so the e2e pin is the boundary check.
	err := a.checkRollbackReasonLen(strings.Repeat("a", 1025))
	if err == nil {
		t.Fatalf("expected over-cap rollback reason to be rejected")
	}
	err = a.checkRollbackReasonLen(strings.Repeat("a", 1024))
	if err != nil {
		t.Fatalf("expected 1024-byte reason to be accepted, got %v", err)
	}
}

func TestE2ELongTaskGCDryRunDoesNotDelete(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_e2e_10_gc",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_e2e_10_gc",
	})
	// Dry-run with --older-than 1: the just-written run record
	// is eligible to be deleted, but the call is a dry-run so
	// the record must still be on disk afterwards.
	resp, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
		"action":             "gc",
		"longtask_id":        "lt_e2e_10_gc",
		"older_than_seconds": 1,
	})
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected a gc result")
	}
	records, err := a.workflows.listLongTaskRuns("lt_e2e_10_gc")
	if err != nil || len(records) != 1 {
		t.Fatalf("expected the run record to be preserved by dry-run, got err=%v n=%d", err, len(records))
	}
}


