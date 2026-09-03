package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/backend"
)

type backendPromptStream struct {
	backend             Backend
	turn                PromptTurn
	sessionID           string
	submit              *backend.SubmitResult
	events              <-chan events.Event
	watchedTurnIDs      map[string]struct{}
	resumeFallbackText  map[string]string
	resolvedApprovalIDs map[string]struct{}
	collected           strings.Builder
	streamed            bool
	deltaSinceComplete  bool
	lastTodoPlan        []acp.PlanEntry
}

func newBackendPromptStream(bk Backend, turn PromptTurn, sessionID string, submit *backend.SubmitResult, eventCh <-chan events.Event) *backendPromptStream {
	stream := &backendPromptStream{
		backend:             bk,
		turn:                turn,
		sessionID:           sessionID,
		submit:              submit,
		events:              eventCh,
		watchedTurnIDs:      make(map[string]struct{}),
		resumeFallbackText:  make(map[string]string),
		resolvedApprovalIDs: make(map[string]struct{}),
	}
	if turnID := strings.TrimSpace(submit.TurnID); turnID != "" {
		stream.watchedTurnIDs[turnID] = struct{}{}
	}
	return stream
}

func (s *backendPromptStream) collect(ctx context.Context) (PromptResult, error) {
	for {
		select {
		case <-ctx.Done():
			return PromptResult{
				FinalText:  strings.TrimSpace(s.collected.String()),
				StopReason: acp.StopReasonCancelled,
			}, nil
		case event := <-s.events:
			if !s.accepts(event) {
				continue
			}
			result, done, err := s.handleEvent(ctx, event)
			if err != nil || done {
				return result, err
			}
		}
	}
}

func (s *backendPromptStream) accepts(event events.Event) bool {
	if len(s.watchedTurnIDs) > 0 {
		_, ok := s.watchedTurnIDs[strings.TrimSpace(event.TurnID)]
		return ok
	}
	return event.TurnID == s.submit.TurnID
}

func (s *backendPromptStream) handleEvent(ctx context.Context, event events.Event) (PromptResult, bool, error) {
	switch event.Type {
	case events.EventAssistantTextDelta:
		s.handleAssistantTextDelta(ctx, event)
	case events.EventAssistantThinkingDelta:
		s.handleAssistantThinkingDelta(ctx, event)
	case events.EventAssistantMessageComplete:
		s.handleAssistantMessageComplete(event)
	case events.EventToolCallStarted:
		s.handleToolCallStarted(ctx, event)
	case events.EventTodoListUpdated:
		s.handleTodoListUpdated(ctx, event)
	case events.EventToolCallFinished:
		s.handleToolCallFinished(ctx, event)
	case events.EventWarningRaised:
		s.handleWarning(ctx, event)
	case events.EventErrorRaised:
		result, err := s.handleError(event)
		return result, true, err
	case events.EventTurnCompleted:
		return s.handleTurnCompleted(ctx, event)
	}
	return PromptResult{}, false, nil
}

// handleAssistantThinkingDelta forwards godex reasoning deltas as ACP
// agent_thought_chunk updates so clients (e.g. VS Code) can render the model's
// thinking alongside the reply. Thinking is streamed separately from the final
// text: it never enters s.collected, so the end-turn reply stays clean.
func (s *backendPromptStream) handleAssistantThinkingDelta(ctx context.Context, event events.Event) {
	payload, ok := event.Payload.(events.TextPayload)
	if !ok || strings.TrimSpace(payload.Text) == "" {
		return
	}
	if s.turn.Updater == nil {
		return
	}
	if err := s.turn.Updater.Update(ctx, acp.UpdateAgentThoughtText(payload.Text)); err != nil {
		logger.Warnf("ACP thought update: %v", err)
		return
	}
}

func (s *backendPromptStream) handleAssistantTextDelta(ctx context.Context, event events.Event) {
	payload, ok := event.Payload.(events.TextPayload)
	if !ok || payload.Text == "" {
		return
	}
	s.collected.WriteString(payload.Text)
	s.deltaSinceComplete = true
	if s.turn.Updater == nil {
		return
	}
	if err := s.turn.Updater.Update(ctx, acp.UpdateAgentMessageText(payload.Text)); err != nil {
		logger.Warnf("ACP session update: %v", err)
		return
	}
	s.streamed = true
}

func (s *backendPromptStream) handleAssistantMessageComplete(event events.Event) {
	payload, ok := event.Payload.(events.TextPayload)
	if ok && !s.deltaSinceComplete && payload.Text != "" {
		s.collected.WriteString(payload.Text)
	}
	s.deltaSinceComplete = false
}

func (s *backendPromptStream) handleToolCallStarted(ctx context.Context, event events.Event) {
	payload, ok := event.Payload.(events.ToolCallPayload)
	if !ok || payload.Name == "" || s.turn.Updater == nil {
		return
	}
	callID := strings.TrimSpace(payload.ID)
	if callID == "" {
		callID = fmt.Sprintf("tc_%s", payload.Name)
	}
	_ = s.turn.Updater.Update(ctx, acp.StartToolCall(
		acp.ToolCallId(callID),
		toolCallTitle(payload.Name, payload.Input),
		acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartKind(toolKind(payload.Name)),
		acp.WithStartRawInput(payload.Input),
	))
}

func (s *backendPromptStream) handleTodoListUpdated(ctx context.Context, event events.Event) {
	payload, ok := event.Payload.(events.TodoListPayload)
	if !ok || s.turn.Updater == nil {
		return
	}
	callID := strings.TrimSpace(payload.SourceToolCallID)
	if callID == "" {
		callID = "tc_todo_write"
	}
	_ = s.turn.Updater.Update(ctx, acp.UpdateToolCall(
		acp.ToolCallId(callID),
		acp.WithUpdateTitle(todoToolTitle(payload)),
		acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
		acp.WithUpdateRawOutput(todoRawOutput(payload)),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(payload.RenderPlain()))}),
	))
	s.lastTodoPlan = todoPlanEntries(payload)
	_ = s.turn.Updater.Update(ctx, acp.UpdatePlan(s.lastTodoPlan...))
}

func (s *backendPromptStream) handleToolCallFinished(ctx context.Context, event events.Event) {
	payload, ok := event.Payload.(events.ToolCallPayload)
	if !ok || payload.Name == "" || s.turn.Updater == nil {
		return
	}
	if payload.Name == "todo_write" && strings.TrimSpace(payload.Error) == "" {
		return
	}
	callID := strings.TrimSpace(payload.ID)
	if callID == "" {
		callID = fmt.Sprintf("tc_%s", payload.Name)
	}
	output := strings.TrimSpace(payload.Output)
	if len(output) > 500 {
		output = output[:500] + "…"
	}
	// A tool call that errored (or timed out) must be reported as failed so
	// the ACP client renders a failure instead of a successful completion.
	status := acp.ToolCallStatusCompleted
	if strings.TrimSpace(payload.Error) != "" || payload.TimedOut {
		status = acp.ToolCallStatusFailed
	}
	_ = s.turn.Updater.Update(ctx, acp.UpdateToolCall(
		acp.ToolCallId(callID),
		acp.WithUpdateTitle(toolCallTitle(payload.Name, payload.Input)),
		acp.WithUpdateRawOutput(output),
		acp.WithUpdateRawInput(payload.Input),
		acp.WithUpdateStatus(status),
	))
}

func (s *backendPromptStream) handleWarning(ctx context.Context, event events.Event) {
	payload, ok := event.Payload.(events.NoticePayload)
	if !ok || payload.Message == "" || s.turn.Updater == nil {
		return
	}
	_ = s.turn.Updater.Update(ctx, acp.UpdateAgentMessageText(fmt.Sprintf("[warning] %s", payload.Message)))
}

func (s *backendPromptStream) handleError(event events.Event) (PromptResult, error) {
	if finalText := strings.TrimSpace(s.collected.String()); finalText != "" {
		return PromptResult{FinalText: finalText, StopReason: acp.StopReasonEndTurn, Streamed: s.streamed}, nil
	}
	payload, _ := event.Payload.(events.NoticePayload)
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "agent error"
	}
	return PromptResult{}, errors.New(message)
}

func (s *backendPromptStream) handleTurnCompleted(ctx context.Context, event events.Event) (PromptResult, bool, error) {
	payload, _ := event.Payload.(events.TurnPayload)
	status := strings.TrimSpace(payload.Status)
	if strings.EqualFold(status, "pending_approval") {
		requestID := strings.TrimSpace(s.submit.PendingRequestID)
		if _, seen := s.resolvedApprovalIDs[requestID]; requestID != "" && seen {
			return PromptResult{}, false, nil
		}
		result, done, err := s.resolvePendingApproval(ctx, requestID)
		return result, done, err
	}

	finalText := strings.TrimSpace(s.collected.String())
	if finalText == "" {
		finalText = strings.TrimSpace(s.resumeFallbackText[strings.TrimSpace(event.TurnID)])
	}
	if strings.EqualFold(status, "canceled") || strings.EqualFold(status, "cancelled") {
		return PromptResult{FinalText: finalText, StopReason: acp.StopReasonCancelled, Streamed: s.streamed}, true, nil
	}
	if strings.EqualFold(status, "error") && finalText == "" {
		return PromptResult{}, true, errors.New("agent turn failed")
	}
	if strings.EqualFold(status, "completed") && s.turn.Updater != nil && s.lastTodoPlan != nil {
		_ = s.turn.Updater.Update(ctx, acp.UpdatePlan(completePlanEntries(s.lastTodoPlan)...))
	}
	return PromptResult{FinalText: finalText, StopReason: acp.StopReasonEndTurn, Streamed: s.streamed}, true, nil
}

func (s *backendPromptStream) resolvePendingApproval(ctx context.Context, requestID string) (PromptResult, bool, error) {
	items, pendingErr := s.backend.PendingPermissions(ctx, s.sessionID)
	if pendingErr != nil {
		logger.Warnf("ACP pending permissions lookup: %v", pendingErr)
	}
	approval, ok, err := resolveNativeApproval(ctx, s.turn.PermissionRequester, s.backend, s.sessionID, requestID, items)
	if err != nil {
		return PromptResult{}, true, err
	}
	if !ok {
		return PromptResult{FinalText: renderPendingApproval(requestID, items), StopReason: acp.StopReasonEndTurn}, true, nil
	}
	if approval.RequestID != "" {
		s.resolvedApprovalIDs[approval.RequestID] = struct{}{}
	}
	if approval.ContinueTurnID == "" {
		return approval.Result, true, nil
	}
	s.watchedTurnIDs[approval.ContinueTurnID] = struct{}{}
	if approval.FallbackText != "" {
		s.resumeFallbackText[approval.ContinueTurnID] = approval.FallbackText
	}
	return PromptResult{}, false, nil
}
