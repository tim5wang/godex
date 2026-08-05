package relay

import (
	"strings"
	"testing"
	"time"
)

func sampleSnapshot() NodeSnapshot {
	return NodeSnapshot{
		Version:      "v1.2.0",
		Capabilities: []string{"chat", "terminal"},
		Sessions: []SessionInfo{
			{ID: "s1", Title: "fix bug", Running: true, UpdatedAt: time.Now()},
		},
		Jobs: []JobInfo{
			{ID: "j1", Name: "deploy", Status: "running", Phase: "planning", Turn: 1, Total: 5},
		},
		Approvals: []ApprovalInfo{
			{ID: "ap1", SessionID: "s1", Intent: "run go test", Status: "pending"},
		},
		GeneratedAt: time.Now(),
	}
}

func TestEventStoreRecordsSnapshotAndOverview(t *testing.T) {
	store := NewEventStore()
	snap := sampleSnapshot()
	store.RecordSnapshot("node-a", snap)

	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected overview for node-a")
	}
	if overview.NodeID != "node-a" {
		t.Errorf("node id = %q, want node-a", overview.NodeID)
	}
	if overview.Version != "v1.2.0" {
		t.Errorf("version = %q, want v1.2.0", overview.Version)
	}
	if len(overview.Capabilities) != 2 {
		t.Errorf("capabilities = %v, want 2 entries", overview.Capabilities)
	}
	if len(overview.Sessions) != 1 || overview.Sessions[0].ID != "s1" {
		t.Errorf("sessions = %+v, want s1", overview.Sessions)
	}
	if len(overview.Jobs) != 1 || overview.Jobs[0].ID != "j1" {
		t.Errorf("jobs = %+v, want j1", overview.Jobs)
	}
	if len(overview.Approvals) != 1 || overview.Approvals[0].ID != "ap1" {
		t.Errorf("approvals = %+v, want ap1", overview.Approvals)
	}
	if overview.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt set")
	}
}

func TestEventStoreOverviewUnknownNode(t *testing.T) {
	store := NewEventStore()
	if _, ok := store.Overview("ghost"); ok {
		t.Fatal("expected no overview for unknown node")
	}
}

func TestEventStoreDiffEmitsProgressEvents(t *testing.T) {
	store := NewEventStore()
	first := sampleSnapshot()
	store.RecordSnapshot("node-a", first)

	// The node reports job progress: planning → executing, turn 1 → 3.
	second := first
	second.Jobs = []JobInfo{
		{ID: "j1", Name: "deploy", Status: "running", Phase: "executing", Turn: 3, Total: 5},
	}
	store.RecordSnapshot("node-a", second)

	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected overview")
	}
	if len(overview.Jobs) != 1 || overview.Jobs[0].Phase != "executing" || overview.Jobs[0].Turn != 3 {
		t.Fatalf("job not updated: %+v", overview.Jobs)
	}
	joined := joinEventDetails(overview.RecentEvents)
	if !strings.Contains(joined, "j1") {
		t.Errorf("expected recent events to mention job j1, got %q", joined)
	}
}

func TestEventStoreKeepsRecentEventsLimited(t *testing.T) {
	store := NewEventStore()
	for i := 0; i < 60; i++ {
		store.RecordEvent("node-a", "heartbeat", "tick")
	}
	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected overview")
	}
	if len(overview.RecentEvents) > 50 {
		t.Errorf("recent events = %d, want capped at 50", len(overview.RecentEvents))
	}
	if len(overview.RecentEvents) == 0 {
		t.Error("expected at least one retained event")
	}
}

func TestEventStoreSnapshotOverwritesJobState(t *testing.T) {
	store := NewEventStore()
	first := sampleSnapshot()
	store.RecordSnapshot("node-a", first)

	// The job finished; the node reports it no longer (job list shrinks).
	second := first
	second.Jobs = nil
	store.RecordSnapshot("node-a", second)

	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected overview")
	}
	if len(overview.Jobs) != 0 {
		t.Errorf("jobs = %+v, want none after job finished", overview.Jobs)
	}
}

func joinEventDetails(events []StoredEvent) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Detail)
		b.WriteString(";")
	}
	return b.String()
}
