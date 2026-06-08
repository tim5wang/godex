package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (a *Agent) appendLongTaskRepair(ctx context.Context, workflowID string, view longTaskView, storyID, reason string, maxAttempts int) (longTaskRepairSummary, longTaskView, error) {
	_ = ctx
	story, ok := longTaskStoryByID(view, storyID)
	if !ok {
		return longTaskRepairSummary{}, view, nil
	}
	if story.RepairAttempts >= maxAttempts {
		return longTaskRepairSummary{}, view, nil
	}
	spec, err := a.workflows.loadLongTaskSpec(workflowID)
	if err != nil {
		return longTaskRepairSummary{}, longTaskView{}, err
	}
	attempt := story.RepairAttempts + 1
	repairID := longTaskRepairNodeID(storyID, attempt)
	failedNodeID := firstNonEmpty(story.NodeID, storyID)
	summary := longTaskRepairSummary{
		StoryID:      story.ID,
		FailedNodeID: failedNodeID,
		RepairNodeID: repairID,
		Attempt:      attempt,
		Reason:       strings.TrimSpace(reason),
	}
	appended, err := a.appendWorkflowNodes(workflowID, []workflowNodeInput{{
		ID:              repairID,
		Kind:            "repair",
		Title:           "Repair " + storyID,
		Prompt:          renderLongTaskRepairPrompt(spec, story, failedNodeID, reason),
		HandoffPolicy:   workflowHandoffPolicySummaryArtifacts,
		HandoffFrom:     []string{failedNodeID},
		HandoffMaxBytes: workflowDefaultHandoffMaxBytes,
		AgentType:       storyAgentType(spec, storyID),
		WriteScope:      storyWriteScope(spec, storyID),
	}}, nil, "longtask-repair-"+repairID, failedNodeID, reason)
	if err != nil {
		return longTaskRepairSummary{}, longTaskView{}, err
	}
	state, err := a.workflows.load(workflowID)
	if err != nil {
		return longTaskRepairSummary{}, longTaskView{}, err
	}
	now := time.Now().UTC()
	rewired := false
	cascadedCancels := 0
	for i := range state.Nodes {
		node := &state.Nodes[i]
		if node.ID == repairID {
			continue
		}
		// Reference replacement applies to both DependsOn and HandoffFrom
		// regardless of node status. Downstream nodes that were already
		// running when the failure happened need to be cancelled and
		// re-queued so the new repair node is observed before they resume.
		depChanged := replaceWorkflowDep(node.DependsOn, failedNodeID, repairID)
		handoffChanged := replaceWorkflowDep(node.HandoffFrom, failedNodeID, repairID)
		if !depChanged && !handoffChanged {
			continue
		}
		rewired = true
		if node.Status == workflowStatusRunning && strings.TrimSpace(node.JobID) != "" {
			if _, cancelErr := a.subagentJobs.Cancel(node.JobID); cancelErr == nil {
				cascadedCancels++
				_ = a.workflows.appendEvent(workflowID, map[string]interface{}{
					"event":      "repair_cascade_cancel",
					"story_id":   storyID,
					"node_id":    node.ID,
					"job_id":     node.JobID,
					"failed":     failedNodeID,
					"repair":     repairID,
					"cascade_at": now,
				})
			}
			node.Status = workflowStatusPending
			node.JobID = ""
			node.IdentityID = ""
			node.AgentIdentity = AgentIdentity{}
			node.Error = ""
			node.HandoffRef = ""
			node.HandoffDigest = ""
			node.ResultPreview = ""
			node.Verdict = ""
			node.Attempt = 0
			node.CreatedAt = now
			node.UpdatedAt = now
			node.FinishedAt = time.Time{}
		}
	}
	if rewired {
		state.Summary.UpdatedAt = now
		if err := a.workflows.save(state); err != nil {
			return longTaskRepairSummary{}, longTaskView{}, err
		}
		return summary, a.longTaskViewFromSpec(spec, workflowViewFromState(state)), nil
	}
	return summary, a.longTaskViewFromSpec(spec, appended), nil
}

func longTaskStoryByID(view longTaskView, storyID string) (longTaskStoryView, bool) {
	for _, story := range view.Stories {
		if story.ID == storyID {
			return story, true
		}
	}
	return longTaskStoryView{}, false
}

func replaceWorkflowDep(deps []string, oldID, newID string) bool {
	changed := false
	for i := range deps {
		if deps[i] == oldID {
			deps[i] = newID
			changed = true
		}
	}
	return changed
}

func longTaskRepairNodeID(storyID string, attempt int) string {
	return fmt.Sprintf("%s_repair_%d", storyID, attempt)
}

func renderLongTaskRepairPrompt(spec longTaskSpec, story longTaskStoryView, failedNodeID, reason string) string {
	var builder strings.Builder
	builder.WriteString("You are executing a fresh repair attempt for one Ralph-style GoDex long-task story. Fix this story only.\n\n")
	if spec.Project != "" {
		builder.WriteString("Project: ")
		builder.WriteString(spec.Project)
		builder.WriteString("\n")
	}
	if spec.Description != "" {
		builder.WriteString("Long task description: ")
		builder.WriteString(spec.Description)
		builder.WriteString("\n")
	}
	builder.WriteString("Story ID: ")
	builder.WriteString(story.ID)
	if story.Title != "" {
		builder.WriteString("\nTitle: ")
		builder.WriteString(story.Title)
	}
	if story.Description != "" {
		builder.WriteString("\nDescription: ")
		builder.WriteString(story.Description)
	}
	if len(story.AcceptanceCriteria) > 0 {
		builder.WriteString("\nAcceptance criteria:")
		for _, item := range story.AcceptanceCriteria {
			builder.WriteString("\n- ")
			builder.WriteString(item)
		}
	}
	builder.WriteString("\n\nFailed attempt node: ")
	builder.WriteString(failedNodeID)
	if reason != "" {
		builder.WriteString("\nFailure reason: ")
		builder.WriteString(reason)
	}
	if story.ValidationRef != "" {
		builder.WriteString("\nValidation artifact: ")
		builder.WriteString(story.ValidationRef)
	}
	if story.ResultPreview != "" {
		builder.WriteString("\nPrevious result preview:\n")
		builder.WriteString(story.ResultPreview)
	}
	if len(spec.QualityChecks) > 0 {
		builder.WriteString("\n\nRequired quality checks before reporting pass:")
		for _, check := range spec.QualityChecks {
			builder.WriteString("\n- ")
			builder.WriteString(check)
		}
	}
	builder.WriteString("\n\nCompletion contract:\n- Keep changes minimal and focused on this repair.\n- Finish with an explicit line: Verdict: pass|fail|blocked|needs_fix.\n- Include a compact summary, changed files, validation run, and reusable learnings.\n")
	return builder.String()
}

func longTaskAllStoriesPass(view longTaskView) bool {
	if len(view.Stories) == 0 {
		return false
	}
	for _, story := range view.Stories {
		if !story.Passes {
			return false
		}
	}
	return true
}

func longTaskBlockedStory(view longTaskView) (string, string) {
	for _, story := range view.Stories {
		if story.Status == workflowStatusError {
			return story.ID, firstNonEmpty(story.Error, "story entered error state")
		}
		if story.ValidationStatus == longTaskValidationFail {
			return story.ID, "validation failed"
		}
		if story.Status == workflowStatusCompleted {
			switch normalizeWorkflowVerdict(story.Verdict) {
			case workflowVerdictBlocked:
				return story.ID, "story reported blocked"
			case workflowVerdictNeedsFix:
				return story.ID, "story reported needs_fix"
			case workflowVerdictFail:
				return story.ID, "story reported fail"
			}
		}
	}
	return "", ""
}
