package agent

import (
	"fmt"
	"strings"
)

func (a *Agent) longTaskViewForState(state workflowState) (longTaskView, error) {
	return a.longTaskViewForWorkflow(workflowViewFromState(state))
}

func (a *Agent) longTaskViewForWorkflow(workflow workflowView) (longTaskView, error) {
	spec, err := a.workflows.loadLongTaskSpec(workflow.WorkflowID)
	if err != nil {
		return longTaskView{}, err
	}
	return a.longTaskViewFromSpec(spec, workflow), nil
}

func (a *Agent) longTaskViewFromSpec(spec longTaskSpec, workflow workflowView) longTaskView {
	view := longTaskView{
		LongTaskID:    spec.ID,
		WorkflowID:    spec.WorkflowID,
		Project:       spec.Project,
		BranchName:    spec.BranchName,
		Description:   spec.Description,
		QualityChecks: append([]string{}, spec.QualityChecks...),
		Status:        workflow.Status,
		Total:         workflow.Total,
		Pending:       workflow.Pending,
		Running:       workflow.Running,
		Completed:     workflow.Completed,
		Failed:        workflow.Failed,
		Workflow:      workflow,
	}
	for _, story := range spec.Stories {
		node, repairs := latestLongTaskStoryNode(workflow, story.ID)
		validationStatus, validationRef := a.longTaskStoryValidationView(spec, workflow.WorkflowID, node)
		mergeStatus, commitStatus, commitHash, commitRef := a.longTaskStoryCommitView(workflow.WorkflowID, node)
		passes := node.Status == workflowStatusCompleted &&
			normalizeWorkflowVerdict(node.Verdict) == workflowVerdictPass &&
			(validationStatus == longTaskValidationPass || validationStatus == longTaskValidationSkipped) &&
			longTaskMergeCommitAllowsPass(mergeStatus, commitStatus)
		view.Stories = append(view.Stories, LongTaskStoryView{
			ID:                 story.ID,
			NodeID:             node.ID,
			RepairAttempts:     repairs,
			Title:              story.Title,
			Description:        story.Description,
			AcceptanceCriteria: append([]string{}, story.AcceptanceCriteria...),
			Priority:           story.Priority,
			DependsOn:          append([]string{}, story.DependsOn...),
			Status:             node.Status,
			Passes:             passes,
			Verdict:            node.Verdict,
			JobID:              node.JobID,
			HandoffRef:         node.HandoffRef,
			ResultPreview:      node.ResultPreview,
			Error:              node.Error,
			ValidationStatus:   validationStatus,
			ValidationRef:      validationRef,
			MergeStatus:        mergeStatus,
			CommitStatus:       commitStatus,
			CommitHash:         commitHash,
			CommitRef:          commitRef,
			UpdatedAt:          node.UpdatedAt,
		})
	}
	return view
}

func latestLongTaskStoryNode(workflow workflowView, storyID string) (workflowNodeView, int) {
	var base workflowNodeView
	var latest workflowNodeView
	latestRepair := 0
	prefix := storyID + "_repair_"
	for _, node := range workflow.Nodes {
		if node.ID == storyID {
			base = node
			continue
		}
		if !strings.HasPrefix(node.ID, prefix) {
			continue
		}
		var attempt int
		if _, err := fmt.Sscanf(strings.TrimPrefix(node.ID, prefix), "%d", &attempt); err != nil || attempt <= 0 {
			continue
		}
		if attempt > latestRepair {
			latestRepair = attempt
			latest = node
		}
	}
	if latestRepair > 0 {
		return latest, latestRepair
	}
	return base, 0
}

func (a *Agent) longTaskStoryValidationView(spec longTaskSpec, workflowID string, node workflowNodeView) (string, string) {
	if node.ID == "" {
		return "", ""
	}
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	ref := longTaskValidationRef(node.ID, attempt)
	if validation, err := a.workflows.loadLongTaskValidation(workflowID, node.ID, attempt); err == nil {
		return validation.Status, ref
	}
	if node.Status == workflowStatusCompleted && normalizeWorkflowVerdict(node.Verdict) == workflowVerdictPass {
		if len(spec.QualityChecks) == 0 {
			return longTaskValidationSkipped, ""
		}
		return longTaskValidationPending, ref
	}
	return "", ref
}

func (a *Agent) longTaskStoryCommitView(workflowID string, node workflowNodeView) (string, string, string, string) {
	if node.ID == "" {
		return "", "", "", ""
	}
	attempt := node.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	ref := longTaskCommitRef(node.ID, attempt)
	artifact, err := a.workflows.loadLongTaskCommit(workflowID, node.ID, attempt)
	if err != nil {
		if node.Status == workflowStatusCompleted && normalizeWorkflowVerdict(node.Verdict) == workflowVerdictPass {
			return "", "", "", ref
		}
		return "", "", "", ""
	}
	return artifact.MergeStatus, artifact.CommitStatus, artifact.CommitHash, ref
}

func longTaskMergeCommitAllowsPass(mergeStatus, commitStatus string) bool {
	switch mergeStatus {
	case "", longTaskMergeSkippedNoJob, longTaskMergeSkippedNoScope, subagentMergeMerged, subagentMergeNoChanges:
	default:
		return false
	}
	return commitStatus != longTaskCommitFailed
}
