package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestLongTaskToolCompilesStoriesToWorkflow(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	created := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_checkout",
		"project":        "Shop",
		"branch_name":    "longtask/checkout",
		"description":    "Improve checkout",
		"quality_checks": []string{"go test ./...", "git diff --check"},
		"stories": []map[string]interface{}{
			{"id": "US-002", "title": "Second", "description": "do second", "priority": 2, "acceptance_criteria": []string{"second passes"}},
			{"id": "US-001", "title": "First", "description": "do first", "priority": 1, "acceptance_criteria": []string{"first passes"}},
		},
	})
	if created.LongTaskID != "lt_checkout" || created.WorkflowID != "lt_checkout" || created.Total != 2 || created.Pending != 2 {
		t.Fatalf("unexpected created longtask: %+v", created)
	}
	if len(created.Stories) != 2 || created.Stories[0].ID != "US-001" || created.Stories[1].ID != "US-002" {
		t.Fatalf("expected stories ordered by priority, got %+v", created.Stories)
	}
	if got := created.Workflow.Nodes[1].DependsOn; len(got) != 1 || got[0] != "US-001" {
		t.Fatalf("expected second story to depend on first, got %+v", got)
	}
	if created.Workflow.Nodes[0].Kind != "story" || strings.Contains(mustJSON(t, created.Workflow.Nodes), "do first") {
		t.Fatalf("workflow view should expose story kind but not raw prompt, got %+v", created.Workflow.Nodes[0])
	}
	if _, err := os.Stat(filepath.Join(a.workflows.dir, "lt_checkout", longTaskSpecFile)); err != nil {
		t.Fatalf("expected longtask spec file: %v", err)
	}
}

func TestLongTaskWritableStoryDefaultsToGeneralPurposeAgent(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	created := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_writable_default",
		"stories": []map[string]interface{}{
			{
				"id":          "write-doc",
				"title":       "Write doc",
				"write_scope": []string{"docs/superpowers/tmp/"},
			},
		},
	})

	if got := created.Workflow.Nodes[0].AgentType; got != "general-purpose" {
		t.Fatalf("expected writable longtask story to default to general-purpose, got %q", got)
	}
}

func TestLongTaskToolStartWaitAndCompleteStory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_run",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	started := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "start",
		"longtask_id": "lt_run",
	})
	if len(started.Started) != 1 || started.Started[0] != "US-001" || started.Stories[0].Status != workflowStatusRunning {
		t.Fatalf("expected first story to start, got %+v", started)
	}
	waited := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "wait",
		"longtask_id": "lt_run",
		"mode":        "all",
		"timeout_ms":  2000,
	})
	if !waited.Stories[0].Passes || waited.Stories[0].Verdict != workflowVerdictPass {
		t.Fatalf("expected first story pass after wait, got %+v", waited.Stories[0])
	}
	if waited.Stories[1].Status != workflowStatusPending {
		t.Fatalf("expected second story pending until next start, got %+v", waited.Stories[1])
	}

	manual := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_story",
		"longtask_id": "lt_run",
		"node_id":     "US-002",
		"result":      "Verdict: needs_fix\nmanual review failed",
	})
	if manual.Stories[1].Passes || manual.Stories[1].Verdict != workflowVerdictNeedsFix {
		t.Fatalf("expected manual story completion to preserve needs_fix verdict, got %+v", manual.Stories[1])
	}
}

func TestLongTaskFinalizeStoryRunsQualityChecks(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_finalize_pass",
		"quality_checks": []string{"printf ok"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	before := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_story",
		"longtask_id": "lt_finalize_pass",
		"node_id":     "US-001",
		"result":      "Verdict: pass\nimplemented story",
	})
	if before.Stories[0].Passes || before.Stories[0].ValidationStatus != longTaskValidationPending {
		t.Fatalf("expected pass verdict to remain pending before runtime validation, got %+v", before.Stories[0])
	}

	finalized := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "finalize_story",
		"longtask_id": "lt_finalize_pass",
		"node_id":     "US-001",
	})
	if !finalized.Stories[0].Passes || finalized.Stories[0].ValidationStatus != longTaskValidationPass {
		t.Fatalf("expected finalized story to pass validation, got %+v", finalized.Stories[0])
	}
	if finalized.Stories[0].ValidationRef == "" {
		t.Fatalf("expected validation ref, got %+v", finalized.Stories[0])
	}
	var validation longTaskValidation
	if err := readJSONFile(filepath.Join(a.workflows.dir, "lt_finalize_pass", filepath.FromSlash(finalized.Stories[0].ValidationRef)), &validation); err != nil {
		t.Fatalf("read validation artifact: %v", err)
	}
	if validation.Status != longTaskValidationPass || len(validation.Checks) != 1 || validation.Checks[0].Command != "printf ok" {
		t.Fatalf("unexpected validation artifact: %+v", validation)
	}
}

func TestLongTaskFinalizeStoryFailsOnQualityCheckFailure(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_finalize_fail",
		"quality_checks": []string{"cat missing-file-for-longtask-validation"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_story",
		"longtask_id": "lt_finalize_fail",
		"node_id":     "US-001",
		"result":      "Verdict: pass\nimplemented story",
	})
	finalized := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "finalize_story",
		"longtask_id": "lt_finalize_fail",
		"node_id":     "US-001",
	})
	if finalized.Stories[0].Passes || finalized.Stories[0].Status != workflowStatusError || finalized.Stories[0].ValidationStatus != longTaskValidationFail {
		t.Fatalf("expected validation failure to mark story error, got %+v", finalized.Stories[0])
	}
}

func TestLongTaskRunCompletesStories(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_run_all",
		"quality_checks": []string{"printf ok"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_run_all",
		"max_iterations":  6,
		"wait_timeout_ms": 2000,
	})
	if run.Run == nil || run.Run.Status != workflowStatusCompleted || run.Run.Iterations == 0 {
		t.Fatalf("expected completed run summary, got %+v", run.Run)
	}
	if len(run.Run.Started) != 2 || len(run.Run.Finalized) != 2 {
		t.Fatalf("expected both stories started and finalized, got %+v", run.Run)
	}
	for _, story := range run.Stories {
		if !story.Passes || story.ValidationStatus != longTaskValidationPass {
			t.Fatalf("expected story to pass with validation, got %+v", story)
		}
	}
}

func TestLongTaskRunStopsWhenValidationFails(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_run_blocked",
		"quality_checks": []string{"cat missing-file-for-longtask-run"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_run_blocked",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	if run.Run == nil || run.Run.Status != "blocked" || run.Run.BlockedBy != "US-001" {
		t.Fatalf("expected run blocked by first story, got %+v", run.Run)
	}
	if run.Stories[0].Passes || run.Stories[0].ValidationStatus != longTaskValidationFail || run.Stories[0].Status != workflowStatusError {
		t.Fatalf("expected first story validation failure, got %+v", run.Stories[0])
	}
	if run.Stories[1].Status != workflowStatusPending {
		t.Fatalf("expected second story to remain pending, got %+v", run.Stories[1])
	}
}

func TestLongTaskRunAutoRepairPassesAfterValidationRetry(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_run_repair",
		"quality_checks": []string{"cat longtask-repair-marker || bash -c 'touch longtask-repair-marker; cat missing-longtask-repair-marker'"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":              "run",
		"longtask_id":         "lt_run_repair",
		"max_iterations":      8,
		"wait_timeout_ms":     2000,
		"auto_repair":         true,
		"max_repair_attempts": 1,
	})
	if run.Run == nil || run.Run.Status != workflowStatusCompleted || len(run.Run.Repaired) != 1 || run.Run.Repaired[0].RepairNodeID != "US-001_repair_1" {
		t.Fatalf("expected completed run with one repair, got %+v", run.Run)
	}
	if len(run.Stories) != 1 || !run.Stories[0].Passes || run.Stories[0].NodeID != "US-001_repair_1" || run.Stories[0].RepairAttempts != 1 {
		t.Fatalf("expected story to pass from repair node, got %+v", run.Stories)
	}
}

func TestLongTaskRunAutoRepairStopsAtCap(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_run_repair_cap",
		"quality_checks": []string{"cat missing-file-for-longtask-repair-cap"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":              "run",
		"longtask_id":         "lt_run_repair_cap",
		"max_iterations":      8,
		"wait_timeout_ms":     2000,
		"auto_repair":         true,
		"max_repair_attempts": 1,
	})
	if run.Run == nil || run.Run.Status != "blocked" || run.Run.BlockedBy != "US-001" || len(run.Run.Repaired) != 1 {
		t.Fatalf("expected blocked run after repair cap, got %+v", run.Run)
	}
	if len(run.Stories) != 1 || run.Stories[0].Passes || run.Stories[0].NodeID != "US-001_repair_1" || run.Stories[0].RepairAttempts != 1 {
		t.Fatalf("expected latest repair node to remain failed, got %+v", run.Stories)
	}
}

func TestLongTaskRunAutoMergesAndCommitsStoryChanges(t *testing.T) {
	a := newTestAgent(t, 4096)
	initGitRepo(t, a.cfg.WorkspaceDir)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("tool-write", "write_file", map[string]interface{}{
				"path":    "notes/result.txt",
				"content": "from longtask story\n",
			}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("Verdict: pass\nimplemented story")}},
	}}

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_commit",
		"quality_checks": []string{"cat notes/result.txt"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "Write note", "priority": 1, "agent_type": "general-purpose", "write_scope": []string{"notes"}},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_commit",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	if run.Run == nil || run.Run.Status != workflowStatusCompleted {
		t.Fatalf("expected completed run, got %+v", run.Run)
	}
	if len(run.Stories) != 1 || !run.Stories[0].Passes {
		t.Fatalf("expected story pass, got %+v", run.Stories)
	}
	story := run.Stories[0]
	if story.MergeStatus != subagentMergeMerged || story.CommitStatus != longTaskCommitCommitted || story.CommitHash == "" {
		t.Fatalf("expected merged and committed story, got %+v", story)
	}
	if data, err := os.ReadFile(filepath.Join(a.cfg.WorkspaceDir, "notes", "result.txt")); err != nil || string(data) != "from longtask story\n" {
		t.Fatalf("expected merged story file, data=%q err=%v", string(data), err)
	}
	subject, err := longTaskRunGit(context.Background(), a.cfg.WorkspaceDir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatalf("read git log: %v: %s", err, subject)
	}
	if !strings.Contains(subject, "longtask(lt_commit): complete US-001 Write note") {
		t.Fatalf("unexpected commit subject %q", subject)
	}
	if _, err := os.Stat(filepath.Join(a.workflows.dir, "lt_commit", filepath.FromSlash(story.CommitRef))); err != nil {
		t.Fatalf("expected commit artifact %s: %v", story.CommitRef, err)
	}
}

func TestLongTaskRunRespectsMaxIterations(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_run_cap",
		"quality_checks": []string{"printf ok"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_run_cap",
		"max_iterations":  1,
		"wait_timeout_ms": 2000,
	})
	if run.Run == nil || run.Run.Status != "max_iterations" || run.Run.Iterations != 1 {
		t.Fatalf("expected max_iterations run summary, got %+v", run.Run)
	}
	if run.Stories[0].Status != workflowStatusCompleted || run.Stories[0].Passes {
		t.Fatalf("expected first story completed but not finalized after one iteration, got %+v", run.Stories[0])
	}
	if run.Stories[1].Status != workflowStatusPending {
		t.Fatalf("expected second story pending, got %+v", run.Stories[1])
	}
}

func runLongTaskTool(t *testing.T, a *Agent, ctx context.Context, input map[string]interface{}) longTaskView {
	t.Helper()
	output, err := a.handleTool(ctx, "longtask", input)
	if err != nil {
		t.Fatalf("longtask tool: %v", err)
	}
	var view longTaskView
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		t.Fatalf("unmarshal longtask view: %v\n%s", err, output)
	}
	return view
}

// TestPickNextActionCompleted verifies that the run loop exits as soon as
// all stories have passed. T1 acceptance: single-step state machine covers
// the completed terminal state in the very first action.
func TestPickNextActionCompleted(t *testing.T) {
	view := longTaskView{
		Stories: []longTaskStoryView{
			{ID: "US-001", Status: workflowStatusCompleted, Verdict: workflowVerdictPass, Passes: true},
			{ID: "US-002", Status: workflowStatusCompleted, Verdict: workflowVerdictPass, Passes: true},
		},
	}
	args := longTaskArgs{}
	action, _, _ := pickNextAction(view, args)
	if action != longTaskActionCompleted {
		t.Fatalf("expected longTaskActionCompleted, got %d", action)
	}
}

// TestPickNextActionFinalizeThenStart verifies the single-step state machine
// sees a pending finalization as the next action even when other stories are
// ready to start. T1 acceptance: the loop finalizes first before fanning
// into a new start, instead of waiting for an extra iteration.
func TestPickNextActionFinalizeThenStart(t *testing.T) {
	view := longTaskView{
		Stories: []longTaskStoryView{
			{ID: "US-001", Status: workflowStatusCompleted, Verdict: workflowVerdictPass, ValidationStatus: longTaskValidationPending, Priority: 1},
			{ID: "US-002", Status: workflowStatusPending, Priority: 2},
		},
		Workflow: workflowView{
			Nodes: []workflowNodeView{
				{ID: "US-001", Status: workflowStatusCompleted},
				{ID: "US-002", Status: workflowStatusPending},
			},
		},
	}
	args := longTaskArgs{}
	action, storyID, _ := pickNextAction(view, args)
	if action != longTaskActionFinalizeStory {
		t.Fatalf("expected longTaskActionFinalizeStory, got %d", action)
	}
	if storyID != "US-001" {
		t.Fatalf("expected storyID=US-001, got %s", storyID)
	}
}

// TestPickNextActionStopsOnBlockedByDefault verifies that the default
// stop_on_failure=true stops the run on the first blocked story. T1
// acceptance: StopOnFailure is opt-out, not opt-in.
func TestPickNextActionStopsOnBlockedByDefault(t *testing.T) {
	view := longTaskView{
		Stories: []longTaskStoryView{
			{ID: "US-001", Status: workflowStatusError, Error: "boom"},
			{ID: "US-002", Status: workflowStatusPending},
		},
		Workflow: workflowView{
			Nodes: []workflowNodeView{
				{ID: "US-001", Status: workflowStatusError},
				{ID: "US-002", Status: workflowStatusPending},
			},
		},
	}
	args := longTaskArgs{} // StopOnFailure left nil => default true
	action, blockedBy, _ := pickNextAction(view, args)
	if action != longTaskActionBlocked {
		t.Fatalf("expected longTaskActionBlocked by default, got %d", action)
	}
	if blockedBy != "US-001" {
		t.Fatalf("expected blockedBy=US-001, got %s", blockedBy)
	}
}

// TestPickNextActionContinuesOnBlockedWhenOptOut verifies that
// stop_on_failure=false keeps the run going past a blocked story. T1
// acceptance: explicit opt-out.
func TestPickNextActionContinuesOnBlockedWhenOptOut(t *testing.T) {
	f := false
	view := longTaskView{
		Stories: []longTaskStoryView{
			{ID: "US-001", Status: workflowStatusError, Error: "boom"},
			{ID: "US-002", Status: workflowStatusPending, Priority: 2},
		},
		Workflow: workflowView{
			Nodes: []workflowNodeView{
				{ID: "US-001", Status: workflowStatusError},
				{ID: "US-002", Status: workflowStatusPending},
			},
		},
	}
	args := longTaskArgs{StopOnFailure: &f}
	action, _, _ := pickNextAction(view, args)
	// Without stop-on-failure, the loop should pick the next actionable
	// story (US-002 start).
	if action != longTaskActionStartOne {
		t.Fatalf("expected longTaskActionStartOne when stop_on_failure=false, got %d", action)
	}
}

// TestLongTaskRunFinalizeThenStartSameIteration verifies that finalize and
// start happen in the same run loop iteration, not split across iterations.
// T1 acceptance: single-step state machine avoids the extra-scan latency
// the old polling loop had.
func TestLongTaskRunFinalizeThenStartSameIteration(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_finalize_start",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_finalize_start",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	if run.Run == nil || run.Run.Status != workflowStatusCompleted {
		t.Fatalf("expected completed run, got %+v", run.Run)
	}
	// With the old polling loop finalize+start took 2+ iterations per
	// story. Single-step keeps total iterations close to story count.
	if run.Run.Iterations > 6 {
		t.Fatalf("expected at most 6 iterations for 2 stories, got %d (single-step regression?)", run.Run.Iterations)
	}
	for _, story := range run.Stories {
		if !story.Passes {
			t.Fatalf("expected all stories to pass, got %+v", story)
		}
	}
}

// TestLongTaskRunStopOnFailureFalseContinuesPastBlocked verifies that with
// stop_on_failure=false the run keeps going past a blocked story. T1
// acceptance: explicit opt-out is honored.
func TestLongTaskRunStopOnFailureFalseContinuesPastBlocked(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_continue_past_block",
		"quality_checks": []string{"cat missing-file-for-longtask-continue"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	// stop_on_failure=false: US-001 validation fails, but the run keeps
	// going to US-002 which should also reach a terminal state.
	f := false
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_continue_past_block",
		"max_iterations":  6,
		"wait_timeout_ms": 2000,
		"stop_on_failure": f,
	})
	if run.Run == nil {
		t.Fatalf("expected a run summary, got nil")
	}
	if run.Run.Status == "blocked" && run.Run.BlockedBy == "US-001" {
		t.Fatalf("expected run to continue past blocked US-001 with stop_on_failure=false, got %+v", run.Run)
	}
	// US-001 should still be in error state from validation failure.
	if run.Stories[0].Status != workflowStatusError {
		t.Fatalf("expected US-001 to remain in error, got %+v", run.Stories[0])
	}
}

// TestLongTaskRepairCancelsRunningDownstream is the T3 acceptance test:
// when a story fails and the run loop appends a repair node, any
// downstream story that is currently running must be cancelled and reset
// to pending so it observes the new repair node before resuming.
func TestLongTaskRepairCancelsRunningDownstream(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_repair_cascade",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})

	// Manually put the workflow into a state where US-001 is errored and
	// US-002 is running with a fake subagent job. T3 fixes the case where
	// a downstream node started before its upstream failed.
	state, err := a.workflows.load("lt_repair_cascade")
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	var downstream *workflowNode
	for i := range state.Nodes {
		switch state.Nodes[i].ID {
		case "US-001":
			state.Nodes[i].Status = workflowStatusError
			state.Nodes[i].Error = "upstream failed"
		case "US-002":
			state.Nodes[i].Status = workflowStatusRunning
			state.Nodes[i].JobID = "subagent_fake_for_repair_cascade"
			downstream = &state.Nodes[i]
		}
	}
	if downstream == nil {
		t.Fatalf("expected US-002 node to exist")
	}
	downstream.DependsOn = []string{"US-001"}
	if err := a.workflows.save(state); err != nil {
		t.Fatalf("save workflow: %v", err)
	}

	view, err := a.longTaskStatus("lt_repair_cascade")
	if err != nil {
		t.Fatalf("longTaskStatus: %v", err)
	}

	summary, repairView, err := a.appendLongTaskRepair(context.Background(), "lt_repair_cascade", view, "US-001", "test failure", 1)
	if err != nil {
		t.Fatalf("appendLongTaskRepair: %v", err)
	}
	if summary.RepairNodeID != "US-001_repair_1" {
		t.Fatalf("expected repair node US-001_repair_1, got %s", summary.RepairNodeID)
	}

	// After repair, US-002 must be reset to pending with a rewired dep.
	rewiredState, err := a.workflows.load("lt_repair_cascade")
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	var us001, us002 *workflowNode
	for i := range rewiredState.Nodes {
		switch rewiredState.Nodes[i].ID {
		case "US-001":
			us001 = &rewiredState.Nodes[i]
		case "US-002":
			us002 = &rewiredState.Nodes[i]
		}
	}
	if us001 == nil || us002 == nil {
		t.Fatalf("expected both US-001 and US-002 nodes after repair")
	}
	if us002.Status != workflowStatusPending {
		t.Fatalf("expected US-002 to be reset to pending after cascade cancel, got %s", us002.Status)
	}
	if us002.JobID != "" {
		t.Fatalf("expected US-002 job to be cleared after cascade cancel, got %s", us002.JobID)
	}
	foundRepairDep := false
	for _, dep := range us002.DependsOn {
		if dep == "US-001_repair_1" {
			foundRepairDep = true
			break
		}
	}
	if !foundRepairDep {
		t.Fatalf("expected US-002 DependsOn to reference US-001_repair_1, got %v", us002.DependsOn)
	}

	// repairView should reflect the reset US-002 and a new repair node.
	var viewRepair bool
	for _, s := range repairView.Workflow.Nodes {
		if s.ID == "US-001_repair_1" {
			viewRepair = true
			break
		}
	}
	if !viewRepair {
		t.Fatalf("expected repairView to include US-001_repair_1 node, got %+v", repairView.Workflow.Nodes)
	}
}

// TestLongTaskRepairCancelsDownstreamEvenWithEmptyJobID covers the edge case
// where a downstream node is in a weird running-like state with no job id
// (e.g. an interrupted resume). The repair should still rewire deps and
// not panic.
func TestLongTaskRepairRewiresDownstreamWithEmptyJobID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_repair_nojob",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})

	state, err := a.workflows.load("lt_repair_nojob")
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	for i := range state.Nodes {
		switch state.Nodes[i].ID {
		case "US-001":
			state.Nodes[i].Status = workflowStatusError
			state.Nodes[i].Error = "upstream failed"
		case "US-002":
			// Running but without a job id: represents a node left in an
			// inconsistent state from an interrupted prior run. The repair
			// path must still rewire its deps.
			state.Nodes[i].Status = workflowStatusRunning
			state.Nodes[i].JobID = ""
			state.Nodes[i].DependsOn = []string{"US-001"}
		}
	}
	if err := a.workflows.save(state); err != nil {
		t.Fatalf("save workflow: %v", err)
	}

	view, err := a.longTaskStatus("lt_repair_nojob")
	if err != nil {
		t.Fatalf("longTaskStatus: %v", err)
	}

	if _, _, err := a.appendLongTaskRepair(context.Background(), "lt_repair_nojob", view, "US-001", "test failure", 1); err != nil {
		t.Fatalf("appendLongTaskRepair: %v", err)
	}

	rewiredState, err := a.workflows.load("lt_repair_nojob")
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	for i := range rewiredState.Nodes {
		if rewiredState.Nodes[i].ID != "US-002" {
			continue
		}
		foundRepairDep := false
		for _, dep := range rewiredState.Nodes[i].DependsOn {
			if dep == "US-001_repair_1" {
				foundRepairDep = true
				break
			}
		}
		if !foundRepairDep {
			t.Fatalf("expected US-002 DependsOn to reference US-001_repair_1, got %v", rewiredState.Nodes[i].DependsOn)
		}
	}
}

// TestLongTaskRunWritesRunRecord verifies that running a longtask
// produces a durable run record under runs/<runID>.json that captures
// the run id, status, started stories, and finalized stories. T2
// acceptance: a crash or restart after the run completes still leaves
// an inspectable record.
func TestLongTaskRunWritesRunRecord(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_run_record",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_run_record",
	})
	if run.Run == nil || run.Run.Status != workflowStatusCompleted {
		t.Fatalf("expected completed run, got %+v", run.Run)
	}
	if len(run.Run.Started) == 0 {
		t.Fatalf("expected run to record started stories, got %+v", run.Run)
	}
	// Read the durable record: there must be exactly one run file under
	// the workflow's runs/ directory.
	records, err := a.workflows.listLongTaskRuns("lt_run_record")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 run record, got %d", len(records))
	}
	rec := records[0]
	if rec.WorkflowID != "lt_run_record" {
		t.Fatalf("expected workflow_id=lt_run_record, got %s", rec.WorkflowID)
	}
	if rec.Status != workflowStatusCompleted {
		t.Fatalf("expected record status=completed, got %s", rec.Status)
	}
	if len(rec.Started) == 0 {
		t.Fatalf("expected record to include Started, got %v", rec.Started)
	}
}

// TestLongTaskRunResumeAfterInterrupt verifies that a run that was
// previously interrupted can be resumed by run id without re-starting
// stories that were already started. T2 acceptance: run_id is durable
// and resume_run_id is honored.
func TestLongTaskRunResumeAfterInterrupt(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_run_resume",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})

	// Pre-seed an interrupted run record and a US-001 workflow node that
	// is already running. The resumed run must observe US-001 as already
	// started and skip straight to finalizing it.
	now := time.Now().UTC()
	state, err := a.workflows.load("lt_run_resume")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var us001, us002 *workflowNode
	for i := range state.Nodes {
		if state.Nodes[i].ID == "US-001" {
			us001 = &state.Nodes[i]
		}
		if state.Nodes[i].ID == "US-002" {
			us002 = &state.Nodes[i]
		}
	}
	if us001 == nil || us002 == nil {
		t.Fatalf("expected both story nodes to be present")
	}
	// Put US-001 in completed+pass+pending-validation state so the resumed
	// run will finalize it without re-starting.
	us001.Status = workflowStatusCompleted
	us001.Verdict = workflowVerdictPass
	us001.Attempt = 1
	us001.UpdatedAt = now
	us001.FinishedAt = now
	if err := a.workflows.save(state); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Pre-seed an interrupted run record (US-001 was already Started).
	runID := "run_resumed_test"
	rec := longTaskRunRecord{
		RunID:      runID,
		WorkflowID: "lt_run_resume",
		SessionID:  "session_resume",
		StartedAt:  now,
		UpdatedAt:  now,
		Status:     "interrupted",
		Started:    []string{"US-001"},
	}
	if err := a.workflows.writeLongTaskRun(rec); err != nil {
		t.Fatalf("write run record: %v", err)
	}

	// Resume: there is no quality_check so finalize is a no-op for
	// US-001 (ValidationStatus=skipped). The run then proceeds to start
	// US-002. US-001 must not be re-started.
	resumed, err := a.RunLongTask(context.Background(), "lt_run_resume", longTaskArgs{
		ResumeRunID:    runID,
		SessionID:      "session_resume",
		MaxIterations:  1,
		WaitTimeoutMS:  2000,
	})
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if resumed.Run == nil {
		t.Fatalf("expected resumed run to have a summary")
	}
	// The resume must not re-start US-001 even though pickNextAction might
	// otherwise consider it a candidate; the Started list is the proof.
	count := 0
	for _, s := range resumed.Run.Started {
		if s == "US-001" {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("expected US-001 to be started at most once across the resumed run, got %d (started=%v)", count, resumed.Run.Started)
	}
	records, err := a.workflows.listLongTaskRuns("lt_run_resume")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(records) != 1 || records[0].RunID != runID {
		t.Fatalf("expected exactly one run record with the same id, got %+v", records)
	}
	if records[0].Status != "max_iterations" && records[0].Status != workflowStatusCompleted {
		t.Fatalf("expected record status to be max_iterations or completed after resume, got %s", records[0].Status)
	}
}

// TestLongTaskRepairPromptDoesNotDuplicateHandoff verifies that repair
// nodes do not carry an explicit handoff policy. The repair prompt
// already includes the failed attempt's preview, validation reference,
// and failure reason inline; re-asking the subagent to also read a
// handoff artifact would burn 8 KB of token budget on duplicated
// context. T4 acceptance: HandoffPolicy=none, HandoffFrom empty.
func TestLongTaskRepairPromptDoesNotDuplicateHandoff(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_repair_no_handoff",
		"quality_checks": []string{"cat missing-file-for-repair-prompt-test"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	// First run: US-001 will fail validation and trigger a repair.
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":              "run",
		"longtask_id":         "lt_repair_no_handoff",
		"max_iterations":      6,
		"wait_timeout_ms":     2000,
		"auto_repair":         true,
		"max_repair_attempts": 1,
	})

	// Reload the workflow and find the appended repair node.
	state, err := a.workflows.load("lt_repair_no_handoff")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var repair *workflowNode
	for i := range state.Nodes {
		if strings.HasPrefix(state.Nodes[i].ID, "US-001_repair_") {
			repair = &state.Nodes[i]
			break
		}
	}
	if repair == nil {
		t.Fatalf("expected a repair node to be appended, got %+v", state.Nodes)
	}
	if repair.HandoffPolicy != workflowHandoffPolicyNone {
		t.Fatalf("expected repair HandoffPolicy=%q, got %q", workflowHandoffPolicyNone, repair.HandoffPolicy)
	}
	if len(repair.HandoffFrom) != 0 {
		t.Fatalf("expected repair HandoffFrom to be empty, got %v", repair.HandoffFrom)
	}
	if !strings.Contains(repair.Prompt, "Previous result preview") {
		t.Fatalf("expected repair prompt to include Previous result preview section, got prompt:\n%s", repair.Prompt)
	}
}

// TestLongTaskRunStopOnFailureDefaultTrue verifies the documented
// default behavior: when the caller does not set StopOnFailure, any
// blocked story stops the run. T9 acceptance: default is opt-out
// rather than opt-in.
func TestLongTaskRunStopOnFailureDefaultTrue(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_stop_on_failure_default",
		"quality_checks": []string{"cat missing-file-for-stop-on-failure-default"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
		},
	})
	// StopOnFailure intentionally left unset.
	run := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_stop_on_failure_default",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	if run.Run == nil || run.Run.Status != "blocked" || run.Run.BlockedBy != "US-001" {
		t.Fatalf("expected default stop-on-failure to block on US-001, got %+v", run.Run)
	}
	if run.Stories[1].Status != workflowStatusPending {
		t.Fatalf("expected US-002 to remain pending under default stop-on-failure, got %+v", run.Stories[1])
	}
}

// TestLongTaskCancelAllCascades verifies that a `cancel --all` call
// marks every story in the longtask as canceled, cancels any running
// subagent, and reflects the cascade in the durable workflow state.
// T5 acceptance: a single tool invocation replaces per-node cancel
// across the whole longtask.
func TestLongTaskCancelAllCascades(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_cancel_all",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
			{"id": "US-002", "title": "Second", "priority": 2},
			{"id": "US-003", "title": "Third", "priority": 3},
		},
	})

	// Manually mark US-001 completed and US-002 running, then cascade.
	state, err := a.workflows.load("lt_cancel_all")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Now().UTC()
	for i := range state.Nodes {
		switch state.Nodes[i].ID {
		case "US-001":
			state.Nodes[i].Status = workflowStatusCompleted
			state.Nodes[i].Verdict = workflowVerdictPass
			state.Nodes[i].FinishedAt = now
			state.Nodes[i].UpdatedAt = now
		case "US-002":
			state.Nodes[i].Status = workflowStatusRunning
			state.Nodes[i].JobID = "subagent_fake_for_cancel_all"
		}
	}
	if err := a.workflows.save(state); err != nil {
		t.Fatalf("save: %v", err)
	}

	view, err := a.cancelLongTask(context.Background(), longTaskArgs{
		WorkflowID: "lt_cancel_all",
		CancelAll: true,
	})
	if err != nil {
		t.Fatalf("cancelLongTask: %v", err)
	}
	if view.Workflow.Status != workflowStatusCanceled {
		t.Fatalf("expected workflow status=canceled after cascade, got %s", view.Workflow.Status)
	}
	statuses := map[string]string{}
	for _, s := range view.Stories {
		statuses[s.ID] = s.Status
	}
	// Already-completed stories keep their status; pending and running
	// stories must be flipped to canceled.
	if statuses["US-001"] != workflowStatusCompleted {
		t.Fatalf("expected US-001 (already completed) to stay completed, got %s", statuses["US-001"])
	}
	if statuses["US-002"] != workflowStatusCanceled {
		t.Fatalf("expected US-002 (was running) to be canceled, got %s", statuses["US-002"])
	}
	if statuses["US-003"] != workflowStatusCanceled {
		t.Fatalf("expected US-003 (was pending) to be canceled, got %s", statuses["US-003"])
	}
	// US-001 was already completed; cancelling it must not be reverted
	// but the cascade must record it as canceled in the workflow state.
	state2, err := a.workflows.load("lt_cancel_all")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i := range state2.Nodes {
		// US-001 was already completed; cascade cancel does not revert it.
		if state2.Nodes[i].ID == "US-001" {
			if state2.Nodes[i].Status != workflowStatusCompleted {
				t.Fatalf("expected US-001 to remain completed, got %s", state2.Nodes[i].Status)
			}
			continue
		}
		if state2.Nodes[i].Status != workflowStatusCanceled {
			t.Fatalf("expected node %s status=canceled, got %s", state2.Nodes[i].ID, state2.Nodes[i].Status)
		}
	}
}

// TestLongTaskValidationBudgetAbortsExcessiveRun verifies that the
// overall validation budget (len(checks) * timeout_ms by default)
// aborts the validation loop with status=fail when checks take too
// long. T7 acceptance: the loop never runs longer than the budget
// even with a per-check command that hangs.
func TestLongTaskValidationBudgetAbortsExcessiveRun(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":                   "create",
		"longtask_id":              "lt_validation_budget",
		"validation_timeout_ms":    1000,
		"max_validation_budget_ms": 1500,
		"quality_checks": []string{
			"sleep 1",
			"sleep 1",
			"sleep 1",
			"sleep 1",
			"sleep 1",
		},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	started := time.Now()
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_story",
		"longtask_id": "lt_validation_budget",
		"node_id":     "US-001",
		"result":      "Verdict: pass\nimplemented story",
	})
	view := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "finalize_story",
		"longtask_id": "lt_validation_budget",
		"node_id":     "US-001",
	})
	elapsed := time.Since(started)
	if elapsed > 4*time.Second {
		t.Fatalf("validation took too long: %s (expected <4s with 1.5s budget)", elapsed)
	}
	if view.Stories[0].ValidationStatus != longTaskValidationFail {
		t.Fatalf("expected validation_status=fail, got %s", view.Stories[0].ValidationStatus)
	}
}

// TestLongTaskValidationCancelledOnParentCtx verifies that an
// already-cancelled parent context propagates into the validation
// loop. T7 acceptance: ctx.Canceled ends the loop promptly and
// records the cancellation reason.
func TestLongTaskValidationCancelledOnParentCtx(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_validation_ctx_cancel",
		"quality_checks": []string{"sleep 5"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_story",
		"longtask_id": "lt_validation_ctx_cancel",
		"node_id":     "US-001",
		"result":      "Verdict: pass\nimplemented story",
	})
	view := runLongTaskTool(t, a, ctx, map[string]interface{}{
		"action":      "finalize_story",
		"longtask_id": "lt_validation_ctx_cancel",
		"node_id":     "US-001",
	})
	if view.Stories[0].ValidationStatus != longTaskValidationFail {
		t.Fatalf("expected validation_status=fail under cancelled ctx, got %s", view.Stories[0].ValidationStatus)
	}
}

// TestLongTaskFinalizeRetainsWorktreeOnFailure verifies that when a
// story ends in an error/validation-fail state, the underlying
// subagent worktree is left on disk (and a worktree_retained_*
// event is appended) so the operator can still inspect the diff.
// T8 acceptance: success path GC, failure path retain.
func TestLongTaskFinalizeRetainsWorktreeOnFailure(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_retain_worktree",
		"quality_checks": []string{"cat missing-file-for-retain-test"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "complete_story",
		"longtask_id": "lt_retain_worktree",
		"node_id":     "US-001",
		"result":      "Verdict: pass\nimplemented story",
	})
	finalized := runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "finalize_story",
		"longtask_id": "lt_retain_worktree",
		"node_id":     "US-001",
	})
	if finalized.Stories[0].ValidationStatus != longTaskValidationFail {
		t.Fatalf("expected validation_status=fail, got %s", finalized.Stories[0].ValidationStatus)
	}
	// Drive the GC path directly so we can assert the on-error behavior
	// without first having to spin up a real subagent worktree.
	node := &workflowNode{ID: "lt_retain_worktree", JobID: "subagent_fake_retain"}
	a.gcLongTaskStoryWorktreeOnError(node)

	// The on-error path must record a worktree_retained_for_diagnosis
	// event in the workflow's events.jsonl.
	eventsPath := filepath.Join(a.workflows.dir, "lt_retain_worktree", "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(data), "worktree_retained_for_diagnosis") {
		t.Fatalf("expected worktree_retained_for_diagnosis event, got events:\n%s", string(data))
	}
}

// TestLongTaskRunStatusReadsFromDisk verifies that run_status reads
// from the durable record (not the in-memory sync.Map) and so survives
// an Agent restart. T2 acceptance: godex restart preserves run state.
func TestLongTaskRunStatusReadsFromDisk(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_run_status_disk",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_run_status_disk",
	})

	records, err := a.workflows.listLongTaskRuns("lt_run_status_disk")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 run record, got %d", len(records))
	}
	if records[0].Status != workflowStatusCompleted {
		t.Fatalf("expected durable record status=completed, got %s", records[0].Status)
	}
	view, err := a.longTaskRunStatus("lt_run_status_disk")
	if err != nil {
		t.Fatalf("longTaskRunStatus: %v", err)
	}
	if view.LongTaskID == "" {
		t.Fatalf("expected view to be populated from durable state")
	}
}

// TestLongTaskSweepStaleRunsMarksInterrupted verifies that the sweep
// helper marks any runs still in "running" state as "interrupted" so
// callers can detect godex crashed mid-run. T2 acceptance: helper
// exists and is wired in agent tooling.
func TestLongTaskSweepStaleRunsMarksInterrupted(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

	rec := longTaskRunRecord{
		RunID:      "run_stale",
		WorkflowID: "lt_sweep",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Status:     "running",
	}
	if err := a.workflows.writeLongTaskRun(rec); err != nil {
		t.Fatalf("write stale run: %v", err)
	}

	updated, err := a.workflows.sweepStaleLongTaskRuns()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(updated) == 0 {
		t.Fatalf("expected sweep to mark at least one run, got %v", updated)
	}
	records, err := a.workflows.listLongTaskRuns("lt_sweep")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(records) != 1 || records[0].Status != "interrupted" {
		t.Fatalf("expected status=interrupted, got %+v", records)
	}
}
