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
//
// Consecutive assistant_text_delta events of the same turn are merged into a
// single event (text appended) so a chatty agent — codex-acp streams the reply
// in 1-10 char chunks — cannot flood the bounded window and evict the tool
// calls / thinking deltas around it. Live streaming is unaffected: the
// broadcaster forwards the original events; only the persisted timeline is
// compacted.
func (r *Recorder) Emit(event Event) {
	if r == nil || !recordableEvent(event) {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if event.Type == EventAssistantTextDelta && len(r.events) > 0 {
		last := &r.events[len(r.events)-1]
		if last.Type == EventAssistantTextDelta && last.TurnID == event.TurnID && last.SessionID == event.SessionID {
			// Same turn, still streaming: append the fragment to the open
			// delta instead of creating a new entry.
			if lastText, ok := last.Payload.(TextPayload); ok {
				if newText, ok := event.Payload.(TextPayload); ok {
					last.Payload = TextPayload{Role: newText.Role, Text: lastText.Text + newText.Text}
					last.Timestamp = event.Timestamp
					return
				}
			}
		}
	}

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
		// Text deltas ARE persisted (like thinking deltas): the ACP harness
		// streams short process-text fragments between tool calls, and a
		// re-entered conversation must rebuild them interleaved with the tool
		// log. The final consolidated answer still comes from
		// assistant_message_completed / snapshot messages; the frontend
		// dedupes per-turn delta text against that consolidated text.
		return true
	default:
		return true
	}
}

// RecordableEvent reports whether an event should be retained in timelines and
// reconnect replay buffers.
func RecordableEvent(event Event) bool {
	return recordableEvent(event)
}
