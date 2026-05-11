package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
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
}

type subagentStartOptions struct {
	SessionID      string
	ParentTurnID   string
	ParentID       string
	AgentType      string
	RoleID         string
	RoleName       string
	PackageName    string
	Prompt         string
	BasePrompt     string
	ToolNames      []string
	WriteScope     []string
	PreviewJobIDs  []string
	DefaultBundles []string
	ToolPolicy     []string
	Capabilities   []string
	ModelHint      string
	BudgetHint     string
	Display        map[string]string
	RuntimeContext automation.SessionContext
	MaxTurns       int
	MaxConcurrent  int
	JobTimeoutMS   int
}

func subagentJobsDir(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	root := strings.TrimSpace(cfg.StateDir)
	if root == "" {
		root = filepath.Join(strings.TrimSpace(cfg.WorkspaceDir), ".godex")
	}
	if root == "" {
		return ""
	}
	return filepath.Join(root, "subagents")
}

func newSubagentJobStore(dir string) *subagentJobStore {
	store := &subagentJobStore{
		dir:      strings.TrimSpace(dir),
		jobs:     make(map[string]*subagentJob),
		cancels:  make(map[string]context.CancelFunc),
		targets:  make(map[string]subagentEventTarget),
		watchers: make(map[uint64]chan struct{}),
	}
	store.loadAll()
	return store
}

func (s *subagentJobStore) loadAll() {
	if s.dir == "" {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasPrefix(name, ".") {
				continue
			}
			job, err := s.readLayeredJob(name)
			if err != nil {
				continue
			}
			wasActive := job.Status == subagentStatusRunning || job.Status == subagentStatusPending
			s.normalizeLoadedJob(job)
			if wasActive {
				_ = s.saveSummaryLocked(job)
				if len(job.Progress) > 0 {
					_ = s.appendProgressLocked(job, job.Progress[len(job.Progress)-1])
				}
			}
			s.jobs[job.ID] = cloneSubagentJob(job)
			continue
		}
		if filepath.Ext(name) != ".json" {
			continue
		}
		job, err := s.readLegacyJob(name)
		if err != nil {
			continue
		}
		s.normalizeLoadedJob(job)
		if err := s.saveLocked(job); err == nil {
			_ = s.archiveLegacyJob(name)
		}
		s.jobs[job.ID] = cloneSubagentJob(job)
	}
}

func (s *subagentJobStore) readLayeredJob(id string) (*subagentJob, error) {
	var job subagentJob
	if err := readJSONFile(filepath.Join(s.dir, id, subagentSummaryFile), &job); err != nil {
		return nil, err
	}
	if job.ID == "" {
		job.ID = id
	}
	if messages, err := readSubagentMessages(filepath.Join(s.dir, id, subagentMessagesFile)); err == nil {
		job.Messages = messages
	}
	if progress, err := readSubagentProgress(filepath.Join(s.dir, id, subagentProgressFile)); err == nil {
		job.Progress = progress
	}
	return &job, nil
}

func (s *subagentJobStore) readLegacyJob(name string) (*subagentJob, error) {
	var job subagentJob
	if err := readJSONFile(filepath.Join(s.dir, name), &job); err != nil {
		return nil, err
	}
	if job.ID == "" {
		job.ID = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return &job, nil
}

func (s *subagentJobStore) normalizeLoadedJob(job *subagentJob) {
	if job == nil {
		return
	}
	job.AgentType = normalizeSubagentType(job.AgentType)
	if strings.TrimSpace(job.BasePrompt) == "" {
		job.BasePrompt = durableSubagentPromptForRole(job.AgentType, job.WriteScope)
	}
	if len(job.ToolNames) == 0 {
		job.ToolNames = subagentToolNames(job.AgentType)
	}
	job.Identity = NormalizeAgentIdentity(job.Identity, time.Now().UTC(), job.SessionID, "subagent", subagentIdentityName(job), capabilitySummaryForTools(job.ToolNames, job.WriteScope))
	if job.Status == subagentStatusRunning || job.Status == subagentStatusPending || job.Status == subagentStatusPendingApproval {
		now := time.Now().UTC()
		job.Status = subagentStatusInterrupted
		job.Error = "subagent was active when the runtime stopped"
		job.UpdatedAt = now
		job.Progress = appendBoundedSubagentProgress(job.Progress, subagentProgressEvent{
			Time:    now,
			Phase:   string(subagentStatusInterrupted),
			Message: subagentFinishMessage(subagentStatusInterrupted),
			Error:   job.Error,
		})
	}
}

func (s *subagentJobStore) archiveLegacyJob(name string) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil
	}
	legacyDir := filepath.Join(s.dir, subagentLegacyDir)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		return err
	}
	src := filepath.Join(s.dir, name)
	dst := filepath.Join(legacyDir, name)
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}

func readSubagentMessages(path string) ([]protocol.Message, error) {
	var messages []protocol.Message
	if err := readJSONFile(path, &messages); err != nil {
		return nil, err
	}
	return protocol.CloneMessages(messages), nil
}

func readSubagentProgress(path string) ([]subagentProgressEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var progress []subagentProgressEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item subagentProgressEvent
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		progress = appendBoundedSubagentProgress(progress, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return progress, nil
}

func (s *subagentJobStore) Start(agentType, prompt string, toolNames []string, writeScope []string, maxTurns int) (*subagentJob, error) {
	return s.StartWithOptions(subagentStartOptions{
		AgentType:  agentType,
		Prompt:     prompt,
		ToolNames:  toolNames,
		WriteScope: writeScope,
		MaxTurns:   maxTurns,
	})
}

func (s *subagentJobStore) StartWithOptions(opts subagentStartOptions) (*subagentJob, error) {
	now := time.Now().UTC()
	normalizedScope := normalizeWriteScope(opts.WriteScope)
	agentType := normalizeSubagentType(opts.AgentType)
	basePrompt := strings.TrimSpace(opts.BasePrompt)
	if basePrompt == "" {
		basePrompt = durableSubagentPromptForRole(agentType, normalizedScope)
	}
	job := &subagentJob{
		ID:              newSubagentJobID(now),
		SessionID:       strings.TrimSpace(opts.SessionID),
		ParentTurnID:    strings.TrimSpace(opts.ParentTurnID),
		AgentType:       agentType,
		RoleID:          strings.TrimSpace(opts.RoleID),
		RoleName:        strings.TrimSpace(opts.RoleName),
		PackageName:     strings.TrimSpace(opts.PackageName),
		Objective:       subagentObjectiveFromPrompt(opts.Prompt),
		RuntimeContext:  opts.RuntimeContext.Clone(),
		Prompt:          strings.TrimSpace(opts.Prompt),
		BasePrompt:      basePrompt,
		ToolNames:       append([]string{}, opts.ToolNames...),
		WriteScope:      normalizedScope,
		PreviewJobIDs:   normalizeWorkflowStrings(opts.PreviewJobIDs),
		DefaultBundles:  append([]string{}, opts.DefaultBundles...),
		ToolPolicy:      normalizeWorkflowStrings(opts.ToolPolicy),
		Isolation:       subagentIsolationSnapshot,
		WorkspaceOrigin: "snapshot",
		CleanupState:    subagentCleanupPending,
		MergeStatus:     subagentMergePending,
		Status:          subagentStatusRunning,
		Messages:        []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, strings.TrimSpace(opts.Prompt))},
		MaxTurns:        opts.MaxTurns,
		JobTimeoutMS:    opts.JobTimeoutMS,
		CreatedAt:       now,
		UpdatedAt:       now,
		StartedAt:       now,
	}
	job.Identity = NewAgentIdentity(now, job.SessionID, "subagent", subagentIdentityName(job), firstNonEmpty(job.RoleID, job.AgentType), opts.ParentID, "durable_subagent", opts.Capabilities)
	job.Identity.ModelHint = strings.TrimSpace(opts.ModelHint)
	job.Identity.BudgetHint = strings.TrimSpace(opts.BudgetHint)
	job.Identity.Display = cloneStringMap(opts.Display)
	if job.Prompt == "" {
		return nil, fmt.Errorf("missing prompt")
	}
	if len(job.ToolNames) == 0 {
		job.ToolNames = subagentToolNames(job.AgentType)
	}
	if job.MaxTurns <= 0 {
		job.MaxTurns = durableSubagentDefaultMaxTurns
	}
	if strings.TrimSpace(job.Identity.BudgetHint) == "" && job.MaxTurns > 0 {
		job.Identity.BudgetHint = fmt.Sprintf("max_turns:%d", job.MaxTurns)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	job.Sequence = s.nextSequenceLocked(job.SessionID, job.ParentTurnID)
	job.DisplayTitle = subagentDisplayTitle(job)
	if opts.MaxConcurrent > 0 && s.runningCountLocked() >= opts.MaxConcurrent {
		job.Status = subagentStatusPending
		job.StartedAt = time.Time{}
		job.Progress = []subagentProgressEvent{{
			Time:    now,
			Phase:   "pending",
			Message: "Subagent job queued.",
		}}
	} else {
		job.StartedAt = now
		job.Progress = []subagentProgressEvent{{
			Time:    now,
			Phase:   "started",
			Message: "Subagent job started.",
		}}
	}
	s.jobs[job.ID] = cloneSubagentJob(job)
	if err := s.saveLocked(job); err != nil {
		delete(s.jobs, job.ID)
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) RegisterTarget(id string, target subagentEventTarget) {
	if s == nil || strings.TrimSpace(id) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.targets == nil {
		s.targets = make(map[string]subagentEventTarget)
	}
	s.targets[strings.TrimSpace(id)] = target
}

func (s *subagentJobStore) Watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	if s == nil {
		close(ch)
		return ch, func() {}
	}
	s.mu.Lock()
	if s.watchers == nil {
		s.watchers = make(map[uint64]chan struct{})
	}
	s.nextWatcher++
	id := s.nextWatcher
	s.watchers[id] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if registered := s.watchers[id]; registered != nil {
			delete(s.watchers, id)
			close(registered)
		}
		s.mu.Unlock()
	}
}

func (s *subagentJobStore) notifyWatchersLocked() {
	for _, ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *subagentJobStore) StartNextPending(maxConcurrent int) (*subagentJob, subagentEventTarget, error) {
	if s == nil || maxConcurrent <= 0 {
		return nil, subagentEventTarget{}, nil
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningCountLocked() >= maxConcurrent {
		return nil, subagentEventTarget{}, nil
	}
	var next *subagentJob
	for _, job := range s.jobs {
		if job == nil || job.Status != subagentStatusPending {
			continue
		}
		if next == nil || job.CreatedAt.Before(next.CreatedAt) || (job.CreatedAt.Equal(next.CreatedAt) && job.ID < next.ID) {
			next = job
		}
	}
	if next == nil {
		return nil, subagentEventTarget{}, nil
	}
	next.Status = subagentStatusRunning
	next.Error = ""
	next.UpdatedAt = now
	next.StartedAt = now
	progress := subagentProgressEvent{
		Time:    now,
		Phase:   "started",
		Message: "Subagent job started.",
	}
	next.Progress = appendBoundedSubagentProgress(next.Progress, progress)
	if err := s.appendProgressLocked(next, progress); err != nil {
		return nil, subagentEventTarget{}, err
	}
	if err := s.saveSummaryLocked(next); err != nil {
		return nil, subagentEventTarget{}, err
	}
	s.notifyWatchersLocked()
	target := s.targets[next.ID]
	return cloneSubagentJob(next), target, nil
}

func (s *subagentJobStore) runningCountLocked() int {
	count := 0
	for _, job := range s.jobs {
		if job != nil && job.Status == subagentStatusRunning {
			count++
		}
	}
	return count
}

func (s *subagentJobStore) nextSequenceLocked(sessionID, parentTurnID string) int {
	next := 1
	sessionID = strings.TrimSpace(sessionID)
	parentTurnID = strings.TrimSpace(parentTurnID)
	for _, job := range s.jobs {
		if job == nil {
			continue
		}
		if strings.TrimSpace(job.SessionID) != sessionID || strings.TrimSpace(job.ParentTurnID) != parentTurnID {
			continue
		}
		if job.Sequence >= next {
			next = job.Sequence + 1
		}
	}
	return next
}

func (s *subagentJobStore) SetWorkspace(id, worktreeDir, baselineDir, isolation, gitBranch, workspaceOrigin string) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	job.WorktreeDir = strings.TrimSpace(worktreeDir)
	job.BaselineDir = strings.TrimSpace(baselineDir)
	job.Isolation = strings.TrimSpace(isolation)
	if job.Isolation == "" {
		job.Isolation = subagentIsolationSnapshot
	}
	job.GitBranch = strings.TrimSpace(gitBranch)
	job.WorkspaceOrigin = strings.TrimSpace(workspaceOrigin)
	if job.WorkspaceOrigin == "" {
		job.WorkspaceOrigin = job.Isolation
	}
	if job.CleanupState == "" || job.CleanupState == subagentCleanupPending {
		job.CleanupState = subagentCleanupActive
	}
	if job.MergeStatus == "" {
		job.MergeStatus = subagentMergePending
	}
	job.UpdatedAt = now
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) SetMergeStatus(id, status string, progress subagentProgressEvent) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	job.MergeStatus = strings.TrimSpace(status)
	job.UpdatedAt = now
	if job.MergeStatus == subagentMergeMerged || job.MergeStatus == subagentMergeNoChanges {
		job.MergedAt = now
	}
	if progress.Time.IsZero() {
		progress.Time = now
	}
	job.Progress = appendBoundedSubagentProgress(job.Progress, progress)
	if err := s.appendProgressLocked(job, progress); err != nil {
		return nil, err
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) SetWorkspaceCleaned(id string) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	job.WorktreeDir = ""
	job.BaselineDir = ""
	job.GitBranch = ""
	job.CleanupState = subagentCleanupCleaned
	job.UpdatedAt = now
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) Resume(id string) (*subagentJob, error) {
	return s.ResumeWithLimit(id, 0)
}

func (s *subagentJobStore) ResumeWithLimit(id string, maxConcurrent int) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	if _, ok := s.cancels[job.ID]; ok {
		return nil, fmt.Errorf("subagent job %s is already running", job.ID)
	}
	switch job.Status {
	case subagentStatusCompleted, subagentStatusRunning:
		return nil, fmt.Errorf("subagent job %s is %s", job.ID, job.Status)
	}
	if maxConcurrent > 0 && s.runningCountLocked() >= maxConcurrent {
		job.Status = subagentStatusPending
	} else {
		job.Status = subagentStatusRunning
	}
	job.AgentType = normalizeSubagentType(job.AgentType)
	if strings.TrimSpace(job.BasePrompt) == "" {
		job.BasePrompt = durableSubagentPromptForRole(job.AgentType, job.WriteScope)
	}
	if len(job.ToolNames) == 0 {
		job.ToolNames = subagentToolNames(job.AgentType)
	}
	job.Error = ""
	job.Result = ""
	job.UpdatedAt = now
	if job.Status == subagentStatusRunning {
		job.StartedAt = now
	} else {
		job.StartedAt = time.Time{}
	}
	job.FinishedAt = time.Time{}
	progress := subagentProgressEvent{
		Time:    now,
		Phase:   string(job.Status),
		Message: subagentResumeMessage(job.Status),
	}
	job.Progress = appendBoundedSubagentProgress(job.Progress, progress)
	if len(job.Messages) == 0 {
		job.Messages = []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, job.Prompt)}
	}
	if err := s.appendProgressLocked(job, progress); err != nil {
		return nil, err
	}
	if err := s.saveMessagesLocked(job); err != nil {
		return nil, err
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) Get(id string) (*subagentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) List() []*subagentJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*subagentJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		items = append(items, cloneSubagentJob(job))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

func (s *subagentJobStore) Cancel(id string) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	cancel := s.cancels[job.ID]
	if cancel != nil {
		cancel()
	}
	if job.Status == subagentStatusRunning || job.Status == subagentStatusPending {
		job.Status = subagentStatusCanceled
		job.Error = context.Canceled.Error()
		job.UpdatedAt = now
		job.FinishedAt = now
		progress := subagentProgressEvent{
			Time:    now,
			Phase:   "canceled",
			Message: "Subagent job canceled.",
			Error:   job.Error,
		}
		job.Progress = appendBoundedSubagentProgress(job.Progress, progress)
		if err := s.appendProgressLocked(job, progress); err == nil {
			if err := s.saveSummaryLocked(job); err == nil {
				s.notifyWatchersLocked()
			}
		}
	}
	delete(s.targets, job.ID)
	cloned := cloneSubagentJob(job)
	s.mu.Unlock()
	return cloned, nil
}

func (s *subagentJobStore) SetActive(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels[strings.TrimSpace(id)] = cancel
}

func (s *subagentJobStore) ClearActive(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, strings.TrimSpace(id))
}

func (s *subagentJobStore) UpdateMessages(id string, messages []protocol.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return fmt.Errorf("subagent job not found: %s", id)
	}
	job.Messages = protocol.CloneMessages(messages)
	job.UpdatedAt = time.Now().UTC()
	if err := s.saveMessagesLocked(job); err != nil {
		return err
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return err
	}
	s.notifyWatchersLocked()
	return nil
}

func (s *subagentJobStore) AppendProgress(id string, progress subagentProgressEvent) (*subagentJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	progress.Phase = strings.TrimSpace(progress.Phase)
	progress.Message = strings.TrimSpace(progress.Message)
	progress.ToolID = strings.TrimSpace(progress.ToolID)
	progress.ToolName = strings.TrimSpace(progress.ToolName)
	progress.Error = strings.TrimSpace(progress.Error)
	progress.Result = strings.TrimSpace(progress.Result)
	progress.Model = strings.TrimSpace(progress.Model)
	progress.RecoveryHint = strings.TrimSpace(progress.RecoveryHint)
	job.Progress = appendBoundedSubagentProgress(job.Progress, progress)
	job.UpdatedAt = progress.Time
	if err := s.appendProgressLocked(job, progress); err != nil {
		return nil, err
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) Finish(id string, status subagentJobStatus, result, errorText string) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	if job.Status == subagentStatusCanceled && status != subagentStatusCanceled {
		return cloneSubagentJob(job), nil
	}
	job.Status = status
	job.Result = strings.TrimSpace(result)
	job.Error = strings.TrimSpace(errorText)
	job.UpdatedAt = now
	job.FinishedAt = now
	progress := subagentProgressEvent{
		Time:    now,
		Phase:   string(status),
		Message: subagentFinishMessage(status),
		Error:   job.Error,
		Result:  job.Result,
	}
	job.Progress = appendBoundedSubagentProgress(job.Progress, progress)
	if err := s.appendProgressLocked(job, progress); err != nil {
		return nil, err
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	delete(s.targets, job.ID)
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) SetPendingApproval(id, errorText string) (*subagentJob, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[strings.TrimSpace(id)]
	if job == nil {
		return nil, fmt.Errorf("subagent job not found: %s", id)
	}
	job.Status = subagentStatusPendingApproval
	job.Error = strings.TrimSpace(errorText)
	job.UpdatedAt = now
	job.FinishedAt = time.Time{}
	delete(s.cancels, job.ID)
	progress := subagentProgressEvent{
		Time:    now,
		Phase:   string(subagentStatusPendingApproval),
		Message: "Subagent job is waiting for tool approval.",
		Error:   job.Error,
	}
	job.Progress = appendBoundedSubagentProgress(job.Progress, progress)
	if err := s.appendProgressLocked(job, progress); err != nil {
		return nil, err
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return nil, err
	}
	s.notifyWatchersLocked()
	return cloneSubagentJob(job), nil
}

func (s *subagentJobStore) saveLocked(job *subagentJob) error {
	if s == nil || s.dir == "" || job == nil {
		return nil
	}
	if err := s.saveSummaryLocked(job); err != nil {
		return err
	}
	if err := s.saveMessagesLocked(job); err != nil {
		return err
	}
	return s.writeProgressLogLocked(job)
}

func (s *subagentJobStore) saveSummaryLocked(job *subagentJob) error {
	if s == nil || s.dir == "" || job == nil {
		return nil
	}
	dir := s.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	var summary map[string]interface{}
	if err := json.Unmarshal(data, &summary); err != nil {
		return err
	}
	delete(summary, "messages")
	delete(summary, "progress")
	summary["message_count"] = len(job.Messages)
	summary["progress_count"] = len(job.Progress)
	return fsutil.WriteJSONAtomic(filepath.Join(dir, subagentSummaryFile), summary, 0644)
}

func (s *subagentJobStore) saveMessagesLocked(job *subagentJob) error {
	if s == nil || s.dir == "" || job == nil {
		return nil
	}
	dir := s.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, subagentMessagesFile), protocol.CloneMessages(job.Messages), 0644)
}

func (s *subagentJobStore) writeProgressLogLocked(job *subagentJob) error {
	if s == nil || s.dir == "" || job == nil {
		return nil
	}
	dir := s.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, subagentProgressFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	for _, progress := range job.Progress {
		data, err := json.Marshal(progress)
		if err != nil {
			_ = file.Close()
			return err
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

func (s *subagentJobStore) appendProgressLocked(job *subagentJob, progress subagentProgressEvent) error {
	if s == nil || s.dir == "" || job == nil {
		return nil
	}
	dir := s.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, subagentProgressFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	data, err := json.Marshal(progress)
	if err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (s *subagentJobStore) jobDir(id string) string {
	return filepath.Join(s.dir, strings.TrimSpace(id))
}

type subagentEventTarget struct {
	sessionID string
	turnID    string
	sink      events.Sink
}

type subagentEventContextKey struct{}

func withSubagentEventTarget(ctx context.Context, target subagentEventTarget) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, subagentEventContextKey{}, target)
}

// WithSubagentEvents attaches a durable subagent event target to ctx for
// callers outside the agent loop, such as package command dispatchers.
func WithSubagentEvents(ctx context.Context, sessionID, turnID string, sink events.Sink) context.Context {
	return withSubagentEventTarget(ctx, subagentEventTarget{
		sessionID: strings.TrimSpace(sessionID),
		turnID:    strings.TrimSpace(turnID),
		sink:      sink,
	})
}

func subagentEventTargetFromContext(ctx context.Context) subagentEventTarget {
	if ctx == nil {
		return subagentEventTarget{}
	}
	target, _ := ctx.Value(subagentEventContextKey{}).(subagentEventTarget)
	return target
}

type durableSubagentStartRequest struct {
	Prompt          string
	AgentType       string
	WriteScope      []string
	RequiredBundles []string
	RequiredTools   []string
	PreviewJobIDs   []string
	MaxTurns        int
	JobTimeoutMS    int
}

func (a *Agent) StartDurableSubagent(prompt, agentType string, writeScope []string) (*subagentJob, error) {
	return a.StartDurableSubagentWithContext(context.Background(), prompt, agentType, writeScope)
}

func (a *Agent) StartDurableSubagentWithContext(ctx context.Context, prompt, agentType string, writeScope []string) (*subagentJob, error) {
	return a.startDurableSubagentWithContext(ctx, durableSubagentStartRequest{
		Prompt:     prompt,
		AgentType:  agentType,
		WriteScope: writeScope,
	})
}

func (a *Agent) startDurableSubagentWithContext(ctx context.Context, req durableSubagentStartRequest) (*subagentJob, error) {
	prompt := a.rewriteSubagentPromptWorkspacePaths(req.Prompt)
	role, hasRole := a.resolveSubagentRole(req.AgentType)
	target := subagentEventTargetFromContext(ctx)
	runtimeCtx := tools.SessionContextFromContext(ctx)
	if strings.TrimSpace(runtimeCtx.SessionID) == "" {
		runtimeCtx.SessionID = target.sessionID
	}
	if strings.TrimSpace(runtimeCtx.Source) == "" && strings.HasPrefix(strings.TrimSpace(runtimeCtx.SessionID), "web-") {
		runtimeCtx.Source = string(message.SourceWeb)
	}
	requiredBundles := subagentRequiredBundles(prompt, req.RequiredBundles)
	start := subagentStartOptions{
		SessionID:      target.sessionID,
		ParentTurnID:   target.turnID,
		ParentID:       target.turnID,
		AgentType:      req.AgentType,
		Prompt:         prompt,
		ToolNames:      subagentToolNamesForRole(req.AgentType, nil),
		WriteScope:     req.WriteScope,
		PreviewJobIDs:  req.PreviewJobIDs,
		RuntimeContext: runtimeCtx,
		MaxTurns:       a.normalizeSubagentMaxTurns(req.MaxTurns),
		MaxConcurrent:  a.subagentMaxConcurrentJobs(),
		JobTimeoutMS:   a.normalizeSubagentJobTimeoutMS(req.JobTimeoutMS),
	}
	if hasRole {
		start.AgentType = role.ID
		start.RoleID = role.ID
		start.RoleName = role.Name
		start.PackageName = role.PackageName
		start.BasePrompt = subagentBasePromptForRole(role, req.WriteScope)
		start.ToolNames = subagentToolNamesForRole(role.ID, &role)
		start.DefaultBundles = append([]string{}, role.DefaultBundles...)
		start.ToolPolicy = append([]string{}, role.ToolPolicy...)
		start.Capabilities = roleCapabilitySummary(role, start.ToolNames, req.WriteScope)
		start.ModelHint = role.ModelHint
		start.BudgetHint = role.BudgetHint
		start.Display = roleDisplayMap(role.Display)
	}
	start.ToolNames = appendRequiredSubagentTools(start.ToolNames, requiredBundles, req.RequiredTools)
	start.ToolNames = narrowSubagentWriteTools(start.ToolNames, req.WriteScope)
	if hasRole {
		start.Capabilities = roleCapabilitySummary(role, start.ToolNames, req.WriteScope)
	}
	if err := a.validateSubagentRequiredCapabilities(requiredBundles, req.RequiredTools); err != nil {
		return nil, err
	}
	if err := a.validateSubagentToolInheritance(start.ToolNames); err != nil {
		return nil, err
	}
	if len(start.Capabilities) == 0 {
		start.Capabilities = capabilitySummaryForTools(start.ToolNames, req.WriteScope)
	}
	job, err := a.subagentJobs.StartWithOptions(start)
	if err != nil {
		return nil, err
	}
	a.subagentJobs.RegisterTarget(job.ID, target)
	target.emitIdentity(job)
	if job.Status == subagentStatusPending {
		target.emit(job, "pending", "Subagent job queued.", "", "", "", "")
		return job, nil
	}
	target.emit(job, "started", "Subagent job started.", "", "", "", "")
	jobID := job.ID
	job, err = a.prepareSubagentWorkspace(job)
	if err != nil {
		finished, _ := a.subagentJobs.Finish(jobID, subagentStatusError, "", err.Error())
		target.emit(finished, string(subagentStatusError), "Subagent workspace preparation failed.", "", "", err.Error(), "")
		a.startPendingSubagents(target.sink)
		return nil, err
	}
	target.emit(job, "worktree_prepared", "Subagent isolated workspace prepared.", "", "", "", "")
	a.runSubagentJobAsync(job.ID, target)
	return job, nil
}

func (a *Agent) rewriteSubagentPromptWorkspacePaths(prompt string) string {
	if a == nil || a.cfg == nil {
		return prompt
	}
	workspace := strings.TrimSpace(a.cfg.WorkspaceDir)
	if workspace == "" {
		return prompt
	}
	clean := filepath.Clean(workspace)
	slashClean := filepath.ToSlash(clean)
	out := prompt
	if clean != "." && clean != string(os.PathSeparator) {
		out = strings.ReplaceAll(out, clean+string(os.PathSeparator), "")
	}
	if slashClean != "." && slashClean != "/" {
		out = strings.ReplaceAll(out, slashClean+"/", "")
	}
	return out
}

func (a *Agent) subagentBatchLimit() int {
	if a != nil && a.cfg != nil && a.cfg.Tools.Subagent.MaxBatchSize > 0 {
		return a.cfg.Tools.Subagent.MaxBatchSize
	}
	return 8
}

func (a *Agent) subagentMaxConcurrentJobs() int {
	if a != nil && a.cfg != nil && a.cfg.Tools.Subagent.MaxConcurrentJobs > 0 {
		return a.cfg.Tools.Subagent.MaxConcurrentJobs
	}
	return 4
}

func (a *Agent) subagentDefaultMaxTurns() int {
	if a != nil && a.cfg != nil && a.cfg.Tools.Subagent.DefaultMaxTurns > 0 {
		return a.cfg.Tools.Subagent.DefaultMaxTurns
	}
	return durableSubagentDefaultMaxTurns
}

func (a *Agent) normalizeSubagentMaxTurns(value int) int {
	defaultValue := a.subagentDefaultMaxTurns()
	if value > defaultValue {
		return value
	}
	return defaultValue
}

func (a *Agent) subagentMaxJobTimeoutMS() int {
	if a != nil && a.cfg != nil {
		return a.cfg.Tools.Subagent.MaxJobTimeoutMs
	}
	return 7200000
}

func (a *Agent) normalizeSubagentJobTimeoutMS(value int) int {
	if value <= 0 {
		return 0
	}
	maxTimeout := a.subagentMaxJobTimeoutMS()
	if maxTimeout > 0 && value > maxTimeout {
		return maxTimeout
	}
	return value
}

func (a *Agent) startPendingSubagents(sink events.Sink) {
	if a == nil || a.subagentJobs == nil {
		return
	}
	limit := a.subagentMaxConcurrentJobs()
	for {
		job, target, err := a.subagentJobs.StartNextPending(limit)
		if err != nil || job == nil {
			return
		}
		if target.sink == nil {
			target = subagentEventTarget{
				sessionID: job.SessionID,
				turnID:    job.ParentTurnID,
				sink:      sink,
			}
		}
		target.emit(job, "started", "Subagent job started.", "", "", "", "")
		prepared, err := a.prepareSubagentWorkspace(job)
		if err != nil {
			finished, _ := a.subagentJobs.Finish(job.ID, subagentStatusError, "", err.Error())
			target.emit(finished, string(subagentStatusError), "Subagent workspace preparation failed.", "", "", err.Error(), "")
			continue
		}
		target.emit(prepared, "worktree_prepared", "Subagent isolated workspace prepared.", "", "", "", "")
		a.runSubagentJobAsync(prepared.ID, target)
	}
}

func (a *Agent) ResumeDurableSubagent(id string) (*subagentJob, error) {
	return a.ResumeDurableSubagentWithContext(context.Background(), id)
}

func (a *Agent) ResumeDurableSubagentWithContext(ctx context.Context, id string) (*subagentJob, error) {
	job, err := a.subagentJobs.ResumeWithLimit(id, a.subagentMaxConcurrentJobs())
	if err != nil {
		return nil, err
	}
	target := subagentEventTargetFromContext(ctx)
	a.subagentJobs.RegisterTarget(job.ID, target)
	if job.Status == subagentStatusPending {
		target.emit(job, "pending", "Subagent job queued for resume.", "", "", "", "")
		return job, nil
	}
	target.emit(job, "resumed", "Subagent job resumed.", "", "", "", "")
	if err := a.ensureSubagentWorkspace(job); err != nil {
		finished, _ := a.subagentJobs.Finish(job.ID, subagentStatusError, "", err.Error())
		target.emit(finished, string(subagentStatusError), "Subagent isolated workspace is unavailable.", "", "", err.Error(), "")
		a.startPendingSubagents(target.sink)
		return nil, err
	}
	a.runSubagentJobAsync(job.ID, target)
	return job, nil
}

// ListDurableSubagents returns durable subagent jobs scoped to one session.
func (a *Agent) ListDurableSubagents(sessionID string) []DurableSubagentJobView {
	if a == nil || a.subagentJobs == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	jobs := a.subagentJobs.List()
	out := make([]DurableSubagentJobView, 0, len(jobs))
	for _, job := range jobs {
		if !subagentJobMatchesSession(job, sessionID) {
			continue
		}
		out = append(out, durableSubagentJobView(job))
	}
	return out
}

// GetDurableSubagent returns one durable subagent job scoped to a session.
func (a *Agent) GetDurableSubagent(sessionID, id string) (DurableSubagentJobView, error) {
	job, err := a.getDurableSubagentForSession(sessionID, id)
	if err != nil {
		return DurableSubagentJobView{}, err
	}
	return durableSubagentJobView(job), nil
}

// CancelDurableSubagentWithContext cancels a durable subagent and emits a
// session timeline event when a target is present on ctx.
func (a *Agent) CancelDurableSubagentWithContext(ctx context.Context, sessionID, id string) (DurableSubagentJobView, error) {
	if _, err := a.getDurableSubagentForSession(sessionID, id); err != nil {
		return DurableSubagentJobView{}, err
	}
	job, err := a.subagentJobs.Cancel(id)
	if err != nil {
		return DurableSubagentJobView{}, err
	}
	subagentEventTargetFromContext(ctx).emit(job, "canceled", "Subagent job canceled.", "", "", job.Error, "")
	return durableSubagentJobView(job), nil
}

// ResumeDurableSubagentViewWithContext resumes a durable subagent and returns
// its public API view.
func (a *Agent) ResumeDurableSubagentViewWithContext(ctx context.Context, sessionID, id string) (DurableSubagentJobView, error) {
	if _, err := a.getDurableSubagentForSession(sessionID, id); err != nil {
		return DurableSubagentJobView{}, err
	}
	job, err := a.ResumeDurableSubagentWithContext(ctx, id)
	if err != nil {
		return DurableSubagentJobView{}, err
	}
	return durableSubagentJobView(job), nil
}

func (a *Agent) runSubagentJobAsync(id string, target subagentEventTarget) {
	ctx, cancel := context.WithCancel(context.Background())
	a.subagentJobs.SetActive(id, cancel)
	go func() {
		defer func() {
			a.subagentJobs.ClearActive(id)
			a.startPendingSubagents(target.sink)
		}()
		a.runSubagentJob(ctx, id, target)
	}()
}

func (a *Agent) runSubagentJob(ctx context.Context, id string, target subagentEventTarget) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return
	}
	if strings.TrimSpace(job.SessionID) != "" {
		ctx = tools.WithSessionID(ctx, job.SessionID)
	}
	if strings.TrimSpace(job.RuntimeContext.SessionID) != "" || strings.TrimSpace(job.RuntimeContext.Source) != "" {
		ctx = tools.WithSessionContext(ctx, job.RuntimeContext)
	}
	runCtx := ctx
	var timeoutCancel context.CancelFunc
	if job.JobTimeoutMS > 0 {
		runCtx, timeoutCancel = context.WithTimeout(ctx, time.Duration(job.JobTimeoutMS)*time.Millisecond)
		defer timeoutCancel()
	}
	messages := protocol.CloneMessages(job.Messages)
	if len(messages) == 0 {
		messages = []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, job.Prompt)}
	}
	prompts := conversation.PromptLayers{Base: strings.TrimSpace(job.BasePrompt)}
	result, err := conversation.Runner{
		Caller: a.client,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return conversation.NewRequest(a.cfg.Model, a.cfg.MaxTokens, a.cfg.ReasoningEffort, prompts.Build(), messages, a.toolHandler.ActiveSchemas(job.ToolNames...)), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
			_ = a.subagentJobs.UpdateMessages(id, messages)
			a.recordSubagentProgress(id, target, subagentProgressEvent{
				Phase:   "assistant_message",
				Message: previewSubagentText(protocol.MessageText(msg)),
			})
		},
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
			_ = a.subagentJobs.UpdateMessages(id, messages)
			a.recordSubagentProgress(id, target, subagentProgressEvent{
				Phase:   "tool_results",
				Message: "Subagent tool results checkpointed.",
			})
		},
		AppendRuntimeFeedback: func(msg protocol.Message) {
			messages = append(messages, msg)
			_ = a.subagentJobs.UpdateMessages(id, messages)
			a.recordSubagentProgress(id, target, subagentProgressEvent{
				Phase:   "loop_guard_recovery",
				Message: previewSubagentText(protocol.MessageText(msg)),
			})
		},
		ExecuteTool: func(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
			return a.executeSubagentToolForJob(ctx, name, input, job)
		},
		ToolResultFilter: a.filterModelToolResult,
		OnToolStarted: func(block protocol.Block) {
			a.recordSubagentProgress(id, target, subagentProgressEvent{
				Phase:    "tool_started",
				Message:  "Subagent started tool " + strings.TrimSpace(block.Name) + ".",
				ToolID:   block.ID,
				ToolName: block.Name,
			})
		},
		OnToolFinished: func(tool conversation.ExecutedTool) {
			progress := subagentProgressEvent{
				Phase:    "tool_finished",
				Message:  "Subagent finished tool " + strings.TrimSpace(tool.Name) + ".",
				ToolID:   tool.ID,
				ToolName: tool.Name,
				Error:    tool.Error,
			}
			if tool.Error != "" {
				progress.Message = "Subagent tool " + strings.TrimSpace(tool.Name) + " failed."
			}
			a.recordSubagentProgress(id, target, progress)
		},
		OnPhase: func(event conversation.PhaseEvent) {
			if isRunnerProgressPhase(event.Phase) {
				progress := subagentProgressEvent{
					Phase:        event.Phase,
					Message:      event.Message,
					ToolID:       event.ToolID,
					ToolName:     event.ToolName,
					Iteration:    event.Iteration,
					MaxTurns:     job.MaxTurns,
					Model:        event.Model,
					RecoveryHint: event.RecoveryHint,
				}
				canceling := errors.Is(runCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
				if canceling && event.Phase == conversation.PhaseInterrupted {
					// Cancel already persists a terminal progress entry; avoid racing
					// test/workspace cleanup with a second post-cancel progress write.
				} else if event.Phase == conversation.PhaseRecoveryAttempt || event.Phase == conversation.PhaseError || event.Phase == conversation.PhaseInterrupted {
					a.recordSubagentProgress(id, target, progress)
				} else {
					a.appendSubagentProgress(id, progress)
				}
			}
			target.emitRunnerPhase(job, event)
		},
		MaxTurns: job.MaxTurns,
	}.Run(runCtx)
	if err != nil {
		status := subagentStatusError
		errorText := err.Error()
		var pendingPermission tools.ErrPermissionPending
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			status = subagentStatusTimeout
			errorText = fmt.Sprintf("subagent job timed out after %dms", job.JobTimeoutMS)
		} else if errors.Is(err, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			status = subagentStatusCanceled
		} else if errors.As(err, &pendingPermission) {
			pending, _ := a.subagentJobs.SetPendingApproval(id, errorText)
			target.emit(pending, string(subagentStatusPendingApproval), subagentFinishMessage(subagentStatusPendingApproval), "", "", errorText, "")
			return
		} else if errors.Is(err, conversation.ErrMaxTurnsReached) {
			errorText = a.subagentMaxTurnsErrorText(id, job, result)
		}
		finished, _ := a.subagentJobs.Finish(id, status, "", errorText)
		target.emit(finished, string(status), subagentFinishMessage(status), "", "", errorText, "")
		return
	}
	output := ""
	if result != nil {
		output = result.LastAssistantText
	}
	if strings.TrimSpace(output) == "" {
		output = "(subagent completed with no text output)"
	}
	finished, _ := a.subagentJobs.Finish(id, subagentStatusCompleted, output, "")
	target.emit(finished, string(subagentStatusCompleted), subagentFinishMessage(subagentStatusCompleted), "", "", "", output)
}

func (a *Agent) subagentMaxTurnsErrorText(id string, job *subagentJob, result *conversation.Result) string {
	maxTurns := 0
	role := ""
	agentType := ""
	latestPhase := ""
	latestTool := ""
	if job != nil {
		maxTurns = job.MaxTurns
		role = firstNonEmpty(job.RoleName, job.RoleID)
		agentType = job.AgentType
	}
	if current, err := a.subagentJobs.Get(id); err == nil {
		if maxTurns <= 0 {
			maxTurns = current.MaxTurns
		}
		if role == "" {
			role = firstNonEmpty(current.RoleName, current.RoleID)
		}
		if agentType == "" {
			agentType = current.AgentType
		}
		for i := len(current.Progress) - 1; i >= 0; i-- {
			progress := current.Progress[i]
			if latestPhase == "" && strings.TrimSpace(progress.Phase) != "" {
				latestPhase = strings.TrimSpace(progress.Phase)
			}
			if latestTool == "" && strings.TrimSpace(progress.ToolName) != "" {
				latestTool = strings.TrimSpace(progress.ToolName)
			}
			if latestPhase != "" && latestTool != "" {
				break
			}
		}
	}
	if maxTurns <= 0 && result != nil {
		maxTurns = result.Turns
	}
	lead := fmt.Sprintf("subagent job %s reached max turns", id)
	if maxTurns > 0 {
		lead = fmt.Sprintf("subagent job %s reached max turns after %d turns", id, maxTurns)
	}
	parts := []string{lead}
	if role != "" {
		parts = append(parts, "role="+role)
	}
	if agentType != "" {
		parts = append(parts, "agent_type="+agentType)
	}
	if latestPhase != "" {
		parts = append(parts, "last_phase="+latestPhase)
	}
	if latestTool != "" {
		parts = append(parts, "last_tool="+latestTool)
	}
	if result != nil && strings.TrimSpace(result.RecoveryHint) != "" {
		parts = append(parts, "hint="+strings.TrimSpace(result.RecoveryHint))
	}
	return strings.Join(parts, "; ")
}

func (a *Agent) prepareSubagentWorkspace(job *subagentJob) (*subagentJob, error) {
	if job == nil {
		return nil, fmt.Errorf("missing subagent job")
	}
	root := strings.TrimSpace(a.subagentJobs.dir)
	if root == "" && a.cfg != nil {
		root = filepath.Join(a.cfg.TempDir, "subagents")
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("missing subagent job directory")
	}
	workspace := strings.TrimSpace(a.cfg.WorkspaceDir)
	if workspace == "" {
		return nil, fmt.Errorf("missing workspace directory")
	}

	if a.subagentReadOnlyIsolation() == subagentIsolationSharedReadOnly && subagentJobReadOnly(job) {
		return a.subagentJobs.SetWorkspace(job.ID, workspace, "", subagentIsolationSharedReadOnly, "", "shared_workspace")
	}

	worktreeDir := filepath.Join(root, "worktrees", job.ID)
	baselineDir := filepath.Join(root, "baselines", job.ID)
	isolation := subagentIsolationSnapshot
	origin := "snapshot"
	gitBranch := ""
	repo, clean := cleanGitRepository(workspace)
	if clean {
		branch := subagentGitBranchName(job.ID)
		if err := createSubagentGitWorktree(repo, worktreeDir, branch); err != nil {
			return nil, fmt.Errorf("prepare subagent git worktree: %w", err)
		}
		isolation = subagentIsolationGitWorktree
		origin = "git_clean"
		gitBranch = branch
	} else if repo != "" && a.subagentGitDirtyIsolation() == subagentGitDirtyOverlay {
		branch := subagentGitBranchName(job.ID)
		if err := createSubagentGitWorktree(repo, worktreeDir, branch); err != nil {
			return nil, fmt.Errorf("prepare subagent dirty git worktree: %w", err)
		}
		if err := applyGitDirtyOverlay(repo, worktreeDir); err != nil {
			_ = removeSubagentGitWorktree(repo, worktreeDir, branch)
			return nil, fmt.Errorf("prepare subagent dirty overlay: %w", err)
		}
		isolation = subagentIsolationGitWorktree
		origin = "git_dirty_overlay"
		gitBranch = branch
	} else if repo == "" && a.subagentNonGitWriteIsolation() == subagentNonGitDeny {
		return nil, fmt.Errorf("non-git write isolation policy denies write-capable subagent workspaces")
	} else if repo == "" && a.subagentNonGitWriteIsolation() == subagentIsolationSharedApproval {
		if len(job.WriteScope) > 0 {
			if err := copyScopeSnapshot(workspace, baselineDir, job.WriteScope); err != nil {
				return nil, fmt.Errorf("prepare subagent baseline: %w", err)
			}
		}
		return a.subagentJobs.SetWorkspace(job.ID, workspace, baselineDir, subagentIsolationSharedApproval, "", "non_git_shared_with_approval")
	} else {
		if err := copyWorkspaceSnapshot(workspace, worktreeDir); err != nil {
			return nil, fmt.Errorf("prepare subagent worktree: %w", err)
		}
		if repo != "" {
			origin = "git_dirty_snapshot"
		} else {
			origin = "non_git_snapshot"
		}
	}
	if len(job.WriteScope) > 0 {
		if err := copyScopeSnapshot(workspace, baselineDir, job.WriteScope); err != nil {
			return nil, fmt.Errorf("prepare subagent baseline: %w", err)
		}
	}
	if err := a.applyPreviewJobsToSubagentWorkspace(job, worktreeDir, baselineDir); err != nil {
		return nil, err
	}
	return a.subagentJobs.SetWorkspace(job.ID, worktreeDir, baselineDir, isolation, gitBranch, origin)
}

func (a *Agent) subagentReadOnlyIsolation() string {
	if a == nil || a.cfg == nil {
		return subagentIsolationSharedReadOnly
	}
	switch strings.ToLower(strings.TrimSpace(a.cfg.Tools.Subagent.ReadOnlyIsolation)) {
	case subagentIsolationSnapshot:
		return subagentIsolationSnapshot
	default:
		return subagentIsolationSharedReadOnly
	}
}

func (a *Agent) subagentGitDirtyIsolation() string {
	if a == nil || a.cfg == nil {
		return subagentGitDirtyOverlay
	}
	switch strings.ToLower(strings.TrimSpace(a.cfg.Tools.Subagent.GitDirtyIsolation)) {
	case subagentIsolationSnapshot:
		return subagentIsolationSnapshot
	default:
		return subagentGitDirtyOverlay
	}
}

func (a *Agent) subagentNonGitWriteIsolation() string {
	if a == nil || a.cfg == nil {
		return subagentNonGitCopySnapshot
	}
	switch strings.ToLower(strings.TrimSpace(a.cfg.Tools.Subagent.NonGitWriteIsolation)) {
	case subagentIsolationSharedApproval:
		return subagentIsolationSharedApproval
	case subagentNonGitDeny:
		return subagentNonGitDeny
	default:
		return subagentNonGitCopySnapshot
	}
}

func subagentJobReadOnly(job *subagentJob) bool {
	if job == nil || len(normalizeWriteScope(job.WriteScope)) > 0 {
		return false
	}
	for _, name := range job.ToolNames {
		if isDurableSubagentWriteTool(name) {
			return false
		}
	}
	return true
}

func cleanGitRepository(workspace string) (string, bool) {
	repoRoot, err := gitOutput(workspace, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(repoRoot) == "" {
		return "", false
	}
	status, err := gitOutput(repoRoot, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) != "" {
		return strings.TrimSpace(repoRoot), false
	}
	return strings.TrimSpace(repoRoot), true
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitOutputBytes(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func subagentGitBranchName(jobID string) string {
	base := strings.ToLower(strings.TrimSpace(jobID))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base = strings.Trim(b.String(), "-.")
	if base == "" {
		base = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if len(base) > 48 {
		base = base[len(base)-48:]
	}
	return "godex-subagent-" + base
}

func createSubagentGitWorktree(repoRoot, worktreeDir, branch string) error {
	if err := os.RemoveAll(worktreeDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return err
	}
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreeDir, "HEAD")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func applyGitDirtyOverlay(repoRoot, worktreeDir string) error {
	diff, err := gitOutputBytes(repoRoot, "diff", "--binary", "HEAD", "--", ".")
	if err != nil {
		return fmt.Errorf("git diff dirty overlay: %w: %s", err, strings.TrimSpace(string(diff)))
	}
	if len(strings.TrimSpace(string(diff))) > 0 {
		cmd := exec.Command("git", "apply", "--binary", "--whitespace=nowarn")
		cmd.Dir = worktreeDir
		cmd.Stdin = strings.NewReader(string(diff))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git apply dirty overlay: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return copyGitDirtyOverlayUntracked(repoRoot, worktreeDir)
}

const subagentDirtyOverlayMaxUntrackedBytes int64 = 2 * 1024 * 1024

func copyGitDirtyOverlayUntracked(repoRoot, worktreeDir string) error {
	out, err := gitOutputBytes(repoRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("git ls-files untracked: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, raw := range strings.Split(string(out), "\x00") {
		rel := filepath.Clean(strings.TrimSpace(raw))
		if rel == "" || rel == "." || shouldSkipDirtyOverlayPath(rel) {
			continue
		}
		src, err := safeJoinUnderRoot(repoRoot, rel)
		if err != nil {
			return err
		}
		dst, err := safeJoinUnderRoot(worktreeDir, rel)
		if err != nil {
			return err
		}
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		if info.Mode().IsRegular() && info.Size() > subagentDirtyOverlayMaxUntrackedBytes {
			continue
		}
		if err := copyFileOrSymlink(src, dst, info); err != nil {
			return err
		}
	}
	return nil
}

func shouldSkipDirtyOverlayPath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "." || rel == "" {
		return true
	}
	base := pathBase(rel)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		switch part {
		case ".git", ".godex", "node_modules", ".pnpm-store", ".next", ".nuxt", ".turbo", ".cache", "coverage", "dist", "build", "tmp", "temp":
			return true
		}
	}
	return false
}

func pathBase(rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), "/")
	idx := strings.LastIndex(rel, "/")
	if idx >= 0 {
		return rel[idx+1:]
	}
	return rel
}

func (a *Agent) applyPreviewJobsToSubagentWorkspace(job *subagentJob, worktreeDir, baselineDir string) error {
	if job == nil || len(job.PreviewJobIDs) == 0 {
		return nil
	}
	for _, depJobID := range normalizeWorkflowStrings(job.PreviewJobIDs) {
		depJob, err := a.subagentJobs.Get(depJobID)
		if err != nil {
			return fmt.Errorf("preview merge dependency %s: %w", depJobID, err)
		}
		if depJob.Status != subagentStatusCompleted {
			return fmt.Errorf("preview merge dependency %s is %s", depJobID, depJob.Status)
		}
		review, err := reviewSubagentJob(depJob)
		if err != nil {
			return fmt.Errorf("preview merge dependency %s: %w", depJobID, err)
		}
		if len(review.Changes) == 0 {
			continue
		}
		if err := applySubagentChanges(worktreeDir, depJob.WorktreeDir, review.Changes); err != nil {
			return fmt.Errorf("preview merge dependency %s into worktree: %w", depJobID, err)
		}
		if len(job.WriteScope) > 0 {
			if err := applySubagentChanges(baselineDir, depJob.WorktreeDir, review.Changes); err != nil {
				return fmt.Errorf("preview merge dependency %s into baseline: %w", depJobID, err)
			}
		}
	}
	return nil
}

func (a *Agent) ensureSubagentWorkspace(job *subagentJob) error {
	if job == nil {
		return fmt.Errorf("missing subagent job")
	}
	if strings.TrimSpace(job.WorktreeDir) == "" {
		prepared, err := a.prepareSubagentWorkspace(job)
		if err != nil {
			return err
		}
		*job = *prepared
		return nil
	}
	info, err := os.Stat(job.WorktreeDir)
	if err != nil {
		return fmt.Errorf("subagent worktree is unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("subagent worktree is not a directory: %s", job.WorktreeDir)
	}
	if len(job.WriteScope) > 0 {
		if info, err := os.Stat(job.BaselineDir); err != nil || !info.IsDir() {
			return fmt.Errorf("subagent baseline is unavailable for merge review: %s", job.BaselineDir)
		}
	}
	return nil
}

func (a *Agent) ReviewDurableSubagent(id string) (subagentReview, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentReview{}, err
	}
	return reviewSubagentJob(job)
}

func (a *Agent) MergeDurableSubagent(id string) (subagentMergeResult, error) {
	return a.MergeDurableSubagentWithContext(context.Background(), id)
}

func (a *Agent) MergeDurableSubagentWithContext(ctx context.Context, id string) (subagentMergeResult, error) {
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

func (a *Agent) CleanupDurableSubagentWorkspace(id string) (subagentWorkspaceCleanupResult, error) {
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return subagentWorkspaceCleanupResult{}, err
	}
	if !subagentWorkspaceCanBeCleaned(job) {
		return subagentWorkspaceCleanupResult{JobID: job.ID, Reason: "subagent workspace is not eligible for cleanup"}, nil
	}
	if err := cleanupSubagentWorkspace(job, a.cfg.WorkspaceDir); err != nil {
		return subagentWorkspaceCleanupResult{}, err
	}
	if _, err := a.subagentJobs.SetWorkspaceCleaned(job.ID); err != nil {
		return subagentWorkspaceCleanupResult{}, err
	}
	return subagentWorkspaceCleanupResult{JobID: job.ID, Cleaned: true}, nil
}

func CleanupSubagentWorkspaces(cfg *config.Config, opts SubagentWorkspaceGCOptions) (SubagentWorkspaceGCResult, error) {
	if cfg == nil {
		return SubagentWorkspaceGCResult{DryRun: opts.DryRun}, fmt.Errorf("missing config")
	}
	store := newSubagentJobStore(subagentJobsDir(cfg))
	agent := &Agent{cfg: cfg, subagentJobs: store}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	result := SubagentWorkspaceGCResult{DryRun: opts.DryRun}
	for _, job := range store.List() {
		if !subagentWorkspaceGCEligible(job, opts, now) {
			continue
		}
		bytes := subagentWorkspaceBytes(job)
		item := SubagentWorkspaceGCItem{
			JobID:        job.ID,
			Isolation:    job.Isolation,
			MergeStatus:  job.MergeStatus,
			CleanupState: job.CleanupState,
			Bytes:        bytes,
		}
		result.Candidates++
		result.Bytes += bytes
		if !opts.DryRun {
			cleanup, err := agent.CleanupDurableSubagentWorkspace(job.ID)
			if err != nil {
				item.Reason = err.Error()
			} else {
				item.Cleaned = cleanup.Cleaned
				item.Reason = cleanup.Reason
				if cleanup.Cleaned {
					result.Cleaned++
				}
			}
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func subagentWorkspaceGCEligible(job *subagentJob, opts SubagentWorkspaceGCOptions, now time.Time) bool {
	if job == nil || job.CleanupState == subagentCleanupCleaned || strings.TrimSpace(job.WorktreeDir) == "" {
		return false
	}
	if opts.MergedOnly {
		return job.MergeStatus == subagentMergeMerged || job.MergeStatus == subagentMergeNoChanges
	}
	if subagentWorkspaceCanBeCleaned(job) {
		return true
	}
	if opts.OlderThan > 0 && subagentStatusTerminal(job.Status) && !job.FinishedAt.IsZero() {
		return now.Sub(job.FinishedAt) >= opts.OlderThan
	}
	return false
}

func subagentWorkspaceBytes(job *subagentJob) int64 {
	if job != nil && (job.Isolation == subagentIsolationSharedReadOnly || job.Isolation == subagentIsolationSharedApproval) {
		return dirSize(job.BaselineDir)
	}
	var total int64
	for _, path := range []string{job.WorktreeDir, job.BaselineDir} {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		total += dirSize(path)
	}
	return total
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func subagentWorkspaceCanBeCleaned(job *subagentJob) bool {
	if job == nil || job.CleanupState == subagentCleanupCleaned {
		return false
	}
	switch job.MergeStatus {
	case subagentMergeMerged, subagentMergeNoChanges:
		return subagentStatusTerminal(job.Status)
	default:
		return false
	}
}

func cleanupSubagentWorkspace(job *subagentJob, repoRoot string) error {
	if job == nil {
		return nil
	}
	if job.Isolation == subagentIsolationGitWorktree && strings.TrimSpace(job.GitBranch) != "" {
		if err := removeSubagentGitWorktree(repoRoot, job.WorktreeDir, job.GitBranch); err != nil {
			return err
		}
	} else if strings.TrimSpace(job.WorktreeDir) != "" && filepath.Clean(job.WorktreeDir) != filepath.Clean(repoRoot) {
		if err := os.RemoveAll(job.WorktreeDir); err != nil {
			return err
		}
	}
	if strings.TrimSpace(job.BaselineDir) != "" {
		if err := os.RemoveAll(job.BaselineDir); err != nil {
			return err
		}
	}
	return nil
}

func removeSubagentGitWorktree(repoRoot, worktreeDir, branch string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		repoRoot = filepath.Dir(filepath.Dir(filepath.Clean(worktreeDir)))
	}
	if strings.TrimSpace(worktreeDir) != "" {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(worktreeDir)
			_, _ = gitOutput(repoRoot, "worktree", "prune")
			if strings.TrimSpace(string(out)) != "" {
				return fmt.Errorf("git worktree remove: %w: %s", err, strings.TrimSpace(string(out)))
			}
			return fmt.Errorf("git worktree remove: %w", err)
		}
	}
	if strings.TrimSpace(branch) != "" {
		cmd := exec.Command("git", "branch", "-D", branch)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "not found") {
			return fmt.Errorf("git branch delete: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ReviewDurableSubagentView returns a public review view scoped to one session.
func (a *Agent) ReviewDurableSubagentView(sessionID, id string) (DurableSubagentReviewView, error) {
	if _, err := a.getDurableSubagentForSession(sessionID, id); err != nil {
		return DurableSubagentReviewView{}, err
	}
	review, err := a.ReviewDurableSubagent(id)
	if err != nil {
		return DurableSubagentReviewView{}, err
	}
	return durableSubagentReviewView(review), nil
}

// MergeDurableSubagentViewWithContext merges one durable subagent after
// validating it belongs to the requested session.
func (a *Agent) MergeDurableSubagentViewWithContext(ctx context.Context, sessionID, id string) (DurableSubagentMergeView, error) {
	if _, err := a.getDurableSubagentForSession(sessionID, id); err != nil {
		return DurableSubagentMergeView{}, err
	}
	result, err := a.MergeDurableSubagentWithContext(ctx, id)
	if err != nil {
		return DurableSubagentMergeView{}, err
	}
	return durableSubagentMergeView(result), nil
}

func (a *Agent) getDurableSubagentForSession(sessionID, id string) (*subagentJob, error) {
	if a == nil || a.subagentJobs == nil {
		return nil, fmt.Errorf("subagent runtime unavailable")
	}
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDurableSubagentNotFound, strings.TrimSpace(id))
	}
	if !subagentJobMatchesSession(job, strings.TrimSpace(sessionID)) {
		return nil, fmt.Errorf("%w: %s", ErrDurableSubagentNotFound, strings.TrimSpace(id))
	}
	return job, nil
}

func subagentJobMatchesSession(job *subagentJob, sessionID string) bool {
	if job == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return true
	}
	return strings.TrimSpace(job.SessionID) == sessionID
}

func durableSubagentJobView(job *subagentJob) DurableSubagentJobView {
	if job == nil {
		return DurableSubagentJobView{}
	}
	progress := durableSubagentProgressViews(job.Progress)
	objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt))
	displayTitle := strings.TrimSpace(job.DisplayTitle)
	if displayTitle == "" {
		displayTitle = subagentDisplayTitle(&subagentJob{
			Sequence:  job.Sequence,
			RoleName:  job.RoleName,
			RoleID:    job.RoleID,
			AgentType: job.AgentType,
			Objective: objective,
			Prompt:    job.Prompt,
		})
	}
	diagnostics := subagentDiagnosticsFromProgress(job.Progress)
	view := DurableSubagentJobView{
		JobID:             job.ID,
		SessionID:         job.SessionID,
		ParentTurnID:      job.ParentTurnID,
		Identity:          job.Identity,
		IdentityID:        job.Identity.ID,
		AgentType:         job.AgentType,
		RoleID:            job.RoleID,
		RoleName:          job.RoleName,
		PackageName:       job.PackageName,
		Sequence:          job.Sequence,
		Objective:         objective,
		DisplayTitle:      displayTitle,
		Prompt:            job.Prompt,
		Status:            string(job.Status),
		Result:            job.Result,
		Error:             job.Error,
		CreatedAt:         job.CreatedAt,
		UpdatedAt:         job.UpdatedAt,
		StartedAt:         job.StartedAt,
		FinishedAt:        job.FinishedAt,
		MergedAt:          job.MergedAt,
		WriteScope:        append([]string{}, job.WriteScope...),
		DefaultBundles:    append([]string{}, job.DefaultBundles...),
		ToolPolicy:        append([]string{}, job.ToolPolicy...),
		ToolNames:         append([]string{}, job.ToolNames...),
		WorktreeDir:       job.WorktreeDir,
		Isolation:         job.Isolation,
		WorkspaceOrigin:   job.WorkspaceOrigin,
		GitBranch:         job.GitBranch,
		CleanupState:      job.CleanupState,
		MergeStatus:       job.MergeStatus,
		JobTimeoutMS:      job.JobTimeoutMS,
		MaxTurns:          job.MaxTurns,
		ModelRequestCount: diagnostics.ModelRequestCount,
		ToolCallCount:     diagnostics.ToolCallCount,
		LastRunnerPhase:   diagnostics.LastRunnerPhase,
		LastIteration:     diagnostics.LastIteration,
		LastRecoveryHint:  diagnostics.LastRecoveryHint,
		Progress:          progress,
	}
	if len(progress) > 0 {
		latest := progress[len(progress)-1]
		view.LastPhase = latest.Phase
		view.LastMessage = latest.Message
		view.LastToolID = latest.ToolID
		view.LastToolName = latest.ToolName
	}
	for i := len(progress) - 1; i >= 0; i-- {
		if strings.TrimSpace(view.LastToolName) == "" && strings.TrimSpace(progress[i].ToolName) != "" {
			view.LastToolName = progress[i].ToolName
			view.LastToolID = progress[i].ToolID
		}
		if strings.TrimSpace(view.LastMessage) == "" && strings.TrimSpace(progress[i].Message) != "" {
			view.LastMessage = progress[i].Message
		}
	}
	return view
}

func durableSubagentProgressViews(items []subagentProgressEvent) []DurableSubagentProgressView {
	out := make([]DurableSubagentProgressView, 0, len(items))
	for _, item := range items {
		out = append(out, DurableSubagentProgressView{
			Timestamp:    item.Time,
			Phase:        item.Phase,
			Message:      item.Message,
			ToolID:       item.ToolID,
			ToolName:     item.ToolName,
			Error:        item.Error,
			Result:       item.Result,
			Iteration:    item.Iteration,
			MaxTurns:     item.MaxTurns,
			Model:        item.Model,
			RecoveryHint: item.RecoveryHint,
		})
	}
	return out
}

func durableSubagentReviewView(review subagentReview) DurableSubagentReviewView {
	return DurableSubagentReviewView{
		JobID:         review.JobID,
		WorktreeDir:   review.WorktreeDir,
		WriteScope:    append([]string{}, review.WriteScope...),
		Changes:       durableSubagentFileChangeViews(review.Changes),
		Diff:          review.Diff,
		DiffTruncated: review.DiffTruncated,
		Conflicts:     append([]string{}, review.Conflicts...),
	}
}

func durableSubagentMergeView(result subagentMergeResult) DurableSubagentMergeView {
	return DurableSubagentMergeView{
		JobID:       result.JobID,
		Status:      result.Status,
		Applied:     durableSubagentFileChangeViews(result.Applied),
		Conflicts:   append([]string{}, result.Conflicts...),
		WorktreeDir: result.WorktreeDir,
	}
}

func durableSubagentFileChangeViews(items []subagentFileChange) []DurableSubagentFileChangeView {
	out := make([]DurableSubagentFileChangeView, 0, len(items))
	for _, item := range items {
		out = append(out, DurableSubagentFileChangeView{
			Path:   item.Path,
			Status: item.Status,
			Bytes:  item.Bytes,
			Binary: item.Binary,
		})
	}
	return out
}

func reviewSubagentJob(job *subagentJob) (subagentReview, error) {
	if job == nil {
		return subagentReview{}, fmt.Errorf("missing subagent job")
	}
	if strings.TrimSpace(job.WorktreeDir) == "" {
		return subagentReview{}, fmt.Errorf("subagent job has no isolated worktree")
	}
	if len(job.WriteScope) == 0 {
		return subagentReview{}, fmt.Errorf("subagent review requires write_scope")
	}
	if strings.TrimSpace(job.BaselineDir) == "" {
		return subagentReview{}, fmt.Errorf("subagent job has no merge baseline")
	}
	changes, err := collectSubagentChanges(job.BaselineDir, job.WorktreeDir, job.WriteScope)
	if err != nil {
		return subagentReview{}, err
	}
	diff, truncated := buildSubagentDiffPreview(job.BaselineDir, job.WorktreeDir, changes, subagentDiffPreviewLimit)
	return subagentReview{
		JobID:         job.ID,
		WorktreeDir:   job.WorktreeDir,
		WriteScope:    append([]string{}, job.WriteScope...),
		Changes:       changes,
		Diff:          diff,
		DiffTruncated: truncated,
	}, nil
}

func (a *Agent) executeSubagentToolForJob(ctx context.Context, name string, input map[string]interface{}, job *subagentJob) (conversation.ToolExecutionResult, error) {
	if job == nil {
		return conversation.ToolExecutionResult{}, fmt.Errorf("missing subagent job")
	}
	if !subagentJobAllowsTool(job.ToolNames, name) {
		return conversation.ToolExecutionResult{}, fmt.Errorf("capability denied: subagent %s is not allowed to call tool:%s", job.ID, strings.TrimSpace(name))
	}
	if err := enforceSubagentWriteScope(name, input, job.WriteScope); err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	checkedInput, err := a.authorizeSubagentTool(ctx, name, input, job)
	if err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	input = checkedInput
	if job.Isolation == subagentIsolationSharedReadOnly && name == "bash" {
		command, _ := input["command"].(string)
		if !sharedReadOnlyShellCommand(command) {
			return conversation.ToolExecutionResult{}, fmt.Errorf("shared read-only subagent cannot run shell command %q; use read_file or request an isolated write-capable subagent", strings.TrimSpace(command))
		}
	}
	if isDurableSubagentInheritedParentTool(name) {
		return a.handleToolResult(ctx, name, input)
	}
	workspace := strings.TrimSpace(job.WorktreeDir)
	if workspace == "" {
		return a.executeSubagentToolWithScope(ctx, name, input, job.WriteScope)
	}
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	if a.cfg != nil && strings.TrimSpace(a.cfg.TempDir) != "" {
		tempDir = filepath.Join(a.cfg.TempDir, "subagents", job.ID)
	}
	var execution tooling.ExecutionConfig
	if a.cfg != nil {
		execution = executionConfigFromRuntime(a.cfg.Tools.Execution)
	}
	execution = executionConfigForSubagentRole(execution, job.ToolPolicy)
	return executeSubagentToolWithHandlers(ctx, name, input, workspaceSubagentToolHandlers(workspace, tempDir, execution))
}

func sharedReadOnlyShellCommand(command string) bool {
	segments, err := tooling.DisallowedShellCommands(command)
	if err != nil || len(segments) > 0 {
		return false
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, ">|;&`$") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd", "ls", "find", "rg", "grep", "sed", "cat", "head", "tail", "wc":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "diff", "log", "show", "grep", "ls-files":
			return true
		}
	}
	return false
}

const subagentPermissionSenderPrefix = "subagent:"

func (a *Agent) authorizeSubagentTool(ctx context.Context, name string, input map[string]interface{}, job *subagentJob) (map[string]interface{}, error) {
	normalized := cloneStringAnyMap(input)
	if strings.TrimSpace(name) != "bash" {
		return normalized, nil
	}
	if a == nil || a.permissions == nil || job == nil {
		return normalized, nil
	}
	runtimeCtx := tools.SessionContextFromContext(ctx)
	if strings.TrimSpace(runtimeCtx.SessionID) == "" {
		runtimeCtx.SessionID = firstNonEmpty(job.RuntimeContext.SessionID, job.SessionID)
	}
	if strings.TrimSpace(runtimeCtx.Source) == "" {
		runtimeCtx.Source = job.RuntimeContext.Source
	}
	if strings.TrimSpace(runtimeCtx.Source) == "" && strings.HasPrefix(strings.TrimSpace(runtimeCtx.SessionID), "web-") {
		runtimeCtx.Source = string(message.SourceWeb)
	}
	runtimeCtx.Sender = subagentPermissionSenderPrefix + strings.TrimSpace(job.ID)
	call := tools.ToolCall{
		Name:            strings.TrimSpace(name),
		RawInput:        cloneStringAnyMap(input),
		NormalizedInput: normalized,
		SessionContext:  runtimeCtx.Clone(),
	}
	_, err := tools.NewPermissionInterceptorWithReview(a.permissions, a.reviewPermissionRequest)(ctx, &call)
	if err != nil {
		return normalized, err
	}
	return cloneStringAnyMap(call.NormalizedInput), nil
}

func subagentJobAllowsTool(toolNames []string, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, allowed := range toolNames {
		if strings.EqualFold(strings.TrimSpace(allowed), name) {
			return true
		}
	}
	return false
}

func (a *Agent) executeSubagentToolWithScope(ctx context.Context, name string, input map[string]interface{}, writeScope []string) (conversation.ToolExecutionResult, error) {
	if err := enforceSubagentWriteScope(name, input, writeScope); err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	return a.executeSubagentTool(ctx, name, input)
}

type subagentToolHandlers struct {
	runBash   func(context.Context, string, bool) (conversation.ToolExecutionResult, error)
	readFile  func(context.Context, string, int, int, int) (conversation.ToolExecutionResult, error)
	writeFile func(context.Context, string, string) (conversation.ToolExecutionResult, error)
	editFile  func(context.Context, string, string, string) (conversation.ToolExecutionResult, error)
}

func executeSubagentToolWithHandlers(ctx context.Context, name string, input map[string]interface{}, handlers subagentToolHandlers) (conversation.ToolExecutionResult, error) {
	switch name {
	case "bash":
		cmd, _ := input["command"].(string)
		allowUnlisted, _ := input["_allow_unlisted_commands"].(bool)
		return handlers.runBash(ctx, cmd, allowUnlisted)
	case "read_file":
		path, _ := input["path"].(string)
		return handlers.readFile(ctx, path, subagentToolLimit(input["limit"]), subagentToolLimit(input["offset"]), subagentToolLimit(input["start_line"]))
	case "write_file":
		path, _ := input["path"].(string)
		content, _ := input["content"].(string)
		return handlers.writeFile(ctx, path, content)
	case "edit_file":
		path, _ := input["path"].(string)
		oldText, _ := input["old_text"].(string)
		newText, _ := input["new_text"].(string)
		return handlers.editFile(ctx, path, oldText, newText)
	default:
		return conversation.ToolExecutionResult{}, fmt.Errorf("unknown tool: %s", name)
	}
}

func workspaceSubagentToolHandlers(workspace, tempDir string, execution tooling.ExecutionConfig) subagentToolHandlers {
	executor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspace, tempDir, execution)
	return subagentToolHandlers{
		runBash: func(ctx context.Context, cmd string, allowUnlisted bool) (conversation.ToolExecutionResult, error) {
			options := tools.ShellCommandOptionsForContext(tools.SessionContextFromContext(ctx), tooling.ShellCommandOptions{
				AllowUnlistedCommands: allowUnlisted,
			})
			output, err := executor.RunShellBudgetedWithOptions(ctx, cmd, options)
			return conversation.ToolExecutionResult{
				Output:        output.ModelText(),
				ArtifactPaths: compactNonEmptyStrings(output.FilePath),
			}, err
		},
		readFile: func(_ context.Context, path string, limit, offset, startLine int) (conversation.ToolExecutionResult, error) {
			output, err := executor.ReadFileRange(path, limit, offset, startLine)
			return conversation.ToolExecutionResult{Output: output}, err
		},
		writeFile: func(_ context.Context, path, content string) (conversation.ToolExecutionResult, error) {
			output, err := executor.WriteFile(path, content)
			return conversation.ToolExecutionResult{Output: output}, err
		},
		editFile: func(_ context.Context, path, oldText, newText string) (conversation.ToolExecutionResult, error) {
			output, err := executor.EditFile(path, oldText, newText)
			return conversation.ToolExecutionResult{Output: output}, err
		},
	}
}

func executionConfigForSubagentRole(base tooling.ExecutionConfig, toolPolicy []string) tooling.ExecutionConfig {
	for _, item := range toolPolicy {
		key, value, ok := splitToolPolicy(item)
		if !ok {
			continue
		}
		switch key {
		case "shell:allow":
			base.ShellAllowPatterns = append(base.ShellAllowPatterns, value)
		case "shell:deny":
			base.ShellDenyPatterns = append(base.ShellDenyPatterns, value)
		}
	}
	return base
}

func splitToolPolicy(item string) (string, string, bool) {
	item = strings.TrimSpace(item)
	if item == "" {
		return "", "", false
	}
	for _, prefix := range []string{"shell:allow:", "shell:deny:"} {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimSuffix(prefix, ":"), strings.TrimSpace(strings.TrimPrefix(item, prefix)), strings.TrimSpace(strings.TrimPrefix(item, prefix)) != ""
		}
	}
	return "", "", false
}

func subagentToolLimit(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

func enforceSubagentWriteScope(name string, input map[string]interface{}, writeScope []string) error {
	if (name != "write_file" && name != "edit_file") || len(writeScope) == 0 {
		return nil
	}
	path, _ := input["path"].(string)
	if !pathAllowedByWriteScope(path, writeScope) {
		return fmt.Errorf("path %q is outside subagent write scope", path)
	}
	return nil
}

func copyWorkspaceSnapshot(src, dst string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return fmt.Errorf("missing source or destination")
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return copyDirFiltered(src, dst, true)
}

func copyScopeSnapshot(src, dst string, scope []string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return fmt.Errorf("missing source or destination")
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, rel := range normalizeWriteScope(scope) {
		srcPath, err := safeJoinUnderRoot(src, rel)
		if err != nil {
			return err
		}
		dstPath, err := safeJoinUnderRoot(dst, rel)
		if err != nil {
			return err
		}
		info, err := os.Lstat(srcPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyDirFiltered(srcPath, dstPath, true); err != nil {
				return err
			}
			continue
		}
		if err := copyFileOrSymlink(srcPath, dstPath, info); err != nil {
			return err
		}
	}
	return nil
}

func copyDirFiltered(src, dst string, skipGenerated bool) error {
	src = filepath.Clean(src)
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if entry.IsDir() && skipGenerated && shouldSkipSubagentSnapshotDir(entry.Name()) {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFileOrSymlink(path, target, info)
	})
}

func copyFileOrSymlink(src, dst string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func shouldSkipSubagentSnapshotDir(name string) bool {
	switch strings.TrimSpace(name) {
	case ".git", ".godex", "node_modules", ".pnpm-store", ".next", ".nuxt", ".turbo", ".cache", "coverage", "dist", "build":
		return true
	default:
		return false
	}
}

func collectSubagentChanges(baselineDir, worktreeDir string, scope []string) ([]subagentFileChange, error) {
	paths := map[string]struct{}{}
	for _, root := range []string{baselineDir, worktreeDir} {
		for _, rel := range normalizeWriteScope(scope) {
			path, err := safeJoinUnderRoot(root, rel)
			if err != nil {
				return nil, err
			}
			if err := collectFilesUnder(root, path, paths); err != nil {
				return nil, err
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	changes := make([]subagentFileChange, 0, len(ordered))
	for _, rel := range ordered {
		baseInfo, baseExists, err := fileSnapshot(filepath.Join(baselineDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		workInfo, workExists, err := fileSnapshot(filepath.Join(worktreeDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		switch {
		case !baseExists && !workExists:
			continue
		case !baseExists && workExists:
			changes = append(changes, subagentFileChange{Path: rel, Status: "added", Bytes: workInfo.size, Binary: workInfo.binary})
		case baseExists && !workExists:
			changes = append(changes, subagentFileChange{Path: rel, Status: "deleted", Bytes: baseInfo.size, Binary: baseInfo.binary})
		case baseInfo.hash != workInfo.hash || baseInfo.mode != workInfo.mode:
			changes = append(changes, subagentFileChange{Path: rel, Status: "modified", Bytes: workInfo.size, Binary: workInfo.binary || baseInfo.binary})
		}
	}
	return changes, nil
}

func collectFilesUnder(root, start string, out map[string]struct{}) error {
	info, err := os.Lstat(start)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	add := func(path string) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
		if rel != "" && rel != "." {
			out[rel] = struct{}{}
		}
		return nil
	}
	if !info.IsDir() {
		return add(start)
	}
	return filepath.WalkDir(start, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == start {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipSubagentSnapshotDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return add(path)
	})
}

type subagentFileSnapshot struct {
	hash   string
	size   int64
	mode   os.FileMode
	binary bool
}

func fileSnapshot(path string) (subagentFileSnapshot, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return subagentFileSnapshot{}, false, nil
	}
	if err != nil {
		return subagentFileSnapshot{}, false, err
	}
	if info.IsDir() {
		return subagentFileSnapshot{}, false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return subagentFileSnapshot{}, false, err
		}
		sum := sha256.Sum256([]byte("symlink:" + target))
		return subagentFileSnapshot{hash: fmt.Sprintf("%x", sum[:]), size: int64(len(target)), mode: info.Mode(), binary: false}, true, nil
	}
	if !info.Mode().IsRegular() {
		return subagentFileSnapshot{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return subagentFileSnapshot{}, false, err
	}
	sum := sha256.Sum256(data)
	return subagentFileSnapshot{
		hash:   fmt.Sprintf("%x", sum[:]),
		size:   info.Size(),
		mode:   info.Mode().Perm(),
		binary: !utf8.Valid(data),
	}, true, nil
}

func detectSubagentMergeConflicts(workspaceDir, baselineDir, worktreeDir string, changes []subagentFileChange) ([]string, error) {
	conflicts := make([]string, 0)
	for _, change := range changes {
		mainPath := filepath.Join(workspaceDir, filepath.FromSlash(change.Path))
		basePath := filepath.Join(baselineDir, filepath.FromSlash(change.Path))
		workPath := filepath.Join(worktreeDir, filepath.FromSlash(change.Path))
		mainInfo, mainExists, err := fileSnapshot(mainPath)
		if err != nil {
			return nil, err
		}
		baseInfo, baseExists, err := fileSnapshot(basePath)
		if err != nil {
			return nil, err
		}
		workInfo, workExists, err := fileSnapshot(workPath)
		if err != nil {
			return nil, err
		}
		if mainExists && workExists && mainInfo.hash == workInfo.hash && mainInfo.mode == workInfo.mode {
			continue
		}
		if baseExists != mainExists {
			conflicts = append(conflicts, change.Path)
			continue
		}
		if baseExists && (baseInfo.hash != mainInfo.hash || baseInfo.mode != mainInfo.mode) {
			conflicts = append(conflicts, change.Path)
		}
	}
	return conflicts, nil
}

func applySubagentChanges(workspaceDir, worktreeDir string, changes []subagentFileChange) error {
	for _, change := range changes {
		mainPath := filepath.Join(workspaceDir, filepath.FromSlash(change.Path))
		workPath := filepath.Join(worktreeDir, filepath.FromSlash(change.Path))
		switch change.Status {
		case "deleted":
			if err := os.Remove(mainPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		case "added", "modified":
			info, err := os.Lstat(workPath)
			if err != nil {
				return err
			}
			if err := copyFileOrSymlink(workPath, mainPath, info); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildSubagentDiffPreview(baselineDir, worktreeDir string, changes []subagentFileChange, limit int) (string, bool) {
	if limit <= 0 {
		limit = subagentDiffPreviewLimit
	}
	var builder strings.Builder
	truncated := false
	for _, change := range changes {
		if builder.Len() >= limit {
			truncated = true
			break
		}
		chunk := subagentDiffForChange(baselineDir, worktreeDir, change)
		if chunk == "" {
			continue
		}
		if builder.Len()+len(chunk) > limit {
			chunk = chunk[:limit-builder.Len()]
			truncated = true
		}
		builder.WriteString(chunk)
		if !strings.HasSuffix(chunk, "\n") {
			builder.WriteString("\n")
		}
	}
	return builder.String(), truncated
}

func subagentDiffForChange(baselineDir, worktreeDir string, change subagentFileChange) string {
	header := fmt.Sprintf("### %s (%s)\n", change.Path, change.Status)
	if change.Binary {
		return header + "[binary file omitted]\n"
	}
	basePath := filepath.Join(baselineDir, filepath.FromSlash(change.Path))
	workPath := filepath.Join(worktreeDir, filepath.FromSlash(change.Path))
	baseArg, cleanupBase, err := diffPathOrEmpty(basePath)
	if err != nil {
		return header + fmt.Sprintf("[diff unavailable: %v]\n", err)
	}
	defer cleanupBase()
	workArg, cleanupWork, err := diffPathOrEmpty(workPath)
	if err != nil {
		return header + fmt.Sprintf("[diff unavailable: %v]\n", err)
	}
	defer cleanupWork()
	cmd := exec.Command("diff", "-u", "--label", "a/"+change.Path, "--label", "b/"+change.Path, baseArg, workArg)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return header + fmt.Sprintf("[diff unavailable: %v]\n", err)
		}
	}
	if len(output) == 0 {
		return header
	}
	return header + string(output)
}

func diffPathOrEmpty(path string) (string, func(), error) {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, func() {}, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", func() {}, err
	}
	file, err := os.CreateTemp("", "godex-subagent-empty-*")
	if err != nil {
		return "", func() {}, err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", func() {}, err
	}
	return name, func() { _ = os.Remove(name) }, nil
}

func safeJoinUnderRoot(root, rel string) (string, error) {
	root = filepath.Clean(root)
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if rel == "" || rel == "." {
		return root, nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q is outside workspace", rel)
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if path != root {
		parent := root + string(os.PathSeparator)
		if !strings.HasPrefix(path, parent) {
			return "", fmt.Errorf("path %q is outside workspace", rel)
		}
	}
	return path, nil
}

func compactNonEmptyStrings(items ...string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func subagentToolNames(agentType string) []string {
	if normalizeSubagentType(agentType) == "general-purpose" {
		return []string{"bash", "read_file", "write_file", "edit_file"}
	}
	return []string{"read_file"}
}

func subagentRequiredBundles(prompt string, explicit []string) []string {
	bundles := append([]string{}, explicit...)
	bundles = append(bundles, implicitBundlesForQuery(prompt)...)
	if looksLikeWebResearchPrompt(prompt) {
		bundles = append(bundles, bundleWeb)
	}
	return uniqueStrings(bundles)
}

func looksLikeWebResearchPrompt(prompt string) bool {
	query := strings.ToLower(strings.TrimSpace(prompt))
	if query == "" {
		return false
	}
	if containsAny(query,
		"网络检索", "网页检索", "联网检索", "网上调研", "网络调研", "联网调研",
		"源头链接", "来源链接", "引用链接", "官方来源", "网页来源",
		"web research", "online research", "internet research", "source links", "official sources", "official pages",
	) {
		return true
	}
	hasResearchCue := containsAny(query, "调研", "研究", "research", "investigate")
	if !hasResearchCue {
		return false
	}
	return containsAny(query, "网页", "网站", "网上", "联网", "搜索", "检索", "链接", "来源", "source", "search", "web", "online", "internet", "url", "link")
}

func appendRequiredSubagentTools(base, bundles, explicitTools []string) []string {
	out := append([]string{}, base...)
	for _, bundle := range bundles {
		out = append(out, subagentToolsForRequiredBundle(bundle)...)
	}
	out = append(out, explicitTools...)
	return uniqueStrings(out)
}

func subagentToolsForRequiredBundle(bundle string) []string {
	switch strings.ToLower(strings.TrimSpace(bundle)) {
	case bundleWeb:
		return []string{"web_search", "web_fetch"}
	default:
		return nil
	}
}

func (a *Agent) resolveSubagentRole(agentType string) (pkgregistry.Role, bool) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" || strings.EqualFold(agentType, "Explore") || agentType == "general-purpose" || a == nil || a.cfg == nil {
		return pkgregistry.Role{}, false
	}
	role, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).GetRole(agentType, true)
	if err != nil {
		return pkgregistry.Role{}, false
	}
	return role, true
}

func subagentToolNamesForRole(agentType string, role *pkgregistry.Role) []string {
	if role == nil {
		return subagentToolNames(agentType)
	}
	var tools []string
	if len(role.Tools) > 0 {
		for _, name := range role.Tools {
			name = strings.TrimSpace(name)
			if !supportedDurableSubagentTool(name) {
				continue
			}
			if !role.WriteEnabled && isDurableSubagentWriteTool(name) {
				continue
			}
			tools = append(tools, name)
		}
	} else {
		tools = []string{"bash", "read_file"}
		if role.WriteEnabled {
			tools = append(tools, "write_file", "edit_file")
		}
	}
	if len(tools) == 0 {
		return []string{"bash", "read_file"}
	}
	return uniqueStrings(tools)
}

func supportedDurableSubagentTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "bash", "read_file", "write_file", "edit_file", "web_search", "web_fetch":
		return true
	default:
		return false
	}
}

func isDurableSubagentInheritedParentTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "web_search", "web_fetch":
		return true
	default:
		return false
	}
}

func isDurableSubagentWriteTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "write_file", "edit_file":
		return true
	default:
		return false
	}
}

func narrowSubagentWriteTools(toolNames []string, writeScope []string) []string {
	hasWriteScope := len(normalizeWriteScope(writeScope)) > 0
	out := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if name == "bash" && !hasWriteScope {
			continue
		}
		if isDurableSubagentWriteTool(name) && !hasWriteScope {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"read_file"}
	}
	return uniqueStrings(out)
}

func uniqueStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (a *Agent) validateSubagentToolInheritance(toolNames []string) error {
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !supportedDurableSubagentTool(name) {
			return fmt.Errorf("capability denied: child subagent requested tool:%s outside parent active tool policy", name)
		}
		if a != nil && a.toolHandler != nil && a.toolHandler.Get(name) != nil && !a.toolHandler.IsActive(name) {
			return fmt.Errorf("capability denied: child subagent requested inactive parent tool:%s", name)
		}
	}
	return nil
}

func (a *Agent) validateSubagentRequiredCapabilities(requiredBundles, requiredTools []string) error {
	missingBundles := make([]string, 0)
	for _, bundle := range uniqueStrings(requiredBundles) {
		bundle = strings.TrimSpace(bundle)
		if bundle == "" {
			continue
		}
		for _, toolName := range subagentToolsForRequiredBundle(bundle) {
			if !a.subagentParentToolActive(toolName) {
				missingBundles = append(missingBundles, bundle)
				break
			}
		}
	}
	missingTools := make([]string, 0)
	for _, toolName := range uniqueStrings(requiredTools) {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		if !a.subagentParentToolActive(toolName) {
			missingTools = append(missingTools, toolName)
		}
	}
	if len(missingBundles) == 0 && len(missingTools) == 0 {
		return nil
	}
	return fmt.Errorf(
		"subagent_capability_required: missing active parent capability for bundle(s) %s tool(s) %s; enable required bundle(s) with tool_exchange and retry task",
		strings.Join(uniqueStrings(missingBundles), ","),
		strings.Join(uniqueStrings(missingTools), ","),
	)
}

func (a *Agent) subagentParentToolActive(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || a == nil || a.toolHandler == nil {
		return false
	}
	return a.toolHandler.Get(name) != nil && a.toolHandler.IsActive(name)
}

func roleCapabilitySummary(role pkgregistry.Role, toolNames []string, writeScope []string) []string {
	items := capabilitySummaryForTools(toolNames, writeScope)
	for _, capability := range role.Capabilities {
		if subagentCapabilityAllowed(capability, toolNames, writeScope) {
			items = append(items, strings.TrimSpace(capability))
		}
	}
	if strings.TrimSpace(role.ModelHint) != "" {
		items = append(items, "model:"+strings.TrimSpace(role.ModelHint))
	}
	return uniqueStrings(items)
}

func subagentCapabilityAllowed(capability string, toolNames []string, writeScope []string) bool {
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return false
	}
	if strings.HasPrefix(capability, "tool:") {
		return subagentJobAllowsTool(toolNames, strings.TrimPrefix(capability, "tool:"))
	}
	if strings.HasPrefix(capability, "file:write:") {
		path := strings.TrimPrefix(capability, "file:write:")
		if path == "*" {
			return false
		}
		return pathAllowedByWriteScope(path, writeScope)
	}
	if strings.HasPrefix(capability, "file:read:") {
		return true
	}
	if strings.HasPrefix(capability, "shell:") {
		return subagentJobAllowsTool(toolNames, "bash")
	}
	return false
}

func capabilitySummaryForTools(toolNames []string, writeScope []string) []string {
	items := make([]string, 0, len(toolNames)+len(writeScope)+2)
	hasWriteTool := false
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name != "" {
			items = append(items, "tool:"+name)
		}
		if isDurableSubagentWriteTool(name) {
			hasWriteTool = true
		}
	}
	if len(writeScope) == 0 {
		items = append(items, "file:read:*")
	} else {
		for _, scope := range normalizeWriteScope(writeScope) {
			items = append(items, "file:read:"+scope)
			if hasWriteTool {
				items = append(items, "file:write:"+scope)
			}
		}
	}
	return uniqueStrings(items)
}

func subagentIdentityName(job *subagentJob) string {
	if job == nil {
		return "Subagent"
	}
	if strings.TrimSpace(job.RoleName) != "" {
		return strings.TrimSpace(job.RoleName)
	}
	if strings.TrimSpace(job.AgentType) != "" {
		return strings.TrimSpace(job.AgentType)
	}
	return "Subagent"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringAnyMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func normalizeSubagentType(agentType string) string {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return "Explore"
	}
	if agentType == "general-purpose" {
		return "general-purpose"
	}
	if strings.EqualFold(agentType, "explore") {
		return "Explore"
	}
	return agentType
}

func durableSubagentPromptForRole(agentType string, writeScope []string) string {
	lines := []string{
		"You are a durable subagent. Work independently, keep progress concise, and end with a clear handoff summary.",
		"Prefer workspace-relative paths. Do not revert unrelated user changes.",
	}
	scope := normalizeWriteScope(writeScope)
	if len(scope) > 0 {
		lines = append(lines,
			"Your shell and file tools run in an isolated workspace snapshot. Changes are not applied to the main workspace until the lead agent reviews and merges them.",
			"Use rg, sed, or focused read_file ranges to locate evidence before reading large files. Once you have enough evidence, stop exploring and produce the handoff.",
		)
	} else {
		lines = append(lines,
			"This is a read-only assignment. Use read_file with focused path ranges; shell commands are not available for shared read-only subagents.",
			"Once you have enough evidence, stop exploring and produce the handoff.",
		)
	}
	role := normalizeSubagentType(agentType)
	if role != "" && role != "Explore" && role != "general-purpose" {
		lines = append(lines, "Named role: "+role+". Treat this role name as guidance for your perspective and handoff style.")
	}
	if len(scope) > 0 {
		lines = append(lines, "Write scope: "+strings.Join(scope, ", ")+". Only changes under this scope are mergeable.")
	}
	return strings.Join(lines, " ")
}

func subagentBasePromptForRole(role pkgregistry.Role, writeScope []string) string {
	roleID := strings.TrimSpace(role.ID)
	if roleID == "" {
		roleID = role.Name
	}
	base := durableSubagentPromptForRole(roleID, writeScope)
	if strings.TrimSpace(role.Name) != "" && strings.TrimSpace(role.Name) != strings.TrimSpace(roleID) {
		base += " Display role name: " + strings.TrimSpace(role.Name) + "."
	}
	if strings.TrimSpace(role.Description) != "" {
		base += " Role description: " + strings.TrimSpace(role.Description) + "."
	}
	if strings.TrimSpace(role.BasePrompt) != "" {
		base += "\n\nRole instructions:\n" + strings.TrimSpace(role.BasePrompt)
	}
	return base
}

func durableSubagentPrompt(writeScope []string) string {
	return durableSubagentPromptForRole("", writeScope)
}

func normalizeWriteScope(scope []string) []string {
	out := make([]string, 0, len(scope))
	seen := make(map[string]struct{}, len(scope))
	for _, item := range scope {
		item = strings.Trim(strings.TrimSpace(filepath.ToSlash(item)), "/")
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func pathAllowedByWriteScope(path string, scope []string) bool {
	path = strings.Trim(strings.TrimSpace(filepath.ToSlash(path)), "/")
	if path == "" || strings.HasPrefix(path, "../") || path == ".." {
		return false
	}
	for _, item := range normalizeWriteScope(scope) {
		if path == item || strings.HasPrefix(path, item+"/") {
			return true
		}
	}
	return false
}

func (a *Agent) recordSubagentProgress(id string, target subagentEventTarget, progress subagentProgressEvent) {
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	job, err := a.subagentJobs.AppendProgress(id, progress)
	if err != nil {
		return
	}
	target.emit(job, progress.Phase, progress.Message, progress.ToolID, progress.ToolName, progress.Error, progress.Result)
}

func (a *Agent) appendSubagentProgress(id string, progress subagentProgressEvent) {
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	_, _ = a.subagentJobs.AppendProgress(id, progress)
}

func (t subagentEventTarget) emit(job *subagentJob, phase, message, toolID, toolName, errorText, result string) {
	if t.sink == nil || job == nil {
		return
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt))
	displayTitle := job.DisplayTitle
	if strings.TrimSpace(displayTitle) == "" {
		displayTitle = subagentDisplayTitle(&subagentJob{
			Sequence:  job.Sequence,
			RoleName:  job.RoleName,
			RoleID:    job.RoleID,
			AgentType: job.AgentType,
			Objective: objective,
			Prompt:    job.Prompt,
		})
	}
	diagnostics := subagentDiagnosticsFromProgress(job.Progress)
	t.sink.Emit(events.Event{
		SessionID: t.sessionID,
		TurnID:    t.turnID,
		Type:      events.EventSubagentJobUpdated,
		Timestamp: updatedAt,
		Payload: events.SubagentJobPayload{
			JobID:             job.ID,
			ParentTurnID:      job.ParentTurnID,
			Sequence:          job.Sequence,
			Objective:         objective,
			DisplayTitle:      displayTitle,
			IdentityID:        job.Identity.ID,
			AgentType:         job.AgentType,
			RoleID:            job.RoleID,
			RoleName:          job.RoleName,
			PackageName:       job.PackageName,
			Status:            string(job.Status),
			Phase:             strings.TrimSpace(phase),
			Message:           strings.TrimSpace(message),
			ToolID:            strings.TrimSpace(toolID),
			ToolName:          strings.TrimSpace(toolName),
			Error:             strings.TrimSpace(errorText),
			Result:            previewSubagentText(result),
			ToolNames:         append([]string{}, job.ToolNames...),
			CapabilitySummary: append([]string{}, job.Identity.CapabilitySummary...),
			ModelHint:         job.Identity.ModelHint,
			BudgetHint:        job.Identity.BudgetHint,
			MaxTurns:          job.MaxTurns,
			ModelRequestCount: diagnostics.ModelRequestCount,
			ToolCallCount:     diagnostics.ToolCallCount,
			LastRunnerPhase:   diagnostics.LastRunnerPhase,
			LastIteration:     diagnostics.LastIteration,
			LastRecoveryHint:  diagnostics.LastRecoveryHint,
			WriteScope:        append([]string{}, job.WriteScope...),
			WorktreeDir:       job.WorktreeDir,
			Isolation:         job.Isolation,
			WorkspaceOrigin:   job.WorkspaceOrigin,
			GitBranch:         job.GitBranch,
			CleanupState:      job.CleanupState,
			MergeStatus:       job.MergeStatus,
			UpdatedAt:         updatedAt,
		},
	})
}

func (t subagentEventTarget) emitIdentity(job *subagentJob) {
	if t.sink == nil || job == nil || strings.TrimSpace(job.Identity.ID) == "" {
		return
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	t.sink.Emit(events.Event{
		SessionID: t.sessionID,
		TurnID:    t.turnID,
		Type:      events.EventAgentIdentityUpdated,
		Timestamp: updatedAt,
		Payload: events.AgentIdentityPayload{
			ID:                job.Identity.ID,
			Name:              job.Identity.Name,
			Kind:              job.Identity.Kind,
			Role:              job.Identity.Role,
			ParentID:          job.Identity.ParentID,
			SessionID:         job.Identity.SessionID,
			Source:            job.Identity.Source,
			CapabilitySummary: append([]string{}, job.Identity.CapabilitySummary...),
			ModelHint:         job.Identity.ModelHint,
			BudgetHint:        job.Identity.BudgetHint,
			Display:           cloneStringMap(job.Identity.Display),
			LastActivityAt:    updatedAt,
		},
	})
}

func (t subagentEventTarget) emitRunnerPhase(job *subagentJob, phase conversation.PhaseEvent) {
	if t.sink == nil || job == nil {
		return
	}
	updatedAt := time.Now().UTC()
	if !job.UpdatedAt.IsZero() {
		updatedAt = job.UpdatedAt
	}
	objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt))
	displayTitle := job.DisplayTitle
	if strings.TrimSpace(displayTitle) == "" {
		displayTitle = subagentDisplayTitle(&subagentJob{
			Sequence:  job.Sequence,
			RoleName:  job.RoleName,
			RoleID:    job.RoleID,
			AgentType: job.AgentType,
			Objective: objective,
			Prompt:    job.Prompt,
		})
	}
	t.sink.Emit(events.Event{
		SessionID: t.sessionID,
		TurnID:    t.turnID,
		Type:      events.EventRunnerPhaseChanged,
		Timestamp: updatedAt,
		Payload: events.RunnerPhasePayload{
			RunnerID:     job.ID,
			ActorKind:    "subagent",
			ActorID:      firstNonEmpty(job.Identity.ID, job.ID),
			Objective:    objective,
			DisplayTitle: displayTitle,
			Phase:        phase.Phase,
			Iteration:    phase.Iteration,
			MaxTurns:     job.MaxTurns,
			Model:        phase.Model,
			Message:      phase.Message,
			ToolID:       phase.ToolID,
			ToolName:     phase.ToolName,
			RecoveryHint: phase.RecoveryHint,
		},
	})
}

func appendBoundedSubagentProgress(items []subagentProgressEvent, progress subagentProgressEvent) []subagentProgressEvent {
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	progress.Phase = strings.TrimSpace(progress.Phase)
	progress.Message = strings.TrimSpace(progress.Message)
	progress.ToolID = strings.TrimSpace(progress.ToolID)
	progress.ToolName = strings.TrimSpace(progress.ToolName)
	progress.Error = strings.TrimSpace(progress.Error)
	progress.Result = strings.TrimSpace(progress.Result)
	progress.Model = strings.TrimSpace(progress.Model)
	progress.RecoveryHint = strings.TrimSpace(progress.RecoveryHint)
	if progress.Phase == "" && progress.Message == "" && progress.ToolID == "" && progress.ToolName == "" && progress.Error == "" && progress.Result == "" && progress.Iteration == 0 && progress.MaxTurns == 0 && progress.Model == "" && progress.RecoveryHint == "" {
		return items
	}
	out := append(append([]subagentProgressEvent{}, items...), progress)
	if len(out) > subagentProgressLimit {
		out = out[len(out)-subagentProgressLimit:]
	}
	return out
}

func cloneSubagentProgress(items []subagentProgressEvent) []subagentProgressEvent {
	if len(items) == 0 {
		return nil
	}
	return append([]subagentProgressEvent{}, items...)
}

func subagentFinishMessage(status subagentJobStatus) string {
	switch status {
	case subagentStatusCompleted:
		return "Subagent job completed."
	case subagentStatusPending:
		return "Subagent job queued."
	case subagentStatusPendingApproval:
		return "Subagent job is waiting for tool approval."
	case subagentStatusCanceled:
		return "Subagent job canceled."
	case subagentStatusInterrupted:
		return "Subagent job interrupted."
	case subagentStatusTimeout:
		return "Subagent job timed out."
	case subagentStatusError:
		return "Subagent job failed."
	default:
		return "Subagent job updated."
	}
}

func subagentResumeMessage(status subagentJobStatus) string {
	if status == subagentStatusPending {
		return "Subagent job queued for resume."
	}
	return "Subagent job resumed."
}

func previewSubagentText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= 400 {
		return text
	}
	return string(runes[:400]) + "..."
}

func subagentObjectiveFromPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	for _, block := range strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n\n") {
		block = strings.Join(strings.Fields(block), " ")
		if block == "" {
			continue
		}
		runes := []rune(block)
		if len(runes) > 96 {
			return string(runes[:96]) + "..."
		}
		return block
	}
	return ""
}

func subagentDisplayTitle(job *subagentJob) string {
	if job == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if job.Sequence > 0 {
		parts = append(parts, fmt.Sprintf("#%d", job.Sequence))
	}
	if label := firstNonEmpty(job.RoleName, job.RoleID, job.AgentType); label != "" {
		parts = append(parts, label)
	}
	if objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt)); objective != "" {
		parts = append(parts, objective)
	}
	return strings.Join(parts, " · ")
}

type subagentDiagnostics struct {
	ModelRequestCount int
	ToolCallCount     int
	LastRunnerPhase   string
	LastIteration     int
	LastRecoveryHint  string
}

func subagentDiagnosticsFromProgress(progress []subagentProgressEvent) subagentDiagnostics {
	var diagnostics subagentDiagnostics
	for _, item := range progress {
		switch item.Phase {
		case conversation.PhaseModelRequest:
			diagnostics.ModelRequestCount++
		case "tool_finished":
			diagnostics.ToolCallCount++
		}
		if isRunnerProgressPhase(item.Phase) {
			diagnostics.LastRunnerPhase = item.Phase
		}
		if item.Iteration > 0 {
			diagnostics.LastIteration = item.Iteration
		}
		if strings.TrimSpace(item.RecoveryHint) != "" {
			diagnostics.LastRecoveryHint = strings.TrimSpace(item.RecoveryHint)
		}
	}
	return diagnostics
}

func isRunnerProgressPhase(phase string) bool {
	switch phase {
	case conversation.PhaseModelRequest,
		conversation.PhaseAwaitingTools,
		conversation.PhaseToolsCompleted,
		conversation.PhaseFinalResponse,
		conversation.PhaseError,
		conversation.PhaseInterrupted,
		conversation.PhaseRecoveryAttempt:
		return true
	default:
		return false
	}
}

func newSubagentJobID(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("subagent_%d", now.UnixNano())
}

func cloneSubagentJob(job *subagentJob) *subagentJob {
	if job == nil {
		return nil
	}
	cloned := *job
	cloned.ToolNames = append([]string{}, job.ToolNames...)
	cloned.WriteScope = append([]string{}, job.WriteScope...)
	cloned.PreviewJobIDs = append([]string{}, job.PreviewJobIDs...)
	cloned.DefaultBundles = append([]string{}, job.DefaultBundles...)
	cloned.Messages = protocol.CloneMessages(job.Messages)
	cloned.Progress = cloneSubagentProgress(job.Progress)
	return &cloned
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
