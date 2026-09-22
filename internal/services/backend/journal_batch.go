package backend

import (
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/domain/events"
)

const (
	// eventJournalFlushInterval is the fallback cap for non-delta events that
	// arrive between streams (tool lifecycle, phases). Streaming deltas are
	// held without any timer and only drain when the stream-end event arrives,
	// so this timer normally fires only during sparse mid-turn activity.
	eventJournalFlushInterval = 3 * time.Second
	// eventJournalFlushBatchSize is a memory safety net only. Normal streaming
	// drains at stream completion; this cap bounds pathological runs that never
	// emit a non-delta event.
	eventJournalFlushBatchSize = 4096
)

// sessionEventBatcher coalesces timeline/journal persistence so streaming
// deltas do not perform file + SQLite I/O on the model stream goroutine.
// Streaming deltas accumulate without a timer; the first non-delta event after
// them (normally model_request_completed) drains the burst in one flush. Turn
// boundary events (user accepted, turn completed, snapshot ready) write through
// synchronously when the batch is idle so crash-recovery semantics and tests
// stay deterministic, and sparse mid-turn events batch under a 3s fallback.
type sessionEventBatcher struct {
	service *Service
	session *sessionState

	mu      sync.Mutex
	pending []events.Event
	timer   *time.Timer
	closed  bool
}

func newSessionEventBatcher(service *Service, session *sessionState) *sessionEventBatcher {
	return &sessionEventBatcher{service: service, session: session}
}

// Add queues one event for persistence.
func (b *sessionEventBatcher) Add(event events.Event) {
	if b == nil {
		return
	}
	delta := isHighFrequencyRuntimeDelta(event.Type)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if delta {
		// Streaming deltas never arm the timer: they drain when the stream-end
		// event arrives, so a long stream does zero persistence until it is
		// complete.
		b.pending = append(b.pending, event)
		if len(b.pending) >= eventJournalFlushBatchSize {
			batch := b.pending
			b.pending = nil
			b.stopTimerLocked()
			b.mu.Unlock()
			b.flushBatch(batch)
		} else {
			b.mu.Unlock()
		}
		return
	}
	if len(b.pending) > 0 {
		// A non-delta event marks the end of a streaming burst: drain the
		// accumulated deltas together with it in one flush.
		batch := append(b.pending, event)
		b.pending = nil
		b.stopTimerLocked()
		b.mu.Unlock()
		b.flushBatch(batch)
		return
	}
	if isTurnBoundaryEvent(event.Type) {
		b.mu.Unlock()
		b.flushBatch([]events.Event{event})
		return
	}
	b.pending = append(b.pending, event)
	if b.timer == nil {
		b.timer = time.AfterFunc(eventJournalFlushInterval, b.flush)
	}
	b.mu.Unlock()
}

// FlushSync drains any pending events immediately. Called before journal
// rotation and at turn completion so a truncate never drops buffered events.
func (b *sessionEventBatcher) FlushSync() {
	if b == nil {
		return
	}
	b.flush()
}

func (b *sessionEventBatcher) flush() {
	b.mu.Lock()
	if b.closed || len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.pending
	b.pending = nil
	b.stopTimerLocked()
	b.mu.Unlock()
	b.flushBatch(batch)
}

func (b *sessionEventBatcher) stopTimerLocked() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

func (b *sessionEventBatcher) flushBatch(batch []events.Event) {
	if b == nil || b.service == nil || b.session == nil || len(batch) == 0 {
		return
	}
	_ = b.service.appendSessionEventJournalBatch(b.session, batch)
	_ = b.service.writeSessionTimeline(b.session)
}

// isHighFrequencyRuntimeDelta reports events that stream at high frequency and
// are held until stream completion instead of triggering per-event persistence.
func isHighFrequencyRuntimeDelta(eventType events.EventType) bool {
	switch eventType {
	case events.EventAssistantThinkingDelta, events.EventAssistantTextDelta:
		return true
	default:
		return false
	}
}

// isTurnBoundaryEvent reports events that must hit the journal immediately
// (write-through when the batch is idle): turn acceptance and terminal events
// anchor the crash-recovery delta and the rotate-before-truncate ordering.
func isTurnBoundaryEvent(eventType events.EventType) bool {
	switch eventType {
	case events.EventUserMessageAccepted, events.EventTurnCompleted, events.EventSnapshotReady:
		return true
	default:
		return false
	}
}
