package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (a *Agent) finalizeLongTaskStory(ctx context.Context, workflowID, nodeID string) (longTaskView, error) {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return longTaskView{}, err
	}
	spec, err := a.workflows.loadLongTaskSpec(state.Summary.ID)
	if err != nil {
		return longTaskView{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return longTaskView{}, fmt.Errorf("missing node_id")
	}
	nodeIndex := -1
	for i := range state.Nodes {
		if state.Nodes[i].ID == nodeID {
			nodeIndex = i
			break
		}
	}
	if nodeIndex < 0 {
		return longTaskView{}, fmt.Errorf("longtask story not found: %s", nodeID)
	}
	node := &state.Nodes[nodeIndex]
	if node.Status != workflowStatusCompleted {
		return longTaskView{}, fmt.Errorf("longtask story %s is not completed", nodeID)
	}
	if normalizeWorkflowVerdict(node.Verdict) != workflowVerdictPass {
		return a.longTaskViewForState(state)
	}
	validation, err := a.runLongTaskValidation(ctx, spec, *node)
	if err != nil {
		return longTaskView{}, err
	}
	if err := a.workflows.writeLongTaskValidation(state.Summary.ID, validation); err != nil {
		return longTaskView{}, err
	}
	if validation.Status == longTaskValidationFail {
		now := time.Now().UTC()
		node.Status = workflowStatusError
		node.Error = "longtask validation failed"
		node.UpdatedAt = now
		node.FinishedAt = now
		state.Summary.UpdatedAt = now
		a.refreshWorkflowStatus(&state)
		if err := a.workflows.save(state); err != nil {
			return longTaskView{}, err
		}
	}
	if validation.Status == longTaskValidationPass || validation.Status == longTaskValidationSkipped {
		commitArtifact, commitErr := a.finalizeLongTaskMergeCommit(ctx, spec, *node)
		if commitErr != nil {
			return longTaskView{}, commitErr
		}
		if err := a.workflows.writeLongTaskCommit(state.Summary.ID, commitArtifact); err != nil {
			return longTaskView{}, err
		}
		if longTaskCommitBlocksStory(commitArtifact) {
			now := time.Now().UTC()
			node.Status = workflowStatusError
			node.Error = firstNonEmpty(commitArtifact.Error, "longtask merge/commit failed")
			node.UpdatedAt = now
			node.FinishedAt = now
			state.Summary.UpdatedAt = now
			a.refreshWorkflowStatus(&state)
			if err := a.workflows.save(state); err != nil {
				return longTaskView{}, err
			}
		}
		_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "longtask_merge_commit", "node_id": nodeID, "merge_status": commitArtifact.MergeStatus, "commit_status": commitArtifact.CommitStatus, "commit_hash": commitArtifact.CommitHash, "commit_ref": longTaskCommitRef(nodeID, commitArtifact.Attempt), "at": commitArtifact.CreatedAt})
	}
	a.gcLongTaskStoryWorktree(node)
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]interface{}{"event": "longtask_validation", "node_id": nodeID, "status": validation.Status, "validation_ref": longTaskValidationRef(nodeID, validation.Attempt), "at": validation.CreatedAt})
	return a.longTaskViewForState(state)
}

func (a *Agent) gcLongTaskStoryWorktree(node *workflowNode) {
	if strings.TrimSpace(node.JobID) == "" {
		return
	}
	job, err := a.subagentJobs.Get(node.JobID)
	if err != nil || strings.TrimSpace(job.WorktreeDir) == "" {
		return
	}
	_, _ = a.CleanupDurableSubagentWorkspace(node.JobID)
}

func (a *Agent) gcLongTaskWorktrees(workflowID string) (int, error) {
	state, err := a.workflowState(workflowID)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for i := range state.Nodes {
		node := &state.Nodes[i]
		if node.Status != workflowStatusCompleted && node.Status != workflowStatusError {
			continue
		}
		if strings.TrimSpace(node.JobID) == "" {
			continue
		}
		if cleanup, err := a.CleanupDurableSubagentWorkspace(node.JobID); err == nil && cleanup.Cleaned {
			cleaned++
		}
	}
	return cleaned, nil
}

func (a *Agent) finalizeLongTaskMergeCommit(ctx context.Context, spec longTaskSpec, node workflowNode) (longTaskCommitArtifact, error) {
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	artifact := longTaskCommitArtifact{
		WorkflowID:   spec.WorkflowID,
		NodeID:       node.ID,
		Attempt:      attempt,
		JobID:        node.JobID,
		MergePolicy:  normalizeLongTaskMergePolicy(spec.MergePolicy),
		CommitPolicy: normalizeLongTaskCommitPolicy(spec.CommitPolicy),
		CreatedAt:    time.Now().UTC(),
	}
	if strings.TrimSpace(node.JobID) == "" {
		artifact.MergeStatus = longTaskMergeSkippedNoJob
		artifact.CommitStatus = longTaskCommitSkippedNoChange
		return artifact, nil
	}
	job, err := a.subagentJobs.Get(node.JobID)
	if err != nil {
		artifact.MergeStatus = "failed"
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = err.Error()
		return artifact, nil
	}
	if len(job.WriteScope) == 0 {
		artifact.MergeStatus = longTaskMergeSkippedNoScope
		artifact.CommitStatus = longTaskCommitSkippedNoChange
		return artifact, nil
	}
	if artifact.MergePolicy == longTaskMergePolicyReviewOnly {
		review, reviewErr := a.ReviewDurableSubagent(node.JobID)
		if reviewErr != nil {
			artifact.MergeStatus = "failed"
			artifact.CommitStatus = longTaskCommitFailed
			artifact.Error = reviewErr.Error()
			return artifact, nil
		}
		artifact.MergeStatus = longTaskMergeSkippedReview
		artifact.ChangedFiles = append([]subagentFileChange{}, review.Changes...)
		artifact.CommitStatus = longTaskCommitSkippedDisabled
		artifact.Error = "merge_policy review_only requires manual review/merge"
		return artifact, nil
	}
	merge, err := a.MergeDurableSubagentWithContext(ctx, node.JobID)
	if err != nil {
		artifact.MergeStatus = "failed"
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = err.Error()
		return artifact, nil
	}
	artifact.MergeStatus = merge.Status
	artifact.ChangedFiles = append([]subagentFileChange{}, merge.Applied...)
	if merge.Status == subagentMergeConflict {
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = strings.Join(merge.Conflicts, "\n")
		return artifact, nil
	}
	if merge.Status != subagentMergeMerged && merge.Status != subagentMergeNoChanges {
		artifact.CommitStatus = longTaskCommitFailed
		artifact.Error = "subagent merge did not complete"
		return artifact, nil
	}
	if artifact.CommitPolicy == longTaskCommitPolicyNone {
		artifact.CommitStatus = longTaskCommitSkippedDisabled
		return artifact, nil
	}
	if len(artifact.ChangedFiles) == 0 || merge.Status == subagentMergeNoChanges {
		artifact.CommitStatus = longTaskCommitSkippedNoChange
		return artifact, nil
	}
	if !longTaskGitRepo(a.cfg.WorkspaceDir) {
		artifact.CommitStatus = longTaskCommitSkippedNoGit
		return artifact, nil
	}
	message := longTaskCommitMessage(spec, node)
	hash, err := longTaskGitCommit(ctx, a.cfg.WorkspaceDir, artifact.ChangedFiles, message)
	if err != nil {
		artifact.CommitStatus = longTaskCommitFailed
		artifact.CommitMessage = message
		artifact.Error = err.Error()
		return artifact, nil
	}
	artifact.CommitStatus = longTaskCommitCommitted
	artifact.CommitHash = hash
	artifact.CommitMessage = message
	return artifact, nil
}
