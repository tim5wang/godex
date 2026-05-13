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
	if len(items) == 0 {
		return nil
	}
	return append([]string(nil), items...)
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
