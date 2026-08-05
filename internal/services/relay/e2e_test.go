package relay

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSnapshotPushEndToEnd wires the full observation pipeline the same way
// cmd/godex serve does on the real system:
//
//	node side:   Observer polls a StateProvider and pushes NodeSnapshot events
//	             through the Agent's outbound relay connection
//	center side: Hub receives event frames, StoreEvents records them into the
//	             EventStore, and Overview returns the aggregated view
//
// It verifies the Phase 2 acceptance criterion: when a node runs a longtask,
// the center can see its phase/turn progress.
func TestSnapshotPushEndToEnd(t *testing.T) {
	// Center side: hub + event store wired like cmd/godex main.go.
	store := NewEventStore()
	hub := NewHub(func(nodeID, credential string) bool {
		return nodeID == "node-a" && credential == "ck_secret"
	})
	hub.SetEventSink(StoreEvents(store))
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Shutdown(context.Background())

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Node side: agent + observer with a mutable provider.
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

	waitNodeOnline(t, hub, "node-a")

	provider := &mutableProvider{snap: NodeSnapshot{
		Version: "v1.2.0",
		Jobs:    []JobInfo{{ID: "j1", Name: "deploy", Status: "running", Phase: "planning", Turn: 1, Total: 5}},
	}}
	observer := NewObserver(agent, provider, 20*time.Millisecond)
	if err := observer.Start(context.Background()); err != nil {
		t.Fatalf("observer start: %v", err)
	}
	defer observer.Stop(context.Background())

	// The center must see the job appear with its initial phase/turn.
	waitForOverviewJob(t, store, "node-a", "j1", "planning", 1)

	// The node reports progress; the center must see phase/turn advance.
	provider.set(NodeSnapshot{
		Version: "v1.2.0",
		Jobs:    []JobInfo{{ID: "j1", Name: "deploy", Status: "running", Phase: "executing", Turn: 3, Total: 5}},
	})
	waitForOverviewJob(t, store, "node-a", "j1", "executing", 3)
}

// mutableProvider is a StateProvider whose snapshot can be swapped between polls.
type mutableProvider struct {
	mu   sync.Mutex
	snap NodeSnapshot
}

func (p *mutableProvider) Snapshot(_ context.Context) (NodeSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snap, nil
}

func (p *mutableProvider) set(snap NodeSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap = snap
}

func waitNodeOnline(t *testing.T, hub *Hub, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline(nodeID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %s never came online", nodeID)
}

func waitForOverviewJob(t *testing.T, store *EventStore, nodeID, jobID, phase string, turn int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		overview, ok := store.Overview(nodeID)
		if ok {
			for _, job := range overview.Jobs {
				if job.ID == jobID && job.Phase == phase && job.Turn == turn {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	overview, _ := store.Overview(nodeID)
	t.Fatalf("timed out waiting for job %s phase=%s turn=%d; overview=%+v", jobID, phase, turn, overview)
}
