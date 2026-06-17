package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const projectLedgerFileName = "project_ledger.json"

type ProjectLedger struct {
	SessionID    string                 `json:"session_id"`
	Goal         string                 `json:"goal,omitempty"`
	CurrentPhase string                 `json:"current_phase,omitempty"`
	ChangedFiles []string               `json:"changed_files,omitempty"`
	Commands     []ProjectLedgerCommand `json:"commands,omitempty"`
	Validation   []string               `json:"validation,omitempty"`
	Decisions    []string               `json:"decisions,omitempty"`
	Risks        []string               `json:"risks,omitempty"`
	Blockers     []string               `json:"blockers,omitempty"`
	NextSteps    []string               `json:"next_steps,omitempty"`
	RecentTurns  []ProjectLedgerTurn    `json:"recent_turns,omitempty"`
	UpdatedAt    time.Time              `json:"updated_at"`
	Compact      string                 `json:"compact,omitempty"`
}

type ProjectLedgerCommand struct {
	Command string    `json:"command"`
	Status  string    `json:"status,omitempty"`
	Summary string    `json:"summary,omitempty"`
	At      time.Time `json:"at"`
}

type ProjectLedgerTurn struct {
	TurnID  string    `json:"turn_id"`
	User    string    `json:"user,omitempty"`
	Status  string    `json:"status,omitempty"`
	Summary string    `json:"summary,omitempty"`
	At      time.Time `json:"at"`
}

type ProjectLedgerPatch struct {
	Goal         string   `json:"goal,omitempty"`
	CurrentPhase string   `json:"current_phase,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Validation   []string `json:"validation,omitempty"`
	Decisions    []string `json:"decisions,omitempty"`
	Risks        []string `json:"risks,omitempty"`
	Blockers     []string `json:"blockers,omitempty"`
	NextSteps    []string `json:"next_steps,omitempty"`
}

func (s *Service) ProjectLedger(sessionID string) (ProjectLedger, error) {
	ledger, err := s.readProjectLedger(sessionID)
	if err != nil {
		return ProjectLedger{}, err
	}
	ledger.SessionID = sessionID
	ledger.Compact = renderProjectLedgerCompact(ledger)
	return ledger, nil
}

func (s *Service) UpdateProjectLedger(sessionID string, patch ProjectLedgerPatch) (ProjectLedger, error) {
	ledger, err := s.readProjectLedger(sessionID)
	if err != nil {
		return ProjectLedger{}, err
	}
	ledger.SessionID = sessionID
	if strings.TrimSpace(patch.Goal) != "" {
		ledger.Goal = strings.TrimSpace(patch.Goal)
	}
	if strings.TrimSpace(patch.CurrentPhase) != "" {
		ledger.CurrentPhase = strings.TrimSpace(patch.CurrentPhase)
	}
	if patch.ChangedFiles != nil {
		ledger.ChangedFiles = normalizeLedgerStrings(patch.ChangedFiles, 80)
	}
	if patch.Validation != nil {
		ledger.Validation = normalizeLedgerStrings(patch.Validation, 40)
	}
	if patch.Decisions != nil {
		ledger.Decisions = normalizeLedgerStrings(patch.Decisions, 40)
	}
	if patch.Risks != nil {
		ledger.Risks = normalizeLedgerStrings(patch.Risks, 40)
	}
	if patch.Blockers != nil {
		ledger.Blockers = normalizeLedgerStrings(patch.Blockers, 40)
	}
	if patch.NextSteps != nil {
		ledger.NextSteps = normalizeLedgerStrings(patch.NextSteps, 40)
	}
	ledger.UpdatedAt = s.now()
	if err := s.writeProjectLedger(sessionID, ledger); err != nil {
		return ProjectLedger{}, err
	}
	ledger.Compact = renderProjectLedgerCompact(ledger)
	return ledger, nil
}

func (s *Service) compactProjectLedgerForSession(sessionID string) string {
	ledger, err := s.ProjectLedger(sessionID)
	if err != nil {
		return ""
	}
	return ledger.Compact
}

func (s *Service) updateProjectLedgerFromTurn(session *sessionState, turnID string, envelope message.Envelope, status string, runErr error, priorMessageCount int, at time.Time) error {
	if session == nil {
		return nil
	}
	ledger, err := s.readProjectLedger(session.id)
	if err != nil {
		return err
	}
	ledger.SessionID = session.id
	userText := strings.TrimSpace(envelope.BodyText())
	if ledger.Goal == "" && userText != "" {
		ledger.Goal = truncateLedgerText(userText, 240)
	}
	if status == "error" || runErr != nil {
		ledger.CurrentPhase = "blocked"
	} else {
		ledger.CurrentPhase = "active"
	}
	turn := ProjectLedgerTurn{TurnID: turnID, User: truncateLedgerText(userText, 180), Status: status, At: at}

	eventsForTurn := session.timeline.Entries(0)
	for _, event := range eventsForTurn {
		if strings.TrimSpace(event.TurnID) != turnID {
			continue
		}
		switch event.Type {
		case events.EventToolCallFinished:
			payload := decodeToolCallPayload(event.Payload)
			ledger.ChangedFiles = append(ledger.ChangedFiles, ledgerPathsFromTool(payload)...)
			if cmd := ledgerCommandFromTool(payload); cmd.Command != "" {
				cmd.At = event.Timestamp
				ledger.Commands = append(ledger.Commands, cmd)
				if isValidationCommand(cmd.Command) {
					ledger.Validation = append(ledger.Validation, cmd.Summary)
				}
			}
			if strings.TrimSpace(payload.Error) != "" {
				ledger.Risks = append(ledger.Risks, fmt.Sprintf("%s failed: %s", payload.Name, truncateLedgerText(payload.Error, 180)))
			}
			decisions, blockers := ledgerWorkflowSummariesFromTool(payload)
			ledger.Decisions = append(ledger.Decisions, decisions...)
			ledger.Blockers = append(ledger.Blockers, blockers...)
		case events.EventSubagentJobUpdated:
			payload := decodeSubagentPayload(event.Payload)
			if strings.TrimSpace(payload.Result) != "" {
				ledger.Decisions = append(ledger.Decisions, fmt.Sprintf("subagent %s: %s", payload.JobID, truncateLedgerText(payload.Result, 220)))
			}
			if strings.TrimSpace(payload.Error) != "" {
				ledger.Blockers = append(ledger.Blockers, fmt.Sprintf("subagent %s failed: %s", payload.JobID, truncateLedgerText(payload.Error, 180)))
			}
			ledger.ChangedFiles = append(ledger.ChangedFiles, payload.WriteScope...)
		}
	}

	messages := session.agent.GetMessages()
	if priorMessageCount < 0 || priorMessageCount > len(messages) {
		priorMessageCount = 0
	}
	for _, msg := range messages[priorMessageCount:] {
		if msg.Role != protocol.RoleAssistant {
			continue
		}
		text := truncateLedgerText(protocol.MessageText(msg), 260)
		if text != "" {
			turn.Summary = text
		}
	}
	if turn.Summary != "" {
		ledger.Decisions = append(ledger.Decisions, turn.Summary)
	}
	if runErr != nil {
		ledger.Blockers = append(ledger.Blockers, truncateLedgerText(runErr.Error(), 240))
	}
	ledger.RecentTurns = append(ledger.RecentTurns, turn)
	normalizeProjectLedger(&ledger, at)
	return s.writeProjectLedger(session.id, ledger)
}

func (s *Service) readProjectLedger(sessionID string) (ProjectLedger, error) {
	data, exists, err := readOptionalFile(filepath.Join(s.sessionDir(sessionID), projectLedgerFileName))
	if err != nil || !exists {
		return ProjectLedger{SessionID: sessionID}, err
	}
	var ledger ProjectLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ProjectLedger{}, err
	}
	if ledger.SessionID == "" {
		ledger.SessionID = sessionID
	}
	return ledger, nil
}

func (s *Service) writeProjectLedger(sessionID string, ledger ProjectLedger) error {
	ledger.Compact = ""
	path := filepath.Join(s.sessionDir(sessionID), projectLedgerFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, ledger, 0644)
}

func normalizeProjectLedger(ledger *ProjectLedger, at time.Time) {
	ledger.ChangedFiles = normalizeLedgerStrings(ledger.ChangedFiles, 80)
	sort.Strings(ledger.ChangedFiles)
	ledger.Validation = normalizeLedgerStrings(ledger.Validation, 40)
	ledger.Decisions = normalizeLedgerStrings(ledger.Decisions, 40)
	ledger.Risks = normalizeLedgerStrings(ledger.Risks, 40)
	ledger.Blockers = normalizeLedgerStrings(ledger.Blockers, 40)
	ledger.NextSteps = normalizeLedgerStrings(ledger.NextSteps, 40)
	ledger.Commands = trimLedgerCommands(ledger.Commands, 40)
	ledger.RecentTurns = trimLedgerTurns(ledger.RecentTurns, 20)
	ledger.UpdatedAt = at
}

func normalizeLedgerStrings(items []string, limit int) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = truncateLedgerText(item, 320)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func trimLedgerCommands(items []ProjectLedgerCommand, limit int) []ProjectLedgerCommand {
	out := make([]ProjectLedgerCommand, 0, len(items))
	for _, item := range items {
		item.Command = truncateLedgerText(item.Command, 240)
		item.Summary = truncateLedgerText(item.Summary, 240)
		if item.Command != "" {
			out = append(out, item)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func trimLedgerTurns(items []ProjectLedgerTurn, limit int) []ProjectLedgerTurn {
	out := make([]ProjectLedgerTurn, 0, len(items))
	for _, item := range items {
		item.User = truncateLedgerText(item.User, 180)
		item.Summary = truncateLedgerText(item.Summary, 260)
		if item.TurnID != "" {
			out = append(out, item)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func renderProjectLedgerCompact(ledger ProjectLedger) string {
	var builder strings.Builder
	writeLedgerLine(&builder, "Goal", ledger.Goal)
	writeLedgerLine(&builder, "Current phase", ledger.CurrentPhase)
	writeLedgerList(&builder, "Changed files", ledger.ChangedFiles, 12)
	var commands []string
	for _, cmd := range ledger.Commands {
		line := cmd.Command
		if cmd.Status != "" {
			line += " [" + cmd.Status + "]"
		}
		if cmd.Summary != "" {
			line += " - " + cmd.Summary
		}
		commands = append(commands, line)
	}
	writeLedgerList(&builder, "Recent commands", commands, 8)
	writeLedgerList(&builder, "Validation", ledger.Validation, 8)
	writeLedgerList(&builder, "Decisions", ledger.Decisions, 8)
	writeLedgerList(&builder, "Risks", ledger.Risks, 6)
	writeLedgerList(&builder, "Blockers", ledger.Blockers, 6)
	writeLedgerList(&builder, "Next steps", ledger.NextSteps, 8)
	text := strings.TrimSpace(builder.String())
	return truncateLedgerText(text, 4000)
}

func writeLedgerLine(builder *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	builder.WriteString(label)
	builder.WriteString(": ")
	builder.WriteString(value)
	builder.WriteString("\n")
}

func writeLedgerList(builder *strings.Builder, label string, values []string, limit int) {
	values = normalizeLedgerStrings(values, limit)
	if len(values) == 0 {
		return
	}
	builder.WriteString(label)
	builder.WriteString(":\n")
	for _, value := range values {
		builder.WriteString("- ")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
}

func decodeToolCallPayload(value any) events.ToolCallPayload {
	if payload, ok := value.(events.ToolCallPayload); ok {
		return payload
	}
	data, _ := json.Marshal(value)
	var payload events.ToolCallPayload
	_ = json.Unmarshal(data, &payload)
	return payload
}

func decodeSubagentPayload(value any) events.SubagentJobPayload {
	if payload, ok := value.(events.SubagentJobPayload); ok {
		return payload
	}
	data, _ := json.Marshal(value)
	var payload events.SubagentJobPayload
	_ = json.Unmarshal(data, &payload)
	return payload
}

func ledgerPathsFromTool(payload events.ToolCallPayload) []string {
	var paths []string
	for _, key := range []string{"path", "file_path", "target", "filename"} {
		if value, ok := payload.Input[key].(string); ok && strings.TrimSpace(value) != "" {
			paths = append(paths, filepath.ToSlash(strings.TrimSpace(value)))
		}
	}
	for _, path := range payload.ArtifactPaths {
		if strings.TrimSpace(path) != "" {
			paths = append(paths, filepath.ToSlash(strings.TrimSpace(path)))
		}
	}
	return paths
}

func ledgerCommandFromTool(payload events.ToolCallPayload) ProjectLedgerCommand {
	if payload.Name != "bash" && payload.Name != "background" {
		return ProjectLedgerCommand{}
	}
	command := ""
	if value, ok := payload.Input["command"].(string); ok {
		command = value
	}
	if command == "" {
		if value, ok := payload.Input["cmd"].(string); ok {
			command = value
		}
	}
	status := "ok"
	if strings.TrimSpace(payload.Error) != "" {
		status = "error"
	}
	summary := truncateLedgerText(payload.Output, 180)
	if status == "error" {
		summary = truncateLedgerText(payload.Error, 180)
	}
	return ProjectLedgerCommand{Command: strings.TrimSpace(command), Status: status, Summary: summary}
}

func isValidationCommand(command string) bool {
	command = strings.ToLower(command)
	return strings.Contains(command, "go test") ||
		strings.Contains(command, "pnpm") ||
		strings.Contains(command, "npm test") ||
		strings.Contains(command, "pytest") ||
		strings.Contains(command, "cargo test") ||
		strings.Contains(command, "git diff --check")
}

func ledgerWorkflowSummariesFromTool(payload events.ToolCallPayload) ([]string, []string) {
	if payload.Name != "workflow" || strings.TrimSpace(payload.Output) == "" {
		return nil, nil
	}
	var view struct {
		WorkflowID string `json:"workflow_id"`
		Status     string `json:"status"`
		Completed  int    `json:"completed"`
		Failed     int    `json:"failed"`
		Nodes      []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			ResultPreview string `json:"result_preview"`
			Error         string `json:"error"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(payload.Output), &view); err != nil {
		return nil, nil
	}
	workflowID := strings.TrimSpace(view.WorkflowID)
	if workflowID == "" {
		workflowID = "workflow"
	}
	var decisions []string
	var blockers []string
	if view.Status == "completed" {
		decisions = append(decisions, fmt.Sprintf("workflow %s completed: %d nodes", workflowID, view.Completed))
	}
	if view.Failed > 0 {
		blockers = append(blockers, fmt.Sprintf("workflow %s has %d failed nodes", workflowID, view.Failed))
	}
	for _, node := range view.Nodes {
		nodeID := strings.TrimSpace(node.ID)
		if nodeID == "" {
			continue
		}
		if strings.TrimSpace(node.ResultPreview) != "" && node.Status == "completed" {
			decisions = append(decisions, fmt.Sprintf("workflow %s/%s: %s", workflowID, nodeID, truncateLedgerText(node.ResultPreview, 220)))
		}
		if strings.TrimSpace(node.Error) != "" {
			blockers = append(blockers, fmt.Sprintf("workflow %s/%s failed: %s", workflowID, nodeID, truncateLedgerText(node.Error, 180)))
		}
	}
	return decisions, blockers
}

func truncateLedgerText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
