package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// Phase 4.1 — send_input / followup_task: queue input messages for a durable
// subagent and inject them through the runner's injection channel while the
// job is running.

func TestSubagentAppendAndDrainPendingInputs(t *testing.T) {
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
	if _, err := store.AppendPendingInputs(job.ID, []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first input"),
		protocol.NewTextMessage(protocol.RoleUser, "second input"),
	}); err != nil {
		t.Fatalf("append pending inputs: %v", err)
	}
	got, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(got.PendingInputs) != 2 {
		t.Fatalf("expected 2 pending inputs, got %d", len(got.PendingInputs))
	}
	// Drain with a limit of 1.
	drained, err := store.DrainPendingInputs(job.ID, 1)
	if err != nil {
		t.Fatalf("drain pending inputs: %v", err)
	}
	if len(drained) != 1 || protocol.MessageText(drained[0]) != "first input" {
		t.Fatalf("expected one drained input, got %+v", drained)
	}
	got, _ = store.Get(job.ID)
	if len(got.PendingInputs) != 1 || protocol.MessageText(got.PendingInputs[0]) != "second input" {
		t.Fatalf("expected remaining pending input, got %+v", got.PendingInputs)
	}
}

func TestSubagentAppendPendingInputsRejectsTerminalJob(t *testing.T) {
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
	if _, err := store.Finish(job.ID, subagentStatusCompleted, "done", ""); err != nil {
		t.Fatalf("finish job: %v", err)
	}
	if _, err := store.AppendPendingInputs(job.ID, []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "too late")}); err == nil {
		t.Fatal("expected append to finished job to fail")
	}
}

func TestSubagentToolSendInputQueuesMessage(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

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
	result, err := a.handleTool(context.Background(), "subagent", map[string]interface{}{
		"action": "send_input",
		"job_id": job.ID,
		"input":  "please check the docs",
	})
	if err != nil {
		t.Fatalf("send_input tool: %v", err)
	}
	var view subagentModelJobView
	if err := json.Unmarshal([]byte(result), &view); err != nil {
		t.Fatalf("parse send_input view: %v\n%s", err, result)
	}
	if view.JobID != job.ID {
		t.Fatalf("expected job id %s, got %s", job.ID, view.JobID)
	}
	got, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(got.PendingInputs) != 1 || !strings.Contains(protocol.MessageText(got.PendingInputs[0]), "check the docs") {
		t.Fatalf("expected queued input on job, got %+v", got.PendingInputs)
	}
}

func TestSubagentToolFollowupTaskQueuesMessage(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)

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
	if _, err := a.handleTool(context.Background(), "subagent", map[string]interface{}{
		"action": "followup_task",
		"job_id": job.ID,
		"input":  "now fix the failing test",
	}); err != nil {
		t.Fatalf("followup_task tool: %v", err)
	}
	got, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(got.PendingInputs) != 1 || !strings.Contains(protocol.MessageText(got.PendingInputs[0]), "fix the failing test") {
		t.Fatalf("expected followup queued on job, got %+v", got.PendingInputs)
	}
}

func TestSubagentRunnerInjectsQueuedInput(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.toolHandler.ActivateBundles(bundleSubagent)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("first turn, no tools")}},
		{Content: []protocol.Block{protocol.TextBlock("done after input")}},
	}}

	job, err := a.subagentJobs.StartWithOptions(subagentStartOptions{
		AgentType:     "Explore",
		Prompt:        "work",
		ToolNames:     []string{"todo_read"},
		MaxTurns:      2,
		WorkerID:      localGoDexWorkerID,
		SandboxID:     "sandbox:local:test",
		BasePrompt:    "base",
		ParentID:      "turn-1",
		ContextBudget: 100000,
	})
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	if _, err := a.subagentJobs.AppendPendingInputs(job.ID, []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "user followup directive"),
	}); err != nil {
		t.Fatalf("append pending input: %v", err)
	}

	// Drive the job synchronously so the injection channel is exercised.
	a.runSubagentJob(context.Background(), job.ID, subagentEventTarget{})

	got, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != subagentStatusCompleted {
		t.Fatalf("expected completed job, got %s: %s", got.Status, got.Error)
	}
	found := false
	for _, msg := range got.Messages {
		if strings.Contains(protocol.MessageText(msg), "user followup directive") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected queued input to be injected into messages, got %+v", got.Messages)
	}
}
