package agent

import (
	"strings"

	"github.com/tim5wang/godex/internal/workerruntime"
)

const localGoDexWorkerID = "worker:godex:local"

func workerRequestFromSubagentStartOptions(start subagentStartOptions) workerruntime.JobRequest {
	return workerruntime.JobRequest{
		WorkerID:       firstNonEmpty(strings.TrimSpace(start.WorkerID), localGoDexWorkerID),
		SessionID:      strings.TrimSpace(start.SessionID),
		ParentTurnID:   strings.TrimSpace(start.ParentTurnID),
		ParentID:       strings.TrimSpace(start.ParentID),
		AgentType:      strings.TrimSpace(start.AgentType),
		RoleID:         strings.TrimSpace(start.RoleID),
		RoleName:       strings.TrimSpace(start.RoleName),
		PackageName:    strings.TrimSpace(start.PackageName),
		Prompt:         strings.TrimSpace(start.Prompt),
		BasePrompt:     strings.TrimSpace(start.BasePrompt),
		PreviewJobIDs:  append([]string{}, start.PreviewJobIDs...),
		RuntimeContext: start.RuntimeContext.Clone(),
		ModelHint:      strings.TrimSpace(start.ModelHint),
		BudgetHint:     strings.TrimSpace(start.BudgetHint),
		Display:        cloneStringMap(start.Display),
		MaxTurns:       start.MaxTurns,
		JobTimeoutMS:   start.JobTimeoutMS,
		Capabilities: workerruntime.CapabilitySet{
			ToolNames:         append([]string{}, start.ToolNames...),
			RequiredBundles:   append([]string{}, start.RequiredBundles...),
			RequiredTools:     append([]string{}, start.RequiredTools...),
			DefaultBundles:    append([]string{}, start.DefaultBundles...),
			BundleOverrides:   append([]string{}, start.BundleOverrides...),
			DeactivateBundles: append([]string{}, start.DeactivateBundles...),
			ToolPolicy:        append([]string{}, start.ToolPolicy...),
			WriteScope:        append([]string{}, start.WriteScope...),
			SandboxID:         strings.TrimSpace(start.SandboxID),
		},
	}.Clone()
}

func subagentStartOptionsFromWorkerRequest(req workerruntime.JobRequest, maxConcurrent int) subagentStartOptions {
	req = req.Clone()
	return subagentStartOptions{
		SessionID:       req.SessionID,
		ParentTurnID:    req.ParentTurnID,
		ParentID:        req.ParentID,
		AgentType:       req.AgentType,
		RoleID:          req.RoleID,
		RoleName:        req.RoleName,
		PackageName:     req.PackageName,
		Prompt:          req.Prompt,
		BasePrompt:      req.BasePrompt,
		ToolNames:         append([]string{}, req.Capabilities.ToolNames...),
		WriteScope:        append([]string{}, req.Capabilities.WriteScope...),
		PreviewJobIDs:     append([]string{}, req.PreviewJobIDs...),
		RequiredBundles:   append([]string{}, req.Capabilities.RequiredBundles...),
		RequiredTools:     append([]string{}, req.Capabilities.RequiredTools...),
		DefaultBundles:    append([]string{}, req.Capabilities.DefaultBundles...),
		BundleOverrides:   append([]string{}, req.Capabilities.BundleOverrides...),
		DeactivateBundles: append([]string{}, req.Capabilities.DeactivateBundles...),
		ToolPolicy:        append([]string{}, req.Capabilities.ToolPolicy...),
		SandboxID:       req.Capabilities.SandboxID,
		WorkerID:        firstNonEmpty(req.WorkerID, localGoDexWorkerID),
		ModelHint:       req.ModelHint,
		BudgetHint:      req.BudgetHint,
		ContextBudget:   roleContextBudgetTokens(req.RoleID, req.AgentType),
		Display:         cloneStringMap(req.Display),
		RuntimeContext:  req.RuntimeContext.Clone(),
		MaxTurns:        req.MaxTurns,
		MaxConcurrent:   maxConcurrent,
		JobTimeoutMS:    req.JobTimeoutMS,
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
			ToolNames:         append([]string{}, job.ToolNames...),
			DefaultBundles:    append([]string{}, job.DefaultBundles...),
			BundleOverrides:   append([]string{}, job.BundleOverrides...),
			DeactivateBundles: append([]string{}, job.DeactivateBundles...),
			ToolPolicy:        append([]string{}, job.ToolPolicy...),
			WriteScope:        append([]string{}, job.WriteScope...),
			SandboxID:         job.SandboxID,
		},
	}
}

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
