package agent

import (
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/domain/events"
)

// TestSubagentJobUpdatedEventCarriesScopeLabel verifies subagent progress
// events (subagent_job_updated) carry the target's scope_label (6.2 M5).
func TestSubagentJobUpdatedEventCarriesScopeLabel(t *testing.T) {
	recorder := events.NewRecorder(8)
	target := subagentEventTarget{
		sessionID:  "session-a",
		turnID:     "turn-1",
		sink:       recorder,
		scopeLabel: "session:session-a",
	}
	job := &subagentJob{
		ID:        "job-1",
		SessionID: "session-a",
		Status:    subagentStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}

	target.emit(job, "started", "Subagent job started.", "", "", "", "")

	entries := recorder.Entries(0)
	if len(entries) == 0 {
		t.Fatal("expected at least one recorded event")
	}
	last := entries[len(entries)-1]
	if last.Type != events.EventSubagentJobUpdated {
		t.Fatalf("expected subagent_job_updated, got %q", last.Type)
	}
	payload, ok := last.Payload.(events.SubagentJobPayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", last.Payload)
	}
	if payload.ScopeLabel != "session:session-a" {
		t.Fatalf("expected scope_label %q, got %q", "session:session-a", payload.ScopeLabel)
	}
}

// TestRunnerPhaseEventCarriesScopeLabel verifies runner phase events emitted
// from the subagent event target carry the scope label (6.2 M5).
func TestRunnerPhaseEventCarriesScopeLabel(t *testing.T) {
	recorder := events.NewRecorder(8)
	target := subagentEventTarget{
		sessionID:  "session-a",
		turnID:     "turn-1",
		sink:       recorder,
		scopeLabel: "session:session-a",
	}
	job := &subagentJob{
		ID:        "job-1",
		SessionID: "session-a",
		Status:    subagentStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}

	target.emitRunnerPhase(job, conversation.PhaseEvent{Phase: "thinking", Iteration: 1})

	entries := recorder.Entries(0)
	if len(entries) == 0 {
		t.Fatal("expected at least one recorded event")
	}
	last := entries[len(entries)-1]
	if last.Type != events.EventRunnerPhaseChanged {
		t.Fatalf("expected runner_phase_changed, got %q", last.Type)
	}
	payload, ok := last.Payload.(events.RunnerPhasePayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", last.Payload)
	}
	if payload.ScopeLabel != "session:session-a" {
		t.Fatalf("expected scope_label %q, got %q", "session:session-a", payload.ScopeLabel)
	}
}

// TestSubagentEventWithoutScopeKeepsEmptyLabel verifies events emitted with an
// empty scope label remain valid (backward compatibility: old events without
// scope_label still parse).
func TestSubagentEventWithoutScopeKeepsEmptyLabel(t *testing.T) {
	recorder := events.NewRecorder(8)
	target := subagentEventTarget{
		sessionID: "session-legacy",
		turnID:    "turn-1",
		sink:      recorder,
	}
	job := &subagentJob{
		ID:        "job-legacy",
		SessionID: "session-legacy",
		Status:    subagentStatusRunning,
		UpdatedAt: time.Now().UTC(),
	}

	target.emit(job, "started", "started", "", "", "", "")

	entries := recorder.Entries(0)
	if len(entries) == 0 {
		t.Fatal("expected at least one recorded event")
	}
	payload, ok := entries[len(entries)-1].Payload.(events.SubagentJobPayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", entries[len(entries)-1].Payload)
	}
	if payload.ScopeLabel != "" {
		t.Fatalf("expected empty scope_label for legacy event, got %q", payload.ScopeLabel)
	}
}
