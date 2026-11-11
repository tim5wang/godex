package backend

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrRuntimeStopping is returned when work arrives while the runtime is draining.
var ErrRuntimeStopping = errors.New("session runtime is shutting down")

// In-flight turn draining — why this exists.
//
// A turn's work does not end when Submit/SubmitAsync returns. Those only mark the turn as
// started and hand the real work to a detached goroutine (turn.go), which writes session
// files; the event journal additionally writes on a timer, and the title generator runs in
// its own goroutine. So anything that ends the process — a shutdown, or a test's TempDir
// cleanup — races those writes.
//
// In tests the race surfaced as `TempDir RemoveAll cleanup: ... directory not empty`:
// RemoveAll lists the directory, a racing write creates a new entry (WriteFileAtomic puts
// its temp file in that same directory), and removing the directory itself then fails with
// ENOTEMPTY. Which test failed changed from run to run, because it is a race and not a
// defect in any one test.
//
// A drain has to do two things, and skipping either makes it unbounded:
//
//   - Refuse new work, including the queued-turn hand-off. A finishing turn calls
//     startQueuedTurns, so without this the drain chases a moving target and can never
//     finish.
//   - Cancel what is in flight instead of waiting for it to complete on its own. A turn has
//     no hard deadline, and some callers park the model call on a channel that only
//     cancellation releases — "wait for it to finish" would hang forever.
//
// Cancellation is bounded because it reaches the real HTTP request (conversation client →
// http.NewRequestWithContext), so the turn returns promptly rather than waiting out the
// provider's response time.

// drainGrace bounds a drain so a shutdown cannot outlive the process's own shutdown budget.
const drainGrace = 10 * time.Second

// isStopping reports whether new work must be refused for this session.
func (s *sessionState) isStopping() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopping
}

// beginStopping flips the session into "no new work" and reports whether it was ALREADY
// stopping, so a nested drain does not clear a flag an outer drain still relies on.
func (s *sessionState) beginStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.stopping
	s.stopping = true
	return was
}

func (s *sessionState) endStopping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = false
	// The background context was cancelled by the drain; drop it so a later writer (or a
	// drain of a restarted service) gets a fresh one instead of an already-cancelled parent.
	s.bgCtx = nil
	s.bgCancel = nil
}

// backgroundContext is the parent for detached writers of this session. Cancelling it is
// how a drain bounds writers whose own timeout is longer than the drain's budget.
//
// It also closes a race: a writer can be registered just before a drain starts and reach
// this call just after, in which case cancelBackground already ran and saw no context to
// cancel. Returning an already-cancelled context while stopping means such a writer gives up
// instead of adding a write the drain would then have to wait out.
func (s *sessionState) backgroundContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bgCtx == nil {
		s.bgCtx, s.bgCancel = context.WithCancelCause(context.Background())
	}
	if s.stopping && s.bgCancel != nil {
		s.bgCancel(ErrRuntimeStopping)
	}
	return s.bgCtx
}

func (s *sessionState) cancelBackground() {
	s.mu.Lock()
	cancel := s.bgCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel(ErrRuntimeStopping)
	}
}

// cancelActive cancels the in-flight turn, if any, and reports whether there was one.
// It does not clear `active`: the turn's own goroutine does that when it actually exits,
// which is the only moment the session is genuinely idle.
func (s *sessionState) cancelActive() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if active == nil || active.cancel == nil {
		return active != nil
	}
	active.cancel(ErrTurnCanceled)
	return true
}

// waitIdle blocks until this session has no active turn, bounded by ctx.
//
// sync.Cond has no context support, so a watcher broadcasts when ctx fires; otherwise a turn
// that ignored cancellation would park the drain past its deadline.
func (s *sessionState) waitIdle(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	// Lazily initialize so every session constructor (including future ones) is covered
	// without threading a constructor change through the struct literals.
	if s.idle == nil {
		s.idle = sync.NewCond(&s.mu)
	}
	idle := s.idle
	s.mu.Unlock()

	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		idle.Broadcast()
		s.mu.Unlock()
	})
	defer stop()

	s.mu.Lock()
	// `running` covers the window between acquire and setActiveTurn: a turn that has
	// claimed the session but has not recorded itself as active yet. `bgCount` covers
	// detached writers such as the title generator.
	for s.active != nil || s.running || s.bgCount > 0 {
		if ctx.Err() != nil {
			break
		}
		idle.Wait()
	}
	s.mu.Unlock()

	// The session files were written by the turn body, but the event journal may still hold
	// a timer-driven batch. Flushing here is what actually removes the last racing writer.
	if s.journal != nil {
		s.journal.FlushSync()
	}
	// The agent layer owns a writer this session cannot see: the shadow security screener
	// records its audit asynchronously, so it can land after the turn is over (see
	// agent.WaitBackground). Without this, the drain can return while that write is pending.
	if s.agent != nil {
		if err := s.agent.WaitBackground(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// residentSessions snapshots the sessions currently in memory.
func (s *Service) residentSessions() []*sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions := make([]*sessionState, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session != nil {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

// WaitIdleSessions drains every resident session for a shutdown: refuse new work, let what is
// in flight finish, then wait (bounded) for the resulting writes to land.
//
// It deliberately does NOT cancel in-flight turns. Cancelling persists the turn as "canceled"
// and clears the pending resume, while recovery after a restart only revives turns left as
// "running"/"canceling" (see canResumeTurnStatus and recoverInterruptedTurn). So cancelling
// here would silently convert resumable work into lost work — the opposite of what a shutdown
// should do. If a turn is still running when the caller's deadline expires, the drain returns
// ctx.Err() and the turn stays "running", which is exactly the state the next start recovers.
//
// It restores each session to a usable state before returning, so one instance can be stopped
// and started again (the FlowSession tests do exactly that).
func (s *Service) WaitIdleSessions(ctx context.Context) error {
	return s.drainSessions(ctx, false)
}

// CancelIdleSessions is the test-shaped drain: it refuses new work AND cancels in-flight turns
// before waiting. Tests need the cancellation because their model stubs park on a channel that
// only cancellation releases, so a graceful wait would always burn the whole deadline. It is
// not used for shutdown for the reason spelled out on WaitIdleSessions.
func (s *Service) CancelIdleSessions(ctx context.Context) error {
	return s.drainSessions(ctx, true)
}

// drainSessions refuses new work, optionally cancels in-flight turns, and waits (bounded) for
// the session writers to go quiet.
func (s *Service) drainSessions(ctx context.Context, cancelInFlight bool) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, drainGrace)
		defer cancel()
	}
	sessions := s.residentSessions()
	if len(sessions) == 0 {
		return nil
	}

	// Sessions that were already stopping belong to an outer drain; leaving their flag set
	// would make a later Stop/Start cycle refuse all work.
	restore := make([]*sessionState, 0, len(sessions))
	for _, session := range sessions {
		if !session.beginStopping() {
			restore = append(restore, session)
		}
	}
	defer func() {
		for _, session := range restore {
			session.endStopping()
		}
	}()

	if cancelInFlight {
		// Cancel before waiting: a turn that does not honour cancellation would otherwise
		// spend the whole grace period before we even ask it to stop. Detached writers are
		// cancelled too, so a 15s title call cannot outlast this drain's (shorter) deadline.
		for _, session := range sessions {
			session.cancelActive()
			session.cancelBackground()
		}
	}

	errs := make([]error, len(sessions))
	var wg sync.WaitGroup
	for i, session := range sessions {
		wg.Add(1)
		go func(i int, session *sessionState) {
			defer wg.Done()
			errs[i] = session.waitIdle(ctx)
		}(i, session)
	}
	wg.Wait()

	var joined error
	for _, err := range errs {
		joined = errors.Join(joined, err)
	}
	return joined
}
