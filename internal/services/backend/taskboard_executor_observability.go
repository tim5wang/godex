package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// ---------------------------------------------------------------------------
// Execution observability + recovery (the "PJM can see why a run is stuck and
// nudge it back" half). These methods inspect live execution sessions through
// the backend Service, write the findings back into the ledger (so the board
// reflects reality without opening any conversation), finalize executions whose
// session has already failed, and let a human/PJM append a message to a running
// or stalled execution session to recover it.
// ---------------------------------------------------------------------------

// taskboardRecoveryActor is the actor recorded on recovery messages submitted
// into an execution session.
const taskboardRecoveryActor = "taskboard"

// Observe inspects the running execution for a card and returns where the run
// currently is (stage), how it last failed (error type / message), and what it
// last did (tool). The bool reports whether the run is still live (running a
// turn in-process or resumed from a persisted active turn).
func (e *TaskboardExecutor) Observe(ctx context.Context, cardID, executionID string) (taskboard.ExecutionObservation, bool, error) {
	card, err := e.ledger.GetCard(cardID)
	if err != nil {
		return taskboard.ExecutionObservation{}, false, err
	}
	ex, ok := findExecution(card, executionID)
	if !ok {
		return taskboard.ExecutionObservation{}, false, fmt.Errorf("taskboard: execution %s not found on card %s", executionID, cardID)
	}
	sessionID := ex.SessionID
	if sessionID == "" && ex.Host != nil {
		sessionID = ex.Host.SessionID
	}
	if sessionID == "" {
		// No session recorded yet; nothing to observe.
		return taskboard.ExecutionObservation{}, false, nil
	}

	snapshot, err := e.service.Snapshot(ctx, sessionID)
	if err != nil {
		// Session unavailable on disk / not loadable: the run cannot be live,
		// so report it as not running without fabricating a stage.
		return taskboard.ExecutionObservation{}, false, nil
	}

	obs, live := observeFromSnapshot(snapshot)
	// Write the observation back so the ledger always reflects the latest.
	_, _ = e.ledger.UpdateExecutionObservation(cardID, executionID, obs)
	return obs, live, nil
}

// Reconcile walks every running execution across the ledger, observes its
// session, and:
//   - finalizes (failed/cancelled) an execution whose session already reached a
//     terminal error — the classic "disk says running but the model errored out"
//     zombie that left PJM with no insight;
//   - otherwise writes the observation snapshot back so the board shows where
//     the run is stalled (thinking / tool call / waiting approval).
//
// Reconcile never invents a completion: it only finalizes on concrete evidence
// (terminal error turn or an explicit cancel), so a genuinely running task is
// never prematurely closed.
func (e *TaskboardExecutor) Reconcile(ctx context.Context) (taskboard.ReconcileReport, error) {
	report := taskboard.ReconcileReport{}
	for _, card := range e.ledger.ListCards(taskboard.CardFilter{}) {
		for i := range card.Executions {
			ex := &card.Executions[i]
			if ex.Status != taskboard.ExecutionRunning {
				continue
			}
			report.Scanned++
			sessionID := ex.SessionID
			if sessionID == "" && ex.Host != nil {
				sessionID = ex.Host.SessionID
			}
			if sessionID == "" {
				continue
			}
			snapshot, err := e.service.Snapshot(ctx, sessionID)
			if err != nil {
				// Not loadable = the persisted session is gone; leave it to a
				// repair/cleanup step rather than auto-finalizing on a guess.
				continue
			}
			obs, live := observeFromSnapshot(snapshot)
			if !live && obs.ErrorType != "" {
				// The run is not executing a turn and the session recorded a
				// terminal error — finalize it as failed with the insight.
				status := taskboard.ExecutionFailed
				if obs.ErrorType == taskboard.ErrTypeCancelled {
					status = taskboard.ExecutionCancelled
				}
				summary := obs.LastError
				if summary == "" {
					summary = "run errored (no detail)"
				}
				if _, err := e.ledger.FinishExecutionWithObs(card.ID, ex.ID, status, summary, obs); err == nil {
					report.Finalized++
				}
				continue
			}
			// Otherwise record the current stage for observability.
			if _, err := e.ledger.UpdateExecutionObservation(card.ID, ex.ID, obs); err == nil {
				report.Observed++
			}
		}
	}
	return report, nil
}

// Recover appends a message into the card's execution session to nudge a
// running/stalled task (break a thinking loop, apply new params, give a fresh
// instruction). Returns the session id delivered to. If the session is busy the
// message is queued and injected on the next drain window (SubmitAsync follow-up
// semantics), so recovery never blocks.
func (e *TaskboardExecutor) Recover(ctx context.Context, cardID, executionID, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("taskboard: recovery message is required")
	}
	card, err := e.ledger.GetCard(cardID)
	if err != nil {
		return "", err
	}
	ex, ok := findExecution(card, executionID)
	if !ok {
		return "", fmt.Errorf("taskboard: execution %s not found on card %s", executionID, cardID)
	}
	sessionID := ex.SessionID
	if sessionID == "" && ex.Host != nil {
		sessionID = ex.Host.SessionID
	}
	if sessionID == "" {
		return "", fmt.Errorf("taskboard: execution %s has no session to recover", executionID)
	}
	// Ensure the execution session is open (loads it after a restart).
	if _, err := e.service.OpenSession(ctx, e.executionLocator(card, ex)); err != nil {
		return "", fmt.Errorf("taskboard: reopen execution session: %w", err)
	}
	envelope := message.NewRuntimeEnvelope(message.SourceBackground, sessionID, taskboardRecoveryActor, text, e.service.now(), map[string]string{
		taskboardCardIDMetadataKey:   card.ID,
		"taskboard_recovery":     "1",
		"taskboard_execution":    executionID,
	})
	if _, err := e.service.SubmitAsync(ctx, sessionID, envelope, SubmitOptions{QueueMode: QueueModeFollowUp}); err != nil {
		return "", fmt.Errorf("taskboard: submit recovery message to %s: %w", sessionID, err)
	}
	return sessionID, nil
}

// Retry replays the last retryable (errored/cancelled/interrupted) turn of the
// card's execution session. Returns the new turn id. Used when a model-level
// failure (e.g. empty provider response) is retryable after adjusting the
// threshold or prompt.
func (e *TaskboardExecutor) Retry(ctx context.Context, cardID, executionID string) (string, error) {
	card, err := e.ledger.GetCard(cardID)
	if err != nil {
		return "", err
	}
	ex, ok := findExecution(card, executionID)
	if !ok {
		return "", fmt.Errorf("taskboard: execution %s not found on card %s", executionID, cardID)
	}
	sessionID := ex.SessionID
	if sessionID == "" && ex.Host != nil {
		sessionID = ex.Host.SessionID
	}
	if sessionID == "" {
		return "", fmt.Errorf("taskboard: execution %s has no session to retry", executionID)
	}
	if _, err := e.service.OpenSession(ctx, e.executionLocator(card, ex)); err != nil {
		return "", fmt.Errorf("taskboard: reopen execution session: %w", err)
	}
	snapshot, err := e.service.Snapshot(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("taskboard: read execution session: %w", err)
	}
	lastTurnID := retryableTurnID(snapshot.Turns)
	if lastTurnID == "" {
		return "", fmt.Errorf("taskboard: execution %s has no retryable turn", executionID)
	}
	result, err := e.service.RetryTurnAsync(ctx, sessionID, lastTurnID)
	if err != nil {
		return "", fmt.Errorf("taskboard: retry turn %s: %w", lastTurnID, err)
	}
	return result.TurnID, nil
}

// observeFromSnapshot derives the observability snapshot (stage / error / last
// tool) and whether the run is still live from a session snapshot.
func observeFromSnapshot(snapshot Snapshot) (taskboard.ExecutionObservation, bool) {
	obs := taskboard.ExecutionObservation{}

	// Pending approval is the strongest "waiting" signal.
	if snapshot.ActivePermissionBlocker != nil && len(snapshot.PendingPermissions) > 0 {
		obs.Stage = taskboard.StageWaitingApproval
	}

	// Map current phase to a coarse stage.
	phase := strings.TrimSpace(snapshot.ActivePhase)
	switch phase {
	case conversation.PhaseModelRequest:
		if obs.Stage == "" {
			obs.Stage = taskboard.StageThinking
		}
	case conversation.PhaseAwaitingTools:
		if obs.Stage == "" {
			obs.Stage = taskboard.StageToolCall
		}
	case conversation.PhaseFinalResponse:
		if obs.Stage == "" {
			obs.Stage = taskboard.StageFinalResponse
		}
	case conversation.PhaseError:
		obs.Stage = taskboard.StageError
		obs.ErrorType = taskboard.ErrTypeProvider
	case conversation.PhaseInterrupted:
		obs.Stage = taskboard.StageInterrupted
		obs.ErrorType = taskboard.ErrTypeInterrupted
	}

	// Derive error/phase details from the latest turn record when the live phase
	// is not available (idle, crashed, or the session was reloaded from disk).
	turns := snapshot.Turns
	if len(turns) > 0 {
		last := turns[len(turns)-1]
		if last.LastToolName != "" {
			obs.LastTool = last.LastToolName
		}
		if strings.TrimSpace(last.Error) != "" {
			obs.LastError = strings.TrimSpace(last.Error)
		}
		status := strings.TrimSpace(last.Status)
		if obs.ErrorType == "" && status == "error" {
			obs.ErrorType = classifyExecutionError(obs, last)
			if obs.Stage == "" {
				obs.Stage = taskboard.StageError
			}
		}
		if obs.LastTool != "" && obs.ErrorType == "" && status == "error" {
			obs.ErrorType = taskboard.ErrTypeTool
		}
		if status == "canceled" {
			obs.ErrorType = taskboard.ErrTypeCancelled
			obs.Stage = taskboard.StageInterrupted
		}
	}

	// Live only if the session is actively running a turn.
	live := snapshot.Running || snapshot.ActiveTurnID != ""
	if !live && obs.Stage == "" {
		obs.Stage = taskboard.StageIdle
	}
	if obs.ErrorType == "" && obs.Stage == taskboard.StageError {
		obs.ErrorType = taskboard.ErrTypeUnknown
	}
	return obs, live
}

// classifyExecutionError buckets an error turn as provider or tool based on
// whether a tool was the last action before the failure.
func classifyExecutionError(obs taskboard.ExecutionObservation, last TurnRecord) string {
	if strings.TrimSpace(obs.LastTool) != "" || strings.TrimSpace(last.LastToolName) != "" {
		return taskboard.ErrTypeTool
	}
	return taskboard.ErrTypeProvider
}

// findExecution locates the execution record by id on a card.
func findExecution(card taskboard.Card, executionID string) (taskboard.Execution, bool) {
	for _, ex := range card.Executions {
		if ex.ID == executionID {
			return ex, true
		}
	}
	return taskboard.Execution{}, false
}

// retryableTurnID returns the latest turn on the record that can be retried.
func retryableTurnID(turns []TurnRecord) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].CanRetry {
			return turns[i].ID
		}
	}
	return ""
}

// executionLocator reconstructs the session locator for a card's execution, so
// an execution session can be reopened (loads from disk) to observe/recover it
// after a restart.
func (e *TaskboardExecutor) executionLocator(card taskboard.Card, ex taskboard.Execution) SessionLocator {
	channel := taskboardSessionChannel
	key := "card-" + card.ID
	metadata := map[string]string{}
	if ex.Host != nil {
		if strings.TrimSpace(ex.Host.Channel) != "" {
			channel = ex.Host.Channel
		}
		if strings.TrimSpace(ex.Host.Key) != "" {
			key = ex.Host.Key
		}
		if strings.TrimSpace(ex.Host.ProjectDir) != "" {
			metadata[sessionProjectDirMetadataKey] = ex.Host.ProjectDir
		}
	}
	return SessionLocator{Channel: channel, Key: key, Metadata: metadata}
}
