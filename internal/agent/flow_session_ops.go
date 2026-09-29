package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/platform/idgen"
)

// CreateFlowSession starts a long-lived runtime instance pinned to one
// compiled session-mode Flow version.
func (a *Agent) CreateFlowSession(flowID, version string, inputs map[string]any) (FlowSessionView, error) {
	if a == nil || a.flows == nil {
		return FlowSessionView{}, fmt.Errorf("flow session store unavailable")
	}
	flowID = strings.TrimSpace(flowID)
	flowLock := a.flows.flowLock(flowID)
	flowLock.Lock()
	defer flowLock.Unlock()
	rec, err := a.ResolveRunVersion(flowID, version)
	if err != nil {
		return FlowSessionView{}, err
	}
	if rec.Flow == nil || rec.Compiled == nil {
		return FlowSessionView{}, fmt.Errorf("version %s has no runnable definition", rec.Version)
	}
	if flow.EffectiveExecutionMode(rec.Flow.ExecutionMode) != flow.ExecutionModeSession {
		return FlowSessionView{}, fmt.Errorf("flow %s version %s uses request execution mode; create a FlowRun instead", flowID, rec.Version)
	}
	if err := flow.ValidateInputValues(rec.Flow, inputs); err != nil {
		return FlowSessionView{}, err
	}
	now := time.Now().UTC()
	session := flowSessionRecord{
		SessionID:           idgen.New("fs_", 16),
		FlowID:              flowID,
		Version:             rec.Version,
		Digest:              rec.Compiled.Digest,
		Status:              "active",
		Inputs:              inputs,
		State:               map[string]any{},
		ExecutionGeneration: 1,
		SourceCursors:       map[string]flowSessionSourceCursor{},
		StartedAt:           now,
		UpdatedAt:           now,
	}
	if err := a.flows.createFlowSession(session); err != nil {
		return FlowSessionView{}, err
	}
	return flowSessionView(session), nil
}

// GetFlowSession returns the durable snapshot for one session.
func (a *Agent) GetFlowSession(flowID, sessionID string) (FlowSessionView, error) {
	if a == nil || a.flows == nil {
		return FlowSessionView{}, fmt.Errorf("flow session store unavailable")
	}
	rec, err := a.flows.loadFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID))
	if err != nil {
		return FlowSessionView{}, err
	}
	return flowSessionView(rec), nil
}

// GetFlowSessionWorkflow returns the immutable scheduling contract pinned by
// one session. It is used by adapters and the backend lane scheduler.
func (a *Agent) GetFlowSessionWorkflow(flowID, sessionID string) (*flow.SessionWorkflowSpec, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow session store unavailable")
	}
	rec, err := a.flows.loadFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	version, err := a.flows.loadVersion(rec.FlowID, rec.Version)
	if err != nil {
		return nil, err
	}
	if version.Compiled == nil || version.Compiled.Digest != rec.Digest {
		return nil, fmt.Errorf("pinned Flow version %s no longer matches session digest", rec.Version)
	}
	if version.Compiled.SessionWorkflow == nil {
		return nil, fmt.Errorf("pinned Flow version %s has no session workflow", rec.Version)
	}
	spec := *version.Compiled.SessionWorkflow
	spec.Triggers = append([]flow.SessionTrigger(nil), spec.Triggers...)
	spec.Lanes = append([]flow.SessionLane(nil), spec.Lanes...)
	return &spec, nil
}

// ListFlowSessions returns session snapshots newest first.
func (a *Agent) ListFlowSessions(flowID string) ([]FlowSessionView, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow session store unavailable")
	}
	recs, err := a.flows.listFlowSessions(strings.TrimSpace(flowID))
	if err != nil {
		return nil, err
	}
	out := make([]FlowSessionView, 0, len(recs))
	for _, rec := range recs {
		out = append(out, flowSessionView(rec))
	}
	return out, nil
}

// PauseFlowSession pauses event ingress. Repeating the same transition is
// idempotent.
func (a *Agent) PauseFlowSession(flowID, sessionID string) (FlowSessionView, error) {
	return a.transitionFlowSession(flowID, sessionID, "paused")
}

// ResumeFlowSession resumes an explicitly paused session. Sessions that were
// already active remain active.
func (a *Agent) ResumeFlowSession(flowID, sessionID string) (FlowSessionView, error) {
	return a.transitionFlowSession(flowID, sessionID, "active")
}

// EndFlowSession completes or cancels an active/paused session.
func (a *Agent) EndFlowSession(flowID, sessionID, outcome string) (FlowSessionView, error) {
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	if outcome == "" {
		outcome = "completed"
	}
	if outcome != "completed" && outcome != "canceled" {
		return FlowSessionView{}, fmt.Errorf("flow session outcome must be completed or canceled")
	}
	return a.transitionFlowSession(flowID, sessionID, outcome)
}

func (a *Agent) transitionFlowSession(flowID, sessionID, target string) (FlowSessionView, error) {
	if a == nil || a.flows == nil {
		return FlowSessionView{}, fmt.Errorf("flow session store unavailable")
	}
	rec, err := a.flows.transitionFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID), target)
	if err != nil {
		return FlowSessionView{}, err
	}
	return flowSessionView(rec), nil
}

// AppendFlowSessionEvent persists one bounded structured event. When a source
// sequence is supplied, the most recent matching event from that source is
// replay-safe; older or conflicting sequence reuse is rejected.
func (a *Agent) AppendFlowSessionEvent(flowID, sessionID string, input FlowSessionEventInput) (FlowSessionEventReceipt, error) {
	if a == nil || a.flows == nil {
		return FlowSessionEventReceipt{}, fmt.Errorf("flow session store unavailable")
	}
	input, err := NormalizeFlowSessionEventInput(input)
	if err != nil {
		return FlowSessionEventReceipt{}, err
	}
	rec, err := a.flows.loadFlowSession(strings.TrimSpace(flowID), strings.TrimSpace(sessionID))
	if err != nil {
		return FlowSessionEventReceipt{}, err
	}
	version, err := a.flows.loadVersion(rec.FlowID, rec.Version)
	if err != nil {
		return FlowSessionEventReceipt{}, err
	}
	if trigger := flowSessionTrigger(version.Compiled, input.Type); trigger != nil &&
		flow.EffectiveSessionDelivery(trigger.Delivery) == flow.SessionDeliveryLatestWins {
		return FlowSessionEventReceipt{}, fmt.Errorf("event type %q uses latest_wins delivery; submit it through the signals endpoint", input.Type)
	}
	sequence, duplicate, err := a.flows.appendFlowSessionEvent(
		rec.FlowID, rec.SessionID, input,
	)
	if err != nil {
		return FlowSessionEventReceipt{}, err
	}
	return FlowSessionEventReceipt{SessionID: strings.TrimSpace(sessionID), Sequence: sequence, Duplicate: duplicate}, nil
}

// FlowSessionEvents returns a sequence-paginated event journal.
func (a *Agent) FlowSessionEvents(flowID, sessionID string, afterSequence uint64, limit int) ([]FlowSessionEvent, error) {
	if a == nil || a.flows == nil {
		return nil, fmt.Errorf("flow session store unavailable")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxFlowSessionEventReadLimit {
		limit = maxFlowSessionEventReadLimit
	}
	return a.flows.flowSessionEvents(
		strings.TrimSpace(flowID), strings.TrimSpace(sessionID), afterSequence, limit,
	)
}

// FlowSessionEventPage reads a journal page with a reusable byte offset, so a
// stream can catch up without rescanning the already-delivered journal prefix.
func (a *Agent) FlowSessionEventPage(
	flowID, sessionID string,
	afterSequence uint64,
	offset int64,
	limit int,
) ([]FlowSessionEvent, int64, error) {
	if a == nil || a.flows == nil {
		return nil, offset, fmt.Errorf("flow session store unavailable")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxFlowSessionEventReadLimit {
		limit = maxFlowSessionEventReadLimit
	}
	return a.flows.flowSessionEventPage(
		strings.TrimSpace(flowID), strings.TrimSpace(sessionID), afterSequence, offset, limit,
	)
}

func NormalizeFlowSessionEventInput(input FlowSessionEventInput) (FlowSessionEventInput, error) {
	input.Source = strings.TrimSpace(input.Source)
	input.Type = strings.TrimSpace(input.Type)
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if !input.OccurredAt.IsZero() {
		input.OccurredAt = input.OccurredAt.UTC()
	}
	if input.Source == "" || len(input.Source) > 128 {
		return FlowSessionEventInput{}, fmt.Errorf("event source must be between 1 and 128 characters")
	}
	if input.Source == "godex" {
		return FlowSessionEventInput{}, fmt.Errorf("event source %q is reserved", input.Source)
	}
	if input.Type == "" || len(input.Type) > 128 {
		return FlowSessionEventInput{}, fmt.Errorf("event type must be between 1 and 128 characters")
	}
	if len(input.CorrelationID) > 256 {
		return FlowSessionEventInput{}, fmt.Errorf("correlation_id must not exceed 256 characters")
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage(`{}`)
	}
	if len(input.Payload) > MaxFlowSessionEventPayloadBytes {
		return FlowSessionEventInput{}, ErrFlowSessionEventTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return FlowSessionEventInput{}, fmt.Errorf("event payload must be a JSON object: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return FlowSessionEventInput{}, fmt.Errorf("event payload must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return FlowSessionEventInput{}, fmt.Errorf("event payload must contain one JSON object: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return FlowSessionEventInput{}, err
	}
	if len(canonical) > MaxFlowSessionEventPayloadBytes {
		return FlowSessionEventInput{}, ErrFlowSessionEventTooLarge
	}
	input.Payload = canonical
	return input, nil
}

func normalizeFlowSessionEventInput(input FlowSessionEventInput) (FlowSessionEventInput, error) {
	return NormalizeFlowSessionEventInput(input)
}
