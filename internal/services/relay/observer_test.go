package relay

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeStateProvider returns a fixed snapshot; tests mutate it between calls.
type fakeStateProvider struct {
	mu      sync.Mutex
	snap    NodeSnapshot
	err     error
	calls   int
}

func (p *fakeStateProvider) Snapshot(_ context.Context) (NodeSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.snap, p.err
}

func (p *fakeStateProvider) set(snap NodeSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap = snap
}

func TestObserverPushesSnapshotWhenStateChanges(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	var (
		sinkMu sync.Mutex
		got    []Frame
	)
	hub.SetEventSink(func(nodeID string, frame Frame) {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		got = append(got, frame)
	})

	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Handler:    fakeLocalHandler(t),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline("node-a") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	provider := &fakeStateProvider{snap: NodeSnapshot{
		Version: "v1.2.0",
		Jobs:    []JobInfo{{ID: "j1", Name: "deploy", Phase: "planning", Turn: 1, Total: 5}},
	}}
	observer := NewObserver(agent, provider, 20*time.Millisecond)
	if err := observer.Start(context.Background()); err != nil {
		t.Fatalf("observer start: %v", err)
	}
	defer observer.Stop(context.Background())

	// First poll must push the initial snapshot.
	waitForFrames(t, &sinkMu, &got, 1)
	assertSnapshotJob(t, got[0], "planning", 1)

	// Provider reports job progress; next poll must push an update.
	provider.set(NodeSnapshot{
		Version: "v1.2.0",
		Jobs:    []JobInfo{{ID: "j1", Name: "deploy", Phase: "executing", Turn: 3, Total: 5}},
	})
	waitForFrames(t, &sinkMu, &got, 2)
	assertSnapshotJob(t, got[1], "executing", 3)
}

func TestObserverSkipsUnchangedSnapshot(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	var (
		sinkMu sync.Mutex
		got    []Frame
	)
	hub.SetEventSink(func(nodeID string, frame Frame) {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		got = append(got, frame)
	})

	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Handler:    fakeLocalHandler(t),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline("node-a") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	provider := &fakeStateProvider{snap: NodeSnapshot{Version: "v1.2.0", Jobs: []JobInfo{{ID: "j1", Phase: "planning"}}}}
	observer := NewObserver(agent, provider, 15*time.Millisecond)
	if err := observer.Start(context.Background()); err != nil {
		t.Fatalf("observer start: %v", err)
	}
	defer observer.Stop(context.Background())

	waitForFrames(t, &sinkMu, &got, 1)

	// Give the observer several more polls with the same snapshot: nothing new.
	time.Sleep(80 * time.Millisecond)
	sinkMu.Lock()
	n := len(got)
	sinkMu.Unlock()
	if n != 1 {
		t.Fatalf("expected only initial snapshot, got %d frames", n)
	}
}

// TestObserverRetriesWhenAgentNotConnected guards against a regression where
// the first poll happened before the agent finished dialing: the snapshot was
// marked as sent, dropped, and never retried because the state did not change.
// The observer must only remember a snapshot after the send succeeded.
func TestObserverRetriesWhenAgentNotConnected(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	var (
		sinkMu sync.Mutex
		got    []Frame
	)
	hub.SetEventSink(func(nodeID string, frame Frame) {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		got = append(got, frame)
	})

	// Start the observer before the agent is online so the first poll's
	// SendEvent fails with "agent not connected".
	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Handler:    fakeLocalHandler(t),
	})

	provider := &fakeStateProvider{snap: NodeSnapshot{Version: "v1.2.0"}}
	observer := NewObserver(agent, provider, 15*time.Millisecond)
	if err := observer.Start(context.Background()); err != nil {
		t.Fatalf("observer start: %v", err)
	}
	defer observer.Stop(context.Background())

	// Now bring the agent online; the observer must eventually deliver the
	// snapshot even though earlier attempts failed.
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sinkMu.Lock()
		n := len(got)
		sinkMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected observer to retry and deliver snapshot after agent connected")
	}
	if got[0].Type != FrameEvent || got[0].Kind != EventKindSnapshot {
		t.Fatalf("unexpected frame: %#v", got[0])
	}
}


func waitForFrames(t *testing.T, mu *sync.Mutex, got *[]Frame, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("timed out waiting for %d frames, got %d", want, len(*got))
}

func assertSnapshotJob(t *testing.T, frame Frame, phase string, turn int) {
	t.Helper()
	if frame.Type != FrameEvent || frame.Kind != EventKindSnapshot {
		t.Fatalf("unexpected frame: %#v", frame)
	}
	var snap NodeSnapshot
	if err := json.Unmarshal(frame.Payload, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(snap.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(snap.Jobs))
	}
	if snap.Jobs[0].Phase != phase || snap.Jobs[0].Turn != turn {
		t.Fatalf("job = %+v, want phase=%s turn=%d", snap.Jobs[0], phase, turn)
	}
}
