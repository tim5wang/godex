package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/sandbox"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/workerruntime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
		SessionID:       target.sessionID,
		ParentTurnID:    target.turnID,
		ParentID:        target.turnID,
		AgentType:       req.AgentType,
		Prompt:          prompt,
		ToolNames:       subagentToolNamesForRole(req.AgentType, nil),
		WriteScope:      req.WriteScope,
		PreviewJobIDs:   req.PreviewJobIDs,
		RequiredBundles: req.RequiredBundles,
		RequiredTools:   req.RequiredTools,
		RuntimeContext:  runtimeCtx,
		SandboxID:       a.SandboxID(),
		MaxTurns:        a.normalizeSubagentMaxTurns(req.MaxTurns),
		MaxConcurrent:   a.subagentMaxConcurrentJobs(),
		JobTimeoutMS:    a.normalizeSubagentJobTimeoutMS(req.JobTimeoutMS),
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
		start.ContextBudget = roleContextBudgetTokens(role.ID, req.AgentType)
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
	start.WorkerID = localGoDexWorkerID
	handle, err := a.WorkerRuntime().Dispatch(ctx, workerRequestFromSubagentStartOptions(start))
	if err != nil {
		return nil, err
	}
	job, err := a.subagentJobs.Get(handle.JobID)
	if err != nil {
		return &subagentJob{
			ID:       handle.JobID,
			WorkerID: handle.WorkerID,
			Status:   subagentJobStatus(handle.Status),
		}, nil
	}
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
	handle, err := a.WorkerRuntime().Resume(ctx, workerruntime.JobRef{JobID: id, WorkerID: localGoDexWorkerID})
	if err != nil {
		return nil, err
	}
	job, err := a.subagentJobs.Get(handle.JobID)
	if err != nil {
		return &subagentJob{
			ID:       handle.JobID,
			WorkerID: handle.WorkerID,
			Status:   subagentJobStatus(handle.Status),
		}, nil
	}
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

	// Acquire a lease for this run. When the lease store is not wired
	// (leased=false), keep the legacy behaviour (no leasing).
	leaseHolder, leased, _ := a.subagentJobs.AcquireLease(id)
	runCtx := ctx
	leaseRelease := func() {}
	if leased {
		var runCancel context.CancelFunc
		runCtx, runCancel = context.WithCancel(ctx)
		go a.subagentLeaseBeatLoop(runCtx, id, leaseHolder.Token, runCancel)
		leaseRelease = func() {
			runCancel()
			a.subagentJobs.ReleaseLease(id, leaseHolder.Token)
		}
	}

	go func() {
		defer func() {
			leaseRelease()
			a.subagentJobs.ClearActive(id)
			a.startPendingSubagents(target.sink)
		}()
		a.runSubagentJob(runCtx, id, target)
	}()
}

// subagentLeaseLostLimit is the number of consecutive missed heartbeats
// before an in-process subagent run is cancelled.
const subagentLeaseLostLimit = 3

// subagentLeaseBeatLoop renews the job lease every ttl/3 and cancels the run
// once the lease is lost on subagentLeaseLostLimit consecutive beats. The loop
// exits as soon as runCtx is done (job finished/released) so no heartbeat
// goroutine outlives the run it serves.
func (a *Agent) subagentLeaseBeatLoop(runCtx context.Context, id, token string, cancel context.CancelFunc) {
	ttl := a.subagentJobs.LeaseTTL()
	interval := ttl / 3
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutiveLost := 0
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			alive, _ := a.subagentJobs.HeartbeatLease(token)
			if alive {
				consecutiveLost = 0
				continue
			}
			consecutiveLost++
			if consecutiveLost >= subagentLeaseLostLimit {
				cancel()
				return
			}
		}
	}
}

// maybeCompactSubagentMessages compacts a subagent's accumulated messages when
// their token estimate exceeds the job's role context budget (roadmap 4.6).
// It runs the rule-based summarizer (no model round-trip) and persists the
// compacted history so a crash mid-run keeps the compressed view. Returns the
// message slice to use for the next request (compacted when over budget).
func (a *Agent) maybeCompactSubagentMessages(ctx context.Context, job *subagentJob, messages []protocol.Message, target subagentEventTarget) []protocol.Message {
	if job == nil || job.ContextBudget <= 0 || len(messages) == 0 {
		return messages
	}
	if estimateMessages(messages) <= job.ContextBudget {
		return messages
	}
	if a == nil || a.compressor == nil {
		return messages
	}
	summarizer := compress.NewRuleBasedSessionSummarizer(a.compressor)
	result, err := summarizer.SummarizeSession(ctx, compress.SessionSummaryRequest{
		History: messages,
	})
	if err != nil || len(result.Messages) == 0 {
		return messages
	}
	// Rule-based summarization may keep the same message count (large tool
	// results become transcript references); require a real token reduction
	// before checkpointing the compacted history.
	if estimateMessages(result.Messages) >= estimateMessages(messages) {
		return messages
	}
	_ = a.subagentJobs.UpdateMessages(job.ID, result.Messages)
	a.recordSubagentProgress(job.ID, target, subagentProgressEvent{
		Phase:   "context_budget_compact",
		Message: "Subagent context compacted to fit role budget.",
	})
	return result.Messages
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
	ctx = conversation.WithUsageContext(ctx, a.usageContext(job.RuntimeContext, job.SessionID, "", job.ID))
	if strings.TrimSpace(job.SandboxID) != "" {
		ctx = tools.WithSandboxID(ctx, job.SandboxID)
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
			messages = a.maybeCompactSubagentMessages(ctx, job, messages, target)
			req := conversation.NewRequest(a.cfg.Model, a.cfg.MaxTokens, a.cfg.ReasoningEffort, prompts.Build(), messages, a.toolHandler.ActiveSchemas(job.ToolNames...))
			if sid := strings.TrimSpace(job.SessionID); sid != "" {
				req.PromptCacheKey = clampCacheKey(sid + ":" + job.ID)
				req.PromptCacheRetention = protocol.CacheRetentionShort
			}
			return req, nil
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
		return a.subagentJobs.SetWorkspace(job.ID, workspace, "", subagentIsolationSharedReadOnly, "", "shared_workspace", sandbox.StableLocalID(workspace))
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
		return a.subagentJobs.SetWorkspace(job.ID, workspace, baselineDir, subagentIsolationSharedApproval, "", "non_git_shared_with_approval", sandbox.StableLocalID(workspace))
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
	return a.subagentJobs.SetWorkspace(job.ID, worktreeDir, baselineDir, isolation, gitBranch, origin, sandbox.StableLocalID(worktreeDir))
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
