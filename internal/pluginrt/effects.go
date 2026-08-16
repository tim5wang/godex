package pluginrt

import (
	"context"
	"sync"
)

// Effect is one reversible registration owned by a plugin instance (a tool,
// an interceptor, a service, an event listener, a resource handle). Running
// the effect registers it; the returned function reverses it.
type Effect func(ctx context.Context) (dispose func() error, err error)

// Ledger records the effects of one plugin instance in registration order and
// reverses them in reverse order on unload (reversible registration).
type Ledger struct {
	mu      sync.Mutex
	effects []Effect
	active  []func() error
	reverted bool
}

// NewLedger creates an empty effect ledger.
func NewLedger() *Ledger { return &Ledger{} }

// Add records an effect for this instance. It does not run it; the instance
// runs effects during Start.
func (l *Ledger) Add(effect Effect) {
	if effect == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.effects = append(l.effects, effect)
}

// Run executes every recorded effect in registration order, collecting
// disposers. On failure the effects already run are reversed immediately.
func (l *Ledger) Run(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reverted {
		return errLedgerReverted
	}
	for _, effect := range l.effects {
		dispose, err := effect(ctx)
		if err != nil {
			l.revertLocked()
			return err
		}
		if dispose != nil {
			l.active = append(l.active, dispose)
		}
	}
	return nil
}

// Revert reverses all active effects in reverse registration order, so
// unload is a clean undo of start. It is idempotent.
func (l *Ledger) Revert() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reverted {
		return nil
	}
	return l.revertLocked()
}

func (l *Ledger) revertLocked() error {
	var firstErr error
	for i := len(l.active) - 1; i >= 0; i-- {
		if err := l.active[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	l.active = nil
	l.reverted = true
	return firstErr
}

var errLedgerReverted = &ledgerRevertedError{}

type ledgerRevertedError struct{}

func (e *ledgerRevertedError) Error() string { return "effect ledger already reverted" }
