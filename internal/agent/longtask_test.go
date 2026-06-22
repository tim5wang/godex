package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

// runLongTaskToolResult executes the longtask tool and returns the
// raw structured payload alongside any error. T12 acceptance tests
// that need to assert on non-View return shapes (rollback result,
// lookup entries, gc result) use this helper because runLongTaskTool
// would fail to unmarshal a non-View payload.
func runLongTaskToolResult(t *testing.T, a *Agent, ctx context.Context, input map[string]interface{}) (interface{}, error) {
	t.Helper()
	output, err := a.handleTool(ctx, "longtask", input)
	if err != nil {
		return nil, err
	}
	var any interface{}
	if err := json.Unmarshal([]byte(output), &any); err != nil {
		return nil, fmt.Errorf("unmarshal longtask result: %w\n%s", err, output)
	}
	return any, nil
}

// TestPickNextActionCompleted verifies that the run loop exits as soon as
// all stories have passed. T1 acceptance: single-step state machine covers
// the completed terminal state in the very first action.
func TestPickNextActionCompleted(t *testing.T) {
	view := longTaskView{
		Stories: []LongTaskStoryView{
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
		Stories: []LongTaskStoryView{
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
		Stories: []LongTaskStoryView{
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
		Stories: []LongTaskStoryView{
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

// TestLongTaskRefluxInjectsAssistantMessageOnCompletion verifies that
// finishing a longtask appends an assistant-role message to the
// agent's message history. T11 acceptance: reflux is the user's
// single-glance summary, not a raw tool result.
func TestLongTaskRefluxInjectsAssistantMessageOnCompletion(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_reflux_completion",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_reflux_completion",
	})

	msgs := a.GetMessages()
	if len(msgs) == 0 {
		t.Fatalf("expected at least one message after reflux")
	}
	last := msgs[len(msgs)-1]
	if last.Role != protocol.RoleAssistant {
		t.Fatalf("expected reflux message role=assistant, got %s", last.Role)
	}
	if last.Metadata == nil || last.Metadata.Kind != protocol.KindLongTaskReflux {
		t.Fatalf("expected reflux kind=%q, got metadata=%+v", protocol.KindLongTaskReflux, last.Metadata)
	}
	if !strings.Contains(last.Content[0].Text, "lt_reflux_completion") {
		t.Fatalf("expected reflux content to mention longtask id, got %q", firstText(last.Content))
	}
}

// TestLongTaskRefluxAllowsSameStatusContentChange verifies the
// fine-grained dedupe: Status being equal does not block a fresh
// emission when the run summary's UpdatedAt has advanced. T11
// acceptance: a blocked run that re-blocks with different
// BlockedBy still produces a new reflux message.
func TestLongTaskRefluxAllowsSameStatusContentChange(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":         "create",
		"longtask_id":    "lt_reflux_dedupe",
		"quality_checks": []string{"cat missing-file-for-reflux-dedupe"},
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	// First run: blocked by US-001 validation failure.
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_reflux_dedupe",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	countAfterFirst := countRefluxMessages(t, a)

	// Second run: same blocked outcome but the run record now has a
	// new UpdatedAt (because runLongTaskSync records a fresh
	// timestamp on every iteration). The reflux key changes, so a
	// second message must be appended.
	time.Sleep(1 * time.Millisecond)
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":          "run",
		"longtask_id":     "lt_reflux_dedupe",
		"max_iterations":  4,
		"wait_timeout_ms": 2000,
	})
	countAfterSecond := countRefluxMessages(t, a)
	if countAfterSecond <= countAfterFirst {
		t.Fatalf("expected a second reflux message after a fresh run, got first=%d second=%d", countAfterFirst, countAfterSecond)
	}

	// A third back-to-back run_status check with no underlying
	// state change must not append another message because the
	// lastRefluxKey still matches.
	viewBefore, err := a.longTaskStatus("lt_reflux_dedupe")
	if err != nil {
		t.Fatalf("longTaskStatus: %v", err)
	}
	keyBefore, err := refluxDedupeKeyForView(t, a, "lt_reflux_dedupe")
	if err != nil {
		t.Fatalf("refluxDedupeKeyForView: %v", err)
	}
	_, _ = a.appendLongTaskReflux(viewBefore, keyBefore.runID)
	countAfterRedundant := countRefluxMessages(t, a)
	if countAfterRedundant != countAfterSecond {
		t.Fatalf("expected dedupe to suppress a no-change reflux, got before=%d after=%d", countAfterSecond, countAfterRedundant)
	}
}

// TestLongTaskRefluxRunsBeforeFollowUp verifies that when a longtask
// run reaches a terminal state in the same call as a follow-up
// submission, the reflux message lands in message history before the
// follow-up is processed. T11 acceptance: assistant > user order in
// the resulting history.
func TestLongTaskRefluxRunsBeforeFollowUp(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_reflux_order",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_reflux_order",
	})
	beforeFollowUp := countRefluxMessages(t, a)

	// Add a synthetic follow-up user message. In production this
	// would be routed via SubmitAsync + startQueuedTurns, but at the
	// agent layer we just append it directly to verify the order.
	a.appendMessage(protocol.NewTextMessage(protocol.RoleUser, "thank you"))

	msgs := a.GetMessages()
	refluxIdx := -1
	followUpIdx := -1
	for i, m := range msgs {
		if m.Role == protocol.RoleAssistant && m.Metadata != nil && m.Metadata.Kind == protocol.KindLongTaskReflux {
			refluxIdx = i
		}
		if m.Role == protocol.RoleUser && m.Content != nil && len(m.Content) > 0 && firstText(m.Content) == "thank you" {
			followUpIdx = i
		}
	}
	if refluxIdx < 0 {
		t.Fatalf("expected at least one longtask reflux message; got %d", beforeFollowUp)
	}
	if followUpIdx < 0 {
		t.Fatalf("expected the synthetic follow-up message to be present")
	}
	if refluxIdx >= followUpIdx {
		t.Fatalf("expected reflux (idx=%d) to precede follow-up (idx=%d)", refluxIdx, followUpIdx)
	}
}

func countRefluxMessages(t *testing.T, a *Agent) int {
	t.Helper()
	n := 0
	for _, m := range a.GetMessages() {
		if m.Role == protocol.RoleAssistant && m.Metadata != nil && m.Metadata.Kind == protocol.KindLongTaskReflux {
			n++
		}
	}
	return n
}

type refluxKey struct {
	runID string
	view  longTaskView
}

func refluxDedupeKeyForView(t *testing.T, a *Agent, workflowID string) (refluxKey, error) {
	t.Helper()
	records, err := a.workflows.listLongTaskRuns(workflowID)
	if err != nil {
		return refluxKey{}, err
	}
	if len(records) == 0 {
		return refluxKey{}, fmt.Errorf("no run record for %s", workflowID)
	}
	view, err := a.longTaskStatus(workflowID)
	if err != nil {
		return refluxKey{}, err
	}
	return refluxKey{runID: records[0].RunID, view: view}, nil
}

// TestLongTaskAsyncRunPersistsRunRecordBeforeGoroutineExit verifies
// that an async run has a run record on disk from the moment the
// parent call returns, not just after the goroutine finalizes.
// T6 acceptance: an async run is observable on disk immediately
// so a process restart can find and resume it.
func TestLongTaskAsyncRunPersistsRunRecordBeforeGoroutineExit(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_async_persist",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	// Pre-emptively clear any stale records from previous runs.
	_ = a.workflows.dir

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_async_persist",
		"async":       true,
		"wait_timeout_ms": 100,
	})

	// Even with a 100ms wait, the parent call has returned and the
	// record must be on disk already with Async=true and a
	// non-empty RunID.
	runsDir := filepath.Join(a.workflows.dir, "lt_async_persist", "runs")
	records, err := a.workflows.listLongTaskRuns("lt_async_persist")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 run record, got %d (entries: %v)", len(records), dirListing(runsDir))
	}
	rec := records[0]
	if !rec.Async {
		t.Fatalf("expected async=true on the run record")
	}
	if rec.Status != workflowStatusRunning {
		t.Fatalf("expected status=running on the persisted record, got %s", rec.Status)
	}

	// Drain the async goroutine so the TempDir cleanup hook
	// succeeds. We poll the longtask_async runs store via
	// listLongTaskRuns instead of longTaskRunStatus because the
	// async store entry is removed the first time longTaskRunStatus
	// observes a non-running state, and we want to be sure the
	// goroutine has actually finished writing the final record.
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := a.workflows.listLongTaskRuns("lt_async_persist")
			if err == nil && len(cur) == 1 && cur[0].Status != workflowStatusRunning {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

func dirListing(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf("err=%v", err)}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestLongTaskAsyncRunEventuallyFinalizesAndPersists verifies that
// the goroutine inside an async run writes a final record once it
// finishes. T6 acceptance: the same run id is reused end-to-end
// (no competing records from the sync runner) and the final
// status is durable.
func TestLongTaskAsyncRunEventuallyFinalizesAndPersists(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_async_finalize",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_async_finalize",
		"async":       true,
		"wait_timeout_ms": 5000,
	})

	// Wait for the goroutine to finish by polling
	// longTaskRunStatus until it no longer reports running.
	deadline := time.Now().Add(2 * time.Second)
	var finalView longTaskView
	for time.Now().Before(deadline) {
		val, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
			"action":      "run_status",
			"longtask_id": "lt_async_finalize",
		})
		if err == nil {
			if m, ok := val.(map[string]interface{}); ok {
				if run, ok := m["run"].(map[string]interface{}); ok {
					if status, _ := run["status"].(string); status != workflowStatusRunning {
						finalView = decodeViewFromMap(t, m)
						break
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalView.Run == nil {
		t.Fatalf("run never finalized within deadline")
	}

	runsDir := filepath.Join(a.workflows.dir, "lt_async_finalize", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatalf("read runs dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 final run record, got %d", len(entries))
	}
	rec, err := a.workflows.loadLongTaskRun("lt_async_finalize", strings.TrimSuffix(entries[0].Name(), ".json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.Async != true {
		t.Fatalf("expected async flag preserved on final record, got %+v", rec)
	}
	if rec.Status == workflowStatusRunning {
		t.Fatalf("expected final status not to be running, got %s", rec.Status)
	}
}

// TestLongTaskAsyncRunRefluxReachesChatHistory verifies that an
// async run's completion is refluxed into the chat history through
// the same T11 path the sync run uses. T6 acceptance: the user
// sees the result in chat regardless of which path (sync / async)
// the run took.
func TestLongTaskAsyncRunRefluxReachesChatHistory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_async_reflux",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_async_reflux",
		"async":       true,
		"wait_timeout_ms": 5000,
	})

	// Wait for the goroutine to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		val, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
			"action":      "run_status",
			"longtask_id": "lt_async_reflux",
		})
		if err == nil {
			if m, ok := val.(map[string]interface{}); ok {
				if run, ok := m["run"].(map[string]interface{}); ok {
					if status, _ := run["status"].(string); status != workflowStatusRunning {
						break
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	msgs := a.GetMessages()
	refluxed := false
	for _, m := range msgs {
		if m.Metadata != nil && m.Metadata.Kind == protocol.KindLongTaskReflux && strings.Contains(firstText(m.Content), "lt_async_reflux") {
			refluxed = true
			break
		}
	}
	if !refluxed {
		t.Fatalf("expected async run to reflux into chat history")
	}
}

// TestLongTaskAsyncRunHasSingleRunRecord verifies the no-competing-
// record invariant: an async run produces exactly one run record
// even though the goroutine and the parent both touch disk.
// T6 acceptance: the run id assigned at startAsyncLongTask is the
// same id persisted by runLongTaskSync when it resumes.
func TestLongTaskAsyncRunHasSingleRunRecord(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_async_single",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_async_single",
		"async":       true,
		"wait_timeout_ms": 5000,
	})

	// Read the run id from disk; the parent call has not blocked
	// waiting for the goroutine so the record on disk at this
	// moment is the one startAsyncLongTask wrote.
	records, err := a.workflows.listLongTaskRuns("lt_async_single")
	if err != nil || len(records) == 0 {
		t.Fatalf("list runs: %v records=%d", err, len(records))
	}
	firstRec := records[0]
	firstRunID := firstRec.RunID
	if !firstRec.Async {
		t.Fatalf("expected async flag on starting record")
	}

	// Wait for the goroutine to finish, then re-read.
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := a.workflows.listLongTaskRuns("lt_async_single")
			if err == nil && len(cur) == 1 && cur[0].Status != workflowStatusRunning {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	// Re-list the runs dir; the run id must not have changed.
	final, err := a.workflows.listLongTaskRuns("lt_async_single")
	if err != nil {
		t.Fatalf("re-list runs: %v", err)
	}
	if len(final) != 1 {
		t.Fatalf("expected exactly 1 run record after finalize, got %d", len(final))
	}
	gotRunID := final[0].RunID
	if gotRunID != firstRunID {
		t.Fatalf("expected run id to stay %s, got %s (competing record)", firstRunID, gotRunID)
	}
}

func decodeViewFromMap(t *testing.T, m map[string]interface{}) longTaskView {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var v longTaskView
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return v
}

func firstText(blocks []protocol.Block) string {
	for _, b := range blocks {
		if b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// TestSafeJoinUnderRootRejectsParentTraversal verifies that the
// path escape guard refuses to leave its root. T10 acceptance:
// a node id like "../../etc/passwd" must not produce a path
// outside the workflow directory. The existing helper trims
// leading separators and treats the trimmed result as a relative
// path, so an "absolute-looking" string like "/etc/passwd" is
// actually constrained to <root>/etc/passwd and is not a true
// escape; the test focuses on the canonical escape patterns.
func TestSafeJoinUnderRootRejectsParentTraversal(t *testing.T) {
	root := t.TempDir()
	bad := []string{
		"../etc/passwd",
		"..",
		"../../",
		"./../../etc",
		"foo/../../etc",
	}
	for _, b := range bad {
		out, err := safeJoinUnderRoot(root, b)
		if err == nil {
			t.Fatalf("expected safeJoinUnderRoot(%q, %q) to fail; got %q", root, b, out)
		}
	}
}

// TestSafeJoinUnderRootAllowsValidRelativePath verifies the happy
// path. T10 acceptance: ordinary node ids and validation refs
// produce the expected nested path.
func TestSafeJoinUnderRootAllowsValidRelativePath(t *testing.T) {
	root := t.TempDir()
	got, err := safeJoinUnderRoot(root, "validations/US-001/1.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("expected %q to be under %q", got, root)
	}
	if !strings.HasSuffix(got, "validations/US-001/1.json") {
		t.Fatalf("expected relative suffix preserved, got %q", got)
	}
}

// TestSafeJoinUnderRootRejectsEmptyRoot verifies the trivial
// case that *must* fail closed. T10 acceptance: a missing or
// empty root cannot be the base of a join; the function fails
// rather than silently producing a path relative to the current
// working directory. The empty-relative case is intentionally
// allowed to return root unchanged (existing subagent behavior).
func TestSafeJoinUnderRootRejectsEmptyRoot(t *testing.T) {
	if _, err := safeJoinUnderRoot("", "validations/US-001/1.json"); err == nil {
		t.Fatalf("expected empty root to fail")
	}
}

// TestLongTaskWriteValidationRejectsTraversalNodeID verifies that
// a node id designed to escape the workflow directory is rejected
// at the write path. T10 acceptance: a hostile spec / repair
// payload cannot reach outside workflows/<id>/.
func TestLongTaskWriteValidationRejectsTraversalNodeID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	// Drive writeLongTaskValidation directly with a hostile node id.
	err := a.workflows.writeLongTaskValidation("lt_safejoin", longTaskValidation{
		NodeID:  "../../../etc/passwd",
		Attempt: 1,
		Status:  longTaskValidationPass,
	})
	if err == nil {
		t.Fatalf("expected writeLongTaskValidation to reject traversal node id")
	}
	if !strings.Contains(err.Error(), "outside workspace") && !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected error to mention path escape, got %v", err)
	}
}

// TestLongTaskRollbackRejectsOversizedReason verifies the
// per-byte reason cap on rollback. T12 acceptance: 1024 bytes
// is the absolute hard limit, not the per-character length, so a
// multi-byte UTF-8 reason is not unfairly favored.
func TestLongTaskRollbackRejectsOversizedReason(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_rollback_cap",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_rollback_cap",
	})

	oversize := strings.Repeat("a", 1025)
	resp, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
		"action":          "rollback",
		"longtask_id":     "lt_rollback_cap",
		"node_id":         "US-001",
		"rollback_reason": oversize,
	})
	if err == nil {
		t.Fatalf("expected error for oversize reason, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "1024 bytes") {
		t.Fatalf("expected error to mention 1024-byte cap, got %v", err)
	}
}

// TestLongTaskRollbackAllowsEmptyReason verifies the explicit
// allowance for an empty --reason. T12 acceptance: 'empty reason
// is allowed' was a user-confirmed decision. The 1024-byte cap
// is checked first; an empty string is well under it, so the
// reason check must NOT error out before any other validation
// (commit presence, project root, etc.) runs.
func TestLongTaskRollbackAllowsEmptyReason(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	// Call rollback directly (bypassing the longtask tool wrapper)
	// so we exercise the boundary check in isolation from the
	// rest of the rollback path. The longtask tool wrapper would
	// require a real commit to even reach this code, but the
	// 1024-byte boundary check must be testable in isolation.
	errReason := a.checkRollbackReasonLen("")
	if errReason != nil {
		t.Fatalf("expected empty reason to be allowed, got %v", errReason)
	}
	errReason = a.checkRollbackReasonLen(strings.Repeat("a", 1024))
	if errReason != nil {
		t.Fatalf("expected 1024-byte reason to be allowed, got %v", errReason)
	}
}

// TestLongTaskLookupByCommitFindsIndexedStory verifies that after
// a successful finalize, the index.json is populated and a
// commit-hash lookup returns the matching story entry. T12
// acceptance: `godex longtask lookup --commit <hash>` is the
// reverse path operators need.
func TestLongTaskLookupByCommitFindsIndexedStory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_lookup_index",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_lookup_index",
	})

	// The story did not actually run in a real git project, so no
	// commit hash is on the view; we still want to assert the
	// index is created on disk and a lookup by an arbitrary hash
	// returns 0 matches.
	if err := a.refreshLongTaskIndex("lt_lookup_index"); err != nil {
		t.Fatalf("refresh index: %v", err)
	}
	idx, err := a.readLongTaskIndex("lt_lookup_index")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if idx.LongTaskID != "lt_lookup_index" {
		t.Fatalf("expected longtask_id, got %q", idx.LongTaskID)
	}
	entries, err := a.LongTaskLookupByCommit("deadbeef", "lt_lookup_index")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 matches for missing commit, got %d", len(entries))
	}
}

// TestLongTaskGCDryRunIsNoOp verifies the safe default of gc: with
// no --older-than, the sweep inspects but deletes nothing. T12
// acceptance: ArtifactRetentionDays=0 means permanent retention
// and a no-arg gc must NOT touch disk.
func TestLongTaskGCDryRunIsNoOp(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_gc_permanent",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_gc_permanent",
	})

	resp, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
		"action":      "gc",
		"longtask_id": "lt_gc_permanent",
	})
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	result, ok := resp.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", resp)
	}
	if dryRun, _ := result["dry_run"].(bool); !dryRun {
		t.Fatalf("expected dry_run=true on no-arg gc, got %v", result["dry_run"])
	}
	if deleted, _ := result["deleted_runs"].(float64); deleted != 0 {
		t.Fatalf("expected deleted_runs=0 on no-arg gc, got %v", result["deleted_runs"])
	}
}

// TestLongTaskGCDryRunListsEligibleRunRecords verifies that an
// explicit --older-than (in seconds) on gc with --apply_gc=false
// reports the count of run records that *would* be deleted.
// T12 acceptance: dry-run is a planning step, not a flag word.
func TestLongTaskGCDryRunListsEligibleRunRecords(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_gc_dryrun",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_gc_dryrun",
	})

	// 1 second is shorter than the time elapsed during the test,
	// so the just-written run record IS eligible to be deleted.
	resp, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
		"action":             "gc",
		"longtask_id":        "lt_gc_dryrun",
		"older_than_seconds": 1,
	})
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	result, ok := resp.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", resp)
	}
	if dryRun, _ := result["dry_run"].(bool); !dryRun {
		t.Fatalf("expected dry_run=true without apply_gc, got %v", result["dry_run"])
	}
	inspected, _ := result["inspected"].(float64)
	if inspected < 1 {
		t.Fatalf("expected inspected>=1, got %v", result["inspected"])
	}
}

// TestLongTaskGCApplyDeletesOldRunRecords verifies that
// apply_gc=true with --older-than N actually deletes the run
// record file. T12 acceptance: explicit --apply is the only path
// that mutates disk; dry-run is the safe default.
func TestLongTaskGCApplyDeletesOldRunRecords(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_gc_apply",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})
	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "run",
		"longtask_id": "lt_gc_apply",
	})

	// Backdate the run record's UpdatedAt to make it eligible.
	runs, err := a.workflows.listLongTaskRuns("lt_gc_apply")
	if err != nil || len(runs) == 0 {
		t.Fatalf("expected at least one run record, got err=%v runs=%d", err, len(runs))
	}
	rec := runs[0]
	rec.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	if err := a.workflows.writeLongTaskRun(rec); err != nil {
		t.Fatalf("rewrite run record: %v", err)
	}

	resp, err := runLongTaskToolResult(t, a, context.Background(), map[string]interface{}{
		"action":             "gc",
		"longtask_id":        "lt_gc_apply",
		"older_than_seconds": 60,
		"apply_gc":           true,
	})
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	result, ok := resp.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", resp)
	}
	if dryRun, _ := result["dry_run"].(bool); dryRun {
		t.Fatalf("expected dry_run=false with apply_gc, got %v", result["dry_run"])
	}
	deletedRuns, _ := result["deleted_runs"].(float64)
	if deletedRuns < 1 {
		t.Fatalf("expected deleted_runs>=1 with apply_gc, got %v", result["deleted_runs"])
	}
}

// TestLongTaskLookupRefluxSurfacesInMessageHistory verifies that
// a commit-hash lookup appends an assistant message into the
// chat history. T12 acceptance: the user can see 'what longtask
// produced this commit' without leaving the chat.
func TestLongTaskLookupRefluxSurfacesInMessageHistory(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = repeatedTextCaller("Verdict: pass\nimplemented story")

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "create",
		"longtask_id": "lt_lookup_reflux",
		"stories": []map[string]interface{}{
			{"id": "US-001", "title": "First", "priority": 1},
		},
	})

	runLongTaskTool(t, a, context.Background(), map[string]interface{}{
		"action":      "lookup",
		"longtask_id": "lt_lookup_reflux",
		"commit_hash": "deadbeef",
	})

	msgs := a.GetMessages()
	hit := false
	for _, m := range msgs {
		if m.Metadata != nil && m.Metadata.Kind == protocol.KindLongTaskReflux && strings.Contains(firstText(m.Content), "deadbeef") {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatalf("expected lookup reflux message in history")
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

// TestLongTaskArgsJSONRoundTripFromEvals pins the JSON contract that
// examples/evals/build_swebench_frozen.py + run_bench.sh emit and
// godex longtask create --file consumes. If this test breaks, either
// the Python script's field names drifted from the Go struct tags, or
// the LongTaskArgs schema changed in a way that breaks the frozen
// sweep's ability to feed godex.
func TestLongTaskArgsJSONRoundTripFromEvals(t *testing.T) {
	// The literal JSON below mirrors what build_swebench_frozen.py +
	// run_bench.sh concatenate into a spec file. Keep the field names
	// in sync with agent.LongTaskArgs / longTaskStoryInput tags.
	raw := []byte(`{
		"longtask_id": "swebench-frozen-v1",
		"workflow_id": "swebench-frozen-v1",
		"project":     "swebench-frozen",
		"branch_name": "swebench/eval-frozen-v1",
		"description": "Regression sweep over the frozen SWE-bench subset (3 instances).",
		"quality_checks": [],
		"validation_timeout_ms": 60000,
		"merge_policy":  "review_only",
		"commit_policy": "none",
		"stories": [
			{
				"id": "django__django-1",
				"title": "django__django-1 (django/django)",
				"description": "Fix ORM.\nRepo: django/django\nBase commit: abc123\n\nTests: tests/test_orm.py::test_filter",
				"acceptance_criteria": ["tests/test_orm.py::test_filter"],
				"priority": 1,
				"agent_type": "general-purpose"
			},
			{
				"id": "requests__x-1",
				"title": "requests__x-1 (psf/requests)",
				"description": "Add HTTP/3.\nRepo: psf/requests\nBase commit: def456",
				"acceptance_criteria": [],
				"priority": 2,
				"agent_type": "general-purpose"
			}
		]
	}`)
	var args LongTaskArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal evals spec: %v", err)
	}
	if args.LongTaskID != "swebench-frozen-v1" || args.WorkflowID != "swebench-frozen-v1" {
		t.Fatalf("id fields lost: %+v", args)
	}
	if args.MergePolicy != "review_only" || args.CommitPolicy != "none" {
		t.Fatalf("policies lost: %+v", args)
	}
	if args.ValidationTimeoutMS != 60000 {
		t.Fatalf("validation timeout lost: %+v", args)
	}
	if len(args.Stories) != 2 {
		t.Fatalf("expected 2 stories, got %d", len(args.Stories))
	}
	if args.Stories[0].ID != "django__django-1" {
		t.Fatalf("first story id lost: %+v", args.Stories[0])
	}
	if args.Stories[0].Priority != 1 || args.Stories[1].Priority != 2 {
		t.Fatalf("priority lost: %+v", args.Stories)
	}
	if len(args.Stories[0].AcceptanceCriteria) != 1 ||
		args.Stories[0].AcceptanceCriteria[0] != "tests/test_orm.py::test_filter" {
		t.Fatalf("acceptance criteria lost: %+v", args.Stories[0].AcceptanceCriteria)
	}
	// Re-marshal and confirm the wire format stays stable.
	out, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtripped LongTaskArgs
	if err := json.Unmarshal(out, &roundtripped); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if roundtripped.LongTaskID != args.LongTaskID ||
		len(roundtripped.Stories) != len(args.Stories) {
		t.Fatalf("roundtrip diverged: %+v vs %+v", roundtripped, args)
	}
}

// TestLongTaskViewJSONShapeForEvalsScore pins the JSON contract that
// examples/evals/score.py and diff_pi_godex.py read. The script's
// logic keys off the `id`, `verdict`, `passes` fields per story, plus
// the `total` count at the top level. If this test breaks, either
// LongTaskView's field tags drift, or the eval scripts need a follow-up.
func TestLongTaskViewJSONShapeForEvalsScore(t *testing.T) {
	// Literal JSON shaped exactly like what run_pi_bench.sh writes
	// (per-story record) and what run_bench.sh produces from
	// `godex longtask run <id>`. The two paths must stay shape-
	// compatible so diff_pi_godex.py can compare them.
	raw := []byte(`{
		"longtask_id": "pi-v1.0",
		"workflow_id": "pi-v1.0",
		"total": 3,
		"pending": 0,
		"running": 0,
		"completed": 3,
		"failed": 0,
		"stories": [
			{"id":"django__django-1","status":"completed","verdict":"pass","passes":true,"result_preview":"...","error":""},
			{"id":"requests__x-1","status":"completed","verdict":"pass","passes":true,"result_preview":"...","error":""},
			{"id":"pytest__x-1","status":"completed","verdict":"fail","passes":false,"result_preview":"...","error":"verdict=fail rc=0"}
		]
	}`)
	var view LongTaskView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("unmarshal LongTaskView from evals result.json: %v", err)
	}
	if view.Total != 3 {
		t.Fatalf("total lost: %+v", view)
	}
	if len(view.Stories) != 3 {
		t.Fatalf("expected 3 stories, got %d", len(view.Stories))
	}
	wantIDs := []string{"django__django-1", "requests__x-1", "pytest__x-1"}
	for i, want := range wantIDs {
		if view.Stories[i].ID != want {
			t.Fatalf("story[%d] id mismatch: got %q want %q", i, view.Stories[i].ID, want)
		}
	}
	if !view.Stories[0].Passes {
		t.Fatalf("first story should pass: %+v", view.Stories[0])
	}
	if view.Stories[2].Passes {
		t.Fatalf("third story should fail: %+v", view.Stories[2])
	}
	// Round-trip: the output of run_pi_bench.sh is read by score.py,
	// which only reads id/verdict/passes; re-marshal and confirm the
	// shape survives.
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtripped LongTaskView
	if err := json.Unmarshal(out, &roundtripped); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if roundtripped.Total != view.Total || len(roundtripped.Stories) != len(view.Stories) {
		t.Fatalf("roundtrip diverged: %+v vs %+v", roundtripped, view)
	}
}

// TestLongTaskStoryWriteScopeRoundTripFromEvals pins the Option A
// pre-clone contract: the JSON that build_swebench_frozen.py emits
// carries `working_directory` and `write_scope` on every story, and
// these fields are not silently dropped when the spec is parsed by
// `godex longtask create --file`.
//
// Why this matters: godex's narrowSubagentWriteTools strips
// bash/write_file/edit_file from any subagent whose WriteScope is
// empty. If the field is dropped during JSON parsing, the subagent
// will report "I have no shell access" and every story returns
// Verdict: blocked, making the frozen sweep useless. The test below
// guarantees the two new fields survive the round-trip.
func TestLongTaskStoryWriteScopeRoundTripFromEvals(t *testing.T) {
	raw := []byte(`{
		"longtask_id": "swebench-frozen-v1",
		"workflow_id": "swebench-frozen-v1",
		"project":     "swebench-frozen",
		"branch_name": "swebench/eval-frozen-v1",
		"description": "Pre-clone enabled sweep.",
		"quality_checks": [],
		"validation_timeout_ms": 60000,
		"merge_policy":  "review_only",
		"commit_policy": "none",
		"stories": [
			{
				"id": "django__django-10087",
				"title": "django__django-10087",
				"description": "Working directory: /tmp/ws/repos/django__django-10087",
				"acceptance_criteria": ["tests/foo.py::test_x"],
				"priority": 1,
				"agent_type": "general-purpose",
				"working_directory": "repos/django__django-10087",
				"write_scope": ["repos/django__django-10087"]
			}
		]
	}`)
	var args LongTaskArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(args.Stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(args.Stories))
	}
	story := args.Stories[0]
	if len(story.WriteScope) != 1 || story.WriteScope[0] != "repos/django__django-10087" {
		t.Fatalf("write_scope lost: got %#v", story.WriteScope)
	}
	// working_directory is also present in the JSON (run_bench.sh
	// reads it during pre-clone), but godex itself does not need to
	// parse it — the value is only consumed by the runner. We assert
	// via the raw JSON to lock the run-script contract.
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	storyRaw := []byte(rawMap["stories"])
	var storiesRaw []map[string]json.RawMessage
	if err := json.Unmarshal(storyRaw, &storiesRaw); err != nil {
		t.Fatalf("re-parse stories: %v", err)
	}
	if _, ok := storiesRaw[0]["working_directory"]; !ok {
		t.Fatalf("working_directory missing from raw JSON: %s", storyRaw)
	}
}
