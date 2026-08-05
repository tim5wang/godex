package backend

import (
	"context"
	"fmt"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/tools"
	"sort"
	"strings"
	"time"
)

func (s *sessionState) acquire(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.gate:
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.gate <- struct{}{}
	}, nil
}

func (s *sessionState) tryAcquire() (func(), bool) {
	select {
	case <-s.gate:
	default:
		return nil, false
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.gate <- struct{}{}
	}, true
}

func (s *sessionState) setActiveTurn(turnID string, cancel context.CancelCauseFunc, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = &activeTurn{id: turnID, cancel: cancel, startedAt: startedAt}
}

func (s *sessionState) activeTurnID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return ""
	}
	return s.active.id
}

func (s *sessionState) updateActivePhase(turnID, phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || strings.TrimSpace(s.active.id) != strings.TrimSpace(turnID) {
		return
	}
	s.active.phase = strings.TrimSpace(phase)
}

func (s *sessionState) clearActiveTurn(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.id != turnID {
		return
	}
	s.active = nil
}

func (s *sessionState) cancelActiveTurn(turnID string) (string, bool) {
	s.mu.RLock()
	active := s.active
	if active == nil || (strings.TrimSpace(turnID) != "" && active.id != strings.TrimSpace(turnID)) {
		s.mu.RUnlock()
		return "", false
	}
	activeTurnID := active.id
	cancel := active.cancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel(ErrTurnCanceled)
	}
	return activeTurnID, true
}

func (s *sessionState) seedTurns(records []TurnRecord) {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	s.turns = normalizeTurnRecords(records)
}

func (s *sessionState) seedQueue(items []QueuedTurn) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.queue = normalizeQueuedTurns(items)
}

func (s *sessionState) clearQueue() {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.queue = nil
}

func (s *sessionState) enqueue(item QueuedTurn) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.queue = append(s.queue, normalizeQueuedTurn(item))
}

func (s *sessionState) peekQueued() (QueuedTurn, bool) {
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	if len(s.queue) == 0 {
		return QueuedTurn{}, false
	}
	return cloneQueuedTurn(s.queue[0]), true
}

func (s *sessionState) dropQueued(id string) bool {
	id = strings.TrimSpace(id)
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if len(s.queue) == 0 || strings.TrimSpace(s.queue[0].ID) != id {
		return false
	}
	s.queue = append([]QueuedTurn{}, s.queue[1:]...)
	return true
}

func (s *sessionState) recordTurnStarted(turnID string, envelope message.Envelope, priorMessageCount int, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	normalized := envelope.Normalized()
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	record := TurnRecord{
		ID:                turnID,
		Status:            "running",
		Source:            string(normalized.Source),
		Sender:            strings.TrimSpace(normalized.Sender),
		Summary:           turnSummary(normalized.BodyText()),
		StartedAt:         now,
		UpdatedAt:         now,
		PriorMessageCount: priorMessageCount,
		Envelope:          &normalized,
	}
	if idx >= 0 {
		existing := s.turns[idx]
		if !existing.StartedAt.IsZero() {
			record.StartedAt = existing.StartedAt
		}
		s.turns[idx] = mergeTurnRecord(existing, record)
	} else {
		s.turns = append(s.turns, record)
	}
	s.turns = trimTurnRecords(s.turns, persistedTurnLimit)
}

func (s *sessionState) updateTurnStatus(turnID, status, pendingRequestID, errorText string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "unknown"
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	record := TurnRecord{ID: turnID, StartedAt: now}
	if idx >= 0 {
		record = s.turns[idx]
	}
	record.Status = status
	record.UpdatedAt = now
	record.PendingRequestID = strings.TrimSpace(pendingRequestID)
	if record.PendingRequestID != "" {
		record.BlockedByPermissionID = record.PendingRequestID
		record.PermissionStatus = tools.PermissionStatusPending
	}
	record.Error = strings.TrimSpace(errorText)
	if isTerminalTurnStatus(status) {
		completedAt := now
		record.CompletedAt = &completedAt
	} else {
		record.CompletedAt = nil
	}
	if idx >= 0 {
		s.turns[idx] = record
	} else {
		s.turns = append(s.turns, record)
	}
	s.turns = trimTurnRecords(s.turns, persistedTurnLimit)
}

func (s *sessionState) updateTurnPermissionStatus(requestID string, status tools.PermissionStatus, now time.Time) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || status == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	for idx := range s.turns {
		if strings.TrimSpace(s.turns[idx].BlockedByPermissionID) != requestID && strings.TrimSpace(s.turns[idx].PendingRequestID) != requestID {
			continue
		}
		s.turns[idx].BlockedByPermissionID = requestID
		s.turns[idx].PermissionStatus = status
		s.turns[idx].UpdatedAt = now
	}
}

func (s *sessionState) markTurnResumeAvailable(turnID, hint string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return
	}
	s.turns[idx].ResumeAvailable = true
	s.turns[idx].RecoveryHint = strings.TrimSpace(hint)
	if now.After(s.turns[idx].UpdatedAt) {
		s.turns[idx].UpdatedAt = now
	}
}

func (s *sessionState) markTurnRetry(turnID, retryOf string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	retryOf = strings.TrimSpace(retryOf)
	if turnID == "" || retryOf == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return
	}
	s.turns[idx].RetryOf = retryOf
	if now.After(s.turns[idx].UpdatedAt) {
		s.turns[idx].UpdatedAt = now
	}
}

func (s *sessionState) updateTurnPhase(turnID, phase, recoveryHint, lastToolName string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return
	}
	record := s.turns[idx]
	if strings.TrimSpace(phase) != "" {
		record.Phase = strings.TrimSpace(phase)
	}
	if strings.TrimSpace(recoveryHint) != "" {
		record.RecoveryHint = strings.TrimSpace(recoveryHint)
	}
	if strings.TrimSpace(lastToolName) != "" {
		record.LastToolName = strings.TrimSpace(lastToolName)
	}
	if now.After(record.UpdatedAt) {
		record.UpdatedAt = now
	}
	s.turns[idx] = record
}

func (s *sessionState) addTurnInjection(turnID string, item QueuedTurn, now time.Time) int {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return 0
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return 0
	}
	normalized := normalizeQueuedTurn(item)
	s.turns[idx].Injections = append(s.turns[idx].Injections, normalized.Envelope.Normalized())
	s.turns[idx].InjectionCount++
	if now.After(s.turns[idx].UpdatedAt) {
		s.turns[idx].UpdatedAt = now
	}
	return len(s.turns[idx].Injections)
}

func (s *sessionState) drainTurnInjections(turnID string, limit int, now time.Time) []message.Envelope {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || limit <= 0 {
		return nil
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 || len(s.turns[idx].Injections) == 0 {
		return nil
	}
	items := append([]message.Envelope{}, s.turns[idx].Injections...)
	sort.SliceStable(items, func(i, j int) bool {
		return strings.EqualFold(items[i].Metadata["queue_mode"], string(QueueModeSteering)) && !strings.EqualFold(items[j].Metadata["queue_mode"], string(QueueModeSteering))
	})
	if len(items) > limit {
		drained := append([]message.Envelope{}, items[:limit]...)
		s.turns[idx].Injections = append([]message.Envelope{}, items[limit:]...)
		s.turns[idx].UpdatedAt = now
		return drained
	}
	s.turns[idx].Injections = nil
	s.turns[idx].UpdatedAt = now
	return items
}

func (s *sessionState) pendingTurnInjections(turnID string) []message.Envelope {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return nil
	}
	return append([]message.Envelope{}, s.turns[idx].Injections...)
}

func (s *sessionState) promoteTurnInjectionsToQueue(turnID string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	var pending []message.Envelope
	s.turnsMu.Lock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx >= 0 && len(s.turns[idx].Injections) > 0 {
		pending = append([]message.Envelope{}, s.turns[idx].Injections...)
		s.turns[idx].Injections = nil
		s.turns[idx].UpdatedAt = now
	}
	s.turnsMu.Unlock()
	for _, envelope := range pending {
		mode := QueueModeFollowUp
		if envelope.Metadata != nil && strings.EqualFold(envelope.Metadata["queue_mode"], string(QueueModeSteering)) {
			mode = QueueModeSteering
		}
		s.enqueue(QueuedTurn{
			ID:        s.nextTurnID(now),
			Mode:      mode,
			Status:    "queued",
			Source:    string(envelope.Source),
			Sender:    strings.TrimSpace(envelope.Sender),
			Summary:   turnSummary(envelope.BodyText()),
			CreatedAt: now,
			UpdatedAt: now,
			Envelope:  envelope.Normalized(),
		})
	}
}

func (s *sessionState) retryableTurnRecord(turnID string) (TurnRecord, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	if idx != len(s.turns)-1 {
		return TurnRecord{}, newTurnNotRetryableError(turnID, "only the latest turn can be retried")
	}
	record := cloneTurnRecord(s.turns[idx])
	if !canRetryTurnStatus(record.Status) {
		return TurnRecord{}, newTurnNotRetryableError(turnID, fmt.Sprintf("status %q cannot be retried", record.Status))
	}
	if record.Envelope == nil {
		return TurnRecord{}, newTurnNotRetryableError(turnID, "original input was not persisted")
	}
	return record, nil
}

func (s *sessionState) resumableTurnRecord(turnID string) (TurnRecord, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	if idx != len(s.turns)-1 {
		return TurnRecord{}, newTurnNotResumableError(turnID, "only the latest turn can be resumed")
	}
	record := cloneTurnRecord(s.turns[idx])
	if !canResumeTurnStatus(record.Status) {
		return TurnRecord{}, newTurnNotResumableError(turnID, fmt.Sprintf("status %q cannot be resumed", record.Status))
	}
	if record.Envelope == nil {
		return TurnRecord{}, newTurnNotResumableError(turnID, "original input was not persisted")
	}
	return record, nil
}

func (s *sessionState) turnRecords(limit int) []TurnRecord {
	if s == nil {
		return nil
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	records := cloneTurnRecords(s.turns)
	if limit <= 0 || len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}

func (s *sessionState) queuedTurns(limit int) []QueuedTurn {
	if s == nil {
		return nil
	}
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	items := cloneQueuedTurns(s.queue)
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func (s *sessionState) hasQueuedRecoveryFor(turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return false
	}
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	for _, item := range s.queue {
		if item.Envelope.Metadata["recovery_of_turn_id"] == turnID {
			return true
		}
	}
	return false
}

func (s *sessionState) snapshotQueuedTurns(limit int) []QueuedTurn {
	items := s.queuedTurns(limit)
	for i := range items {
		items[i].Envelope = message.Envelope{}
	}
	return items
}

func (s *sessionState) snapshotTurnRecords(limit int) []TurnRecord {
	records := s.turnRecords(limit)
	for i := range records {
		records[i].CanRetry = false
		records[i].CanResume = false
		if i == len(records)-1 && canRetryTurnStatus(records[i].Status) && records[i].Envelope != nil {
			records[i].CanRetry = true
		}
		if i == len(records)-1 && canResumeTurnStatus(records[i].Status) && records[i].Envelope != nil {
			records[i].CanResume = true
		}
		records[i].PriorMessageCount = 0
		records[i].Envelope = nil
	}
	return records
}

func (s *sessionState) interruptedTurnIDFromRecords() string {
	records := s.turnRecords(0)
	for i := len(records) - 1; i >= 0; i-- {
		switch records[i].Status {
		case "running", "canceling":
			return records[i].ID
		}
	}
	return ""
}

func (s *sessionState) replayEvents(opts EventReplayOptions) []events.Event {
	if s == nil || s.timeline == nil {
		return nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = snapshotTimelineLimit
	}
	entries := s.timeline.Entries(limit)
	if !opts.ActiveOnly {
		return entries
	}

	s.mu.RLock()
	activeTurnID := ""
	if s.active != nil {
		activeTurnID = s.active.id
	}
	s.mu.RUnlock()
	if activeTurnID == "" {
		return nil
	}

	filtered := make([]events.Event, 0, len(entries))
	for _, event := range entries {
		if event.TurnID == activeTurnID {
			filtered = append(filtered, event)
		}
	}
	return filtered
}
