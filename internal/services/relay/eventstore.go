package relay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// NodeSnapshot is a node-side observation snapshot pushed to the center over
// the relay. The center aggregates these per node; it never persists session
// history (that stays on the node), it only keeps the latest observation state
// and a bounded recent-event log.
type NodeSnapshot struct {
	Version      string         `json:"version,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Sessions     []SessionInfo  `json:"sessions,omitempty"`
	Jobs         []JobInfo      `json:"jobs,omitempty"`
	Approvals    []ApprovalInfo `json:"approvals,omitempty"`
	GeneratedAt  time.Time      `json:"generated_at,omitempty"`
}

// SessionInfo is the center-visible projection of one node session.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	Running   bool      `json:"running,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// JobInfo is the center-visible projection of one node longtask/job.
type JobInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Phase  string `json:"phase,omitempty"`
	Turn   int    `json:"turn,omitempty"`
	Total  int    `json:"total_turns,omitempty"`
}

// ApprovalInfo is the center-visible projection of one node approval request.
type ApprovalInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	Intent    string `json:"intent,omitempty"`
	Status    string `json:"status,omitempty"`
}

// StoredEvent is one recorded observation change for a node.
type StoredEvent struct {
	Kind   string    `json:"kind"`
	Time   time.Time `json:"time"`
	Detail string    `json:"detail,omitempty"`
}

// NodeOverview is the aggregated center-side view of one node.
type NodeOverview struct {
	NodeID       string         `json:"node_id"`
	Version      string         `json:"version,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Sessions     []SessionInfo  `json:"sessions,omitempty"`
	Jobs         []JobInfo      `json:"jobs,omitempty"`
	Approvals    []ApprovalInfo `json:"approvals,omitempty"`
	RecentEvents []StoredEvent  `json:"recent_events,omitempty"`
	LastHealth   time.Time      `json:"last_health,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at,omitempty"`
}

const (
	// maxRecentEvents caps the per-node recent-event log kept in memory.
	maxRecentEvents = 50
	// EventKindSnapshot is emitted whenever a node pushes a full snapshot.
	EventKindSnapshot = "snapshot"
	// EventKindJob is emitted when a job's phase/turn/status changes.
	EventKindJob = "job"
	// EventKindSession is emitted when session state changes.
	EventKindSession = "session"
	// EventKindApproval is emitted when approval state changes.
	EventKindApproval = "approval"
)

type nodeObservation struct {
	snapshot NodeSnapshot
	events   []StoredEvent
	updated  time.Time
}

// EventStore aggregates per-node observation snapshots pushed over the relay.
// It is safe for concurrent use and lives on the center side only.
type EventStore struct {
	mu    sync.Mutex
	nodes map[string]*nodeObservation
}

// NewEventStore creates an empty event store.
func NewEventStore() *EventStore {
	return &EventStore{nodes: make(map[string]*nodeObservation)}
}

// RecordSnapshot stores the latest observation snapshot for nodeID, appending
// bounded recent events derived from the diff against the previous snapshot.
func (s *EventStore) RecordSnapshot(nodeID string, snap NodeSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.nodes[nodeID]
	if !ok {
		obs = &nodeObservation{}
		s.nodes[nodeID] = obs
	}
	now := time.Now()
	obs.events = append(obs.events, diffEvents(obs.snapshot, snap, now)...)
	obs.snapshot = snap
	obs.updated = now
	if len(obs.events) > maxRecentEvents {
		obs.events = obs.events[len(obs.events)-maxRecentEvents:]
	}
}

// RecordEvent appends a single explicit event without touching the snapshot.
func (s *EventStore) RecordEvent(nodeID, kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.nodes[nodeID]
	if !ok {
		obs = &nodeObservation{}
		s.nodes[nodeID] = obs
	}
	obs.events = append(obs.events, StoredEvent{Kind: kind, Time: time.Now(), Detail: detail})
	obs.updated = time.Now()
	if len(obs.events) > maxRecentEvents {
		obs.events = obs.events[len(obs.events)-maxRecentEvents:]
	}
}

// Overview returns the aggregated view for nodeID. The second return value is
// false when the store has never seen the node.
func (s *EventStore) Overview(nodeID string) (NodeOverview, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.nodes[nodeID]
	if !ok {
		return NodeOverview{}, false
	}
	events := make([]StoredEvent, len(obs.events))
	copy(events, obs.events)
	return NodeOverview{
		NodeID:       nodeID,
		Version:      obs.snapshot.Version,
		Capabilities: append([]string(nil), obs.snapshot.Capabilities...),
		Sessions:     append([]SessionInfo(nil), obs.snapshot.Sessions...),
		Jobs:         append([]JobInfo(nil), obs.snapshot.Jobs...),
		Approvals:    append([]ApprovalInfo(nil), obs.snapshot.Approvals...),
		RecentEvents: events,
		LastHealth:   obs.snapshot.GeneratedAt,
		UpdatedAt:    obs.updated,
	}, true
}

// Nodes returns the node IDs the store currently holds observations for.
func (s *EventStore) Nodes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// diffEvents derives human-readable change events between two snapshots.
func diffEvents(prev, next NodeSnapshot, now time.Time) []StoredEvent {
	var events []StoredEvent
	for _, job := range next.Jobs {
		if old, ok := findJob(prev.Jobs, job.ID); ok {
			if old.Phase != job.Phase || old.Turn != job.Turn || old.Status != job.Status {
				events = append(events, StoredEvent{
					Kind: EventKindJob,
					Time: now,
					Detail: fmt.Sprintf("job %s: %s (turn %d/%d, %s)",
						job.ID, job.Name, job.Turn, job.Total, job.Phase),
				})
			}
		} else {
			events = append(events, StoredEvent{
				Kind:   EventKindJob,
				Time:   now,
				Detail: fmt.Sprintf("job started: %s (%s)", job.Name, job.ID),
			})
		}
	}
	for _, session := range next.Sessions {
		if old, ok := findSession(prev.Sessions, session.ID); ok {
			if old.Running != session.Running {
				events = append(events, StoredEvent{
					Kind:   EventKindSession,
					Time:   now,
					Detail: fmt.Sprintf("session %s running=%v", session.Title, session.Running),
				})
			}
		} else if session.Running {
			events = append(events, StoredEvent{
				Kind:   EventKindSession,
				Time:   now,
				Detail: fmt.Sprintf("session running: %s", session.Title),
			})
		}
	}
	for _, approval := range next.Approvals {
		if old, ok := findApproval(prev.Approvals, approval.ID); !ok || old.Status != approval.Status {
			status := approval.Status
			if status == "" {
				status = "pending"
			}
			events = append(events, StoredEvent{
				Kind:   EventKindApproval,
				Time:   now,
				Detail: fmt.Sprintf("approval %s: %s (%s)", status, approval.Intent, approval.SessionID),
			})
		}
	}
	if len(events) == 0 {
		events = append(events, StoredEvent{
			Kind:   EventKindSnapshot,
			Time:   now,
			Detail: "snapshot updated",
		})
	}
	return events
}

func findJob(jobs []JobInfo, id string) (JobInfo, bool) {
	for _, job := range jobs {
		if job.ID == id {
			return job, true
		}
	}
	return JobInfo{}, false
}

func findSession(sessions []SessionInfo, id string) (SessionInfo, bool) {
	for _, session := range sessions {
		if session.ID == id {
			return session, true
		}
	}
	return SessionInfo{}, false
}

func findApproval(approvals []ApprovalInfo, id string) (ApprovalInfo, bool) {
	for _, approval := range approvals {
		if approval.ID == id {
			return approval, true
		}
	}
	return ApprovalInfo{}, false
}

var _ = strings.TrimSpace

// StoreEvents returns an EventSink that records incoming event frames into an
// EventStore. Snapshot-kind frames update the node's observation snapshot;
// other kinds append an explicit event. Malformed or non-event frames are
// ignored so a buggy node cannot corrupt the store.
func StoreEvents(store *EventStore) EventSink {
	return func(nodeID string, frame Frame) {
		if frame.Type != FrameEvent {
			return
		}
		if frame.Kind == EventKindSnapshot {
			var snap NodeSnapshot
			if err := json.Unmarshal(frame.Payload, &snap); err != nil {
				// Keep a store entry so the node still appears, but without a
				// valid snapshot.
				store.RecordEvent(nodeID, EventKindSnapshot, "malformed snapshot payload")
				return
			}
			store.RecordSnapshot(nodeID, snap)
			return
		}
		store.RecordEvent(nodeID, frame.Kind, string(frame.Payload))
	}
}
