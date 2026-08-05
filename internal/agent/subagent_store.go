package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
		WorkerID:        firstNonEmpty(strings.TrimSpace(opts.WorkerID), localGoDexWorkerID),
		SandboxID:       strings.TrimSpace(opts.SandboxID),
		SourceBranchID:  strings.TrimSpace(opts.RuntimeContext.Metadata[subagentSessionGraphBranchMetadataKey]),
		SourceNodeID:    strings.TrimSpace(opts.RuntimeContext.Metadata[subagentSessionGraphNodeMetadataKey]),
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
	if job.SourceBranchID == "" && job.SourceNodeID != "" {
		job.SourceBranchID = "branch:main"
	}
	if job.SourceBranchID != "" || job.SourceNodeID != "" {
		job.WorkerBranchID = firstNonEmpty(strings.TrimSpace(opts.RuntimeContext.Metadata["worker_branch_id"]), "branch:"+job.ID)
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

func (s *subagentJobStore) SetWorkspace(id, worktreeDir, baselineDir, isolation, gitBranch, workspaceOrigin, sandboxID string) (*subagentJob, error) {
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
	job.SandboxID = strings.TrimSpace(sandboxID)
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
