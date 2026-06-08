package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
