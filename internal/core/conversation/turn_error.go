package conversation

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// TurnErrorClass classifies a turn-level failure by retryability.
//
// It mirrors the QM reference (`temp/qm/src/core/turn-error.ts`), which
// distinguishes errors that must not be retried from everything else, and
// extends it with the roadmap's Retryable / Transient split so the runner can
// route each failure appropriately.
type TurnErrorClass int

const (
	// TurnErrorUnknown is the default for errors without a classification.
	TurnErrorUnknown TurnErrorClass = iota
	// TurnErrorRetryable means the request itself is safe to re-send (e.g. a
	// 5xx provider error or a dropped connection).
	TurnErrorRetryable
	// TurnErrorTransient means the failure is momentary and likely resolves
	// with a short backoff (429 rate limit, gateway timeout, deadline).
	TurnErrorTransient
	// TurnErrorNonRetryable means retrying the same request cannot succeed
	// (e.g. a 4xx client error, an invalid request shape, or user cancel).
	TurnErrorNonRetryable
)

func (c TurnErrorClass) String() string {
	switch c {
	case TurnErrorRetryable:
		return "retryable"
	case TurnErrorTransient:
		return "transient"
	case TurnErrorNonRetryable:
		return "non_retryable"
	default:
		return "unknown"
	}
}

// TurnError is an error that carries an explicit retryability classification.
type TurnError interface {
	error
	Class() TurnErrorClass
}

// RetryableTurnError marks a failure that is safe to retry as-is.
type RetryableTurnError struct {
	msg string
}

func (e *RetryableTurnError) Error() string  { return e.msg }
func (e *RetryableTurnError) Class() TurnErrorClass {
	return TurnErrorRetryable
}

// TransientTurnError marks a momentary failure that likely clears on retry.
type TransientTurnError struct {
	msg string
}

func (e *TransientTurnError) Error() string  { return e.msg }
func (e *TransientTurnError) Class() TurnErrorClass {
	return TurnErrorTransient
}

// NonRetryableTurnError marks a permanent failure; retrying cannot succeed.
type NonRetryableTurnError struct {
	msg string
}

func (e *NonRetryableTurnError) Error() string  { return e.msg }
func (e *NonRetryableTurnError) Class() TurnErrorClass {
	return TurnErrorNonRetryable
}

// NewRetryableTurnError builds a RetryableTurnError.
func NewRetryableTurnError(msg string) *RetryableTurnError { return &RetryableTurnError{msg: msg} }

// NewTransientTurnError builds a TransientTurnError.
func NewTransientTurnError(msg string) *TransientTurnError { return &TransientTurnError{msg: msg} }

// NewNonRetryableTurnError builds a NonRetryableTurnError.
func NewNonRetryableTurnError(msg string) *NonRetryableTurnError {
	return &NonRetryableTurnError{msg: msg}
}

// ClassifyTurnError inspects an error and returns its retryability class.
//
// Explicit TurnError implementations win. Otherwise it infers from transport
// and HTTP status semantics, reusing the same signals as shouldRetryError so
// the runner-level routing agrees with the client-level retry budget.
func ClassifyTurnError(err error) TurnErrorClass {
	if err == nil {
		return TurnErrorUnknown
	}
	var classified interface{ Class() TurnErrorClass }
	if errors.As(err, &classified) {
		return classified.Class()
	}
	if errors.Is(err, context.Canceled) {
		return TurnErrorNonRetryable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TurnErrorTransient
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return TurnErrorTransient
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return TurnErrorRetryable
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return ClassifyTurnError(urlErr.Err)
	}
	var statusErr *apiStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return TurnErrorTransient
		}
		if statusErr.StatusCode >= 500 {
			return TurnErrorRetryable
		}
		if statusErr.StatusCode >= 400 {
			return TurnErrorNonRetryable
		}
	}
	return TurnErrorUnknown
}

const genericTurnFailureMessage = "That turn failed and couldn't be completed. The details are in the operator error log."

// TurnFailureMessage returns the user-facing message for a failed turn.
//
// Mirrors `turnFailureMessage` in the QM reference: a NonRetryableTurnError
// with a real message is surfaced verbatim; every other failure gets the
// generic line so provider internals never leak to the user.
func TurnFailureMessage(err error) string {
	var nonRetryable *NonRetryableTurnError
	if errors.As(err, &nonRetryable) && strings.TrimSpace(nonRetryable.msg) != "" {
		return nonRetryable.msg
	}
	return genericTurnFailureMessage
}
