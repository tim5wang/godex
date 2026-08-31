# GoDex Phase 3 Worker Runtime Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote durable subagents from tool-specific implementation details into a local Worker Runtime Protocol that can later be backed by remote workers without changing orchestrator call sites.

**Architecture:** Add a small `internal/workerruntime` package for stable worker/job/progress/result/review/merge contracts. Add an agent-local GoDex worker runtime adapter that maps those contracts onto the existing durable subagent store, workspace preparation, model runner, review, merge, cancel, and resume logic. Route current `task` tool and public durable-subagent methods through this runtime while preserving existing tool schemas, JSON storage compatibility, and local behavior.

**Tech Stack:** Go, existing `internal/agent`, `internal/workerruntime`, `internal/domain/events`, `internal/core/conversation`, `internal/contracts/protocol`, `go test ./internal/workerruntime ./internal/agent ./internal/domain/events`, `go test ./...`.

---

## Scope

This plan implements only Phase 3 from `docs/architecture-v2-spec.md`: Worker Runtime Protocol.

Included:

- Stable local worker runtime contract for job request, job handle, progress event, result, artifact reference, capability inheritance, review, and merge.
- Local GoDex worker runtime implementation backed by current durable subagent jobs.
- Durable subagent start/resume/cancel/review/merge routed through the worker runtime interface.
- Worker ID persisted on durable subagent jobs and exposed through API/model/event views.
- Contract adapters between `subagentJob`/`subagentStartOptions` and `workerruntime` types.
- Compatibility tests proving current durable subagent behavior stays intact.

Excluded:

- Remote worker transport.
- Multi-node worker scheduling.
- Session Graph branch clone/merge from Phase 4.
- Storage backend migration from Phase 5.
- Product UI pages for Worker Inspector.
- Changes to public `task` tool name or input schema.

## Target File Structure

- Create: `internal/workerruntime/types.go`
  - Owns protocol structs and clone helpers for worker job requests, handles, progress, results, artifacts, review, and merge.
- Create: `internal/workerruntime/types_test.go`
  - Unit tests for clone isolation, terminal status detection, capability cloning, and artifact normalization.
- Modify: `internal/agent/agent.go`
  - Add `workerRuntime workerruntime.Runtime` field.
- Create: `internal/agent/worker_runtime.go`
  - Owns the narrow agent-local runtime interface, default runtime accessor, and worker ID constants.
- Create: `internal/agent/worker_contract.go`
  - Owns mapping between durable subagent structs and `internal/workerruntime` contracts.
- Create: `internal/agent/worker_contract_test.go`
  - Tests request/handle/progress/review/merge mapping.
- Create: `internal/agent/local_worker_runtime.go`
  - Implements local GoDex worker runtime over current durable subagent functions.
- Create: `internal/agent/local_worker_runtime_test.go`
  - Tests dispatch, resume, cancel, review, merge, queued job startup, and behavior parity.
- Modify: `internal/agent/subagent_jobs.go`
  - Add `WorkerID` to durable job storage/view and route lifecycle methods through runtime.
- Modify: `internal/agent/subagent_tool.go`
  - Route tool actions through the runtime-backed Agent methods without changing schema.
- Modify: `internal/domain/events/events.go`
  - Add `worker_id` to subagent job event payload.
- Modify: `internal/agent/subagent_jobs_test.go`
  - Extend existing durable subagent tests to assert worker ID and runtime route behavior.
- Modify: `docs/architecture-v2-spec.md`
  - Add Phase 3 implementation note after Phase 3 acceptance criteria.
- Modify: `docs/architecture-v2-spec.en.md`
  - Mirror the Phase 3 implementation note.

## Behavior Invariants

- The `task` tool name, action names, and input schema remain unchanged.
- Existing durable subagent JSON jobs without `worker_id` still load successfully.
- Current local GoDex durable subagent behavior remains default.
- Existing review, merge, cancel, resume, wait, logs, and batch flows remain available.
- Worker IDs are opaque strings; code must not parse an ID prefix for behavior.
- Remote worker support is not implemented in this phase.
- Phase 3 must not introduce Session Graph branch semantics.

---

### Task 1: Add Worker Runtime Contract Types

**Files:**
- Create: `internal/workerruntime/types.go`
- Create: `internal/workerruntime/types_test.go`

- [ ] **Step 1: Write contract tests**

Create `internal/workerruntime/types_test.go`:

```go
package workerruntime

import (
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
)

func TestJobRequestCloneIsDeepCopy(t *testing.T) {
	req := JobRequest{
		JobID:        "job-1",
		WorkerID:     "worker:godex:local",
		SessionID:    "session-1",
		ParentTurnID: "turn-1",
		AgentType:    "general-purpose",
		Prompt:       "inspect repo",
		BasePrompt:   "base",
		RuntimeContext: automation.SessionContext{
			SessionID: "session-1",
			Source:    "web",
		},
		Capabilities: CapabilitySet{
			ToolNames:       []string{"bash", "read_file"},
			RequiredBundles: []string{"web"},
			RequiredTools:   []string{"web_search"},
			DefaultBundles:  []string{"core_code"},
			ToolPolicy:      []string{"shell:allow=go test"},
			WriteScope:      []string{"internal/agent"},
			SandboxID:       "sandbox:local:abc",
		},
		PreviewJobIDs: []string{"job-preview"},
		Display:       map[string]string{"icon": "test"},
		MaxTurns:      12,
		JobTimeoutMS:  5000,
	}

	cloned := req.Clone()
	cloned.Capabilities.ToolNames[0] = "changed"
	cloned.Capabilities.RequiredBundles[0] = "changed"
	cloned.Capabilities.RequiredTools[0] = "changed"
	cloned.Capabilities.DefaultBundles[0] = "changed"
	cloned.Capabilities.ToolPolicy[0] = "changed"
	cloned.Capabilities.WriteScope[0] = "changed"
	cloned.PreviewJobIDs[0] = "changed"
	cloned.Display["icon"] = "changed"

	if req.Capabilities.ToolNames[0] != "bash" {
		t.Fatalf("tool names mutated: %#v", req.Capabilities.ToolNames)
	}
	if req.Capabilities.RequiredBundles[0] != "web" {
		t.Fatalf("required bundles mutated: %#v", req.Capabilities.RequiredBundles)
	}
	if req.Capabilities.RequiredTools[0] != "web_search" {
		t.Fatalf("required tools mutated: %#v", req.Capabilities.RequiredTools)
	}
	if req.Capabilities.DefaultBundles[0] != "core_code" {
		t.Fatalf("default bundles mutated: %#v", req.Capabilities.DefaultBundles)
	}
	if req.Capabilities.ToolPolicy[0] != "shell:allow=go test" {
		t.Fatalf("tool policy mutated: %#v", req.Capabilities.ToolPolicy)
	}
	if req.Capabilities.WriteScope[0] != "internal/agent" {
		t.Fatalf("write scope mutated: %#v", req.Capabilities.WriteScope)
	}
	if req.PreviewJobIDs[0] != "job-preview" {
		t.Fatalf("preview jobs mutated: %#v", req.PreviewJobIDs)
	}
	if req.Display["icon"] != "test" {
		t.Fatalf("display mutated: %#v", req.Display)
	}
}

func TestStatusTerminal(t *testing.T) {
	for _, status := range []Status{StatusCompleted, StatusCanceled, StatusInterrupted, StatusTimeout, StatusError} {
		if !status.Terminal() {
			t.Fatalf("expected %s to be terminal", status)
		}
	}
	for _, status := range []Status{StatusPending, StatusPendingApproval, StatusRunning} {
		if status.Terminal() {
			t.Fatalf("expected %s to be non-terminal", status)
		}
	}
}

func TestArtifactRefNormalizeTrimsFields(t *testing.T) {
	ref := ArtifactRef{
		ID:        " artifact-1 ",
		Path:      " /tmp/out.txt ",
		Kind:      " file ",
		MIMEType:  " text/plain ",
		Producer:  " job-1 ",
		SandboxID: " sandbox:local:abc ",
		CreatedAt: time.Now(),
	}

	normalized := ref.Normalize()
	if normalized.ID != "artifact-1" || normalized.Path != "/tmp/out.txt" || normalized.Kind != "file" || normalized.MIMEType != "text/plain" || normalized.Producer != "job-1" || normalized.SandboxID != "sandbox:local:abc" {
		t.Fatalf("unexpected normalized artifact: %+v", normalized)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/workerruntime -count=1
```

Expected: fail because package `internal/workerruntime` does not exist.

- [ ] **Step 3: Implement contract types**

Create `internal/workerruntime/types.go`:

```go
package workerruntime

import (
	"context"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
)

type Status string

const (
	StatusPending         Status = "pending"
	StatusPendingApproval Status = "pending_approval"
	StatusRunning         Status = "running"
	StatusCompleted       Status = "completed"
	StatusCanceled        Status = "canceled"
	StatusInterrupted     Status = "interrupted"
	StatusTimeout         Status = "timeout"
	StatusError           Status = "error"
)

func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusCanceled, StatusInterrupted, StatusTimeout, StatusError:
		return true
	default:
		return false
	}
}

type CapabilitySet struct {
	ToolNames       []string `json:"tool_names,omitempty"`
	RequiredBundles []string `json:"required_bundles,omitempty"`
	RequiredTools   []string `json:"required_tools,omitempty"`
	DefaultBundles  []string `json:"default_bundles,omitempty"`
	ToolPolicy      []string `json:"tool_policy,omitempty"`
	WriteScope      []string `json:"write_scope,omitempty"`
	SandboxID       string   `json:"sandbox_id,omitempty"`
}

func (c CapabilitySet) Clone() CapabilitySet {
	return CapabilitySet{
		ToolNames:       cloneStrings(c.ToolNames),
		RequiredBundles: cloneStrings(c.RequiredBundles),
		RequiredTools:   cloneStrings(c.RequiredTools),
		DefaultBundles:  cloneStrings(c.DefaultBundles),
		ToolPolicy:      cloneStrings(c.ToolPolicy),
		WriteScope:      cloneStrings(c.WriteScope),
		SandboxID:       strings.TrimSpace(c.SandboxID),
	}
}

type JobRequest struct {
	JobID          string                    `json:"job_id,omitempty"`
	WorkerID       string                    `json:"worker_id,omitempty"`
	SessionID      string                    `json:"session_id,omitempty"`
	ParentTurnID   string                    `json:"parent_turn_id,omitempty"`
	ParentID       string                    `json:"parent_id,omitempty"`
	AgentType      string                    `json:"agent_type,omitempty"`
	RoleID         string                    `json:"role_id,omitempty"`
	RoleName       string                    `json:"role_name,omitempty"`
	PackageName    string                    `json:"package_name,omitempty"`
	Objective      string                    `json:"objective,omitempty"`
	DisplayTitle   string                    `json:"display_title,omitempty"`
	Prompt         string                    `json:"prompt,omitempty"`
	BasePrompt     string                    `json:"base_prompt,omitempty"`
	Capabilities   CapabilitySet             `json:"capabilities,omitempty"`
	PreviewJobIDs  []string                  `json:"preview_job_ids,omitempty"`
	RuntimeContext automation.SessionContext `json:"runtime_context,omitempty"`
	ModelHint      string                    `json:"model_hint,omitempty"`
	BudgetHint     string                    `json:"budget_hint,omitempty"`
	Display        map[string]string         `json:"display,omitempty"`
	MaxTurns       int                       `json:"max_turns,omitempty"`
	JobTimeoutMS   int                       `json:"job_timeout_ms,omitempty"`
}

func (r JobRequest) Clone() JobRequest {
	r.JobID = strings.TrimSpace(r.JobID)
	r.WorkerID = strings.TrimSpace(r.WorkerID)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.ParentTurnID = strings.TrimSpace(r.ParentTurnID)
	r.ParentID = strings.TrimSpace(r.ParentID)
	r.AgentType = strings.TrimSpace(r.AgentType)
	r.RoleID = strings.TrimSpace(r.RoleID)
	r.RoleName = strings.TrimSpace(r.RoleName)
	r.PackageName = strings.TrimSpace(r.PackageName)
	r.Objective = strings.TrimSpace(r.Objective)
	r.DisplayTitle = strings.TrimSpace(r.DisplayTitle)
	r.Prompt = strings.TrimSpace(r.Prompt)
	r.BasePrompt = strings.TrimSpace(r.BasePrompt)
	r.Capabilities = r.Capabilities.Clone()
	r.PreviewJobIDs = cloneStrings(r.PreviewJobIDs)
	r.RuntimeContext = r.RuntimeContext.Clone()
	r.ModelHint = strings.TrimSpace(r.ModelHint)
	r.BudgetHint = strings.TrimSpace(r.BudgetHint)
	r.Display = cloneStringMap(r.Display)
	return r
}

type JobRef struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

type JobHandle struct {
	JobID           string        `json:"job_id"`
	WorkerID        string        `json:"worker_id,omitempty"`
	SessionID       string        `json:"session_id,omitempty"`
	ParentTurnID    string        `json:"parent_turn_id,omitempty"`
	AgentType       string        `json:"agent_type,omitempty"`
	RoleID          string        `json:"role_id,omitempty"`
	RoleName        string        `json:"role_name,omitempty"`
	PackageName     string        `json:"package_name,omitempty"`
	Objective       string        `json:"objective,omitempty"`
	DisplayTitle    string        `json:"display_title,omitempty"`
	Status          Status        `json:"status,omitempty"`
	Error           string        `json:"error,omitempty"`
	Result          Result        `json:"result,omitempty"`
	Capabilities    CapabilitySet `json:"capabilities,omitempty"`
	WorktreeDir     string        `json:"worktree_dir,omitempty"`
	BaselineDir     string        `json:"baseline_dir,omitempty"`
	Isolation       string        `json:"isolation,omitempty"`
	WorkspaceOrigin string        `json:"workspace_origin,omitempty"`
	GitBranch       string        `json:"git_branch,omitempty"`
	CleanupState    string        `json:"cleanup_state,omitempty"`
	MergeStatus     string        `json:"merge_status,omitempty"`
	MaxTurns        int           `json:"max_turns,omitempty"`
	JobTimeoutMS    int           `json:"job_timeout_ms,omitempty"`
	CreatedAt       time.Time     `json:"created_at,omitempty"`
	UpdatedAt       time.Time     `json:"updated_at,omitempty"`
	StartedAt       time.Time     `json:"started_at,omitempty"`
	FinishedAt      time.Time     `json:"finished_at,omitempty"`
	MergedAt        time.Time     `json:"merged_at,omitempty"`
}

type ProgressEvent struct {
	Timestamp    time.Time `json:"timestamp"`
	Phase        string    `json:"phase,omitempty"`
	Message      string    `json:"message,omitempty"`
	ToolID       string    `json:"tool_id,omitempty"`
	ToolName     string    `json:"tool_name,omitempty"`
	Error        string    `json:"error,omitempty"`
	Result       string    `json:"result,omitempty"`
	Iteration    int       `json:"iteration,omitempty"`
	MaxTurns     int       `json:"max_turns,omitempty"`
	Model        string    `json:"model,omitempty"`
	RecoveryHint string    `json:"recovery_hint,omitempty"`
	WorkerID     string    `json:"worker_id,omitempty"`
	JobID        string    `json:"job_id,omitempty"`
	SandboxID    string    `json:"sandbox_id,omitempty"`
}

type ArtifactRef struct {
	ID        string    `json:"artifact_id,omitempty"`
	Path      string    `json:"path,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	MIMEType  string    `json:"mime_type,omitempty"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
	Producer  string    `json:"producer,omitempty"`
	WorkerID  string    `json:"worker_id,omitempty"`
	JobID     string    `json:"job_id,omitempty"`
	SandboxID string    `json:"sandbox_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

func (a ArtifactRef) Normalize() ArtifactRef {
	a.ID = strings.TrimSpace(a.ID)
	a.Path = strings.TrimSpace(a.Path)
	a.Kind = strings.TrimSpace(a.Kind)
	a.MIMEType = strings.TrimSpace(a.MIMEType)
	a.Producer = strings.TrimSpace(a.Producer)
	a.WorkerID = strings.TrimSpace(a.WorkerID)
	a.JobID = strings.TrimSpace(a.JobID)
	a.SandboxID = strings.TrimSpace(a.SandboxID)
	return a
}

type Result struct {
	Text      string        `json:"text,omitempty"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}

type FileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes,omitempty"`
	Binary bool   `json:"binary,omitempty"`
}

type ReviewRequest struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

type ReviewResult struct {
	JobID         string       `json:"job_id"`
	WorkerID      string       `json:"worker_id,omitempty"`
	WorktreeDir   string       `json:"worktree_dir,omitempty"`
	WriteScope    []string     `json:"write_scope,omitempty"`
	Changes       []FileChange `json:"changes,omitempty"`
	Diff          string       `json:"diff,omitempty"`
	DiffTruncated bool         `json:"diff_truncated,omitempty"`
	Conflicts     []string     `json:"conflicts,omitempty"`
}

type MergeRequest struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

type MergeResult struct {
	JobID       string       `json:"job_id"`
	WorkerID    string       `json:"worker_id,omitempty"`
	Status      string       `json:"status"`
	Applied     []FileChange `json:"applied,omitempty"`
	Conflicts   []string     `json:"conflicts,omitempty"`
	WorktreeDir string       `json:"worktree_dir,omitempty"`
}

type Runtime interface {
	Dispatch(context.Context, JobRequest) (JobHandle, error)
	Resume(context.Context, JobRef) (JobHandle, error)
	Cancel(context.Context, JobRef) (JobHandle, error)
	Review(context.Context, ReviewRequest) (ReviewResult, error)
	Merge(context.Context, MergeRequest) (MergeResult, error)
}

func cloneStrings(items []string) []string {
	return append([]string{}, items...)
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
```

- [ ] **Step 4: Run contract tests**

Run:

```bash
go test ./internal/workerruntime -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/workerruntime/types.go internal/workerruntime/types_test.go
git commit -m "feat(worker): add runtime protocol contracts"
```

---

### Task 2: Add Durable Subagent Contract Adapters

**Files:**
- Create: `internal/agent/worker_contract.go`
- Create: `internal/agent/worker_contract_test.go`
- Modify: `internal/agent/subagent_jobs.go`

- [ ] **Step 1: Write adapter tests**

Create `internal/agent/worker_contract_test.go`:

```go
package agent

import (
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/workerruntime"
)

func TestWorkerRequestFromSubagentStartOptions(t *testing.T) {
	start := subagentStartOptions{
		SessionID:      "session-1",
		ParentTurnID:   "turn-1",
		ParentID:       "turn-1",
		AgentType:      "general-purpose",
		RoleID:         "role-1",
		RoleName:       "Reviewer",
		PackageName:    "pkg",
		Prompt:         "inspect repo",
		BasePrompt:     "base",
		ToolNames:      []string{"bash", "read_file"},
		WriteScope:     []string{"internal/agent"},
		PreviewJobIDs:  []string{"job-preview"},
		DefaultBundles: []string{"core_code"},
		ToolPolicy:     []string{"shell:allow=go test"},
		Capabilities:   []string{"tools:bash"},
		SandboxID:      "sandbox:local:abc",
		RuntimeContext: automation.SessionContext{SessionID: "session-1", Source: "web"},
		ModelHint:      "gpt-test",
		BudgetHint:     "max_turns:12",
		Display:        map[string]string{"icon": "test"},
		MaxTurns:       12,
		JobTimeoutMS:   5000,
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
			ToolNames:      []string{"bash", "read_file"},
			WriteScope:     []string{"internal/agent"},
			DefaultBundles: []string{"core_code"},
			ToolPolicy:     []string{"shell:allow=go test"},
			SandboxID:      "sandbox:local:abc",
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run 'TestWorkerRequestFromSubagentStartOptions|TestSubagentStartOptionsFromWorkerRequest|TestWorkerHandleFromSubagentJobIncludesWorkerAndResult' -count=1
```

Expected: fail because adapter functions and `WorkerID` do not exist.

- [ ] **Step 3: Add worker ID to durable subagent jobs**

Modify `internal/agent/subagent_jobs.go`.

Add to `subagentJob` near `SandboxID`:

```go
	WorkerID string `json:"worker_id,omitempty"`
```

Add to `DurableSubagentJobView` near `SandboxID`:

```go
	WorkerID string `json:"worker_id,omitempty"`
```

Add to `subagentStartOptions`:

```go
	WorkerID string
```

In `StartWithOptions`, set:

```go
		WorkerID:        firstNonEmpty(strings.TrimSpace(opts.WorkerID), localGoDexWorkerID),
```

In `durableSubagentJobView`, set:

```go
		WorkerID:          job.WorkerID,
```

- [ ] **Step 4: Implement adapter functions**

Create `internal/agent/worker_contract.go`:

```go
package agent

import (
	"strings"

	"github.com/tim5wang/godex/internal/workerruntime"
)

const localGoDexWorkerID = "worker:godex:local"

func workerRequestFromSubagentStartOptions(start subagentStartOptions) workerruntime.JobRequest {
	return workerruntime.JobRequest{
		WorkerID:      firstNonEmpty(strings.TrimSpace(start.WorkerID), localGoDexWorkerID),
		SessionID:     strings.TrimSpace(start.SessionID),
		ParentTurnID:  strings.TrimSpace(start.ParentTurnID),
		ParentID:      strings.TrimSpace(start.ParentID),
		AgentType:     strings.TrimSpace(start.AgentType),
		RoleID:        strings.TrimSpace(start.RoleID),
		RoleName:      strings.TrimSpace(start.RoleName),
		PackageName:   strings.TrimSpace(start.PackageName),
		Prompt:        strings.TrimSpace(start.Prompt),
		BasePrompt:    strings.TrimSpace(start.BasePrompt),
		PreviewJobIDs: append([]string{}, start.PreviewJobIDs...),
		RuntimeContext: start.RuntimeContext.Clone(),
		ModelHint:     strings.TrimSpace(start.ModelHint),
		BudgetHint:    strings.TrimSpace(start.BudgetHint),
		Display:       cloneStringMap(start.Display),
		MaxTurns:      start.MaxTurns,
		JobTimeoutMS:  start.JobTimeoutMS,
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:      append([]string{}, start.ToolNames...),
			DefaultBundles: append([]string{}, start.DefaultBundles...),
			ToolPolicy:     append([]string{}, start.ToolPolicy...),
			WriteScope:     append([]string{}, start.WriteScope...),
			SandboxID:      strings.TrimSpace(start.SandboxID),
		},
	}.Clone()
}

func subagentStartOptionsFromWorkerRequest(req workerruntime.JobRequest, maxConcurrent int) subagentStartOptions {
	req = req.Clone()
	return subagentStartOptions{
		SessionID:      req.SessionID,
		ParentTurnID:   req.ParentTurnID,
		ParentID:       req.ParentID,
		AgentType:      req.AgentType,
		RoleID:         req.RoleID,
		RoleName:       req.RoleName,
		PackageName:    req.PackageName,
		Prompt:         req.Prompt,
		BasePrompt:     req.BasePrompt,
		ToolNames:      append([]string{}, req.Capabilities.ToolNames...),
		WriteScope:     append([]string{}, req.Capabilities.WriteScope...),
		PreviewJobIDs:  append([]string{}, req.PreviewJobIDs...),
		DefaultBundles: append([]string{}, req.Capabilities.DefaultBundles...),
		ToolPolicy:     append([]string{}, req.Capabilities.ToolPolicy...),
		SandboxID:      req.Capabilities.SandboxID,
		WorkerID:       firstNonEmpty(req.WorkerID, localGoDexWorkerID),
		ModelHint:      req.ModelHint,
		BudgetHint:     req.BudgetHint,
		Display:        cloneStringMap(req.Display),
		RuntimeContext: req.RuntimeContext.Clone(),
		MaxTurns:       req.MaxTurns,
		MaxConcurrent:  maxConcurrent,
		JobTimeoutMS:   req.JobTimeoutMS,
	}
}

func workerHandleFromSubagentJob(job *subagentJob) workerruntime.JobHandle {
	if job == nil {
		return workerruntime.JobHandle{}
	}
	return workerruntime.JobHandle{
		JobID:           job.ID,
		WorkerID:        firstNonEmpty(strings.TrimSpace(job.WorkerID), localGoDexWorkerID),
		SessionID:       job.SessionID,
		ParentTurnID:    job.ParentTurnID,
		AgentType:       job.AgentType,
		RoleID:          job.RoleID,
		RoleName:        job.RoleName,
		PackageName:     job.PackageName,
		Objective:       firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt)),
		DisplayTitle:    job.DisplayTitle,
		Status:          workerruntime.Status(job.Status),
		Error:           job.Error,
		Result:          workerruntime.Result{Text: job.Result},
		WorktreeDir:     job.WorktreeDir,
		BaselineDir:     job.BaselineDir,
		Isolation:       job.Isolation,
		WorkspaceOrigin: job.WorkspaceOrigin,
		GitBranch:       job.GitBranch,
		CleanupState:    job.CleanupState,
		MergeStatus:     job.MergeStatus,
		MaxTurns:        job.MaxTurns,
		JobTimeoutMS:    job.JobTimeoutMS,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		StartedAt:       job.StartedAt,
		FinishedAt:      job.FinishedAt,
		MergedAt:        job.MergedAt,
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:      append([]string{}, job.ToolNames...),
			DefaultBundles: append([]string{}, job.DefaultBundles...),
			ToolPolicy:     append([]string{}, job.ToolPolicy...),
			WriteScope:     append([]string{}, job.WriteScope...),
			SandboxID:      job.SandboxID,
		},
	}
}
```

- [ ] **Step 5: Run adapter tests**

Run:

```bash
go test ./internal/agent -run 'TestWorkerRequestFromSubagentStartOptions|TestSubagentStartOptionsFromWorkerRequest|TestWorkerHandleFromSubagentJobIncludesWorkerAndResult' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/subagent_jobs.go internal/agent/worker_contract.go internal/agent/worker_contract_test.go
git commit -m "feat(agent): map subagents to worker runtime contracts"
```

---

### Task 3: Implement Local GoDex Worker Runtime

**Files:**
- Create: `internal/agent/worker_runtime.go`
- Create: `internal/agent/local_worker_runtime.go`
- Create: `internal/agent/local_worker_runtime_test.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Write local runtime tests**

Create `internal/agent/local_worker_runtime_test.go`:

```go
package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/workerruntime"
)

func TestLocalWorkerRuntimeDispatchStartsDurableSubagent(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller("worker done")

	handle, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "general-purpose",
		Prompt:    "inspect worker runtime",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:  []string{"bash", "read_file", "write_file", "edit_file"},
			WriteScope: []string{"notes"},
			SandboxID:  a.SandboxID(),
		},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("dispatch worker job: %v", err)
	}
	if handle.JobID == "" {
		t.Fatalf("expected job id")
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, handle.JobID, subagentStatusCompleted)
	if completed.WorkerID != localGoDexWorkerID {
		t.Fatalf("worker id %q", completed.WorkerID)
	}
	if completed.Result != "worker done" {
		t.Fatalf("result %q", completed.Result)
	}
}

func TestLocalWorkerRuntimeReviewAndMerge(t *testing.T) {
	a := newTestAgent(t, 4096)
	initGitRepo(t, a.cfg.WorkspaceDir)
	a.client = &sequenceCaller{responses: []protocol.Response{
		{Content: []protocol.Block{
			protocol.ToolUseBlock("edit", "write_file", map[string]interface{}{"path": "notes/out.txt", "content": "worker\n"}),
		}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}

	handle, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "general-purpose",
		Prompt:    "write notes",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:  []string{"bash", "read_file", "write_file", "edit_file"},
			WriteScope: []string{"notes"},
			SandboxID:  a.SandboxID(),
		},
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("dispatch worker job: %v", err)
	}
	waitForSubagentStatus(t, a.subagentJobs, handle.JobID, subagentStatusCompleted)

	review, err := a.WorkerRuntime().Review(context.Background(), workerruntime.ReviewRequest{JobID: handle.JobID, WorkerID: localGoDexWorkerID})
	if err != nil {
		t.Fatalf("review worker job: %v", err)
	}
	if len(review.Changes) != 1 || review.Changes[0].Path != "notes/out.txt" {
		t.Fatalf("unexpected review changes: %+v", review.Changes)
	}

	merge, err := a.WorkerRuntime().Merge(context.Background(), workerruntime.MergeRequest{JobID: handle.JobID, WorkerID: localGoDexWorkerID})
	if err != nil {
		t.Fatalf("merge worker job: %v", err)
	}
	if merge.Status != subagentMergeMerged {
		t.Fatalf("merge status %q", merge.Status)
	}
}

func TestLocalWorkerRuntimeCancel(t *testing.T) {
	a := newTestAgent(t, 4096)
	release := make(chan struct{})
	a.client = blockingSubagentCaller{release: release}

	handle, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  localGoDexWorkerID,
		AgentType: "Explore",
		Prompt:    "wait",
		Capabilities: workerruntime.CapabilitySet{
			ToolNames: []string{"bash", "read_file"},
			SandboxID: a.SandboxID(),
		},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("dispatch worker job: %v", err)
	}

	canceled, err := a.WorkerRuntime().Cancel(context.Background(), workerruntime.JobRef{JobID: handle.JobID, WorkerID: localGoDexWorkerID})
	if err != nil {
		t.Fatalf("cancel worker job: %v", err)
	}
	if canceled.Status != workerruntime.StatusCanceled {
		t.Fatalf("status %q", canceled.Status)
	}
	close(release)
}

func TestLocalWorkerRuntimeRejectsOtherWorkerID(t *testing.T) {
	a := newTestAgent(t, 4096)
	_, err := a.WorkerRuntime().Dispatch(context.Background(), workerruntime.JobRequest{
		WorkerID:  "worker:remote:test",
		AgentType: "Explore",
		Prompt:    "inspect",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported worker") {
		t.Fatalf("expected unsupported worker error, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run 'TestLocalWorkerRuntime' -count=1
```

Expected: fail because `WorkerRuntime` and local runtime do not exist.

- [ ] **Step 3: Add agent runtime field and accessor**

Modify `internal/agent/agent.go`.

Add import:

```go
	"github.com/tim5wang/godex/internal/workerruntime"
```

Add to `Agent`:

```go
	workerRuntime workerruntime.Runtime
```

Create `internal/agent/worker_runtime.go`:

```go
package agent

import "github.com/tim5wang/godex/internal/workerruntime"

func (a *Agent) WorkerRuntime() workerruntime.Runtime {
	if a == nil {
		return nil
	}
	if a.workerRuntime == nil {
		a.workerRuntime = localGoDexWorkerRuntime{agent: a}
	}
	return a.workerRuntime
}
```

- [ ] **Step 4: Implement local runtime**

Create `internal/agent/local_worker_runtime.go`:

```go
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/workerruntime"
)

type localGoDexWorkerRuntime struct {
	agent *Agent
}

func (r localGoDexWorkerRuntime) Dispatch(ctx context.Context, req workerruntime.JobRequest) (workerruntime.JobHandle, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.JobHandle{}, fmt.Errorf("local worker runtime unavailable")
	}
	req = req.Clone()
	if workerID := strings.TrimSpace(req.WorkerID); workerID != "" && workerID != localGoDexWorkerID {
		return workerruntime.JobHandle{}, fmt.Errorf("unsupported worker %q", workerID)
	}
	start := subagentStartOptionsFromWorkerRequest(req, a.subagentMaxConcurrentJobs())
	start.WorkerID = localGoDexWorkerID
	job, err := a.subagentJobs.StartWithOptions(start)
	if err != nil {
		return workerruntime.JobHandle{}, err
	}
	target := subagentEventTargetFromContext(ctx)
	a.subagentJobs.RegisterTarget(job.ID, target)
	target.emitIdentity(job)
	if job.Status == subagentStatusPending {
		target.emit(job, "pending", "Subagent job queued.", "", "", "", "")
		return workerHandleFromSubagentJob(job), nil
	}
	target.emit(job, "started", "Subagent job started.", "", "", "", "")
	jobID := job.ID
	job, err = a.prepareSubagentWorkspace(job)
	if err != nil {
		finished, _ := a.subagentJobs.Finish(jobID, subagentStatusError, "", err.Error())
		target.emit(finished, string(subagentStatusError), "Subagent workspace preparation failed.", "", "", err.Error(), "")
		a.startPendingSubagents(target.sink)
		return workerruntime.JobHandle{}, err
	}
	target.emit(job, "worktree_prepared", "Subagent isolated workspace prepared.", "", "", "", "")
	a.runSubagentJobAsync(job.ID, target)
	return workerHandleFromSubagentJob(job), nil
}

func (r localGoDexWorkerRuntime) Resume(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.JobHandle{}, fmt.Errorf("local worker runtime unavailable")
	}
	if workerID := strings.TrimSpace(ref.WorkerID); workerID != "" && workerID != localGoDexWorkerID {
		return workerruntime.JobHandle{}, fmt.Errorf("unsupported worker %q", workerID)
	}
	job, err := a.subagentJobs.ResumeWithLimit(ref.JobID, a.subagentMaxConcurrentJobs())
	if err != nil {
		return workerruntime.JobHandle{}, err
	}
	target := subagentEventTargetFromContext(ctx)
	a.subagentJobs.RegisterTarget(job.ID, target)
	if job.Status == subagentStatusPending {
		target.emit(job, "pending", "Subagent job queued for resume.", "", "", "", "")
		return workerHandleFromSubagentJob(job), nil
	}
	target.emit(job, "resumed", "Subagent job resumed.", "", "", "", "")
	if err := a.ensureSubagentWorkspace(job); err != nil {
		finished, _ := a.subagentJobs.Finish(job.ID, subagentStatusError, "", err.Error())
		target.emit(finished, string(subagentStatusError), "Subagent isolated workspace is unavailable.", "", "", err.Error(), "")
		a.startPendingSubagents(target.sink)
		return workerruntime.JobHandle{}, err
	}
	a.runSubagentJobAsync(job.ID, target)
	return workerHandleFromSubagentJob(job), nil
}

func (r localGoDexWorkerRuntime) Cancel(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.JobHandle{}, fmt.Errorf("local worker runtime unavailable")
	}
	job, err := a.subagentJobs.Cancel(ref.JobID)
	if err != nil {
		return workerruntime.JobHandle{}, err
	}
	subagentEventTargetFromContext(ctx).emit(job, "canceled", "Subagent job canceled.", "", "", job.Error, "")
	return workerHandleFromSubagentJob(job), nil
}

func (r localGoDexWorkerRuntime) Review(ctx context.Context, req workerruntime.ReviewRequest) (workerruntime.ReviewResult, error) {
	_ = ctx
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.ReviewResult{}, fmt.Errorf("local worker runtime unavailable")
	}
	review, err := a.reviewDurableSubagentDirect(req.JobID)
	if err != nil {
		return workerruntime.ReviewResult{}, err
	}
	return workerReviewFromSubagentReview(review), nil
}

func (r localGoDexWorkerRuntime) Merge(ctx context.Context, req workerruntime.MergeRequest) (workerruntime.MergeResult, error) {
	a := r.agent
	if a == nil || a.subagentJobs == nil {
		return workerruntime.MergeResult{}, fmt.Errorf("local worker runtime unavailable")
	}
	result, err := a.mergeDurableSubagentDirect(ctx, req.JobID)
	if err != nil {
		return workerruntime.MergeResult{}, err
	}
	return workerMergeFromSubagentMerge(result), nil
}
```

- [ ] **Step 5: Add review and merge direct helpers**

In `internal/agent/subagent_jobs.go`, rename the existing implementation bodies:

```go
func (a *Agent) reviewDurableSubagentDirect(id string) (subagentReview, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentReview{}, err
	}
	return reviewSubagentJob(job)
}

func (a *Agent) mergeDurableSubagentDirect(ctx context.Context, id string) (subagentMergeResult, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentMergeResult{}, err
	}
	if len(job.WriteScope) == 0 {
		return subagentMergeResult{}, fmt.Errorf("subagent merge requires write_scope")
	}
	if job.Status == subagentStatusRunning {
		return subagentMergeResult{}, fmt.Errorf("subagent job %s is still running", job.ID)
	}
	review, err := reviewSubagentJob(job)
	if err != nil {
		return subagentMergeResult{}, err
	}
	result := subagentMergeResult{
		JobID:       job.ID,
		Status:      subagentMergePending,
		WorktreeDir: job.WorktreeDir,
	}
	if len(review.Changes) == 0 {
		updated, err := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeNoChanges, subagentProgressEvent{
			Phase:   "merge_reviewed",
			Message: "Subagent merge reviewed with no changes.",
		})
		if err != nil {
			return subagentMergeResult{}, err
		}
		subagentEventTargetFromContext(ctx).emit(updated, "merge_reviewed", "Subagent merge reviewed with no changes.", "", "", "", "")
		result.Status = subagentMergeNoChanges
		return result, nil
	}
	conflicts, err := detectSubagentMergeConflicts(a.cfg.WorkspaceDir, job.BaselineDir, job.WorktreeDir, review.Changes)
	if err != nil {
		return subagentMergeResult{}, err
	}
	if len(conflicts) > 0 {
		updated, updateErr := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeConflict, subagentProgressEvent{
			Phase:   "merge_conflict",
			Message: "Subagent merge has conflicts.",
			Error:   strings.Join(conflicts, "\n"),
		})
		if updateErr != nil {
			return subagentMergeResult{}, updateErr
		}
		subagentEventTargetFromContext(ctx).emit(updated, "merge_conflict", "Subagent merge has conflicts.", "", "", strings.Join(conflicts, "\n"), "")
		result.Status = subagentMergeConflict
		result.Conflicts = conflicts
		return result, nil
	}
	if err := applySubagentChanges(a.cfg.WorkspaceDir, job.WorktreeDir, review.Changes); err != nil {
		return subagentMergeResult{}, err
	}
	updated, err := a.subagentJobs.SetMergeStatus(job.ID, subagentMergeMerged, subagentProgressEvent{
		Phase:   "merged",
		Message: fmt.Sprintf("Subagent merge applied %d file change(s).", len(review.Changes)),
	})
	if err != nil {
		return subagentMergeResult{}, err
	}
	subagentEventTargetFromContext(ctx).emit(updated, "merged", fmt.Sprintf("Subagent merge applied %d file change(s).", len(review.Changes)), "", "", "", "")
	result.Status = subagentMergeMerged
	result.Applied = review.Changes
	return result, nil
}
```

Then update public wrappers in Task 4 to call `WorkerRuntime()`.

- [ ] **Step 6: Add review and merge adapters**

Append to `internal/agent/worker_contract.go`:

```go
func workerReviewFromSubagentReview(review subagentReview) workerruntime.ReviewResult {
	changes := make([]workerruntime.FileChange, 0, len(review.Changes))
	for _, item := range review.Changes {
		changes = append(changes, workerruntime.FileChange{
			Path:   item.Path,
			Status: item.Status,
			Bytes:  item.Bytes,
			Binary: item.Binary,
		})
	}
	return workerruntime.ReviewResult{
		JobID:         review.JobID,
		WorkerID:      localGoDexWorkerID,
		WorktreeDir:   review.WorktreeDir,
		WriteScope:    append([]string{}, review.WriteScope...),
		Changes:       changes,
		Diff:          review.Diff,
		DiffTruncated: review.DiffTruncated,
		Conflicts:     append([]string{}, review.Conflicts...),
	}
}

func workerMergeFromSubagentMerge(result subagentMergeResult) workerruntime.MergeResult {
	applied := make([]workerruntime.FileChange, 0, len(result.Applied))
	for _, item := range result.Applied {
		applied = append(applied, workerruntime.FileChange{
			Path:   item.Path,
			Status: item.Status,
			Bytes:  item.Bytes,
			Binary: item.Binary,
		})
	}
	return workerruntime.MergeResult{
		JobID:       result.JobID,
		WorkerID:    localGoDexWorkerID,
		Status:      result.Status,
		Applied:     applied,
		Conflicts:   append([]string{}, result.Conflicts...),
		WorktreeDir: result.WorktreeDir,
	}
}
```

- [ ] **Step 7: Run local runtime tests**

Run:

```bash
go test ./internal/agent -run 'TestLocalWorkerRuntime' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/agent.go internal/agent/worker_runtime.go internal/agent/local_worker_runtime.go internal/agent/local_worker_runtime_test.go internal/agent/subagent_jobs.go internal/agent/worker_contract.go
git commit -m "feat(agent): add local godex worker runtime"
```

---

### Task 4: Route Durable Subagent Lifecycle Through Worker Runtime

**Files:**
- Modify: `internal/agent/subagent_jobs.go`
- Modify: `internal/agent/subagent_tool.go`
- Modify: `internal/agent/subagent_jobs_test.go`

- [ ] **Step 1: Write fake runtime routing tests**

Append to `internal/agent/local_worker_runtime_test.go`:

```go
type fakeWorkerRuntime struct {
	dispatchReq workerruntime.JobRequest
	resumeRef   workerruntime.JobRef
	cancelRef   workerruntime.JobRef
	reviewReq   workerruntime.ReviewRequest
	mergeReq    workerruntime.MergeRequest
}

func (f *fakeWorkerRuntime) Dispatch(ctx context.Context, req workerruntime.JobRequest) (workerruntime.JobHandle, error) {
	_ = ctx
	f.dispatchReq = req.Clone()
	return workerruntime.JobHandle{JobID: "job-fake", WorkerID: localGoDexWorkerID, Status: workerruntime.StatusRunning}, nil
}

func (f *fakeWorkerRuntime) Resume(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	_ = ctx
	f.resumeRef = ref
	return workerruntime.JobHandle{JobID: ref.JobID, WorkerID: localGoDexWorkerID, Status: workerruntime.StatusRunning}, nil
}

func (f *fakeWorkerRuntime) Cancel(ctx context.Context, ref workerruntime.JobRef) (workerruntime.JobHandle, error) {
	_ = ctx
	f.cancelRef = ref
	return workerruntime.JobHandle{JobID: ref.JobID, WorkerID: localGoDexWorkerID, Status: workerruntime.StatusCanceled}, nil
}

func (f *fakeWorkerRuntime) Review(ctx context.Context, req workerruntime.ReviewRequest) (workerruntime.ReviewResult, error) {
	_ = ctx
	f.reviewReq = req
	return workerruntime.ReviewResult{JobID: req.JobID, WorkerID: localGoDexWorkerID}, nil
}

func (f *fakeWorkerRuntime) Merge(ctx context.Context, req workerruntime.MergeRequest) (workerruntime.MergeResult, error) {
	_ = ctx
	f.mergeReq = req
	return workerruntime.MergeResult{JobID: req.JobID, WorkerID: localGoDexWorkerID, Status: subagentMergeNoChanges}, nil
}

func TestStartDurableSubagentUsesWorkerRuntime(t *testing.T) {
	a := newTestAgent(t, 4096)
	fake := &fakeWorkerRuntime{}
	a.workerRuntime = fake

	job, err := a.StartDurableSubagent("inspect", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	if job.ID != "job-fake" {
		t.Fatalf("job id %q", job.ID)
	}
	if fake.dispatchReq.Prompt != "inspect" || fake.dispatchReq.WorkerID != localGoDexWorkerID {
		t.Fatalf("unexpected dispatch request: %+v", fake.dispatchReq)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run TestStartDurableSubagentUsesWorkerRuntime -count=1
```

Expected: fail because `startDurableSubagentWithContext` still calls the durable job store directly.

- [ ] **Step 3: Route start through WorkerRuntime**

Modify `startDurableSubagentWithContext` in `internal/agent/subagent_jobs.go`.

Keep prompt rewriting, role resolution, capability validation, and `subagentStartOptions` construction. Replace the direct `StartWithOptions`/workspace/run block with:

```go
	start.WorkerID = localGoDexWorkerID
	handle, err := a.WorkerRuntime().Dispatch(ctx, workerRequestFromSubagentStartOptions(start))
	if err != nil {
		return nil, err
	}
	job, err := a.subagentJobs.Get(handle.JobID)
	if err != nil {
		return &subagentJob{ID: handle.JobID, WorkerID: handle.WorkerID, Status: subagentJobStatus(handle.Status)}, nil
	}
	return job, nil
```

- [ ] **Step 4: Route resume, cancel, review, and merge through WorkerRuntime**

Update these functions in `internal/agent/subagent_jobs.go`:

```go
func (a *Agent) ResumeDurableSubagentWithContext(ctx context.Context, id string) (*subagentJob, error) {
	handle, err := a.WorkerRuntime().Resume(ctx, workerruntime.JobRef{JobID: id, WorkerID: localGoDexWorkerID})
	if err != nil {
		return nil, err
	}
	job, err := a.subagentJobs.Get(handle.JobID)
	if err != nil {
		return &subagentJob{ID: handle.JobID, WorkerID: handle.WorkerID, Status: subagentJobStatus(handle.Status)}, nil
	}
	return job, nil
}

func (a *Agent) CancelDurableSubagentWithContext(ctx context.Context, sessionID, id string) (DurableSubagentJobView, error) {
	if _, err := a.getDurableSubagentForSession(sessionID, id); err != nil {
		return DurableSubagentJobView{}, err
	}
	handle, err := a.WorkerRuntime().Cancel(ctx, workerruntime.JobRef{JobID: id, SessionID: sessionID, WorkerID: localGoDexWorkerID})
	if err != nil {
		return DurableSubagentJobView{}, err
	}
	job, err := a.subagentJobs.Get(handle.JobID)
	if err != nil {
		return DurableSubagentJobView{}, err
	}
	return durableSubagentJobView(job), nil
}

func (a *Agent) ReviewDurableSubagent(id string) (subagentReview, error) {
	result, err := a.WorkerRuntime().Review(context.Background(), workerruntime.ReviewRequest{JobID: id, WorkerID: localGoDexWorkerID})
	if err != nil {
		return subagentReview{}, err
	}
	return subagentReviewFromWorkerReview(result), nil
}

func (a *Agent) MergeDurableSubagentWithContext(ctx context.Context, id string) (subagentMergeResult, error) {
	result, err := a.WorkerRuntime().Merge(ctx, workerruntime.MergeRequest{JobID: id, WorkerID: localGoDexWorkerID})
	if err != nil {
		return subagentMergeResult{}, err
	}
	return subagentMergeFromWorkerMerge(result), nil
}
```

- [ ] **Step 5: Add reverse review and merge adapters**

Append to `internal/agent/worker_contract.go`:

```go
func subagentReviewFromWorkerReview(result workerruntime.ReviewResult) subagentReview {
	changes := make([]subagentFileChange, 0, len(result.Changes))
	for _, item := range result.Changes {
		changes = append(changes, subagentFileChange{
			Path:   item.Path,
			Status: item.Status,
			Bytes:  item.Bytes,
			Binary: item.Binary,
		})
	}
	return subagentReview{
		JobID:         result.JobID,
		WorktreeDir:   result.WorktreeDir,
		WriteScope:    append([]string{}, result.WriteScope...),
		Changes:       changes,
		Diff:          result.Diff,
		DiffTruncated: result.DiffTruncated,
		Conflicts:     append([]string{}, result.Conflicts...),
	}
}

func subagentMergeFromWorkerMerge(result workerruntime.MergeResult) subagentMergeResult {
	applied := make([]subagentFileChange, 0, len(result.Applied))
	for _, item := range result.Applied {
		applied = append(applied, subagentFileChange{
			Path:   item.Path,
			Status: item.Status,
			Bytes:  item.Bytes,
			Binary: item.Binary,
		})
	}
	return subagentMergeResult{
		JobID:       result.JobID,
		Status:      result.Status,
		Applied:     applied,
		Conflicts:   append([]string{}, result.Conflicts...),
		WorktreeDir: result.WorktreeDir,
	}
}
```

- [ ] **Step 6: Route `task` tool actions through Agent methods**

In `internal/agent/subagent_tool.go`, update action handlers:

```go
case "cancel":
	view, err := agent.CancelDurableSubagentWithContext(ctx, "", args.JobID)
	if err != nil {
		return tools.ToolResult{}, err
	}
	return tools.ToolResult{Structured: formatSubagentModelJob(&subagentJob{
		ID:          view.JobID,
		WorkerID:    view.WorkerID,
		SandboxID:   view.SandboxID,
		Status:      subagentJobStatus(view.Status),
		Error:       view.Error,
		Result:      view.Result,
		CreatedAt:   view.CreatedAt,
		UpdatedAt:   view.UpdatedAt,
		FinishedAt:  view.FinishedAt,
		MergeStatus: view.MergeStatus,
	})}, nil
```

Keep `resume`, `review`, and `merge` calling the public Agent methods; those methods now route through the runtime.

- [ ] **Step 7: Run routing and existing subagent tests**

Run:

```bash
go test ./internal/agent -run 'TestStartDurableSubagentUsesWorkerRuntime|TestDurableSubagent|TestSubagent|TestWorkflow|TestLongTask' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/subagent_jobs.go internal/agent/subagent_tool.go internal/agent/local_worker_runtime_test.go internal/agent/worker_contract.go
git commit -m "feat(agent): route durable subagents through worker runtime"
```

---

### Task 5: Expose Worker ID In Views And Events

**Files:**
- Modify: `internal/domain/events/events.go`
- Modify: `internal/agent/subagent_jobs.go`
- Modify: `internal/agent/subagent_tool.go`
- Modify: `internal/agent/subagent_jobs_test.go`

- [ ] **Step 1: Write worker ID visibility tests**

Append to `internal/agent/subagent_jobs_test.go`:

```go
func TestDurableSubagentExposesWorkerID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.client = repeatedTextCaller("done")
	got := make(chan events.Event, 8)
	ctx := withSubagentEventTarget(context.Background(), subagentEventTarget{
		sessionID: "session-worker",
		turnID:    "turn-worker",
		sink: events.SinkFunc(func(event events.Event) {
			got <- event
		}),
	})

	job, err := a.StartDurableSubagentWithContext(ctx, "inspect worker id", "Explore", nil)
	if err != nil {
		t.Fatalf("start durable subagent: %v", err)
	}
	completed := waitForSubagentStatus(t, a.subagentJobs, job.ID, subagentStatusCompleted)
	if completed.WorkerID != localGoDexWorkerID {
		t.Fatalf("worker id %q", completed.WorkerID)
	}
	view := durableSubagentJobView(completed)
	if view.WorkerID != localGoDexWorkerID {
		t.Fatalf("view worker id %q", view.WorkerID)
	}
	model := formatSubagentModelJob(completed)
	if model.WorkerID != localGoDexWorkerID {
		t.Fatalf("model worker id %q", model.WorkerID)
	}

	foundEventWorkerID := false
	deadline := time.After(2 * time.Second)
	for !foundEventWorkerID {
		select {
		case event := <-got:
			if event.Type != events.EventSubagentJobUpdated {
				continue
			}
			payload, _ := event.Payload.(events.SubagentJobPayload)
			if payload.WorkerID == localGoDexWorkerID {
				foundEventWorkerID = true
			}
		case <-deadline:
			t.Fatalf("expected subagent event payload worker id %q", localGoDexWorkerID)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run TestDurableSubagentExposesWorkerID -count=1
```

Expected: fail because view/model/event worker ID fields are incomplete.

- [ ] **Step 3: Add worker ID to views and events**

Modify `internal/domain/events/events.go`, `SubagentJobPayload`:

```go
	WorkerID string `json:"worker_id,omitempty"`
```

Modify `subagentEventTarget.emit` in `internal/agent/subagent_jobs.go`:

```go
	WorkerID: firstNonEmpty(job.WorkerID, localGoDexWorkerID),
```

Modify `subagentModelJobView` in `internal/agent/subagent_tool.go`:

```go
	WorkerID string `json:"worker_id,omitempty"`
```

Modify `formatSubagentModelJob`:

```go
	WorkerID: firstNonEmpty(job.WorkerID, localGoDexWorkerID),
```

Modify `formatSubagentJob` map:

```go
	"worker_id": firstNonEmpty(job.WorkerID, localGoDexWorkerID),
```

- [ ] **Step 4: Run worker ID visibility tests**

Run:

```bash
go test ./internal/agent -run 'TestDurableSubagentExposesWorkerID|TestDurableSubagentRecordsSandboxID' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/events/events.go internal/agent/subagent_jobs.go internal/agent/subagent_tool.go internal/agent/subagent_jobs_test.go
git commit -m "feat(agent): expose worker id on durable subagents"
```

---

### Task 6: Add Capability Inheritance Contract Guards

**Files:**
- Modify: `internal/agent/worker_contract_test.go`
- Modify: `internal/agent/subagent_jobs_test.go`

- [ ] **Step 1: Add capability mapping tests**

Append to `internal/agent/worker_contract_test.go`:

```go
func TestWorkerCapabilityContractPreservesInheritanceFields(t *testing.T) {
	start := subagentStartOptions{
		ToolNames:      []string{"bash", "read_file", "web_search"},
		DefaultBundles: []string{"core_code", "web"},
		ToolPolicy:     []string{"shell:allow=go test", "shell:deny=rm -rf"},
		WriteScope:     []string{"docs", "internal/agent"},
		SandboxID:      "sandbox:local:abc",
	}

	req := workerRequestFromSubagentStartOptions(start)
	roundTrip := subagentStartOptionsFromWorkerRequest(req, 2)

	if got := strings.Join(roundTrip.ToolNames, ","); got != "bash,read_file,web_search" {
		t.Fatalf("tool names %q", got)
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
```

- [ ] **Step 2: Add behavior guard for missing inherited tools**

Append to `internal/agent/subagent_jobs_test.go`:

```go
func TestWorkerRuntimePreservesRequiredToolValidation(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	_, err := a.startDurableSubagentWithContext(context.Background(), durableSubagentStartRequest{
		Prompt:        "need inactive web",
		AgentType:     "Explore",
		RequiredTools: []string{"web_search"},
	})
	if err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("expected missing required tool validation, got %v", err)
	}
}
```

- [ ] **Step 3: Run capability tests**

Run:

```bash
go test ./internal/agent -run 'TestWorkerCapabilityContractPreservesInheritanceFields|TestWorkerRuntimePreservesRequiredToolValidation' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/worker_contract_test.go internal/agent/subagent_jobs_test.go
git commit -m "test(agent): guard worker capability inheritance"
```

---

### Task 7: Document Phase 3 Local Worker Runtime Boundary

**Files:**
- Modify: `docs/architecture-v2-spec.md`
- Modify: `docs/architecture-v2-spec.en.md`

- [ ] **Step 1: Update Chinese SPEC**

In `docs/architecture-v2-spec.md`, after Phase 3 acceptance criteria, add:

```markdown
实施说明：

- 当前实现新增 `internal/workerruntime` contract package，定义 job request、progress event、result/artifact、capability、review 和 merge contract。
- Durable subagent start/resume/cancel/review/merge 通过 local GoDex worker runtime adapter 执行，默认行为仍是当前本地 durable subagent。
- Durable subagent records、API/model views 和 events 暴露 `worker_id`，并继续暴露 Phase 2 的 `sandbox_id`。
- Phase 3 不实现 remote transport、distributed scheduling 或 Session Graph branch handoff；这些属于后续阶段。
```

- [ ] **Step 2: Update English SPEC**

In `docs/architecture-v2-spec.en.md`, after Phase 3 acceptance criteria, add:

```markdown
Implementation note:

- The current implementation adds an `internal/workerruntime` contract package for job request, progress event, result/artifact, capability, review, and merge contracts.
- Durable subagent start/resume/cancel/review/merge execute through the local GoDex worker runtime adapter, while the default behavior remains the current local durable subagent.
- Durable subagent records, API/model views, and events expose `worker_id` and continue to expose the Phase 2 `sandbox_id`.
- Phase 3 does not implement remote transport, distributed scheduling, or Session Graph branch handoff; those remain later-phase work.
```

- [ ] **Step 3: Commit**

```bash
git add docs/architecture-v2-spec.md docs/architecture-v2-spec.en.md
git commit -m "docs: describe phase 3 worker runtime boundary"
```

---

### Task 8: Final Verification

**Files:**
- No implementation files.

- [ ] **Step 1: Format touched Go files**

Run:

```bash
gofmt -w internal/workerruntime/types.go internal/workerruntime/types_test.go internal/agent/agent.go internal/agent/worker_runtime.go internal/agent/worker_contract.go internal/agent/worker_contract_test.go internal/agent/local_worker_runtime.go internal/agent/local_worker_runtime_test.go internal/agent/subagent_jobs.go internal/agent/subagent_jobs_test.go internal/agent/subagent_tool.go internal/domain/events/events.go
```

Expected: command exits 0.

- [ ] **Step 2: Run targeted tests**

Run:

```bash
go test ./internal/workerruntime ./internal/agent ./internal/domain/events -count=1
```

Expected: pass.

- [ ] **Step 3: Run full repository tests**

Run:

```bash
go test ./...
```

Expected: pass.

- [ ] **Step 4: Check diff hygiene**

Run:

```bash
git diff --check
```

Expected: no output.

- [ ] **Step 5: Review public tool schema compatibility**

Run:

```bash
go test ./internal/agent -run TestSubagentSchemaUsesJSONSchemaEnumArray -count=1
```

Expected: pass.

- [ ] **Step 6: Inspect worker runtime references**

Run:

```bash
rg -n "WorkerRuntime\\(|workerruntime\\.|worker_id|localGoDexWorkerID" internal/agent internal/workerruntime internal/domain/events
```

Expected:

- `startDurableSubagentWithContext`, `ResumeDurableSubagentWithContext`, `CancelDurableSubagentWithContext`, `ReviewDurableSubagent`, and `MergeDurableSubagentWithContext` route through worker runtime methods.
- `worker_id` appears in durable job storage, public job view, model job view, and subagent event payload.
- No remote worker implementation appears.

- [ ] **Step 7: Commit verification-only changes if any**

If gofmt changed files after previous commits, run:

```bash
git add internal/workerruntime internal/agent internal/domain/events docs/architecture-v2-spec.md docs/architecture-v2-spec.en.md
git commit -m "chore: finalize phase 3 worker runtime protocol"
```

Expected: commit only if files changed after Step 1.
