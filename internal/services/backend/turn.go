package backend

import (
	"context"
	"errors"
	"fmt"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/tools"
	"strings"
	"time"
)

func (s *Service) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*SubmitResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return nil, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	ctx = withSessionLock(ctx, sessionID)
	result, err := s.runUserTurnLocked(ctx, session, envelope)
	release()
	released = true
	return result, err
}

// SubmitAsync appends an inbound envelope, immediately returns the accepted turn,
// and continues the agent turn on a service-owned background context.
func (s *Service) SubmitAsync(ctx context.Context, sessionID string, envelope message.Envelope, options ...SubmitOptions) (*SubmitResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, ok := session.tryAcquire()
	if !ok {
		mode := QueueModeFollowUp
		if len(options) > 0 && strings.TrimSpace(string(options[0].QueueMode)) != "" {
			mode = normalizeQueueMode(options[0].QueueMode)
		}
		if result, injected, err := s.injectActiveTurn(session, envelope, mode); injected || err != nil {
			return result, err
		}
		return s.enqueueTurn(session, envelope, mode)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	turn, result, err := s.startUserTurnLocked(session, envelope, true)
	if err != nil {
		return nil, err
	}

	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(turn.TurnID, cancel, result.UpdatedAt)
	go func() {
		defer func() {
			session.clearActiveTurn(turn.TurnID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, sessionID)
		_, _ = s.finishAgentTurnLocked(runCtx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	}()
	released = true
	return result, nil
}

func (s *Service) enqueueTurn(session *sessionState, envelope message.Envelope, mode QueueMode) (*SubmitResult, error) {
	if session == nil {
		return nil, ErrSessionNotFound
	}
	now := s.now()
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = now
	}
	envelope.SessionID = session.id
	normalized := envelope.Normalized()
	turnID := session.nextTurnID(now)
	item := QueuedTurn{
		ID:        turnID,
		Mode:      normalizeQueueMode(mode),
		Status:    "queued",
		Source:    string(normalized.Source),
		Sender:    strings.TrimSpace(normalized.Sender),
		Summary:   turnSummary(normalized.BodyText()),
		CreatedAt: now,
		UpdatedAt: now,
		Envelope:  normalized,
	}
	session.enqueue(item)
	if err := s.writeSessionQueue(session); err != nil {
		return nil, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        now,
		Category:  "capability",
		Action:    "queue_turn",
		Severity:  "info",
		SessionID: session.id,
		Source:    item.Source,
		Summary:   "Queued " + string(item.Mode) + " message while session was running.",
		Metadata: map[string]string{
			"turn_id": item.ID,
			"mode":    string(item.Mode),
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)
	return &SubmitResult{
		SessionID: session.id,
		TurnID:    turnID,
		Completed: false,
		Status:    "queued",
		UpdatedAt: now,
	}, nil
}

func (s *Service) injectActiveTurn(session *sessionState, envelope message.Envelope, mode QueueMode) (*SubmitResult, bool, error) {
	if session == nil {
		return nil, false, ErrSessionNotFound
	}
	activeTurnID := session.activeTurnID()
	if activeTurnID == "" {
		return nil, false, nil
	}
	now := s.now()
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = now
	}
	envelope.SessionID = session.id
	normalized := envelope.Normalized()
	if normalized.Metadata == nil {
		normalized.Metadata = map[string]string{}
	}
	normalized.Metadata["queue_mode"] = string(normalizeQueueMode(mode))
	injectionID := session.nextTurnID(now)
	item := QueuedTurn{
		ID:        injectionID,
		Mode:      normalizeQueueMode(mode),
		Status:    "injected",
		Source:    string(normalized.Source),
		Sender:    strings.TrimSpace(normalized.Sender),
		Summary:   turnSummary(normalized.BodyText()),
		CreatedAt: now,
		UpdatedAt: now,
		Envelope:  normalized,
	}
	remaining := session.addTurnInjection(activeTurnID, item, now)
	if err := s.writeSessionTurns(session); err != nil {
		return nil, true, err
	}
	injectedText := normalized.BodyText()
	if strings.EqualFold(normalized.Metadata["queue_mode"], string(QueueModeSteering)) {
		// Match the framed text the model receives (see DrainInjections) so
		// the UI shows exactly what was injected.
		injectedText = steeringFrame(injectedText)
	}
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Type:      events.EventUserMessageAccepted,
		Timestamp: now,
		Payload: events.MessagePayload{
			Source:      string(normalized.Source),
			Sender:      strings.TrimSpace(normalized.Sender),
			Text:        injectedText,
			Attachments: normalized.ProtocolAttachments(),
			Metadata:    map[string]string{"queue_mode": string(normalizeQueueMode(mode))},
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Type:      events.EventMessageInjected,
		Timestamp: now,
		Payload: events.MessageInjectedPayload{
			Count:     1,
			Mode:      string(item.Mode),
			Remaining: remaining,
			Summary:   item.Summary,
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)
	return &SubmitResult{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Completed: false,
		Status:    "injected",
		UpdatedAt: now,
	}, true, nil
}

func (s *Service) startQueuedTurns(session *sessionState) {
	if session == nil {
		return
	}
	next, ok := session.peekQueued()
	if !ok {
		return
	}
	release, acquired := session.tryAcquire()
	if !acquired {
		return
	}
	if !session.dropQueued(next.ID) {
		release()
		return
	}
	_ = s.writeSessionQueue(session)
	turn, result, err := s.startUserTurnLocked(session, next.Envelope.Normalized(), true)
	if err != nil {
		release()
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    next.ID,
			Type:      events.EventErrorRaised,
			Timestamp: s.now(),
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to start queued turn: %v", err)},
		})
		return
	}
	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(turn.TurnID, cancel, result.UpdatedAt)
	go func() {
		defer func() {
			session.clearActiveTurn(turn.TurnID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, session.id)
		_, _ = s.finishAgentTurnLocked(runCtx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	}()
}

// steeringFrame wraps a mid-turn steering message so the model treats it as
// an interruption of the running task instead of a deferred follow-up.
func steeringFrame(body string) string {
	return "【用户打断 · Steer】请暂停当前进行中的工作，优先处理这条新指令，并简要说明对原任务的影响：\n\n" + body
}

func normalizeQueueMode(mode QueueMode) QueueMode {
	switch QueueMode(strings.TrimSpace(string(mode))) {
	case QueueModeSteering:
		return QueueModeSteering
	default:
		return QueueModeFollowUp
	}
}

// GetTurn returns the persisted lifecycle state for one turn. It lets a
// reconnecting client recover a turn's status directly instead of pulling the
// whole session snapshot.
func (s *Service) GetTurn(ctx context.Context, sessionID, turnID string) (*TurnRecord, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	record, err := session.getTurnRecord(turnID)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// CancelTurn requests cancellation of the active asynchronous turn.
func (s *Service) CancelTurn(ctx context.Context, sessionID, turnID string) (*CancelTurnResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	activeTurnID, ok := session.cancelActiveTurn(turnID)
	if !ok {
		return nil, newTurnNotFoundError(turnID)
	}
	now := s.now()
	session.updateTurnStatus(activeTurnID, "canceling", "", "", now)
	if err := s.writeSessionTurns(session); err != nil {
		return nil, err
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    activeTurnID,
		Type:      events.EventWarningRaised,
		Timestamp: now,
		Payload:   events.NoticePayload{Message: "Turn cancellation requested."},
	})
	return &CancelTurnResult{
		SessionID: sessionID,
		TurnID:    activeTurnID,
		Status:    "canceling",
		UpdatedAt: now,
	}, nil
}

// RetryTurnAsync replays the latest retryable turn from its persisted input and
// continues execution on a service-owned background context.
func (s *Service) RetryTurnAsync(ctx context.Context, sessionID, turnID string) (*SubmitResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, ok := session.tryAcquire()
	if !ok {
		return nil, newSessionBusyError(sessionID)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	previous, err := session.retryableTurnRecord(turnID)
	if err != nil {
		return nil, err
	}
	if previous.Envelope == nil {
		return nil, newTurnNotRetryableError(turnID, "original input was not persisted")
	}
	currentMessages := session.agent.GetMessages()
	if previous.PriorMessageCount < 0 || previous.PriorMessageCount > len(currentMessages) {
		return nil, newTurnNotRetryableError(turnID, "conversation has changed since the original turn")
	}

	session.agent.TruncateMessages(previous.PriorMessageCount)
	session.agent.ClearPendingResume()
	turn, result, err := s.startUserTurnLocked(session, previous.Envelope.Normalized(), true)
	if err != nil {
		return nil, err
	}
	session.markTurnRetry(turn.TurnID, previous.ID, s.now())
	if err := s.writeSessionTurns(session); err != nil {
		return nil, err
	}
	result.RetryOf = previous.ID

	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(turn.TurnID, cancel, result.UpdatedAt)
	go func() {
		defer func() {
			session.clearActiveTurn(turn.TurnID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, sessionID)
		_, _ = s.finishAgentTurnLocked(runCtx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	}()
	released = true
	return result, nil
}

// ResumeTurnAsync continues the latest interrupted turn from the persisted
// transcript checkpoint instead of replaying the original input from scratch.
func (s *Service) ResumeTurnAsync(ctx context.Context, sessionID, turnID string) (*SubmitResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, ok := session.tryAcquire()
	if !ok {
		return nil, newSessionBusyError(sessionID)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	record, err := session.resumableTurnRecord(turnID)
	if err != nil {
		return nil, err
	}
	envelope := record.Envelope.Normalized()
	runtimeCtx := s.buildRuntimeContext(sessionID, session.locator, envelope)
	runtimeCtx.ProjectLedger, runtimeCtx.ProjectLedgerUpdatedAt = s.projectLedgerForRuntimeContext(sessionID)
	now := s.now()
	session.agent.ClearPendingResume()
	session.updateTurnStatus(record.ID, "running", "", "", now)
	if err := s.persistSession(session, now); err != nil {
		return nil, err
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    record.ID,
		Type:      events.EventWarningRaised,
		Timestamp: now,
		Payload: events.NoticePayload{
			Message: "Resuming interrupted turn from persisted checkpoint.",
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    record.ID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)

	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(record.ID, cancel, now)
	go func() {
		defer func() {
			session.clearActiveTurn(record.ID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, sessionID)
		_, _ = s.finishAgentTurnLocked(runCtx, session, record.ID, envelope, runtimeCtx, record.PriorMessageCount)
	}()
	released = true
	return &SubmitResult{
		SessionID: sessionID,
		TurnID:    record.ID,
		Completed: false,
		Status:    "running",
		UpdatedAt: now,
	}, nil
}

type preparedUserTurn struct {
	TurnID            string
	Envelope          message.Envelope
	RuntimeContext    automation.SessionContext
	PriorMessageCount int
}

func (s *Service) runUserTurnLocked(ctx context.Context, session *sessionState, envelope message.Envelope) (*SubmitResult, error) {
	turn, _, err := s.startUserTurnLocked(session, envelope, false)
	if err != nil {
		return nil, err
	}
	result, err := s.finishAgentTurnLocked(ctx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	// Fire async title generation only when the turn completes cleanly (no
	// pending permission). This avoids racing with recovery/continuation LLM
	// calls in tests and real scenarios.
	if result != nil && !result.PendingApproval && err == nil {
		s.maybeGenerateTitleAsync(session, envelope)
	}
	return result, err
}

func (s *Service) startUserTurnLocked(session *sessionState, envelope message.Envelope, persistRunning bool) (preparedUserTurn, *SubmitResult, error) {
	sessionID := session.id
	now := s.now()
	s.reconcileExpiredPermissionResume(session, now)
	turnID := session.nextTurnID(now)
	runtimeCtx := s.buildRuntimeContext(sessionID, session.locator, envelope)
	if runtimeCtx.Metadata == nil {
		runtimeCtx.Metadata = map[string]string{}
	}
	runtimeCtx.Metadata["turn_id"] = turnID
	attachSessionGraphContext(session, &runtimeCtx)
	runtimeCtx.ProjectLedger, runtimeCtx.ProjectLedgerUpdatedAt = s.projectLedgerForRuntimeContext(sessionID)
	priorMessageCount := len(session.agent.GetMessages())
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = now
	}
	envelope.SessionID = sessionID
	modelEnvelope, eventMetadata, err := s.envelopeWithNoteContext(envelope)
	if err != nil {
		return preparedUserTurn{}, nil, err
	}
	modelEnvelope.SessionID = sessionID
	session.agent.AddEnvelope(modelEnvelope)
	session.setTitleIfEmpty(sessionTitleFromEnvelope(envelope))
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventUserMessageAccepted,
		Timestamp: now,
		Payload: events.MessagePayload{
			Source:      string(envelope.Source),
			Sender:      envelope.Sender,
			Text:        envelope.BodyText(),
			Attachments: envelope.ProtocolAttachments(),
			Metadata:    eventMetadata,
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventAgentIdentityUpdated,
		Timestamp: now,
		Payload: events.AgentIdentityPayload{
			ID:                session.identity.ID,
			Name:              session.identity.Name,
			Kind:              session.identity.Kind,
			Role:              session.identity.Role,
			ParentID:          session.identity.ParentID,
			SessionID:         session.identity.SessionID,
			Source:            session.identity.Source,
			CapabilitySummary: append([]string{}, session.identity.CapabilitySummary...),
			ModelHint:         session.identity.ModelHint,
			BudgetHint:        session.identity.BudgetHint,
			Display:           cloneMapStringString(session.identity.Display),
			LastActivityAt:    now,
		},
	})
	turn := preparedUserTurn{
		TurnID:            turnID,
		Envelope:          envelope,
		RuntimeContext:    runtimeCtx,
		PriorMessageCount: priorMessageCount,
	}
	session.recordTurnStarted(turnID, envelope, priorMessageCount, now)
	if err := s.writeSessionTurns(session); err != nil {
		return preparedUserTurn{}, nil, err
	}
	if !persistRunning {
		return turn, nil, nil
	}

	updatedAt := s.now()
	if err := s.persistSession(session, updatedAt); err != nil {
		return preparedUserTurn{}, nil, err
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)
	return turn, &SubmitResult{
		SessionID: sessionID,
		TurnID:    turnID,
		Completed: false,
		Status:    "running",
		UpdatedAt: updatedAt,
	}, nil
}

func (s *Service) finishAgentTurnLocked(ctx context.Context, session *sessionState, turnID string, envelope message.Envelope, runtimeCtx automation.SessionContext, priorMessageCount int) (*SubmitResult, error) {
	sessionID := session.id
	artifactEvents := &artifactCollector{}
	unsubscribeArtifacts := session.events.Attach(artifactEvents)
	defer unsubscribeArtifacts()
	runSink := events.SinkFunc(func(event events.Event) {
		if event.Type == events.EventRunnerPhaseChanged {
			if payload, ok := event.Payload.(events.RunnerPhasePayload); ok {
				now := event.Timestamp
				if now.IsZero() {
					now = s.now()
				}
				session.updateActivePhase(turnID, payload.Phase)
				session.updateTurnPhase(turnID, payload.Phase, payload.RecoveryHint, payload.ToolName, now)
			}
		}
		session.events.Emit(event)
	})

	runErr := session.agent.RunWithOptions(ctx, agent.RunOptions{
		SessionID:        sessionID,
		TurnID:           turnID,
		ActorID:          session.identity.ID,
		ActorKind:        "main",
		EmitRunnerPhases: true,
		Sink:             runSink,
		RuntimeContext:   runtimeCtx,
		Checkpoint: func() {
			s.checkpointRunningTurn(session, turnID)
		},
		DrainInjections: func(ctx context.Context, limit int) (conversation.InjectionDrain, error) {
			_ = ctx
			now := s.now()
			injected := session.drainTurnInjections(turnID, limit, now)
			if len(injected) == 0 {
				return conversation.InjectionDrain{}, nil
			}
			messages := make([]protocol.Message, 0, len(injected))
			summaries := make([]string, 0, len(injected))
			mode := ""
			for _, envelope := range injected {
				injectedMsg := envelope.ToProtocolMessage(protocol.RoleUser, "", false)
				if envelope.Metadata != nil && strings.EqualFold(envelope.Metadata["queue_mode"], string(QueueModeSteering)) {
					// Steering is an interruption, not a follow-up: frame the
					// injected instruction explicitly so the model pauses the
					// current sub-task instead of treating it as one more
					// queued user message that can be deferred. The metadata
					// text/parts must carry the frame too: BuildAPIMessages
					// rebuilds upgraded user messages from metadata, ignoring
					// msg.Content.
					framed := steeringFrame(envelope.BodyText())
					injectedMsg.Content = []protocol.Block{protocol.TextBlock(framed)}
					if injectedMsg.Metadata == nil {
						injectedMsg.Metadata = &protocol.Metadata{}
					}
					injectedMsg.Metadata.Text = framed
					injectedMsg.Metadata.Parts = nil
				}
				messages = append(messages, injectedMsg)
				if summary := turnSummary(envelope.BodyText()); summary != "" {
					summaries = append(summaries, summary)
				}
				if envelope.Metadata != nil && mode == "" {
					mode = envelope.Metadata["queue_mode"]
				}
			}
			remaining := len(session.pendingTurnInjections(turnID))
			_ = s.writeSessionTurns(session)
			session.events.Emit(events.Event{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      events.EventMessageInjected,
				Timestamp: now,
				Payload: events.MessageInjectedPayload{
					Count:     len(injected),
					Mode:      mode,
					Remaining: remaining,
					Summary:   strings.Join(summaries, " | "),
				},
			})
			return conversation.InjectionDrain{
				Messages:  messages,
				Count:     len(injected),
				Remaining: remaining,
				Mode:      mode,
				Summary:   strings.Join(summaries, " | "),
			}, nil
		},
	})
	returnErr := runErr
	submitStatus := "completed"
	pendingApproval := false
	pendingRequestID := ""
	var pendingErr tools.ErrPermissionPending
	turnCanceled := runErr != nil && errors.Is(context.Cause(ctx), ErrTurnCanceled)
	if errors.As(runErr, &pendingErr) {
		pendingApproval = true
		pendingRequestID = strings.TrimSpace(pendingErr.RequestID)
		submitStatus = "pending_approval"
		returnErr = nil
		session.agent.SetPendingResume(pendingRequestID, priorMessageCount, envelope, runtimeCtx, session.pendingTurnInjections(turnID)...)
	} else if turnCanceled {
		submitStatus = "canceled"
		returnErr = nil
		session.agent.ClearPendingResume()
	} else {
		session.agent.ClearPendingResume()
	}
	artifactAttachments, artifactWarnings := s.materializeArtifactPaths(sessionID, artifactEvents.Paths())
	if len(artifactAttachments) > 0 {
		session.agent.AppendAssistantDelivery("", "", artifactAttachments)
	}
	updatedAt := s.now()
	status := submitStatus
	if runErr != nil && !pendingApproval && !turnCanceled {
		status = "error"
	}
	if status != "pending_approval" {
		session.promoteTurnInjectionsToQueue(turnID, updatedAt)
		_ = s.writeSessionQueue(session)
	}
	errorText := ""
	if status == "error" && runErr != nil {
		// 5.2 Turn Error layering: a NonRetryableTurnError carries a precise
		// user-facing message (e.g. an invalid request shape) that should
		// surface verbatim; every other failure keeps the raw error so
		// provider internals stay debuggable without leaking via the UI.
		if conversation.ClassifyTurnError(runErr) == conversation.TurnErrorNonRetryable {
			errorText = conversation.TurnFailureMessage(runErr)
		} else {
			errorText = runErr.Error()
		}
	}
	session.updateTurnStatus(turnID, status, pendingRequestID, errorText, updatedAt)
	if status == "error" {
		session.updateTurnPhase(turnID, conversation.PhaseError, "", "", updatedAt)
	} else if status == "canceled" || status == "interrupted" {
		session.updateTurnPhase(turnID, conversation.PhaseInterrupted, "", "", updatedAt)
	}
	ledgerErr := s.updateProjectLedgerFromTurn(session, turnID, envelope, status, runErr, priorMessageCount, updatedAt)
	turnWriteErr := s.writeSessionTurns(session)
	persistErr := s.persistSession(session, updatedAt)
	for _, err := range []error{ledgerErr, turnWriteErr, persistErr} {
		if err == nil {
			continue
		}
		if returnErr == nil {
			returnErr = err
		} else {
			session.events.Emit(events.Event{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      events.EventWarningRaised,
				Timestamp: updatedAt,
				Payload: events.NoticePayload{
					Message: fmt.Sprintf("failed to persist session state: %v", err),
				},
			})
		}
	}
	for _, warning := range artifactWarnings {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: warning},
		})
	}

	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})

	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventTurnCompleted,
		Timestamp: updatedAt,
		Payload:   events.TurnPayload{Status: status},
	})
	s.captureSessionSignalCandidates(session, updatedAt, turnID)
	_ = s.writeSessionTimeline(session)

	// The turn reached a terminal status and the last persist above captured
	// the full timeline into a durable checkpoint. Rotate the append-only
	// event journal so it only carries crash-recovery deltas since the last
	// completed turn instead of growing unboundedly. Rotation is best-effort:
	// a stale journal is harmless (events deduplicate on replay), so a failure
	// here never fails the turn itself.
	if isTerminalTurnStatus(status) && persistErr == nil {
		if rotateErr := s.rotateSessionEventJournal(session); rotateErr != nil {
			// best-effort: ignore
		}
	}

	return &SubmitResult{
		SessionID:        sessionID,
		TurnID:           turnID,
		Completed:        runErr == nil,
		Status:           status,
		PendingApproval:  pendingApproval,
		PendingRequestID: pendingRequestID,
		UpdatedAt:        updatedAt,
	}, returnErr
}

func (s *Service) checkpointRunningTurn(session *sessionState, turnID string) {
	if session == nil {
		return
	}
	now := s.now()
	session.updateTurnStatus(turnID, "running", "", "", now)
	if err := s.persistSession(session, now); err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload: events.NoticePayload{
				Message: fmt.Sprintf("failed to persist turn checkpoint: %v", err),
			},
		})
	}
}

func (s *Service) captureSessionSignalCandidates(session *sessionState, now time.Time, turnID string) {
	if session == nil || session.agent == nil {
		return
	}
	timeline := session.timeline.Entries(snapshotTimelineLimit)
	if err := session.agent.CaptureTimelineMemoryCandidates(timeline); err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to capture timeline memory candidates: %v", err)},
		})
	}

	if s.analyze == nil {
		return
	}
	report, err := s.analyze(buildInsightsInput(collectSessionInsightsSnapshot(session, timeline)))
	if err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to analyze insights for memory bridge: %v", err)},
		})
		return
	}
	if err := session.agent.CaptureInsightMemoryCandidates(report); err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to capture insight memory candidates: %v", err)},
		})
	}
}

type sessionInsightsSnapshot struct {
	Messages     []protocol.Message
	ActiveSkills []string
	ToolCatalog  tools.ToolCatalog
	Todos        []todo.Item
	Tasks        []*task.FileTask
	Timeline     []events.Event
}

func collectSessionInsightsSnapshot(session *sessionState, timeline []events.Event) sessionInsightsSnapshot {
	return sessionInsightsSnapshot{
		Messages:     session.agent.GetMessages(),
		ActiveSkills: session.agent.ActiveSkillNames(),
		ToolCatalog:  session.agent.ToolCatalog(),
		Todos:        session.agent.TodoMgr().List(),
		Tasks:        session.agent.TaskMgr().List(),
		Timeline:     append([]events.Event{}, timeline...),
	}
}

func buildInsightsInput(snapshot sessionInsightsSnapshot) insights.Input {
	input := insights.Input{
		CurrentMessages: make([]insights.Message, 0, len(snapshot.Messages)),
		ActiveSkills:    append([]string{}, snapshot.ActiveSkills...),
		ToolCatalog: insights.ToolCatalog{
			ActiveBundles: append([]string{}, snapshot.ToolCatalog.ActiveBundles...),
		},
		Todos: make([]insights.WorkItem, 0, len(snapshot.Todos)),
		Tasks: make([]insights.WorkItem, 0, len(snapshot.Tasks)),
	}

	for _, msg := range snapshot.Messages {
		textParts := make([]string, 0, len(msg.Content))
		toolNames := make([]string, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch string(block.Type) {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_use":
				if block.Name != "" {
					toolNames = append(toolNames, block.Name)
				}
			}
		}
		input.CurrentMessages = append(input.CurrentMessages, insights.Message{
			Text:      strings.Join(textParts, ""),
			ToolNames: toolNames,
		})
	}

	for _, item := range snapshot.Todos {
		input.Todos = append(input.Todos, insights.WorkItem{Status: string(item.Status)})
	}
	for _, item := range snapshot.Tasks {
		input.Tasks = append(input.Tasks, insights.WorkItem{Status: string(item.Status)})
	}
	return input
}

func (s *Service) resumePendingTurnLocked(ctx context.Context, session *sessionState, requestID, turnID string) (*SubmitResult, error) {
	pending := session.agent.PendingResumeState()
	if pending == nil {
		return nil, nil
	}
	if reqID := strings.TrimSpace(requestID); reqID != "" && strings.TrimSpace(pending.RequestID) != "" && strings.TrimSpace(pending.RequestID) != reqID {
		return nil, fmt.Errorf("permission request does not match the blocked turn")
	}
	currentMessages := session.agent.GetMessages()
	if pending.PriorMessageCount < 0 || pending.PriorMessageCount > len(currentMessages) {
		return nil, fmt.Errorf("blocked turn state is no longer resumable")
	}
	session.agent.ClearPendingResume()
	envelope := pending.Envelope.Normalized()
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = s.now()
	}
	envelope.SessionID = session.id
	resumePriorMessageCount := len(currentMessages)
	session.agent.AppendRuntimeFeedback("The previously blocked tool permission has been approved. Continue from the current transcript and retry only the approved tool call if needed. Do not repeat completed analysis, reread files already read, or restart the user's task from the beginning.")
	for _, injected := range pending.Injections {
		mode := QueueModeFollowUp
		if injected.Metadata != nil && strings.EqualFold(injected.Metadata["queue_mode"], string(QueueModeSteering)) {
			mode = QueueModeSteering
		}
		now := s.now()
		session.addTurnInjection(turnID, QueuedTurn{
			ID:        session.nextTurnID(now),
			Mode:      mode,
			Status:    "injected",
			Source:    string(injected.Source),
			Sender:    strings.TrimSpace(injected.Sender),
			Summary:   turnSummary(injected.BodyText()),
			CreatedAt: now,
			UpdatedAt: now,
			Envelope:  injected.Normalized(),
		}, now)
	}
	session.setTitleIfEmpty(sessionTitleFromEnvelope(envelope))
	return s.finishAgentTurnLocked(ctx, session, turnID, envelope, pending.RuntimeContext.Clone(), resumePriorMessageCount)
}

func (s *Service) reconcileExpiredPermissionResume(session *sessionState, now time.Time) {
	if session == nil {
		return
	}
	pending := session.agent.PendingResumeState()
	if pending == nil || strings.TrimSpace(pending.RequestID) == "" {
		return
	}
	requestID := strings.TrimSpace(pending.RequestID)
	for _, item := range session.agent.PendingPermissions(session.id) {
		if strings.TrimSpace(item.ID) == requestID {
			return
		}
	}
	session.updateTurnPermissionStatus(requestID, tools.PermissionStatusExpired, now)
	session.agent.AppendRuntimeFeedback("The previously blocked tool permission expired before it was approved. Do not retry that blocked tool call automatically. Continue from the current transcript with a safer alternative, or ask for fresh approval if the tool call is still necessary.")
	session.agent.ClearPendingResume()
	_ = s.writeSessionTurns(session)
}

// PostRuntimeReply appends a background/runtime assistant reply into an existing session.
func (s *Service) PostRuntimeReply(ctx context.Context, sessionID, text string) error {
	return s.PostRuntimeReplyWithArtifactPaths(ctx, sessionID, text, nil)
}

// PostRuntimeReplyWithArtifactPaths appends a background/runtime assistant
// reply plus any generated local files into an existing session.
func (s *Service) PostRuntimeReplyWithArtifactPaths(ctx context.Context, sessionID, text string, artifactPaths []string) error {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	attachments, warnings := s.materializeArtifactPaths(sessionID, artifactPaths)
	if text == "" && len(attachments) == 0 {
		if len(warnings) > 0 {
			return errors.New(strings.Join(warnings, "; "))
		}
		return nil
	}
	release, err := session.acquire(context.Background())
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	now := s.now()
	turnID := session.nextTurnID(now)
	session.agent.AppendAssistantDelivery(text, protocol.KindBackground, attachments)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil {
		return persistErr
	}

	if text != "" {
		payload := events.TextPayload{Role: protocol.RoleAssistant, Text: text}
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventAssistantTextDelta,
			Timestamp: updatedAt,
			Payload:   payload,
		})
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventAssistantMessageComplete,
			Timestamp: updatedAt,
			Payload:   payload,
		})
	}
	for _, warning := range warnings {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: warning},
		})
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventTurnCompleted,
		Timestamp: updatedAt,
		Payload:   events.TurnPayload{Status: "completed"},
	})
	_ = s.writeSessionTimeline(session)
	return nil
}

// ExecuteCommand runs one serialized slash command against the session.
