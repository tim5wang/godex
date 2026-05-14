package tui

import (
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/agent"
)

type workbenchTab int

const (
	workbenchTabTask workbenchTab = iota
	workbenchTabWorkers
	workbenchTabGraph
	workbenchTabDiff
	workbenchTabLogs
)

type workbenchSummary struct {
	Plan   []string
	Active []string
	Review []string
}

func (m *model) buildWorkbenchSummary() workbenchSummary {
	summary := workbenchSummary{
		Plan:   m.workbenchPlanLines(),
		Active: m.workbenchActiveLines(),
		Review: m.workbenchReviewLines(),
	}
	return summary
}

func (m *model) workbenchPlanLines() []string {
	lines := make([]string, 0, len(m.longTasks)+len(m.snapshot.Tasks)+len(m.snapshot.Todos)+len(m.snapshot.QueuedTurns)+1)
	for _, item := range m.longTasks {
		title := firstNonBlank(item.Project, item.Description, item.WorkflowID, item.LongTaskID)
		if title == "" {
			title = "longtask"
		}
		status := strings.TrimSpace(item.Status)
		if status == "" {
			status = "unknown"
		}
		lines = append(lines, fmt.Sprintf("%s · %s %d/%d · pending %d · failed %d", title, status, item.Running, item.Total, item.Pending, item.Failed))
	}
	for _, item := range m.snapshot.Tasks {
		if item == nil {
			continue
		}
		title := strings.TrimSpace(item.Subject)
		if title == "" {
			title = fmt.Sprintf("task-%d", item.ID)
		}
		if title != "" {
			lines = append(lines, "task · "+title)
		}
	}
	for _, item := range m.snapshot.Todos {
		title := strings.TrimSpace(item.Content)
		if title != "" {
			lines = append(lines, "todo · "+title)
		}
	}
	for _, item := range m.snapshot.QueuedTurns {
		title := firstNonBlank(item.Summary, item.ID)
		if title != "" {
			lines = append(lines, fmt.Sprintf("%s · %s", item.ID, title))
		}
	}
	if len(lines) == 0 {
		return []string{"No active plan. Start with a request or run a longtask."}
	}
	return lines
}

func (m *model) workbenchActiveLines() []string {
	lines := make([]string, 0, len(m.subagents)+4)
	if m.snapshot.Running {
		turn := strings.TrimSpace(m.snapshot.ActiveTurnID)
		if turn == "" {
			turn = "active turn"
		}
		phase := strings.TrimSpace(m.snapshot.ActivePhase)
		if phase == "" {
			phase = "running"
		}
		lines = append(lines, fmt.Sprintf("%s · %s", turn, phase))
	}
	for _, job := range m.subagents {
		if !isActiveSubagent(job) {
			continue
		}
		lines = append(lines, formatSubagentExecutionLine(job))
	}
	if len(lines) == 0 {
		return []string{"Idle"}
	}
	return lines
}

func (m *model) workbenchReviewLines() []string {
	lines := make([]string, 0, len(m.snapshot.PendingPermissions)+len(m.subagents)+1)
	for _, pending := range m.snapshot.PendingPermissions {
		title := strings.TrimSpace(pending.ID)
		if title == "" {
			title = "permission"
		}
		action := strings.TrimSpace(pending.Request.Action)
		if action == "" {
			action = "approval"
		}
		tool := strings.TrimSpace(pending.Request.ToolName)
		if tool != "" {
			action = tool + " " + action
		}
		lines = append(lines, fmt.Sprintf("%s · pending %s", title, action))
	}
	for _, job := range m.subagents {
		if !isReviewableSubagent(job) {
			continue
		}
		title := firstNonBlank(job.DisplayTitle, job.Objective, job.JobID)
		status := firstNonBlank(job.MergeStatus, job.Status)
		lines = append(lines, fmt.Sprintf("%s · %s · %s", job.JobID, title, status))
	}
	if len(lines) == 0 {
		return []string{"Nothing waiting for review"}
	}
	return lines
}

func isActiveSubagent(job agent.DurableSubagentJobView) bool {
	switch strings.ToLower(strings.TrimSpace(job.Status)) {
	case "running", "pending", "resuming":
		return true
	default:
		return false
	}
}

func isReviewableSubagent(job agent.DurableSubagentJobView) bool {
	status := strings.ToLower(strings.TrimSpace(job.Status))
	merge := strings.ToLower(strings.TrimSpace(job.MergeStatus))
	if merge != "" && merge != "merged" {
		return true
	}
	return status == "completed" || status == "failed" || status == "cancelled"
}

func formatSubagentExecutionLine(job agent.DurableSubagentJobView) string {
	title := firstNonBlank(job.DisplayTitle, job.Objective, job.JobID)
	parts := []string{job.JobID}
	if title != "" && title != job.JobID {
		parts = append(parts, title)
	}
	if job.LastPhase != "" {
		parts = append(parts, job.LastPhase)
	} else if job.Status != "" {
		parts = append(parts, job.Status)
	}
	if job.WorkerID != "" {
		parts = append(parts, job.WorkerID)
	}
	if job.SandboxID != "" {
		parts = append(parts, job.SandboxID)
	}
	if job.WorkerBranchID != "" {
		parts = append(parts, job.WorkerBranchID)
	}
	if job.SourceBranchID != "" {
		parts = append(parts, "source "+job.SourceBranchID)
	}
	return strings.Join(nonEmptyStrings(parts), " · ")
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
