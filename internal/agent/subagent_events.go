package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"os"
	"strings"
	"time"
)

type subagentEventTarget struct {
	sessionID  string
	turnID     string
	sink       events.Sink
	scopeLabel string
}

type subagentEventContextKey struct{}

func withSubagentEventTarget(ctx context.Context, target subagentEventTarget) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, subagentEventContextKey{}, target)
}

// WithSubagentEvents attaches a durable subagent event target to ctx for
// callers outside the agent loop, such as package command dispatchers.
func WithSubagentEvents(ctx context.Context, sessionID, turnID string, sink events.Sink) context.Context {
	return withSubagentEventTarget(ctx, subagentEventTarget{
		sessionID: strings.TrimSpace(sessionID),
		turnID:    strings.TrimSpace(turnID),
		sink:      sink,
	})
}

func subagentEventTargetFromContext(ctx context.Context) subagentEventTarget {
	if ctx == nil {
		return subagentEventTarget{}
	}
	target, _ := ctx.Value(subagentEventContextKey{}).(subagentEventTarget)
	return target
}

func (a *Agent) recordSubagentProgress(id string, target subagentEventTarget, progress subagentProgressEvent) {
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	job, err := a.subagentJobs.AppendProgress(id, progress)
	if err != nil {
		return
	}
	target.emit(job, progress.Phase, progress.Message, progress.ToolID, progress.ToolName, progress.Error, progress.Result)
}

func (a *Agent) appendSubagentProgress(id string, progress subagentProgressEvent) {
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	_, _ = a.subagentJobs.AppendProgress(id, progress)
}

func (t subagentEventTarget) emit(job *subagentJob, phase, message, toolID, toolName, errorText, result string) {
	if t.sink == nil || job == nil {
		return
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt))
	displayTitle := job.DisplayTitle
	if strings.TrimSpace(displayTitle) == "" {
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
	t.sink.Emit(events.Event{
		SessionID: t.sessionID,
		TurnID:    t.turnID,
		Type:      events.EventSubagentJobUpdated,
		Timestamp: updatedAt,
		Payload: events.SubagentJobPayload{
			JobID:             job.ID,
			ParentTurnID:      job.ParentTurnID,
			Sequence:          job.Sequence,
			Objective:         objective,
			DisplayTitle:      displayTitle,
			IdentityID:        job.Identity.ID,
			WorkerID:          firstNonEmpty(job.WorkerID, localGoDexWorkerID),
			SourceBranchID:    job.SourceBranchID,
			SourceNodeID:      job.SourceNodeID,
			WorkerBranchID:    job.WorkerBranchID,
			AgentType:         job.AgentType,
			RoleID:            job.RoleID,
			RoleName:          job.RoleName,
			PackageName:       job.PackageName,
			Status:            string(job.Status),
			Phase:             strings.TrimSpace(phase),
			Message:           strings.TrimSpace(message),
			ToolID:            strings.TrimSpace(toolID),
			ToolName:          strings.TrimSpace(toolName),
			Error:             strings.TrimSpace(errorText),
			Result:            previewSubagentText(result),
			ToolNames:         append([]string{}, job.ToolNames...),
			CapabilitySummary: append([]string{}, job.Identity.CapabilitySummary...),
			ModelHint:         job.Identity.ModelHint,
			BudgetHint:        job.Identity.BudgetHint,
			MaxTurns:          job.MaxTurns,
			ModelRequestCount: diagnostics.ModelRequestCount,
			ToolCallCount:     diagnostics.ToolCallCount,
			LastRunnerPhase:   diagnostics.LastRunnerPhase,
			LastIteration:     diagnostics.LastIteration,
			LastRecoveryHint:  diagnostics.LastRecoveryHint,
			WriteScope:        append([]string{}, job.WriteScope...),
			SandboxID:         job.SandboxID,
			WorktreeDir:       job.WorktreeDir,
			Isolation:         job.Isolation,
			WorkspaceOrigin:   job.WorkspaceOrigin,
			GitBranch:         job.GitBranch,
			CleanupState:      job.CleanupState,
			MergeStatus:       job.MergeStatus,
			ScopeLabel:        t.scopeLabel,
			UpdatedAt:         updatedAt,
		},
	})
}

func (t subagentEventTarget) emitIdentity(job *subagentJob) {
	if t.sink == nil || job == nil || strings.TrimSpace(job.Identity.ID) == "" {
		return
	}
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	t.sink.Emit(events.Event{
		SessionID: t.sessionID,
		TurnID:    t.turnID,
		Type:      events.EventAgentIdentityUpdated,
		Timestamp: updatedAt,
		Payload: events.AgentIdentityPayload{
			ID:                job.Identity.ID,
			Name:              job.Identity.Name,
			Kind:              job.Identity.Kind,
			Role:              job.Identity.Role,
			ParentID:          job.Identity.ParentID,
			SessionID:         job.Identity.SessionID,
			Source:            job.Identity.Source,
			CapabilitySummary: append([]string{}, job.Identity.CapabilitySummary...),
			ModelHint:         job.Identity.ModelHint,
			BudgetHint:        job.Identity.BudgetHint,
			Display:           cloneStringMap(job.Identity.Display),
			LastActivityAt:    updatedAt,
		},
	})
}

func (t subagentEventTarget) emitRunnerPhase(job *subagentJob, phase conversation.PhaseEvent) {
	if t.sink == nil || job == nil {
		return
	}
	updatedAt := time.Now().UTC()
	if !job.UpdatedAt.IsZero() {
		updatedAt = job.UpdatedAt
	}
	objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt))
	displayTitle := job.DisplayTitle
	if strings.TrimSpace(displayTitle) == "" {
		displayTitle = subagentDisplayTitle(&subagentJob{
			Sequence:  job.Sequence,
			RoleName:  job.RoleName,
			RoleID:    job.RoleID,
			AgentType: job.AgentType,
			Objective: objective,
			Prompt:    job.Prompt,
		})
	}
	t.sink.Emit(events.Event{
		SessionID: t.sessionID,
		TurnID:    t.turnID,
		Type:      events.EventRunnerPhaseChanged,
		Timestamp: updatedAt,
		Payload: events.RunnerPhasePayload{
			RunnerID:     job.ID,
			ActorKind:    "subagent",
			ActorID:      firstNonEmpty(job.Identity.ID, job.ID),
			Objective:    objective,
			DisplayTitle: displayTitle,
			Phase:        phase.Phase,
			Iteration:    phase.Iteration,
			MaxTurns:     job.MaxTurns,
			Model:        phase.Model,
			Message:      phase.Message,
			ToolID:       phase.ToolID,
			ToolName:     phase.ToolName,
			RecoveryHint: phase.RecoveryHint,
			ScopeLabel:   t.scopeLabel,
		},
	})
}

func appendBoundedSubagentProgress(items []subagentProgressEvent, progress subagentProgressEvent) []subagentProgressEvent {
	if progress.Time.IsZero() {
		progress.Time = time.Now().UTC()
	}
	progress.Phase = strings.TrimSpace(progress.Phase)
	progress.Message = strings.TrimSpace(progress.Message)
	progress.ToolID = strings.TrimSpace(progress.ToolID)
	progress.ToolName = strings.TrimSpace(progress.ToolName)
	progress.Error = strings.TrimSpace(progress.Error)
	progress.Result = strings.TrimSpace(progress.Result)
	progress.Model = strings.TrimSpace(progress.Model)
	progress.RecoveryHint = strings.TrimSpace(progress.RecoveryHint)
	if progress.Phase == "" && progress.Message == "" && progress.ToolID == "" && progress.ToolName == "" && progress.Error == "" && progress.Result == "" && progress.Iteration == 0 && progress.MaxTurns == 0 && progress.Model == "" && progress.RecoveryHint == "" {
		return items
	}
	out := append(append([]subagentProgressEvent{}, items...), progress)
	if len(out) > subagentProgressLimit {
		out = out[len(out)-subagentProgressLimit:]
	}
	return out
}

func cloneSubagentProgress(items []subagentProgressEvent) []subagentProgressEvent {
	if len(items) == 0 {
		return nil
	}
	return append([]subagentProgressEvent{}, items...)
}

func subagentFinishMessage(status subagentJobStatus) string {
	switch status {
	case subagentStatusCompleted:
		return "Subagent job completed."
	case subagentStatusPending:
		return "Subagent job queued."
	case subagentStatusPendingApproval:
		return "Subagent job is waiting for tool approval."
	case subagentStatusCanceled:
		return "Subagent job canceled."
	case subagentStatusInterrupted:
		return "Subagent job interrupted."
	case subagentStatusTimeout:
		return "Subagent job timed out."
	case subagentStatusError:
		return "Subagent job failed."
	default:
		return "Subagent job updated."
	}
}

func subagentResumeMessage(status subagentJobStatus) string {
	if status == subagentStatusPending {
		return "Subagent job queued for resume."
	}
	return "Subagent job resumed."
}

func previewSubagentText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= 400 {
		return text
	}
	return string(runes[:400]) + "..."
}

func subagentObjectiveFromPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	for _, block := range strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n\n") {
		block = strings.Join(strings.Fields(block), " ")
		if block == "" {
			continue
		}
		runes := []rune(block)
		if len(runes) > 96 {
			return string(runes[:96]) + "..."
		}
		return block
	}
	return ""
}

func subagentDisplayTitle(job *subagentJob) string {
	if job == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if job.Sequence > 0 {
		parts = append(parts, fmt.Sprintf("#%d", job.Sequence))
	}
	if label := firstNonEmpty(job.RoleName, job.RoleID, job.AgentType); label != "" {
		parts = append(parts, label)
	}
	if objective := firstNonEmpty(job.Objective, subagentObjectiveFromPrompt(job.Prompt)); objective != "" {
		parts = append(parts, objective)
	}
	return strings.Join(parts, " · ")
}

type subagentDiagnostics struct {
	ModelRequestCount int
	ToolCallCount     int
	LastRunnerPhase   string
	LastIteration     int
	LastRecoveryHint  string
}

func subagentDiagnosticsFromProgress(progress []subagentProgressEvent) subagentDiagnostics {
	var diagnostics subagentDiagnostics
	for _, item := range progress {
		switch item.Phase {
		case conversation.PhaseModelRequest:
			diagnostics.ModelRequestCount++
		case "tool_finished":
			diagnostics.ToolCallCount++
		}
		if isRunnerProgressPhase(item.Phase) {
			diagnostics.LastRunnerPhase = item.Phase
		}
		if item.Iteration > 0 {
			diagnostics.LastIteration = item.Iteration
		}
		if strings.TrimSpace(item.RecoveryHint) != "" {
			diagnostics.LastRecoveryHint = strings.TrimSpace(item.RecoveryHint)
		}
	}
	return diagnostics
}

func isRunnerProgressPhase(phase string) bool {
	switch phase {
	case conversation.PhaseModelRequest,
		conversation.PhaseAwaitingTools,
		conversation.PhaseToolsCompleted,
		conversation.PhaseFinalResponse,
		conversation.PhaseError,
		conversation.PhaseInterrupted,
		conversation.PhaseRecoveryAttempt:
		return true
	default:
		return false
	}
}

func newSubagentJobID(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("subagent_%d", now.UnixNano())
}

func cloneSubagentJob(job *subagentJob) *subagentJob {
	if job == nil {
		return nil
	}
	cloned := *job
	cloned.ToolNames = append([]string{}, job.ToolNames...)
	cloned.WriteScope = append([]string{}, job.WriteScope...)
	cloned.PreviewJobIDs = append([]string{}, job.PreviewJobIDs...)
	cloned.DefaultBundles = append([]string{}, job.DefaultBundles...)
	cloned.BundleOverrides = append([]string{}, job.BundleOverrides...)
	cloned.DeactivateBundles = append([]string{}, job.DeactivateBundles...)
	cloned.Messages = protocol.CloneMessages(job.Messages)
	cloned.Progress = cloneSubagentProgress(job.Progress)
	return &cloned
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
