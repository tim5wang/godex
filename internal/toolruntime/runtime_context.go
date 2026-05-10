package toolruntime

import (
	"context"

	"github.com/tim5wang/godex/internal/domain/automation"
)

type sessionIDKey struct{}
type sessionContextKey struct{}

// WithSessionID annotates tool execution context with the current session ID.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext returns the active session ID for the current tool turn.
func SessionIDFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return value
	}
	return ""
}

// WithSessionContext annotates tool execution context with the current runtime session context.
func WithSessionContext(ctx context.Context, runtimeContext automation.SessionContext) context.Context {
	if runtimeContext.SessionID == "" && runtimeContext.LocatorChannel == "" && runtimeContext.Source == "" && runtimeContext.AgentProfile == "" && runtimeContext.SecurityProfile == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionContextKey{}, runtimeContext)
}

// SessionContextFromContext returns the current runtime session context.
func SessionContextFromContext(ctx context.Context) automation.SessionContext {
	if value, ok := ctx.Value(sessionContextKey{}).(automation.SessionContext); ok {
		return value
	}
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return automation.SessionContext{}
	}
	return automation.SessionContext{SessionID: sessionID}
}
