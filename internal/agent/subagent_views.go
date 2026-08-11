package agent

import (
	"context"
	"fmt"
	"strings"
)

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
		WorkerID:          firstNonEmpty(job.WorkerID, localGoDexWorkerID),
		SandboxID:         job.SandboxID,
		SourceBranchID:    job.SourceBranchID,
		SourceNodeID:      job.SourceNodeID,
		WorkerBranchID:    job.WorkerBranchID,
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
		ContextBudget:     job.ContextBudget,
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
	// Path-A: a read-only subagent (no write_scope) that has finished has
	// nothing in its worktree to merge or review. Surface that as
	// "no_changes" so the Web frontend's classifyWorkerStatus (which
	// promotes an empty/pending mergeStatus to "ready_for_review") does not
	// surface an un-actionable "待审核" badge in the longtask panel.
	// Already-recorded terminal merge statuses (merged/failed/conflicted)
	// are preserved — this normalizer only fills the pending/empty slot.
	if (view.MergeStatus == "" || view.MergeStatus == subagentMergePending) && view.Status == string(subagentStatusCompleted) && len(job.WriteScope) == 0 {
		view.MergeStatus = subagentMergeNoChanges
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
