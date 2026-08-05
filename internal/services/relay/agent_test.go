package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeLocalHandler mimics the node's local HTTP API surface: it echoes the
// request method+path and honors a streaming hint in the query string.
func fakeLocalHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") == "1" {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "no flusher", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "chunk1\n")
			flusher.Flush()
			_, _ = io.WriteString(w, "chunk2\n")
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Path", r.URL.Path)
		_, _ = w.Write([]byte(r.Method + ":" + r.URL.Path + ":" + string(body)))
	})
}

// serveRelayHub runs a Hub behind httptest and returns its ws:// URL.
func serveRelayHub(t *testing.T, validate CredentialValidator) (*Hub, string) {
	t.Helper()
	hub := NewHub(validate)
	server := httptest.NewServer(hub)
	t.Cleanup(func() {
		_ = hub.Shutdown(context.Background())
		server.Close()
	})
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	return hub, wsURL
}

func TestAgentConnectsAndAnswersRequest(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return nodeID == "node-a" && credential == "ck_secret"
	})

	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "ck_secret",
		Version:    "v1.2.0",
		Caps:       []string{"chat", "terminal"},
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
	if !hub.IsOnline("node-a") {
		t.Fatal("expected agent node to register with hub")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "POST", Path: "/api/sessions", Body: []byte("hello")})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("expected status 200, got %d", resp.Status)
	}
	want := "POST:/api/sessions:hello"
	if string(resp.Body) != want {
		t.Fatalf("expected body %q, got %q", want, resp.Body)
	}
	if resp.Headers["X-Echo-Path"] != "/api/sessions" {
		t.Fatalf("expected echoed header, got %#v", resp.Headers)
	}
}

func TestAgentRejectsBadCredential(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	agent := NewAgent(AgentConfig{
		CenterURL:  wsURL,
		NodeID:     "node-a",
		Credential: "wrong",
		Version:    "v1.2.0",
		Handler:    fakeLocalHandler(t),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())

	time.Sleep(200 * time.Millisecond)
	if hub.IsOnline("node-a") {
		t.Fatal("expected node to stay offline with bad credential")
	}
}

func TestAgentStreamsResponse(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/events", Query: "stream=1"})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("expected status 200, got %d", resp.Status)
	}
	// The hub currently aggregates non-streaming bodies; assert both chunks landed.
	body := string(resp.Body)
	if !strings.Contains(body, "chunk1") || !strings.Contains(body, "chunk2") {
		t.Fatalf("expected both chunks in body, got %q", body)
	}
}

func TestAgentReconnectsAfterHubRestart(t *testing.T) {
	// Start a hub, let the agent connect, then close it and start a fresh hub
	// on the same address; the agent should reconnect.
	addr := reserveAddr(t)

	hub1 := NewHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	server1 := startHubOn(t, hub1, addr)
	wsURL := "ws://" + addr

	agent := NewAgent(AgentConfig{
		CenterURL:    wsURL,
		NodeID:       "node-a",
		Credential:   "ck_secret",
		Version:      "v1.2.0",
		Handler:      fakeLocalHandler(t),
		ReconnectMin: 20 * time.Millisecond,
		ReconnectMax: 80 * time.Millisecond,
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub1.IsOnline("node-a") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hub1.IsOnline("node-a") {
		t.Fatal("expected agent to connect to first hub")
	}

	// Tear down the first hub; the agent's connection must drop.
	_ = hub1.Shutdown(context.Background())
	server1.Close()

	// Bring up a second hub on the same address.
	hub2 := NewHub(func(nodeID, credential string) bool { return credential == "ck_secret" })
	server2 := startHubOn(t, hub2, addr)
	defer func() {
		_ = hub2.Shutdown(context.Background())
		server2.Close()
	}()

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub2.IsOnline("node-a") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hub2.IsOnline("node-a") {
		t.Fatal("expected agent to reconnect to second hub")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := hub2.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/meta"})
	if err != nil {
		t.Fatalf("forward after reconnect: %v", err)
	}
	if string(resp.Body) != "GET:/meta:" {
		t.Fatalf("unexpected body after reconnect: %q", resp.Body)
	}
}

// reserveAddr returns an available loopback address that can be rebound later.
func reserveAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve addr: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startHubOn serves hub on a fixed address, closing any listener first so the
// address can be reused across hub restarts.
func startHubOn(t *testing.T, hub *Hub, addr string) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	server := httptest.NewUnstartedServer(hub)
	server.Listener = l
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestAgentLocalHandlerErrors(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
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
	resp, err := hub.Forward(ctx, "node-a", ForwardRequest{Method: "GET", Path: "/broken"})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "boom") {
		t.Fatalf("expected error body, got %q", resp.Body)
	}
}

// TestAgentRespondsToPing verifies the agent answers application-level pings so
// the hub can keep the node online.
func TestAgentRespondsToPing(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })
	hub.SetPingInterval(50 * time.Millisecond)

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
		if hub.LastPong("node-a").After(time.Now().Add(-time.Second)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hub.LastPong("node-a").IsZero() {
		t.Fatal("expected hub to record pong from agent")
	}
}

// helper to build a hub URL that the agent dials (kept for parity with tests
// that construct agent configs programmatically).
func agentWSURL(server *httptest.Server) string {
	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"
	return u.String()
}
