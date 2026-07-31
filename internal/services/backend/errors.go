package backend

import (
	"errors"
	"fmt"
)

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrAttachmentNotFound = errors.New("attachment not found")
	ErrSessionCorrupt     = errors.New("corrupt session state")
	ErrSessionBusy        = errors.New("session is running")
	ErrTurnNotFound       = errors.New("turn not found")
	ErrTurnNotRetryable   = errors.New("turn is not retryable")
	ErrTurnNotResumable   = errors.New("turn is not resumable")
	ErrTurnCanceled       = errors.New("turn canceled")
	// ErrInvalidWorkspaceDir marks a caller-supplied per-session working
	// directory that failed boundary validation (missing, not a
	// directory, unresolvable).  The HTTP layer maps it to 400.
	ErrInvalidWorkspaceDir = errors.New("invalid workspace_dir")
)

func newSessionNotFoundError(sessionID string) error {
	return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
}

func newAttachmentNotFoundError(attachmentID string) error {
	return fmt.Errorf("%w: %s", ErrAttachmentNotFound, attachmentID)
}

func newSessionCorruptError(sessionID, detail string, args ...any) error {
	message := fmt.Sprintf(detail, args...)
	if sessionID == "" {
		return fmt.Errorf("%w: %s", ErrSessionCorrupt, message)
	}
	return fmt.Errorf("%w: session %s: %s", ErrSessionCorrupt, sessionID, message)
}

func newSessionBusyError(sessionID string) error {
	return fmt.Errorf("%w: %s", ErrSessionBusy, sessionID)
}

func newTurnNotFoundError(turnID string) error {
	if turnID == "" {
		return ErrTurnNotFound
	}
	return fmt.Errorf("%w: %s", ErrTurnNotFound, turnID)
}

func newTurnNotRetryableError(turnID, reason string) error {
	if reason == "" {
		reason = "turn cannot be retried"
	}
	if turnID == "" {
		return fmt.Errorf("%w: %s", ErrTurnNotRetryable, reason)
	}
	return fmt.Errorf("%w: %s: %s", ErrTurnNotRetryable, turnID, reason)
}

func newTurnNotResumableError(turnID, reason string) error {
	if reason == "" {
		reason = "turn cannot be resumed"
	}
	if turnID == "" {
		return fmt.Errorf("%w: %s", ErrTurnNotResumable, reason)
	}
	return fmt.Errorf("%w: %s: %s", ErrTurnNotResumable, turnID, reason)
}
