package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/protocol"
)

const (
	maxContinuationSnapshotRunes = 5000
	maxContinuationItems         = 8
)

func (a *Agent) continuationSnapshot(sessionID string, history []protocol.Message) string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("This deterministic snapshot is pinned for the next assistant turn. Preserve it during compaction.\n")
	writeContinuationRecentUser(&b, history)
	writeContinuationTodos(&b, a)
	writeContinuationLongTasks(&b, a)
	writeContinuationSubagents(&b, a, sessionID)
	writeContinuationBlockers(&b, a, sessionID)
	writeContinuationTouchedFiles(&b, history)
	return limitContinuationRunes(b.String())
}

func writeContinuationRecentUser(builder *strings.Builder, history []protocol.Message) {
	users := recentPersistentUserMessages(history, 3)
	builder.WriteString("\nLatest user goals / constraints:\n")
	if len(users) == 0 {
		builder.WriteString("- Not captured.\n")
		return
	}
	for _, msg := range users {
		msg = strings.TrimSpace(msg)
		if msg != "" {
			builder.WriteString("- ")
			builder.WriteString(limitContinuationLine(msg, 420))
			builder.WriteString("\n")
		}
	}
}

func writeContinuationTodos(builder *strings.Builder, a *Agent) {
	builder.WriteString("\nCurrent todos:\n")
	if a.todoMgr == nil {
		builder.WriteString("- Todo manager unavailable.\n")
		return
	}
	items := a.todoMgr.List()
	if len(items) == 0 {
		builder.WriteString("- No active todos.\n")
		return
	}
	for i, item := range items {
		if i >= maxContinuationItems {
			fmt.Fprintf(builder, "- ... %d more todos\n", len(items)-i)
			return
		}
		fmt.Fprintf(builder, "- [%s] %s", item.Status, limitContinuationLine(item.Content, 220))
		if strings.TrimSpace(item.ActiveForm) != "" {
			fmt.Fprintf(builder, " <- %s", limitContinuationLine(item.ActiveForm, 160))
		}
		builder.WriteString("\n")
	}
}

func writeContinuationLongTasks(builder *strings.Builder, a *Agent) {
	builder.WriteString("\nActive LongTasks / workflows:\n")
	items, err := a.ListLongTasks()
	if err != nil || len(items) == 0 {
		builder.WriteString("- None.\n")
		return
	}
	written := 0
	for _, task := range items {
		if written >= maxContinuationItems {
			fmt.Fprintf(builder, "- ... %d more LongTasks\n", len(items)-written)
			return
		}
		if task.Status == workflowStatusCompleted && task.Failed == 0 && task.Running == 0 {
			continue
		}
		fmt.Fprintf(builder, "- %s status=%s passed=%d/%d running=%d failed=%d", task.WorkflowID, task.Status, countPassingLongTaskStories(task), task.Total, task.Running, task.Failed)
		if task.Run != nil {
			if task.Run.BlockedBy != "" {
				fmt.Fprintf(builder, " blocked_by=%s", task.Run.BlockedBy)
			}
			if task.Run.Message != "" {
				fmt.Fprintf(builder, " message=%q", limitContinuationLine(task.Run.Message, 180))
			}
		}
		builder.WriteString("\n")
		for _, story := range task.Stories {
			if story.Passes && story.Status == workflowStatusCompleted {
				continue
			}
			fmt.Fprintf(builder, "  - story %s node=%s status=%s verdict=%s validation=%s merge=%s commit=%s repair_attempts=%d",
				story.ID, story.NodeID, story.Status, story.Verdict, story.ValidationStatus, story.MergeStatus, story.CommitStatus, story.RepairAttempts)
			if story.Error != "" {
				fmt.Fprintf(builder, " error=%q", limitContinuationLine(story.Error, 180))
			}
			builder.WriteString("\n")
		}
		written++
	}
	if written == 0 {
		builder.WriteString("- None.\n")
	}
}

func writeContinuationSubagents(builder *strings.Builder, a *Agent, sessionID string) {
	builder.WriteString("\nActive subagents:\n")
	if a.subagentJobs == nil {
		builder.WriteString("- None.\n")
		return
	}
	jobs := a.subagentJobs.List()
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].UpdatedAt.After(jobs[j].UpdatedAt) })
	written := 0
	for _, job := range jobs {
		if sessionID != "" && job.SessionID != "" && job.SessionID != sessionID {
			continue
		}
		switch job.Status {
		case subagentStatusCompleted, subagentStatusCanceled:
			continue
		}
		if written >= maxContinuationItems {
			builder.WriteString("- ... more subagents omitted\n")
			return
		}
		diagnostics := subagentDiagnosticsFromProgress(job.Progress)
		lastTool := ""
		for i := len(job.Progress) - 1; i >= 0; i-- {
			if strings.TrimSpace(job.Progress[i].ToolName) != "" {
				lastTool = strings.TrimSpace(job.Progress[i].ToolName)
				break
			}
		}
		fmt.Fprintf(builder, "- %s status=%s role=%s objective=%q last_phase=%s last_tool=%s",
			job.ID, job.Status, firstNonEmpty(job.RoleID, job.AgentType), limitContinuationLine(job.Objective, 180), diagnostics.LastRunnerPhase, lastTool)
		if job.Error != "" {
			fmt.Fprintf(builder, " error=%q", limitContinuationLine(job.Error, 180))
		}
		builder.WriteString("\n")
		written++
	}
	if written == 0 {
		builder.WriteString("- None.\n")
	}
}

func writeContinuationBlockers(builder *strings.Builder, a *Agent, sessionID string) {
	builder.WriteString("\nPending approvals / blockers:\n")
	written := 0
	if a.permissions != nil && sessionID != "" {
		for _, item := range a.permissions.ListPending(sessionID) {
			if written >= maxContinuationItems {
				break
			}
			fmt.Fprintf(builder, "- approval %s tool=%s action=%s command=%q reason=%q\n",
				item.ID, item.Request.ToolName, item.Request.Action, limitContinuationLine(item.Request.Command, 180), limitContinuationLine(item.Reason, 180))
			written++
		}
	}
	if pending := a.pendingResumeState(); pending != nil {
		fmt.Fprintf(builder, "- pending resume request=%s prior_messages=%d source=%s\n", pending.RequestID, pending.PriorMessageCount, pending.Envelope.Source)
		written++
	}
	if written == 0 {
		builder.WriteString("- None.\n")
	}
}

func writeContinuationTouchedFiles(builder *strings.Builder, history []protocol.Message) {
	files := collectContinuationFiles(history)
	builder.WriteString("\nTouched files / validation commands:\n")
	if len(files) == 0 {
		builder.WriteString("- Not captured.\n")
		return
	}
	for i, item := range files {
		if i >= maxContinuationItems {
			fmt.Fprintf(builder, "- ... %d more\n", len(files)-i)
			return
		}
		builder.WriteString("- ")
		builder.WriteString(item)
		builder.WriteString("\n")
	}
}

func collectContinuationFiles(history []protocol.Message) []string {
	seen := map[string]struct{}{}
	var items []string
	for _, msg := range history {
		for _, block := range msg.Content {
			if block.Type != protocol.BlockToolUse {
				continue
			}
			visitContinuationToolInput(block.Input, func(key, value string) {
				key = strings.ToLower(strings.TrimSpace(key))
				value = strings.TrimSpace(value)
				if value == "" {
					return
				}
				switch key {
				case "path", "file", "filename", "command":
				default:
					if !strings.HasSuffix(key, "path") {
						return
					}
				}
				item := key + ": " + limitContinuationLine(value, 240)
				if _, ok := seen[item]; ok {
					return
				}
				seen[item] = struct{}{}
				items = append(items, item)
			})
		}
	}
	if len(items) > maxContinuationItems*2 {
		return items[len(items)-maxContinuationItems*2:]
	}
	return items
}

func visitContinuationToolInput(value interface{}, visit func(string, string)) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			switch v := item.(type) {
			case string:
				visit(key, v)
			default:
				visitContinuationToolInput(v, visit)
			}
		}
	case []interface{}:
		for _, item := range typed {
			visitContinuationToolInput(item, visit)
		}
	case string:
		if strings.HasPrefix(strings.TrimSpace(typed), "{") {
			var decoded interface{}
			if err := json.Unmarshal([]byte(typed), &decoded); err == nil {
				visitContinuationToolInput(decoded, visit)
			}
		}
	}
}

func countPassingLongTaskStories(task LongTaskView) int {
	count := 0
	for _, story := range task.Stories {
		if story.Passes {
			count++
		}
	}
	return count
}

func limitContinuationLine(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func limitContinuationRunes(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxContinuationSnapshotRunes {
		return string(runes)
	}
	return string(runes[:maxContinuationSnapshotRunes]) + "\n... snapshot truncated"
}
