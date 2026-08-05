package relay

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// StateProvider produces the node's observation snapshot. On the node side this
// is wired to the local backend service (sessions, longtasks, approvals).
type StateProvider interface {
	Snapshot(ctx context.Context) (NodeSnapshot, error)
}

// Observer periodically collects the node's observation state and pushes a
// snapshot event to the center whenever the state changes. It implements the
// app.LifecycleService contract so it can be started alongside the relay agent.
type Observer struct {
	agent    *Agent
	provider StateProvider
	interval time.Duration

	mu       sync.Mutex
	last     *NodeSnapshot
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewObserver creates an observer that polls provider every interval and
// pushes changed snapshots through agent. A zero/negative interval gets a safe
// default (15s, matching the node heartbeat cadence).
func NewObserver(agent *Agent, provider StateProvider, interval time.Duration) *Observer {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Observer{agent: agent, provider: provider, interval: interval}
}

// Start launches the polling loop. It returns immediately.
func (o *Observer) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.cancel = cancel
	o.mu.Unlock()
	o.wg.Add(1)
	go o.runLoop(runCtx)
	return nil
}

// Stop cancels the polling loop and waits for it to exit.
func (o *Observer) Stop(ctx context.Context) error {
	o.mu.Lock()
	cancel := o.cancel
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		o.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (o *Observer) runLoop(ctx context.Context) {
	defer o.wg.Done()
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	// Push the initial snapshot as soon as the loop starts.
	o.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.poll(ctx)
		}
	}
}

func (o *Observer) poll(ctx context.Context) {
	snap, err := o.provider.Snapshot(ctx)
	if err != nil || ctx.Err() != nil {
		return
	}
	o.mu.Lock()
	if o.last != nil && reflect.DeepEqual(*o.last, snap) {
		o.mu.Unlock()
		return
	}
	o.mu.Unlock()
	// Only remember the snapshot after the send succeeds; otherwise a poll
	// that raced the agent's dial would drop the first snapshot forever.
	if err := o.agent.SendEvent(EventKindSnapshot, snap); err != nil {
		return
	}
	o.mu.Lock()
	copy := snap
	o.last = &copy
	o.mu.Unlock()
}
