package relay

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHubForwardStreamReceivesChunks verifies that a streaming local handler
// (SSE-style: write + flush repeatedly) is delivered to the hub caller in
// real time as separate chunks instead of one buffered blob. This is the
// transport foundation for remote chat events.
func TestHubForwardStreamReceivesChunks(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: chunk1\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: chunk2\n\n")
		flusher.Flush()
	})

	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Handler:    handler,
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
	if !hub.IsOnline("node-a") {
		t.Fatal("expected agent node to register with hub")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var (
		mu     sync.Mutex
		chunks []string
		done   bool
	)
	err := hub.ForwardStream(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/events"}, func(status int, headers map[string]string, chunk []byte, final bool) error {
		mu.Lock()
		defer mu.Unlock()
		if len(chunk) > 0 {
			chunks = append(chunks, string(chunk))
		}
		if final {
			done = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("forward stream: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !done {
		t.Fatal("expected stream to finish with final=true")
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "chunk1") || !strings.Contains(joined, "chunk2") {
		t.Fatalf("expected both chunks in stream, got %q", joined)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected streaming delivery (>=2 chunks), got %d: %q", len(chunks), joined)
	}
}

// TestHubForwardStreamEmptyBody ensures a non-streaming empty response still
// terminates the stream with final=true.
func TestHubForwardStreamEmptyBody(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Handler:    handler,
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	status := 0
	final := false
	err := hub.ForwardStream(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/empty"}, func(s int, _ map[string]string, _ []byte, f bool) error {
		status = s
		final = f
		return nil
	})
	if err != nil {
		t.Fatalf("forward stream: %v", err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", status)
	}
	if !final {
		t.Fatal("expected final=true")
	}
}
