package backend

import (
	"context"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/tools"
	"strings"
)

func (s *Service) PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.PendingPermissions(sessionID), nil
}

// ApprovePermission resolves one pending permission request.
func (s *Service) ApprovePermission(ctx context.Context, sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	if isInFlightACPPermission(session.agent.PendingPermissions(sessionID), requestID) {
		resolution, err := session.agent.ApprovePendingPermission(sessionID, requestID, scope)
		if err != nil {
			return tools.PermissionResolution{}, err
		}
		now := s.now()
		session.updateTurnPermissionStatus(requestID, tools.PermissionStatusApproved, now)
		s.appendPermissionAuditEvent("approve_permission", "info", sessionID, resolution)
		session.events.Emit(events.Event{SessionID: sessionID, Type: events.EventSnapshotReady, Timestamp: now, Payload: events.SnapshotPayload{UpdatedAt: now, Running: true}})
		return resolution, nil
	}
	release, lockedHere, err := s.acquireSessionIfNeeded(ctx, sessionID, session)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	if release != nil {
		defer release()
	}
	resolution, err := session.agent.ApprovePendingPermission(sessionID, requestID, scope)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	now := s.now()
	resolvedRequestID := strings.TrimSpace(resolution.RequestID)
	if resolvedRequestID == "" {
		resolvedRequestID = strings.TrimSpace(requestID)
	}
	session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusApproved, now)
	s.appendPermissionAuditEvent("approve_permission", "info", sessionID, resolution)
	if pending := session.agent.PendingResumeState(); pending != nil && strings.TrimSpace(pending.RequestID) == resolvedRequestID {
		beforeMessages := session.agent.GetMessages()
		beforeCount := len(beforeMessages)
		resumeStart := beforeCount
		for i := beforeCount - 1; i >= 0; i-- {
			if beforeMessages[i].Role == protocol.RoleAssistant {
				resumeStart = i
				break
			}
		}
		resumeTurnID := session.nextTurnID(s.now())
		resolution.ResumeTurnID = resumeTurnID
		resumeResult, resumeErr := s.resumePendingTurnLocked(ctx, session, requestID, resumeTurnID)
		resolution.Resumed = true
		if resumeResult != nil {
			resolution.ResumeStatus = strings.TrimSpace(resumeResult.Status)
			resolution.ResumePendingRequestID = strings.TrimSpace(resumeResult.PendingRequestID)
		}
		if output := strings.TrimSpace(assistantTextSince(session.agent.GetMessages(), resumeStart)); output != "" {
			resolution.ResumeOutput = output
		}
		if resumeErr != nil {
			resolution.ResumeStatus = "error"
			resolution.ResumeError = resumeErr.Error()
		} else {
			resolution.Status = tools.PermissionStatusResumed
			session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusResumed, s.now())
		}
	} else if jobID := subagentJobIDFromPermissionRequest(resolution.Request); jobID != "" {
		resolution.Resumed = true
		view, resumeErr := session.agent.ResumeDurableSubagentViewWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
		if resumeErr != nil {
			resolution.ResumeStatus = "error"
			resolution.ResumeError = resumeErr.Error()
		} else {
			resolution.Status = tools.PermissionStatusResumed
			session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusResumed, s.now())
			if status := strings.TrimSpace(view.Status); status != "" {
				resolution.ResumeStatus = "subagent_" + status
			} else {
				resolution.ResumeStatus = "subagent_resumed"
			}
		}
	} else if lockedHere {
		if err := s.touchSession(session, s.now()); err != nil {
			return tools.PermissionResolution{}, err
		}
	}
	if lockedHere {
		_ = s.writeSessionTurns(session)
	}
	return resolution, nil
}

func subagentJobIDFromPermissionRequest(req tools.PermissionRequest) string {
	sender := strings.TrimSpace(req.Sender)
	if !strings.HasPrefix(sender, "subagent:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(sender, "subagent:"))
}

// DenyPermission resolves one pending permission request with denial.
func (s *Service) DenyPermission(ctx context.Context, sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	if isInFlightACPPermission(session.agent.PendingPermissions(sessionID), requestID) {
		resolution, err := session.agent.DenyPendingPermission(sessionID, requestID, reason)
		if err != nil {
			return tools.PermissionResolution{}, err
		}
		now := s.now()
		session.updateTurnPermissionStatus(requestID, tools.PermissionStatusDenied, now)
		s.appendPermissionAuditEvent("deny_permission", "warning", sessionID, resolution)
		session.events.Emit(events.Event{SessionID: sessionID, Type: events.EventSnapshotReady, Timestamp: now, Payload: events.SnapshotPayload{UpdatedAt: now, Running: true}})
		return resolution, nil
	}
	release, lockedHere, err := s.acquireSessionIfNeeded(ctx, sessionID, session)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	if release != nil {
		defer release()
	}
	resolution, err := session.agent.DenyPendingPermission(sessionID, requestID, reason)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	s.appendPermissionAuditEvent("deny_permission", "warning", sessionID, resolution)
	now := s.now()
	resolvedRequestID := strings.TrimSpace(resolution.RequestID)
	if resolvedRequestID == "" {
		resolvedRequestID = strings.TrimSpace(requestID)
	}
	session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusDenied, now)
	if pending := session.agent.PendingResumeState(); pending != nil && strings.TrimSpace(pending.RequestID) == resolvedRequestID {
		session.agent.AppendRuntimeFeedback("The previously blocked tool permission was denied. Do not retry that blocked tool call. Explain the denial and continue with a safer alternative if possible.")
		session.agent.ClearPendingResume()
	}
	_ = s.writeSessionTurns(session)
	if lockedHere {
		if err := s.touchSession(session, now); err != nil {
			return tools.PermissionResolution{}, err
		}
		return resolution, nil
	}
	return resolution, nil
}

func isInFlightACPPermission(items []tools.PendingPermission, requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	for _, item := range items {
		if item.ID == requestID && strings.HasPrefix(strings.TrimSpace(item.Request.ToolName), "acp:") {
			return true
		}
	}
	return false
}

// ClearMessages clears the current session conversation history and persists the result.
func (s *Service) ClearMessages(ctx context.Context, sessionID string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	session.agent.ClearMessages()
	session.clearQueue()
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	if queueErr := s.writeSessionQueue(session); persistErr == nil && queueErr != nil {
		persistErr = queueErr
	}
	release()
	released = true
	if persistErr != nil {
		return persistErr
	}

	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	return nil
}

// ListSessionSkills returns the discoverable skill catalog for the session workspace.
