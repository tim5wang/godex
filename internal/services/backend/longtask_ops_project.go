package backend

import (
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

// projectSubagentRow converts an agent-layer DurableSubagentJobView
// into the mintui-shaped SubagentRow.
func projectSubagentRow(v agent.DurableSubagentJobView) SubagentRow {
	return SubagentRow{
		JobID:          v.JobID,
		DisplayTitle:   v.DisplayTitle,
		Objective:      v.Objective,
		Status:         v.Status,
		MergeStatus:    v.MergeStatus,
		LastPhase:      v.LastPhase,
		LastMessage:    v.LastMessage,
		Result:         v.Result,
		Error:          v.Error,
		WorkerID:       v.WorkerID,
		SandboxID:      v.SandboxID,
		SourceBranchID: v.SourceBranchID,
		WorkerBranchID: v.WorkerBranchID,
		CreatedAt:      v.CreatedAt,
		UpdatedAt:      v.UpdatedAt,
	}
}

// projectRollbackResult converts an agent rollback result.
func projectRollbackResult(r agent.LongTaskRollbackResult) LongTaskRollbackResult {
	return LongTaskRollbackResult{
		Success:      strings.TrimSpace(r.ConflictRef) == "" && !r.Conflict,
		StoryID:      r.StoryID,
		CommitRevert: r.CommitHash,
		Error:        r.Message,
	}
}

// projectGCSweepResult converts an agent GC sweep result.
func projectGCSweepResult(r agent.LongTaskGCSweepResult) LongTaskGCSweepResult {
	return LongTaskGCSweepResult{
		WorkflowID:   r.WorkflowID,
		RemovedCount: r.DeletedRuns + r.DeletedIndexes,
		RemovedBytes: 0,
		KeptCount:    r.Retained,
		DryRun:       r.DryRun,
	}
}
