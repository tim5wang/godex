package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// Phase 4.2 — review → fix → re-review 双向循环: ReopenForIteration +
// iterate 工具 action。复用 4.1 的 PendingInputs 注入通道，对已完成 job
// 注入 review 反馈并重新运行一轮。

func TestSubagentReopenForIterationAllowsCompletedJob(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      1,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 100000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if _, err := store.Finish(job.ID, subagentStatusCompleted, "first result", ""); err != nil {
		t.Fatalf("finish job: %v", err)
	}
	reopened, err := store.ReopenForIteration(job.ID, []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "review says fix the naming"),
	})
	if err != nil {
		t.Fatalf("reopen for iteration: %v", err)
	}
	if reopened.Status != subagentStatusRunning {
		t.Fatalf("expected reopened job running, got %s", reopened.Status)
	}
	if reopened.Result != "" || reopened.Error != "" {
		t.Fatalf("expected result/error cleared, got result=%q error=%q", reopened.Result, reopened.Error)
	}
	if len(reopened.PendingInputs) != 1 || !strings.Contains(protocol.MessageText(reopened.PendingInputs[0]), "fix the naming") {
		t.Fatalf("expected feedback queued as pending input, got %+v", reopened.PendingInputs)
	}
}

func TestSubagentReopenForIterationRejectsRunningJob(t *testing.T) {
	store := newSubagentJobStore(filepath.Join(t.TempDir(), "subagents"))
	job, err := store.StartWithOptions(subagentStartOptions{
		AgentType:  "general-purpose",
		Prompt:     "work",
		ToolNames:  []string{"todo_read"},
		MaxTurns:   1,
		WorkerID:   localGoDexWorkerID,
		SandboxID:  "sandbox:local:test",
		BasePrompt: "base",
		ParentID:   "turn-1",
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if _, err := store.ReopenForIteration(job.ID, nil); err == nil {
		t.Fatal("expected reopen of running job to fail")
	}
}

func TestSubagentIterateRejectsNonTerminalJob(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	job, err := a.subagentJobs.StartWithOptions(subagentStartOptions{
		AgentType:     "Explore",
		Prompt:        "work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      1,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 100000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if _, err := a.IterateDurableSubagentWithContext(context.Background(), job.ID, "fix it"); err == nil {
		t.Fatal("expected iterate of running job to fail")
	}
}

func TestSubagentToolIterateRerunsWithReviewFeedback(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	// First pass consumes response[0]; the iterate re-run consumes response[1]
	// then one more turn because the injected feedback is drained at the final
	// response boundary and triggers a continuation turn (response[2]).
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("first pass output")}},
		{Content: []protocol.Block{protocol.TextBlock("fixed output after review")}},
		{Content: []protocol.Block{protocol.TextBlock("confirmed fixed")}},
	}}

	job, err := a.subagentJobs.StartWithOptions(subagentStartOptions{
		AgentType:     "general-purpose",
		Prompt:        "work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      3,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 100000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	// First pass completes normally.
	a.runSubagentJob(context.Background(), job.ID, subagentEventTarget{})
	first, _ := a.subagentJobs.Get(job.ID)
	if first.Status != subagentStatusCompleted {
		t.Fatalf("expected first pass completed, got %s: %s", first.Status, first.Error)
	}

	// Second pass: iterate with review feedback.
	result, err := a.handleTool(context.Background(), "subagent", map[string]interface{}{
		"action": "iterate",
		"job_id": job.ID,
		"input":  "review feedback: rename the variable",
	})
	if err != nil {
		t.Fatalf("iterate tool: %v", err)
	}
	var view subagentModelJobView
	if err := json.Unmarshal([]byte(result), &view); err != nil {
		t.Fatalf("parse iterate view: %v\n%s", err, result)
	}
	if view.JobID != job.ID {
		t.Fatalf("expected job id %s, got %s", job.ID, view.JobID)
	}
	got, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != subagentStatusCompleted {
		t.Fatalf("expected job completed after iterate, got %s: %s", got.Status, got.Error)
	}
	if !strings.Contains(got.Result, "fixed output after review") && !strings.Contains(got.Result, "confirmed fixed") {
		t.Fatalf("expected re-run output, got %q", got.Result)
	}
	found := false
	for _, msg := range got.Messages {
		if strings.Contains(protocol.MessageText(msg), "rename the variable") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected review feedback injected into messages, got %+v", got.Messages)
	}
}
