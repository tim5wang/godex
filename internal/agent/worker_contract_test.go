package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/workerruntime"
)

func TestWorkerRequestFromSubagentStartOptions(t *testing.T) {
	start := subagentStartOptions{
		SessionID:       "session-1",
		ParentTurnID:    "turn-1",
		ParentID:        "turn-1",
		AgentType:       "general-purpose",
		RoleID:          "role-1",
		RoleName:        "Reviewer",
		PackageName:     "pkg",
		Prompt:          "inspect repo",
		BasePrompt:      "base",
		ToolNames:       []string{"bash", "read_file"},
		WriteScope:      []string{"internal/agent"},
		PreviewJobIDs:   []string{"job-preview"},
		RequiredBundles: []string{"web"},
		RequiredTools:   []string{"web_search"},
		DefaultBundles:  []string{"core_code"},
		ToolPolicy:      []string{"shell:allow=go test"},
		Capabilities:    []string{"tools:bash"},
		SandboxID:       "sandbox:local:abc",
		RuntimeContext:  automation.SessionContext{SessionID: "session-1", Source: "web"},
		ModelHint:       "gpt-test",
		BudgetHint:      "max_turns:12",
		Display:         map[string]string{"icon": "test"},
		MaxTurns:        12,
		JobTimeoutMS:    5000,
	}

	req := workerRequestFromSubagentStartOptions(start)
	req.Capabilities.ToolNames[0] = "changed"
	req.Display["icon"] = "changed"

	if req.WorkerID != localGoDexWorkerID {
		t.Fatalf("worker id %q", req.WorkerID)
	}
	if req.Capabilities.SandboxID != "sandbox:local:abc" {
		t.Fatalf("sandbox id %q", req.Capabilities.SandboxID)
	}
	if req.Capabilities.RequiredBundles[0] != "web" || req.Capabilities.RequiredTools[0] != "web_search" {
		t.Fatalf("required capabilities were not mapped: %+v", req.Capabilities)
	}
	if start.ToolNames[0] != "bash" {
		t.Fatalf("start tool names mutated: %#v", start.ToolNames)
	}
	if start.Display["icon"] != "test" {
		t.Fatalf("start display mutated: %#v", start.Display)
	}
}

func TestSubagentStartOptionsFromWorkerRequest(t *testing.T) {
	req := workerruntime.JobRequest{
		WorkerID:     localGoDexWorkerID,
		SessionID:    "session-1",
		ParentTurnID: "turn-1",
		ParentID:     "turn-1",
		AgentType:    "general-purpose",
		RoleID:       "role-1",
		RoleName:     "Reviewer",
		PackageName:  "pkg",
		Prompt:       "inspect repo",
		BasePrompt:   "base",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:       []string{"bash", "read_file"},
			RequiredBundles: []string{"web"},
			RequiredTools:   []string{"web_search"},
			WriteScope:      []string{"internal/agent"},
			DefaultBundles:  []string{"core_code"},
			ToolPolicy:      []string{"shell:allow=go test"},
			SandboxID:       "sandbox:local:abc",
		},
		PreviewJobIDs: []string{"job-preview"},
		ModelHint:     "gpt-test",
		BudgetHint:    "max_turns:12",
		Display:       map[string]string{"icon": "test"},
		MaxTurns:      12,
		JobTimeoutMS:  5000,
	}

	start := subagentStartOptionsFromWorkerRequest(req, 3)
	start.ToolNames[0] = "changed"
	start.Display["icon"] = "changed"

	if start.MaxConcurrent != 3 {
		t.Fatalf("max concurrent %d", start.MaxConcurrent)
	}
	if start.SandboxID != "sandbox:local:abc" {
		t.Fatalf("sandbox id %q", start.SandboxID)
	}
	if start.RequiredBundles[0] != "web" || start.RequiredTools[0] != "web_search" {
		t.Fatalf("required capabilities were not mapped: %+v", start)
	}
	if req.Capabilities.ToolNames[0] != "bash" {
		t.Fatalf("request tool names mutated: %#v", req.Capabilities.ToolNames)
	}
	if req.Display["icon"] != "test" {
		t.Fatalf("request display mutated: %#v", req.Display)
	}
}

func TestWorkerHandleFromSubagentJobIncludesWorkerAndResult(t *testing.T) {
	now := time.Now()
	job := &subagentJob{
		ID:           "job-1",
		WorkerID:     localGoDexWorkerID,
		SessionID:    "session-1",
		ParentTurnID: "turn-1",
		AgentType:    "general-purpose",
		RoleID:       "role-1",
		RoleName:     "Reviewer",
		SandboxID:    "sandbox:local:abc",
		WorktreeDir:  "/tmp/worktree",
		Status:       subagentStatusCompleted,
		Result:       "done",
		CreatedAt:    now,
		UpdatedAt:    now,
		FinishedAt:   now,
	}

	handle := workerHandleFromSubagentJob(job)
	if handle.JobID != "job-1" || handle.WorkerID != localGoDexWorkerID || handle.Status != workerruntime.StatusCompleted {
		t.Fatalf("unexpected handle: %+v", handle)
	}
	if handle.Result.Text != "done" {
		t.Fatalf("result text %q", handle.Result.Text)
	}
	if handle.Capabilities.SandboxID != "sandbox:local:abc" {
		t.Fatalf("sandbox id %q", handle.Capabilities.SandboxID)
	}
}

func TestWorkerCapabilityContractPreservesInheritanceFields(t *testing.T) {
	start := subagentStartOptions{
		ToolNames:       []string{"bash", "read_file", "web_search"},
		RequiredBundles: []string{"web"},
		RequiredTools:   []string{"web_search"},
		DefaultBundles:  []string{"core_code", "web"},
		ToolPolicy:      []string{"shell:allow=go test", "shell:deny=rm -rf"},
		WriteScope:      []string{"docs", "internal/agent"},
		SandboxID:       "sandbox:local:abc",
	}

	req := workerRequestFromSubagentStartOptions(start)
	roundTrip := subagentStartOptionsFromWorkerRequest(req, 2)

	if got := strings.Join(roundTrip.ToolNames, ","); got != "bash,read_file,web_search" {
		t.Fatalf("tool names %q", got)
	}
	if got := strings.Join(roundTrip.RequiredBundles, ","); got != "web" {
		t.Fatalf("required bundles %q", got)
	}
	if got := strings.Join(roundTrip.RequiredTools, ","); got != "web_search" {
		t.Fatalf("required tools %q", got)
	}
	if got := strings.Join(roundTrip.DefaultBundles, ","); got != "core_code,web" {
		t.Fatalf("default bundles %q", got)
	}
	if got := strings.Join(roundTrip.ToolPolicy, ","); got != "shell:allow=go test,shell:deny=rm -rf" {
		t.Fatalf("tool policy %q", got)
	}
	if got := strings.Join(roundTrip.WriteScope, ","); got != "docs,internal/agent" {
		t.Fatalf("write scope %q", got)
	}
	if roundTrip.SandboxID != "sandbox:local:abc" {
		t.Fatalf("sandbox id %q", roundTrip.SandboxID)
	}
}
