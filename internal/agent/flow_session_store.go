package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const (
	MaxFlowSessionEventPayloadBytes = 64 << 10
	maxFlowSessionEventSources      = 128
	maxFlowSessionEventReadLimit    = 500
	maxFlowSessionPendingEvents     = 256
	maxFlowSessionOutputBatchBytes  = 64 << 10
	// FlowSessionOutputEventType identifies durable terminal-node output
	// events in the FlowSession event journal.
	FlowSessionOutputEventType = "session.output"
)

var (
	ErrFlowSessionNotFound      = errors.New("flow session not found")
	ErrFlowSessionConflict      = errors.New("flow session state conflict")
	ErrFlowSessionEventTooLarge = errors.New("flow session event payload too large")
	ErrFlowSessionPendingLimit  = errors.New("flow session pending event limit reached")
	ErrFlowSessionPendingEvents = errors.New("flow session has durable events ahead of the latest-wins signal")
	ErrFlowSessionStaleSignal   = errors.New("flow session signal result is stale")
	ErrFlowSessionStaleWork     = errors.New("flow session worker result is stale")
)

// FlowSessionView is the public snapshot of a long-lived FlowSession.
type FlowSessionView struct {
	SessionID              string                       `json:"session_id"`
	FlowID                 string                       `json:"flow_id"`
	Version                string                       `json:"version"`
	Digest                 string                       `json:"digest"`
	Status                 string                       `json:"status"`
	Inputs                 map[string]any               `json:"inputs,omitempty"`
	State                  map[string]any               `json:"state,omitempty"`
	LastSequence           uint64                       `json:"last_sequence"`
	ProcessedSequence      uint64                       `json:"processed_sequence"`
	StateVersion           uint64                       `json:"state_version"`
	ExecutionGeneration    uint64                       `json:"execution_generation"`
	LastError              string                       `json:"last_error,omitempty"`
	LastProcessedEventType string                       `json:"last_processed_event_type,omitempty"`
	LastProcessedNodes     []string                     `json:"last_processed_nodes,omitempty"`
	LastBranchRoutes       map[string]string            `json:"last_branch_routes,omitempty"`
	LastExecution          *FlowSessionExecutionSummary `json:"last_execution,omitempty"`
	InFlight               *FlowSessionInFlight         `json:"in_flight,omitempty"`
	InFlightLanes          []FlowSessionInFlight        `json:"in_flight_lanes,omitempty"`
	LatestSignalOutputs    []FlowSessionOutputSnapshot  `json:"latest_signal_outputs,omitempty"`
	StartedAt              time.Time                    `json:"started_at"`
	UpdatedAt              time.Time                    `json:"updated_at"`
	EndedAt                time.Time                    `json:"ended_at,omitempty"`
}

// FlowSessionExecutionSummary reports the most recently committed event-region
// execution in the session snapshot.
type FlowSessionExecutionSummary struct {
	Delivery       string                     `json:"delivery"`
	LaneID         string                     `json:"lane_id,omitempty"`
	InputSequence  uint64                     `json:"input_sequence,omitempty"`
	EventType      string                     `json:"event_type"`
	Source         string                     `json:"source"`
	SourceSequence uint64                     `json:"source_sequence,omitempty"`
	CorrelationID  string                     `json:"correlation_id,omitempty"`
	Status         string                     `json:"status"`
	StartedAt      time.Time                  `json:"started_at"`
	CompletedAt    time.Time                  `json:"completed_at"`
	DurationMS     int64                      `json:"duration_ms"`
	Nodes          []FlowSessionNodeExecution `json:"nodes,omitempty"`
	OutputCount    int                        `json:"output_count"`
}

// FlowSessionInFlight describes work queued for, running on, or being canceled
// in the backend's in-process session workers. It is not persisted in session.
type FlowSessionInFlight struct {
	Delivery       string     `json:"delivery"`
	LaneID         string     `json:"lane_id,omitempty"`
	Status         string     `json:"status"`
	InputSequence  uint64     `json:"input_sequence,omitempty"`
	EventType      string     `json:"event_type"`
	Source         string     `json:"source"`
	SourceSequence uint64     `json:"source_sequence,omitempty"`
	CorrelationID  string     `json:"correlation_id,omitempty"`
	NodeID         string     `json:"node_id,omitempty"`
	Phase          string     `json:"phase,omitempty"`
	ToolName       string     `json:"tool_name,omitempty"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	DurationMS     int64      `json:"duration_ms,omitempty"`
}

// FlowSessionNodeExecution reports one node's last region-run outcome.
type FlowSessionNodeExecution struct {
	NodeID     string `json:"node_id"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

// FlowSessionOutputSnapshot is the latest coalesced output produced by a
// latest-wins trigger. It replaces the prior snapshot and consumes no journal
// sequence.
type FlowSessionOutputSnapshot struct {
	OutputID       string         `json:"output_id"`
	InputEventType string         `json:"input_event_type"`
	InputSource    string         `json:"input_source"`
	InputSourceSeq uint64         `json:"input_source_sequence,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	NodeID         string         `json:"node_id"`
	Outputs        map[string]any `json:"outputs"`
}

// FlowSessionEventInput is a structured semantic/control event. Raw media and
// high-frequency telemetry should stay on an adapter's bounded hot path.
type FlowSessionEventInput struct {
	Source         string          `json:"source"`
	SourceSequence uint64          `json:"source_sequence,omitempty"`
	Type           string          `json:"type"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	LaneID         string          `json:"-"`
	LaneClass      string          `json:"-"`
	DeadlineMS     int             `json:"-"`
	OverloadPolicy string          `json:"-"`
}

// FlowSessionEvent is one durable, ordered event in a FlowSession journal.
type FlowSessionEvent struct {
	SessionID      string          `json:"flow_session_id"`
	FlowID         string          `json:"flow_id"`
	Version        string          `json:"version"`
	Digest         string          `json:"digest"`
	Source         string          `json:"source"`
	SourceSequence uint64          `json:"source_sequence,omitempty"`
	Sequence       uint64          `json:"sequence"`
	StateVersion   uint64          `json:"state_version"`
	Type           string          `json:"type"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at,omitempty"`
	ReceivedAt     time.Time       `json:"received_at"`
	Payload        json.RawMessage `json:"payload"`
}

// FlowSessionEventReceipt makes retried source-sequenced event submissions
// idempotent without returning the event payload a second time.
type FlowSessionEventReceipt struct {
	SessionID string `json:"flow_session_id"`
	Sequence  uint64 `json:"sequence"`
	Duplicate bool   `json:"duplicate"`
}

// FlowSessionSignalReceipt reports acceptance into the volatile latest-wins
// mailbox. Coalesced means an older queued signal with the same source/type
// was replaced before processing.
type FlowSessionSignalReceipt struct {
	SessionID string `json:"flow_session_id"`
	Coalesced bool   `json:"coalesced"`
}

type flowSessionRecord struct {
	SessionID              string                             `json:"session_id"`
	FlowID                 string                             `json:"flow_id"`
	Version                string                             `json:"version"`
	Digest                 string                             `json:"digest"`
	Status                 string                             `json:"status"`
	Inputs                 map[string]any                     `json:"inputs,omitempty"`
	State                  map[string]any                     `json:"state,omitempty"`
	LastSequence           uint64                             `json:"last_sequence"`
	ProcessedSequence      uint64                             `json:"processed_sequence"`
	StateVersion           uint64                             `json:"state_version"`
	ExecutionGeneration    uint64                             `json:"execution_generation"`
	LastError              string                             `json:"last_error,omitempty"`
	LastProcessedEventType string                             `json:"last_processed_event_type,omitempty"`
	LastProcessedNodes     []string                           `json:"last_processed_nodes,omitempty"`
	LastBranchRoutes       map[string]string                  `json:"last_branch_routes,omitempty"`
	LastExecution          *FlowSessionExecutionSummary       `json:"last_execution,omitempty"`
	LatestSignalOutputs    []FlowSessionOutputSnapshot        `json:"latest_signal_outputs,omitempty"`
	PendingOutputEvents    []FlowSessionEvent                 `json:"pending_output_events,omitempty"`
	SourceCursors          map[string]flowSessionSourceCursor `json:"source_cursors,omitempty"`
	StartedAt              time.Time                          `json:"started_at"`
	UpdatedAt              time.Time                          `json:"updated_at"`
	EndedAt                time.Time                          `json:"ended_at,omitempty"`
}

type flowSessionOutput struct {
	OutputID       string         `json:"output_id"`
	InputSequence  uint64         `json:"input_sequence,omitempty"`
	InputEventType string         `json:"input_event_type"`
	InputSource    string         `json:"input_source"`
	InputSourceSeq uint64         `json:"input_source_sequence,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	NodeID         string         `json:"node_id"`
	Outputs        map[string]any `json:"outputs"`
}

type flowSessionSourceCursor struct {
	SourceSequence uint64 `json:"source_sequence"`
	EventSequence  uint64 `json:"event_sequence"`
	Hash           string `json:"hash"`
}

// Agents are rebuilt per backend request, so their flowStore mutexes do not
// coordinate concurrent writes to one session. Fixed stripes provide
// process-local serialization without retaining one mutex per session.
type flowSessionLockStripes struct {
	sessions [64]sync.Mutex
	flows    [64]sync.Mutex
}

var flowSessionLockRegistry sync.Map // map[canonical flow-store dir]*flowSessionLockStripes

func (s *flowStore) sessionLock(flowID, sessionID string) *sync.Mutex {
	root, err := filepath.Abs(filepath.Clean(s.dir))
	if err != nil {
		root = filepath.Clean(s.dir)
	}
	raw, _ := flowSessionLockRegistry.LoadOrStore(root, &flowSessionLockStripes{})
	stripes := raw.(*flowSessionLockStripes)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(flowID + "\x00" + sessionID))
	return &stripes.sessions[hash.Sum32()%uint32(len(stripes.sessions))]
}

func (s *flowStore) flowLock(flowID string) *sync.Mutex {
	root, err := filepath.Abs(filepath.Clean(s.dir))
	if err != nil {
		root = filepath.Clean(s.dir)
	}
	raw, _ := flowSessionLockRegistry.LoadOrStore(root, &flowSessionLockStripes{})
	stripes := raw.(*flowSessionLockStripes)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(flowID))
	return &stripes.flows[hash.Sum32()%uint32(len(stripes.flows))]
}

func (s *flowStore) flowSessionsDir(flowID string) (string, error) {
	dir, err := s.flowDir(flowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
}

func (s *flowStore) flowSessionDir(flowID, sessionID string) (string, error) {
	if err := validateFlowSessionID(sessionID); err != nil {
		return "", err
	}
	dir, err := s.flowSessionsDir(flowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID), nil
}

func validateFlowSessionID(id string) error {
	if strings.TrimSpace(id) == "" || filepath.Base(id) != id {
		return fmt.Errorf("invalid flow_session_id %q", id)
	}
	for _, r := range id {
		if !(r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("invalid flow_session_id %q", id)
		}
	}
	return nil
}

func (s *flowStore) createFlowSession(rec flowSessionRecord) error {
	lock := s.sessionLock(rec.FlowID, rec.SessionID)
	lock.Lock()
	defer lock.Unlock()

	dir, err := s.flowSessionDir(rec.FlowID, rec.SessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "summary.json")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrFlowSessionConflict, rec.SessionID)
	} else if !os.IsNotExist(err) {
		return err
	}
	return fsutil.WriteJSONAtomic(path, rec, 0644)
}

func (s *flowStore) loadFlowSession(flowID, sessionID string) (flowSessionRecord, error) {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()
	return s.loadFlowSessionLocked(flowID, sessionID)
}

func (s *flowStore) loadFlowSessionLocked(flowID, sessionID string) (flowSessionRecord, error) {
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return flowSessionRecord{}, err
	}
	var rec flowSessionRecord
	path := filepath.Join(dir, "summary.json")
	if err := readJSONFile(path, &rec); err != nil {
		if os.IsNotExist(err) {
			return flowSessionRecord{}, fmt.Errorf("%w: %s", ErrFlowSessionNotFound, sessionID)
		}
		return flowSessionRecord{}, err
	}
	if rec.SessionID != sessionID || rec.FlowID != flowID {
		return flowSessionRecord{}, fmt.Errorf("flow session record identity mismatch for %s", sessionID)
	}
	if rec.SourceCursors == nil {
		rec.SourceCursors = make(map[string]flowSessionSourceCursor)
	}
	changed := false
	if rec.ExecutionGeneration == 0 {
		rec.ExecutionGeneration = 1
		changed = true
	}
	if len(rec.PendingOutputEvents) > 0 {
		if err := s.flushPendingFlowSessionOutputEventsLocked(dir, &rec); err != nil {
			return flowSessionRecord{}, err
		}
	}
	if err := s.recoverFlowSessionTailLocked(dir, &rec); err != nil {
		return flowSessionRecord{}, err
	}
	if rec.ProcessedSequence > rec.LastSequence {
		rec.ProcessedSequence = rec.LastSequence
		changed = true
	}
	if rec.StateVersion < rec.LastSequence {
		rec.StateVersion = rec.LastSequence
		changed = true
	}
	if changed {
		if err := fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644); err != nil {
			return flowSessionRecord{}, err
		}
	}
	return rec, nil
}

func (s *flowStore) recoverFlowSessionTailLocked(dir string, rec *flowSessionRecord) error {
	event, ok, err := readLastFlowSessionEvent(filepath.Join(dir, "events.jsonl"))
	if err != nil || !ok || event.Sequence <= rec.LastSequence {
		return err
	}
	if event.Sequence != rec.LastSequence+1 {
		return fmt.Errorf("flow session %s event journal sequence gap: snapshot=%d journal=%d", rec.SessionID, rec.LastSequence, event.Sequence)
	}
	rec.LastSequence = event.Sequence
	if event.StateVersion > rec.StateVersion {
		rec.StateVersion = event.StateVersion
	}
	rec.UpdatedAt = event.ReceivedAt
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now().UTC()
	}
	if event.SourceSequence > 0 {
		if rec.SourceCursors == nil {
			rec.SourceCursors = make(map[string]flowSessionSourceCursor)
		}
		rec.SourceCursors[event.Source] = flowSessionSourceCursor{
			SourceSequence: event.SourceSequence,
			EventSequence:  event.Sequence,
			Hash: flowSessionEventHash(FlowSessionEventInput{
				Type:          event.Type,
				CorrelationID: event.CorrelationID,
				OccurredAt:    event.OccurredAt,
				Payload:       event.Payload,
			}),
		}
	}
	if rec.ProcessedSequence > rec.LastSequence {
		rec.ProcessedSequence = rec.LastSequence
	}
	switch event.Type {
	case "session.paused":
		rec.Status = "paused"
		rec.ExecutionGeneration++
	case "session.resumed":
		rec.Status = "active"
		rec.ExecutionGeneration++
	case "session.completed":
		rec.Status = "completed"
		rec.EndedAt = event.ReceivedAt
		rec.ProcessedSequence = event.Sequence
		rec.ExecutionGeneration++
	case "session.canceled":
		rec.Status = "canceled"
		rec.EndedAt = event.ReceivedAt
		rec.ProcessedSequence = event.Sequence
		rec.ExecutionGeneration++
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644)
}

func (s *flowStore) listFlowSessions(flowID string) ([]flowSessionRecord, error) {
	dir, err := s.flowSessionsDir(flowID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]flowSessionRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Name(), "summary.json")); os.IsNotExist(err) {
			continue
		}
		rec, err := s.loadFlowSession(flowID, entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func (s *flowStore) transitionFlowSession(flowID, sessionID, target string) (flowSessionRecord, error) {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.loadFlowSessionLocked(flowID, sessionID)
	if err != nil {
		return flowSessionRecord{}, err
	}
	if rec.Status == target {
		return rec, nil
	}
	eventType := ""
	switch target {
	case "paused":
		if rec.Status != "active" {
			return flowSessionRecord{}, fmt.Errorf("%w: cannot pause session in %q state", ErrFlowSessionConflict, rec.Status)
		}
		eventType = "session.paused"
	case "active":
		if rec.Status != "paused" {
			return flowSessionRecord{}, fmt.Errorf("%w: cannot resume session in %q state", ErrFlowSessionConflict, rec.Status)
		}
		eventType = "session.resumed"
	case "completed", "canceled":
		if rec.Status != "active" && rec.Status != "paused" {
			return flowSessionRecord{}, fmt.Errorf("%w: cannot end session in %q state", ErrFlowSessionConflict, rec.Status)
		}
		eventType = "session." + target
	default:
		return flowSessionRecord{}, fmt.Errorf("unsupported flow session status %q", target)
	}

	now := time.Now().UTC()
	event := FlowSessionEvent{
		SessionID:    rec.SessionID,
		FlowID:       rec.FlowID,
		Version:      rec.Version,
		Digest:       rec.Digest,
		Source:       "godex",
		Sequence:     rec.LastSequence + 1,
		StateVersion: rec.StateVersion + 1,
		Type:         eventType,
		ReceivedAt:   now,
		Payload:      json.RawMessage(`{}`),
	}
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return flowSessionRecord{}, err
	}
	if err := appendFlowSessionEvent(filepath.Join(dir, "events.jsonl"), event); err != nil {
		return flowSessionRecord{}, err
	}
	rec.Status = target
	rec.LastSequence = event.Sequence
	rec.StateVersion = event.StateVersion
	rec.ExecutionGeneration++
	rec.UpdatedAt = now
	if target == "completed" || target == "canceled" {
		rec.EndedAt = now
		rec.ProcessedSequence = event.Sequence
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644); err != nil {
		return flowSessionRecord{}, err
	}
	return rec, nil
}

func (s *flowStore) appendFlowSessionEvent(flowID, sessionID string, input FlowSessionEventInput) (uint64, bool, error) {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.loadFlowSessionLocked(flowID, sessionID)
	if err != nil {
		return 0, false, err
	}
	cursor, hasCursor := rec.SourceCursors[input.Source]
	hash := flowSessionEventHash(input)
	if input.SourceSequence > 0 && hasCursor {
		switch {
		case input.SourceSequence == cursor.SourceSequence && hash == cursor.Hash:
			return cursor.EventSequence, true, nil
		case input.SourceSequence <= cursor.SourceSequence:
			return 0, false, fmt.Errorf("%w: source sequence %d is not newer than %d", ErrFlowSessionConflict, input.SourceSequence, cursor.SourceSequence)
		}
	}
	if rec.Status != "active" {
		return 0, false, fmt.Errorf("%w: cannot append events to session in %q state", ErrFlowSessionConflict, rec.Status)
	}
	if rec.LastSequence-rec.ProcessedSequence >= maxFlowSessionPendingEvents {
		return 0, false, ErrFlowSessionPendingLimit
	}
	if !hasCursor && len(rec.SourceCursors) >= maxFlowSessionEventSources {
		return 0, false, fmt.Errorf("%w: session source limit reached", ErrFlowSessionConflict)
	}

	now := time.Now().UTC()
	event := FlowSessionEvent{
		SessionID:      rec.SessionID,
		FlowID:         rec.FlowID,
		Version:        rec.Version,
		Digest:         rec.Digest,
		Source:         input.Source,
		SourceSequence: input.SourceSequence,
		Sequence:       rec.LastSequence + 1,
		StateVersion:   rec.StateVersion + 1,
		Type:           input.Type,
		CorrelationID:  input.CorrelationID,
		OccurredAt:     input.OccurredAt,
		ReceivedAt:     now,
		Payload:        input.Payload,
	}
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return 0, false, err
	}
	if err := appendFlowSessionEvent(filepath.Join(dir, "events.jsonl"), event); err != nil {
		return 0, false, err
	}
	rec.LastSequence = event.Sequence
	rec.StateVersion = event.StateVersion
	rec.UpdatedAt = now
	if input.SourceSequence > 0 {
		rec.SourceCursors[input.Source] = flowSessionSourceCursor{
			SourceSequence: input.SourceSequence,
			EventSequence:  event.Sequence,
			Hash:           hash,
		}
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644); err != nil {
		return 0, false, err
	}
	return event.Sequence, false, nil
}

func (s *flowStore) checkpointFlowSessionCursor(
	flowID, sessionID string,
	sequence, expectedExecutionGeneration uint64,
) error {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.loadFlowSessionLocked(flowID, sessionID)
	if err != nil {
		return err
	}
	if rec.ExecutionGeneration != expectedExecutionGeneration {
		return ErrFlowSessionStaleWork
	}
	if rec.Status != "active" {
		return fmt.Errorf("%w: cannot process events while session is %q", ErrFlowSessionConflict, rec.Status)
	}
	if sequence != rec.ProcessedSequence+1 || sequence > rec.LastSequence {
		return fmt.Errorf("%w: invalid processed sequence %d (processed=%d last=%d)", ErrFlowSessionConflict, sequence, rec.ProcessedSequence, rec.LastSequence)
	}
	rec.ProcessedSequence = sequence
	rec.ExecutionGeneration++
	rec.UpdatedAt = time.Now().UTC()
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644)
}

func (s *flowStore) cancelFlowSessionWork(
	flowID, sessionID string,
	sequence, expectedExecutionGeneration uint64,
	eventType string,
	execution *FlowSessionExecutionSummary,
) (bool, error) {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.loadFlowSessionLocked(flowID, sessionID)
	if err != nil {
		return false, err
	}
	if rec.ProcessedSequence >= sequence ||
		rec.ExecutionGeneration != expectedExecutionGeneration ||
		rec.Status != "active" ||
		sequence != rec.ProcessedSequence+1 ||
		sequence > rec.LastSequence {
		return false, nil
	}
	rec.ProcessedSequence = sequence
	rec.StateVersion++
	rec.ExecutionGeneration++
	rec.LastError = ""
	rec.LastProcessedEventType = eventType
	rec.LastProcessedNodes = nil
	rec.LastBranchRoutes = nil
	rec.LastExecution = cloneFlowSessionExecutionSummary(execution)
	rec.UpdatedAt = time.Now().UTC()

	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return false, err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644); err != nil {
		return false, err
	}
	if err := s.flushPendingFlowSessionOutputEventsLocked(dir, &rec); err != nil {
		return false, err
	}
	return true, nil
}

func (s *flowStore) commitFlowSessionProgress(
	flowID, sessionID string,
	inputSequence uint64,
	expectedExecutionGeneration uint64,
	eventType string,
	nodeIDs []string,
	branchRoutes map[string]string,
	outputs []flowSessionOutput,
	execution *FlowSessionExecutionSummary,
	state map[string]any,
	processErr string,
) error {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.loadFlowSessionLocked(flowID, sessionID)
	if err != nil {
		return err
	}
	if rec.ExecutionGeneration != expectedExecutionGeneration {
		return ErrFlowSessionStaleWork
	}
	if rec.Status != "active" {
		return fmt.Errorf("%w: cannot commit session work while session is %q", ErrFlowSessionConflict, rec.Status)
	}
	if inputSequence != rec.ProcessedSequence+1 || inputSequence > rec.LastSequence {
		return fmt.Errorf("%w: input sequence %d is outside the pending range (%d,%d]", ErrFlowSessionConflict, inputSequence, rec.ProcessedSequence, rec.LastSequence)
	}
	if state != nil {
		rec.State = state
	}
	rec.LastError = processErr
	now := time.Now().UTC()
	rec.ProcessedSequence = inputSequence
	rec.StateVersion++
	rec.ExecutionGeneration++
	rec.LastProcessedEventType = eventType
	rec.LastProcessedNodes = append([]string{}, nodeIDs...)
	rec.LastBranchRoutes = cloneFlowSessionBranchRoutes(branchRoutes)
	rec.LastExecution = cloneFlowSessionExecutionSummary(execution)
	rec.UpdatedAt = now
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return err
	}
	if err := stageFlowSessionOutputEvents(&rec, outputs); err != nil {
		return err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644); err != nil {
		return err
	}
	return s.flushPendingFlowSessionOutputEventsLocked(dir, &rec)
}

func (s *flowStore) checkpointFlowSessionSignal(
	flowID, sessionID string,
	expectedStateVersion, expectedExecutionGeneration uint64,
	state map[string]any,
	branchRoutes map[string]string,
	outputs []flowSessionOutput,
	execution *FlowSessionExecutionSummary,
	processErr string,
) error {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.loadFlowSessionLocked(flowID, sessionID)
	if err != nil {
		return err
	}
	if rec.Status != "active" {
		return fmt.Errorf("%w: cannot apply signals while session is %q", ErrFlowSessionConflict, rec.Status)
	}
	if rec.LastSequence > rec.ProcessedSequence {
		return ErrFlowSessionPendingEvents
	}
	if rec.StateVersion != expectedStateVersion {
		return ErrFlowSessionStaleSignal
	}
	if rec.ExecutionGeneration != expectedExecutionGeneration {
		return ErrFlowSessionStaleSignal
	}
	if state != nil {
		rec.State = state
	}
	rec.LastError = processErr
	rec.LastBranchRoutes = cloneFlowSessionBranchRoutes(branchRoutes)
	now := time.Now().UTC()
	rec.StateVersion++
	rec.ExecutionGeneration++
	rec.UpdatedAt = now
	rec.LastExecution = cloneFlowSessionExecutionSummary(execution)
	latestOutputs, err := snapshotFlowSessionOutputs(outputs)
	if err != nil {
		return err
	}
	rec.LatestSignalOutputs = latestOutputs
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644)
}

func snapshotFlowSessionOutputs(outputs []flowSessionOutput) ([]FlowSessionOutputSnapshot, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	snapshots := make([]FlowSessionOutputSnapshot, 0, len(outputs))
	totalBytes := 0
	for _, output := range outputs {
		snapshot := FlowSessionOutputSnapshot{
			OutputID:       output.OutputID,
			InputEventType: output.InputEventType,
			InputSource:    output.InputSource,
			InputSourceSeq: output.InputSourceSeq,
			CorrelationID:  output.CorrelationID,
			NodeID:         output.NodeID,
			Outputs:        output.Outputs,
		}
		payload, err := json.Marshal(snapshot)
		if err != nil {
			return nil, fmt.Errorf("encode latest FlowSession output from node %q: %w", output.NodeID, err)
		}
		if len(payload) > MaxFlowSessionEventPayloadBytes {
			return nil, ErrFlowSessionEventTooLarge
		}
		totalBytes += len(payload)
		if totalBytes > maxFlowSessionOutputBatchBytes {
			return nil, ErrFlowSessionEventTooLarge
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func stageFlowSessionOutputEvents(rec *flowSessionRecord, outputs []flowSessionOutput) error {
	if rec == nil || len(outputs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	pending := make([]FlowSessionEvent, 0, len(outputs))
	totalBytes := 0
	for _, output := range outputs {
		payload, err := marshalFlowSessionOutputPayload(output)
		if err != nil {
			return fmt.Errorf("encode FlowSession output from node %q: %w", output.NodeID, err)
		}
		if len(payload) > MaxFlowSessionEventPayloadBytes {
			return ErrFlowSessionEventTooLarge
		}
		totalBytes += len(payload)
		if totalBytes > maxFlowSessionOutputBatchBytes {
			return ErrFlowSessionEventTooLarge
		}
		rec.LastSequence++
		rec.StateVersion++
		pending = append(pending, FlowSessionEvent{
			SessionID:     rec.SessionID,
			FlowID:        rec.FlowID,
			Version:       rec.Version,
			Digest:        rec.Digest,
			Source:        "godex",
			Sequence:      rec.LastSequence,
			StateVersion:  rec.StateVersion,
			Type:          FlowSessionOutputEventType,
			CorrelationID: output.CorrelationID,
			OccurredAt:    now,
			ReceivedAt:    now,
			Payload:       payload,
		})
	}
	rec.PendingOutputEvents = pending
	return nil
}

func marshalFlowSessionOutputPayload(output flowSessionOutput) ([]byte, error) {
	return json.Marshal(struct {
		OutputID       string         `json:"output_id"`
		InputSequence  uint64         `json:"input_sequence,omitempty"`
		InputEventType string         `json:"input_event_type"`
		InputSource    string         `json:"input_source"`
		InputSourceSeq uint64         `json:"input_source_sequence,omitempty"`
		NodeID         string         `json:"node_id"`
		Outputs        map[string]any `json:"outputs"`
	}{
		OutputID:       output.OutputID,
		InputSequence:  output.InputSequence,
		InputEventType: output.InputEventType,
		InputSource:    output.InputSource,
		InputSourceSeq: output.InputSourceSeq,
		NodeID:         output.NodeID,
		Outputs:        output.Outputs,
	})
}

func (s *flowStore) flushPendingFlowSessionOutputEventsLocked(dir string, rec *flowSessionRecord) error {
	if rec == nil || len(rec.PendingOutputEvents) == 0 {
		return nil
	}
	path := filepath.Join(dir, "events.jsonl")
	lastEvent, ok, err := readLastFlowSessionEvent(path)
	if err != nil {
		return err
	}
	lastSequence := uint64(0)
	if ok {
		lastSequence = lastEvent.Sequence
	}
	for _, event := range rec.PendingOutputEvents {
		if event.Sequence <= lastSequence {
			continue
		}
		if event.Sequence != lastSequence+1 {
			return fmt.Errorf("flow session %s output journal sequence gap: journal=%d output=%d", rec.SessionID, lastSequence, event.Sequence)
		}
		if err := appendFlowSessionEvent(path, event); err != nil {
			return err
		}
		lastSequence = event.Sequence
	}
	rec.PendingOutputEvents = nil
	return fsutil.WriteJSONAtomic(filepath.Join(dir, "summary.json"), rec, 0644)
}

func (s *flowStore) flowSessionEvents(flowID, sessionID string, after uint64, limit int) ([]FlowSessionEvent, error) {
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.loadFlowSessionLocked(flowID, sessionID); err != nil {
		return nil, err
	}
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	events := make([]FlowSessionEvent, 0, limit)
	for scanner.Scan() {
		var event FlowSessionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode flow session event: %w", err)
		}
		if event.Sequence > after {
			events = append(events, event)
			if len(events) >= limit {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *flowStore) flowSessionEventPage(
	flowID, sessionID string,
	after uint64,
	offset int64,
	limit int,
) ([]FlowSessionEvent, int64, error) {
	if offset < 0 {
		return nil, offset, fmt.Errorf("flow session event offset must not be negative")
	}
	lock := s.sessionLock(flowID, sessionID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.loadFlowSessionLocked(flowID, sessionID); err != nil {
		return nil, offset, err
	}
	dir, err := s.flowSessionDir(flowID, sessionID)
	if err != nil {
		return nil, offset, err
	}
	file, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) && offset == 0 {
			return nil, 0, nil
		}
		return nil, offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset > info.Size() {
		return nil, offset, fmt.Errorf("flow session event offset %d exceeds journal size %d", offset, info.Size())
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	events := make([]FlowSessionEvent, 0, limit)
	nextOffset := offset
	for scanner.Scan() {
		line := scanner.Bytes()
		var event FlowSessionEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, nextOffset, fmt.Errorf("decode flow session event: %w", err)
		}
		nextOffset += int64(len(line))
		if nextOffset < info.Size() {
			var newline [1]byte
			if n, readErr := file.ReadAt(newline[:], nextOffset); readErr == nil && n == 1 && newline[0] == '\n' {
				nextOffset++
			}
		}
		if event.Sequence > after {
			events = append(events, event)
			if len(events) >= limit {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nextOffset, err
	}
	return events, nextOffset, nil
}

func appendFlowSessionEvent(path string, event FlowSessionEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func readLastFlowSessionEvent(path string) (FlowSessionEvent, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		if os.IsNotExist(err) {
			return FlowSessionEvent{}, false, nil
		}
		return FlowSessionEvent{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return FlowSessionEvent{}, false, err
	}
	end := info.Size()
	if end == 0 {
		return FlowSessionEvent{}, false, nil
	}
	finalNewline := false
	var lastByte [1]byte
	for end > 0 {
		if _, err := file.ReadAt(lastByte[:], end-1); err != nil {
			return FlowSessionEvent{}, false, err
		}
		if lastByte[0] != '\n' {
			break
		}
		finalNewline = true
		end--
	}
	if end <= 0 {
		return FlowSessionEvent{}, false, nil
	}

	const chunkSize int64 = 4096
	searchEnd := end
	lineStart := int64(0)
	for searchEnd > 0 {
		chunkStart := searchEnd - chunkSize
		if chunkStart < 0 {
			chunkStart = 0
		}
		chunk := make([]byte, searchEnd-chunkStart)
		if _, err := file.ReadAt(chunk, chunkStart); err != nil && !errors.Is(err, io.EOF) {
			return FlowSessionEvent{}, false, err
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			lineStart = chunkStart + int64(index) + 1
			break
		}
		searchEnd = chunkStart
	}
	line := make([]byte, end-lineStart)
	if _, err := file.ReadAt(line, lineStart); err != nil && !errors.Is(err, io.EOF) {
		return FlowSessionEvent{}, false, err
	}
	var event FlowSessionEvent
	if err := json.Unmarshal(line, &event); err != nil {
		if !finalNewline {
			// A process may stop during an append. Drop only the unterminated
			// tail, then recover from the most recent complete record.
			if err := file.Truncate(lineStart); err != nil {
				return FlowSessionEvent{}, false, err
			}
			if err := file.Sync(); err != nil {
				return FlowSessionEvent{}, false, err
			}
			_ = file.Close()
			return readLastFlowSessionEvent(path)
		}
		return FlowSessionEvent{}, false, fmt.Errorf("decode final flow session event: %w", err)
	}
	if !finalNewline {
		if _, err := file.WriteAt([]byte{'\n'}, info.Size()); err != nil {
			return FlowSessionEvent{}, false, err
		}
		if err := file.Sync(); err != nil {
			return FlowSessionEvent{}, false, err
		}
	}
	return event, true, nil
}

func flowSessionEventHash(input FlowSessionEventInput) string {
	occurredAt := ""
	if !input.OccurredAt.IsZero() {
		occurredAt = input.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	raw := append([]byte(input.Type+"\x00"+input.CorrelationID+"\x00"+occurredAt+"\x00"), input.Payload...)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func flowSessionView(rec flowSessionRecord) FlowSessionView {
	return FlowSessionView{
		SessionID:              rec.SessionID,
		FlowID:                 rec.FlowID,
		Version:                rec.Version,
		Digest:                 rec.Digest,
		Status:                 rec.Status,
		Inputs:                 rec.Inputs,
		State:                  rec.State,
		LastSequence:           rec.LastSequence,
		ProcessedSequence:      rec.ProcessedSequence,
		StateVersion:           rec.StateVersion,
		ExecutionGeneration:    rec.ExecutionGeneration,
		LastError:              rec.LastError,
		LastProcessedEventType: rec.LastProcessedEventType,
		LastProcessedNodes:     append([]string{}, rec.LastProcessedNodes...),
		LastBranchRoutes:       cloneFlowSessionBranchRoutes(rec.LastBranchRoutes),
		LastExecution:          cloneFlowSessionExecutionSummary(rec.LastExecution),
		LatestSignalOutputs:    cloneFlowSessionOutputSnapshots(rec.LatestSignalOutputs),
		StartedAt:              rec.StartedAt,
		UpdatedAt:              rec.UpdatedAt,
		EndedAt:                rec.EndedAt,
	}
}

func cloneFlowSessionExecutionSummary(summary *FlowSessionExecutionSummary) *FlowSessionExecutionSummary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	cloned.Nodes = append([]FlowSessionNodeExecution{}, summary.Nodes...)
	return &cloned
}

func cloneFlowSessionOutputSnapshots(outputs []FlowSessionOutputSnapshot) []FlowSessionOutputSnapshot {
	if len(outputs) == 0 {
		return nil
	}
	cloned := append([]FlowSessionOutputSnapshot{}, outputs...)
	for i := range cloned {
		if cloned[i].Outputs == nil {
			continue
		}
		outputsCopy := make(map[string]any, len(cloned[i].Outputs))
		for key, value := range cloned[i].Outputs {
			outputsCopy[key] = value
		}
		cloned[i].Outputs = outputsCopy
	}
	return cloned
}

func cloneFlowSessionBranchRoutes(routes map[string]string) map[string]string {
	if len(routes) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(routes))
	for branchID, route := range routes {
		cloned[branchID] = route
	}
	return cloned
}

func isActiveFlowSessionStatus(status string) bool {
	return status == "active" || status == "paused"
}
