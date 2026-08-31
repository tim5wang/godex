package conversation

import (
	"context"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
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

// usageHookState holds lightweight in-process subscribers that also receive
// usage events. Unlike the single global observer (used for durable billing
// records), hooks are ephemeral: they exist for live observability surfaces
// such as the per-session prefix-cache hit rate, and are removed explicitly.
var usageHookState struct {
	mu    sync.RWMutex
	hooks map[int]UsageObserver
	next  int
}

// AddUsageHook registers an additional usage listener and returns an
// unsubscribe function. Hooks run synchronously after the primary observer,
// so they must stay cheap (e.g. in-memory aggregation only).
func AddUsageHook(hook UsageObserver) func() {
	if hook == nil {
		return func() {}
	}
	usageHookState.mu.Lock()
	if usageHookState.hooks == nil {
		usageHookState.hooks = make(map[int]UsageObserver)
	}
	usageHookState.next++
	id := usageHookState.next
	usageHookState.hooks[id] = hook
	usageHookState.mu.Unlock()
	return func() {
		usageHookState.mu.Lock()
		delete(usageHookState.hooks, id)
		usageHookState.mu.Unlock()
	}
}

func notifyUsageHooks(ctx context.Context, event UsageEvent) {
	usageHookState.mu.RLock()
	if len(usageHookState.hooks) == 0 {
		usageHookState.mu.RUnlock()
		return
	}
	hooks := make([]UsageObserver, 0, len(usageHookState.hooks))
	for _, hook := range usageHookState.hooks {
		hooks = append(hooks, hook)
	}
	usageHookState.mu.RUnlock()
	for _, hook := range hooks {
		hook(ctx, event)
	}
}

// NotifyUsageHooksForTest dispatches an event to registered hooks only
// (bypassing the primary observer). It exists for unit tests that verify
// hook behaviour without wiring the global observer.
func NotifyUsageHooksForTest(ctx context.Context, event UsageEvent) {
	notifyUsageHooks(ctx, event)
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
	if observer != nil {
		observer(ctx, event)
	}
	notifyUsageHooks(ctx, event)
}
