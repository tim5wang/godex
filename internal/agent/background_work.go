package agent

import (
	"context"
	"sync"
)

// backgroundWork tracks detached goroutines that outlive the call which started them, so a
// drain can wait for their writes to land before the workspace is torn down.
//
// The shadow security screener is why this exists: it classifies fire-and-forget by design
// (so screening never gates or delays the pipeline) and records the verdict afterwards, which
// creates and appends an audit file. That write can therefore land AFTER the turn that
// triggered it has finished — "the turn is done" is not the same as "this session has stopped
// writing".
//
// A plain sync.WaitGroup would not do here:
//   - Wait cannot be bounded, and a drain needs a deadline;
//   - a WaitGroup forbids a positive Add racing a Wait that started at zero, which is exactly
//     the window a drain walks into (it starts waiting while a turn may still be spawning).
//
// A counter plus a condition variable has neither problem.
type backgroundWork struct {
	mu   sync.Mutex
	n    int
	idle *sync.Cond
}

func (b *backgroundWork) start() {
	b.mu.Lock()
	b.n++
	b.mu.Unlock()
}

func (b *backgroundWork) done() {
	b.mu.Lock()
	if b.n > 0 {
		b.n--
	}
	if b.n == 0 && b.idle != nil {
		b.idle.Broadcast()
	}
	b.mu.Unlock()
}

// wait blocks until no tracked work is running, bounded by ctx, and returns ctx.Err() when it
// gave up early. sync.Cond has no context support, so the deadline is delivered by a watcher
// that broadcasts; without it a stalled worker would park the drain past its budget.
func (b *backgroundWork) wait(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	if b.idle == nil {
		b.idle = sync.NewCond(&b.mu)
	}
	idle := b.idle
	b.mu.Unlock()

	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		idle.Broadcast()
		b.mu.Unlock()
	})
	defer stop()

	b.mu.Lock()
	for b.n > 0 {
		if ctx.Err() != nil {
			break
		}
		idle.Wait()
	}
	b.mu.Unlock()
	return ctx.Err()
}

// WaitBackground waits for this agent's detached background writers, bounded by ctx. It exists
// so a session drain can be complete: these writes are outside the turn's lifetime.
func (a *Agent) WaitBackground(ctx context.Context) error {
	if a == nil {
		return nil
	}
	return a.bgWork.wait(ctx)
}
