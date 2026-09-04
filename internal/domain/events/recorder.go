package events

import "sync"

const defaultRecorderCapacity = 200

// Recorder keeps a bounded in-memory history of runtime events for session
// timeline inspection.
type Recorder struct {
	mu       sync.RWMutex
	capacity int
	events   []Event
}

// NewRecorder creates a recorder with a bounded history capacity.
func NewRecorder(capacity int) *Recorder {
	if capacity <= 0 {
		capacity = defaultRecorderCapacity
	}
	return &Recorder{capacity: capacity}
}

// Emit records one event when it is useful for timeline inspection.
func (r *Recorder) Emit(event Event) {
	if r == nil || !recordableEvent(event) {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.events) >= r.capacity {
		copy(r.events, r.events[1:])
		r.events[len(r.events)-1] = event
		return
	}
	r.events = append(r.events, event)
}

// Seed replaces the recorder contents with a bounded copy of the provided
// events, preserving their order.
func (r *Recorder) Seed(items []Event) {
	if r == nil {
		return
	}

	filtered := make([]Event, 0, len(items))
	for _, item := range items {
		if !recordableEvent(item) {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) > r.capacity {
		filtered = filtered[len(filtered)-r.capacity:]
	}

	r.mu.Lock()
	r.events = append([]Event{}, filtered...)
	r.mu.Unlock()
}

// Entries returns the most recent events, newest-last.
func (r *Recorder) Entries(limit int) []Event {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.events) == 0 {
		return nil
	}
	if limit <= 0 || limit >= len(r.events) {
		return append([]Event{}, r.events...)
	}
	return append([]Event{}, r.events[len(r.events)-limit:]...)
}

func recordableEvent(event Event) bool {
	switch event.Type {
	case EventAssistantTextDelta:
		// Text deltas are transient streaming fragments; the final consolidated
		// assistant text is carried by assistant_message_completed (and the
		// snapshot messages), so persisting every delta would duplicate the
		// answer. Thinking deltas ARE persisted so a re-entered conversation
		// can reconstruct the reasoning that streamed between tool calls.
		return false
	default:
		return true
	}
}

// RecordableEvent reports whether an event should be retained in timelines and
// reconnect replay buffers.
func RecordableEvent(event Event) bool {
	return recordableEvent(event)
}
