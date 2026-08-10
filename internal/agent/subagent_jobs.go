package agent

import (
	"context"
	"errors"
	"github.com/tim5wang/godex/internal/core/lease"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"sync"
	"time"
)

type subagentJobStatus string

const (
	subagentStatusPending         subagentJobStatus = "pending"
	subagentStatusPendingApproval subagentJobStatus = "pending_approval"
	subagentStatusRunning         subagentJobStatus = "running"
	subagentStatusCompleted       subagentJobStatus = "completed"
	subagentStatusCanceled        subagentJobStatus = "canceled"
	subagentStatusInterrupted     subagentJobStatus = "interrupted"
	subagentStatusTimeout         subagentJobStatus = "timeout"
	subagentStatusError           subagentJobStatus = "error"
)

const subagentProgressLimit = 80
const durableSubagentDefaultMaxTurns = 45

const (
	subagentSummaryFile  = "summary.json"
	subagentMessagesFile = "messages.json"
	subagentProgressFile = "progress.jsonl"
	subagentLegacyDir    = ".legacy"
)

const (
	subagentIsolationSnapshot       = "snapshot"
	subagentIsolationSharedReadOnly = "shared_readonly"
	subagentIsolationSharedApproval = "shared_with_approval"
	subagentIsolationGitWorktree    = "git_worktree"
	subagentGitDirtyOverlay         = "dirty_overlay"
	subagentNonGitDeny              = "deny"
	subagentNonGitCopySnapshot      = "copy_snapshot"
	subagentCleanupActive           = "active"
	subagentCleanupCleaned          = "cleaned"
	subagentCleanupPending          = "pending"
	subagentMergePending            = "pending"
	subagentMergeMerged             = "merged"
	subagentMergeConflict           = "conflict"
	subagentMergeNoChanges          = "no_changes"
)

const subagentDiffPreviewLimit = 48 * 1024

const (
	subagentSessionGraphBranchMetadataKey = "session_graph_branch_id"
	subagentSessionGraphNodeMetadataKey   = "session_graph_node_id"
)

// ErrDurableSubagentNotFound indicates a requested durable subagent is absent
// or does not belong to the requested session.
var ErrDurableSubagentNotFound = errors.New("durable subagent job not found")

type subagentProgressEvent struct {
	Time         time.Time `json:"time"`
	Phase        string    `json:"phase"`
	Message      string    `json:"message,omitempty"`
	ToolID       string    `json:"tool_id,omitempty"`
	ToolName     string    `json:"tool_name,omitempty"`
	Error        string    `json:"error,omitempty"`
	Result       string    `json:"result,omitempty"`
	Iteration    int       `json:"iteration,omitempty"`
	MaxTurns     int       `json:"max_turns,omitempty"`
	Model        string    `json:"model,omitempty"`
	RecoveryHint string    `json:"recovery_hint,omitempty"`
}

type subagentJob struct {
	ID              string                    `json:"id"`
	SessionID       string                    `json:"session_id,omitempty"`
	ParentTurnID    string                    `json:"parent_turn_id,omitempty"`
	Identity        AgentIdentity             `json:"identity,omitempty"`
	AgentType       string                    `json:"agent_type"`
	RoleID          string                    `json:"role_id,omitempty"`
	RoleName        string                    `json:"role_name,omitempty"`
	PackageName     string                    `json:"package_name,omitempty"`
	Sequence        int                       `json:"sequence,omitempty"`
	Objective       string                    `json:"objective,omitempty"`
	DisplayTitle    string                    `json:"display_title,omitempty"`
	RuntimeContext  automation.SessionContext `json:"runtime_context,omitempty"`
	Prompt          string                    `json:"prompt"`
	BasePrompt      string                    `json:"base_prompt"`
	ToolNames       []string                  `json:"tool_names"`
	WriteScope      []string                  `json:"write_scope,omitempty"`
	DefaultBundles  []string                  `json:"default_bundles,omitempty"`
	ToolPolicy      []string                  `json:"tool_policy,omitempty"`
	WorkerID        string                    `json:"worker_id,omitempty"`
	SandboxID       string                    `json:"sandbox_id,omitempty"`
	SourceBranchID  string                    `json:"source_branch_id,omitempty"`
	SourceNodeID    string                    `json:"source_node_id,omitempty"`
	WorkerBranchID  string                    `json:"worker_branch_id,omitempty"`
	WorktreeDir     string                    `json:"worktree_dir,omitempty"`
	BaselineDir     string                    `json:"baseline_dir,omitempty"`
	PreviewJobIDs   []string                  `json:"preview_job_ids,omitempty"`
	Isolation       string                    `json:"isolation,omitempty"`
	WorkspaceOrigin string                    `json:"workspace_origin,omitempty"`
	GitBranch       string                    `json:"git_branch,omitempty"`
	CleanupState    string                    `json:"cleanup_state,omitempty"`
	MergeStatus     string                    `json:"merge_status,omitempty"`
	Status          subagentJobStatus         `json:"status"`
	Result          string                    `json:"result,omitempty"`
	Error           string                    `json:"error,omitempty"`
	Messages        []protocol.Message        `json:"messages,omitempty"`
	Progress        []subagentProgressEvent   `json:"progress,omitempty"`
	MaxTurns        int                       `json:"max_turns"`
	JobTimeoutMS    int                       `json:"job_timeout_ms,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
	StartedAt       time.Time                 `json:"started_at,omitempty"`
	FinishedAt      time.Time                 `json:"finished_at,omitempty"`
	MergedAt        time.Time                 `json:"merged_at,omitempty"`
	LeaseToken      string                    `json:"lease_token,omitempty"`
	LeaseExpiresAt  time.Time                 `json:"lease_expires_at,omitempty"`
}

// IDString returns the durable job id without exposing the internal job type.
func (j *subagentJob) IDString() string {
	if j == nil {
		return ""
	}
	return j.ID
}

type subagentFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes,omitempty"`
	Binary bool   `json:"binary,omitempty"`
}

type subagentReview struct {
	JobID         string               `json:"job_id"`
	WorktreeDir   string               `json:"worktree_dir,omitempty"`
	WriteScope    []string             `json:"write_scope,omitempty"`
	Changes       []subagentFileChange `json:"changes"`
	Diff          string               `json:"diff,omitempty"`
	DiffTruncated bool                 `json:"diff_truncated,omitempty"`
	Conflicts     []string             `json:"conflicts,omitempty"`
}

type subagentMergeResult struct {
	JobID       string               `json:"job_id"`
	Status      string               `json:"status"`
	Applied     []subagentFileChange `json:"applied,omitempty"`
	Conflicts   []string             `json:"conflicts,omitempty"`
	WorktreeDir string               `json:"worktree_dir,omitempty"`
}

type subagentWorkspaceCleanupResult struct {
	JobID   string `json:"job_id"`
	Cleaned bool   `json:"cleaned"`
	Reason  string `json:"reason,omitempty"`
}

type SubagentWorkspaceGCOptions struct {
	DryRun     bool
	MergedOnly bool
	OlderThan  time.Duration
	Now        time.Time
}

type SubagentWorkspaceGCItem struct {
	JobID        string `json:"job_id"`
	Isolation    string `json:"isolation,omitempty"`
	MergeStatus  string `json:"merge_status,omitempty"`
	CleanupState string `json:"cleanup_state,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	Cleaned      bool   `json:"cleaned,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type SubagentWorkspaceGCResult struct {
	DryRun     bool                      `json:"dry_run,omitempty"`
	Candidates int                       `json:"candidates"`
	Cleaned    int                       `json:"cleaned"`
	Bytes      int64                     `json:"bytes,omitempty"`
	Items      []SubagentWorkspaceGCItem `json:"items,omitempty"`
}

// DurableSubagentProgressView is the public progress shape exposed through API
// surfaces. It intentionally avoids leaking the internal persistence type.
type DurableSubagentProgressView struct {
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
}

// DurableSubagentFileChangeView describes one file-level review/merge change.
type DurableSubagentFileChangeView struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes,omitempty"`
	Binary bool   `json:"binary,omitempty"`
}

// DurableSubagentJobView is the stable API view of one durable subagent job.
type DurableSubagentJobView struct {
	JobID             string                        `json:"job_id"`
	SessionID         string                        `json:"session_id,omitempty"`
	ParentTurnID      string                        `json:"parent_turn_id,omitempty"`
	Identity          AgentIdentity                 `json:"identity,omitempty"`
	IdentityID        string                        `json:"identity_id,omitempty"`
	AgentType         string                        `json:"agent_type,omitempty"`
	RoleID            string                        `json:"role_id,omitempty"`
	RoleName          string                        `json:"role_name,omitempty"`
	PackageName       string                        `json:"package_name,omitempty"`
	Sequence          int                           `json:"sequence,omitempty"`
	Objective         string                        `json:"objective,omitempty"`
	DisplayTitle      string                        `json:"display_title,omitempty"`
	Prompt            string                        `json:"prompt,omitempty"`
	Status            string                        `json:"status,omitempty"`
	Result            string                        `json:"result,omitempty"`
	Error             string                        `json:"error,omitempty"`
	CreatedAt         time.Time                     `json:"created_at"`
	UpdatedAt         time.Time                     `json:"updated_at"`
	StartedAt         time.Time                     `json:"started_at,omitempty"`
	FinishedAt        time.Time                     `json:"finished_at,omitempty"`
	MergedAt          time.Time                     `json:"merged_at,omitempty"`
	WriteScope        []string                      `json:"write_scope,omitempty"`
	DefaultBundles    []string                      `json:"default_bundles,omitempty"`
	ToolPolicy        []string                      `json:"tool_policy,omitempty"`
	ToolNames         []string                      `json:"tool_names,omitempty"`
	WorkerID          string                        `json:"worker_id,omitempty"`
	SandboxID         string                        `json:"sandbox_id,omitempty"`
	SourceBranchID    string                        `json:"source_branch_id,omitempty"`
	SourceNodeID      string                        `json:"source_node_id,omitempty"`
	WorkerBranchID    string                        `json:"worker_branch_id,omitempty"`
	WorktreeDir       string                        `json:"worktree_dir,omitempty"`
	Isolation         string                        `json:"isolation,omitempty"`
	WorkspaceOrigin   string                        `json:"workspace_origin,omitempty"`
	GitBranch         string                        `json:"git_branch,omitempty"`
	CleanupState      string                        `json:"cleanup_state,omitempty"`
	MergeStatus       string                        `json:"merge_status,omitempty"`
	JobTimeoutMS      int                           `json:"job_timeout_ms,omitempty"`
	MaxTurns          int                           `json:"max_turns,omitempty"`
	LastPhase         string                        `json:"last_phase,omitempty"`
	LastMessage       string                        `json:"last_message,omitempty"`
	LastToolID        string                        `json:"last_tool_id,omitempty"`
	LastToolName      string                        `json:"last_tool_name,omitempty"`
	ModelRequestCount int                           `json:"model_request_count,omitempty"`
	ToolCallCount     int                           `json:"tool_call_count,omitempty"`
	LastRunnerPhase   string                        `json:"last_runner_phase,omitempty"`
	LastIteration     int                           `json:"last_iteration,omitempty"`
	LastRecoveryHint  string                        `json:"last_recovery_hint,omitempty"`
	Progress          []DurableSubagentProgressView `json:"progress,omitempty"`
}

// DurableSubagentReviewView is the public review shape for one durable job.
type DurableSubagentReviewView struct {
	JobID         string                          `json:"job_id"`
	WorktreeDir   string                          `json:"worktree_dir,omitempty"`
	WriteScope    []string                        `json:"write_scope,omitempty"`
	Changes       []DurableSubagentFileChangeView `json:"changes"`
	Diff          string                          `json:"diff,omitempty"`
	DiffTruncated bool                            `json:"diff_truncated,omitempty"`
	Conflicts     []string                        `json:"conflicts,omitempty"`
}

// DurableSubagentMergeView is the public merge result shape for one durable job.
type DurableSubagentMergeView struct {
	JobID       string                          `json:"job_id"`
	Status      string                          `json:"status"`
	Applied     []DurableSubagentFileChangeView `json:"applied,omitempty"`
	Conflicts   []string                        `json:"conflicts,omitempty"`
	WorktreeDir string                          `json:"worktree_dir,omitempty"`
}

type subagentJobStore struct {
	mu          sync.Mutex
	dir         string
	jobs        map[string]*subagentJob
	cancels     map[string]context.CancelFunc
	targets     map[string]subagentEventTarget
	watchers    map[uint64]chan struct{}
	nextWatcher uint64
	leaseStore  lease.Store
	leaseTTL    time.Duration
}

type subagentStartOptions struct {
	SessionID       string
	ParentTurnID    string
	ParentID        string
	AgentType       string
	RoleID          string
	RoleName        string
	PackageName     string
	Prompt          string
	BasePrompt      string
	ToolNames       []string
	WriteScope      []string
	PreviewJobIDs   []string
	RequiredBundles []string
	RequiredTools   []string
	DefaultBundles  []string
	ToolPolicy      []string
	Capabilities    []string
	WorkerID        string
	SandboxID       string
	ModelHint       string
	BudgetHint      string
	Display         map[string]string
	RuntimeContext  automation.SessionContext
	MaxTurns        int
	MaxConcurrent   int
	JobTimeoutMS    int
}
