package conversation

import (
	"context"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

type usageContextKey struct{}

type UsageContext struct {
	APIKeyID        string
	SourceChannel   string
	SessionID       string
	TurnID          string
	JobID           string
	TargetProfileID string
	TargetModel     string
	CreditWeight    float64
}

type UsageEvent struct {
	Context  UsageContext
	Request  protocol.Request
	Response *protocol.Response
	Error    error
	Latency  time.Duration
	Stream   bool
}

type UsageObserver func(context.Context, UsageEvent)

var usageObserverState struct {
	mu       sync.RWMutex
	observer UsageObserver
}

func WithUsageContext(ctx context.Context, usage UsageContext) context.Context {
	if usage.APIKeyID == "" && usage.SourceChannel == "" && usage.SessionID == "" && usage.TurnID == "" && usage.JobID == "" {
		return ctx
	}
	return context.WithValue(ctx, usageContextKey{}, usage)
}

func UsageContextFromContext(ctx context.Context) (UsageContext, bool) {
	usage, ok := ctx.Value(usageContextKey{}).(UsageContext)
	return usage, ok
}

func SetUsageObserver(observer UsageObserver) {
	usageObserverState.mu.Lock()
	defer usageObserverState.mu.Unlock()
	usageObserverState.observer = observer
}

func notifyUsage(ctx context.Context, event UsageEvent) {
	usage, ok := UsageContextFromContext(ctx)
	if !ok {
		return
	}
	event.Context = usage
	usageObserverState.mu.RLock()
	observer := usageObserverState.observer
	usageObserverState.mu.RUnlock()
	if observer == nil {
		return
	}
	observer(ctx, event)
}
