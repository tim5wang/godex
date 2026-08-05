package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyHandlerForwardsToNode(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	// Node side: a real agent serving a fake local API.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		payload, _ := json.Marshal(map[string]string{
			"method": r.Method,
			"path":   r.URL.Path,
			"body":   string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	server := httptest.NewServer(proxy)
	defer server.Close()

	// The proxy path mirrors the center webui layout: /api is stripped by the
	// webui before the proxy handler, and the remaining path is the node's
	// local httpapi path (no /api prefix).
	req, err := http.NewRequest("POST", server.URL+"/control/nodes/node-a/proxy/sessions", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]string
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed["method"] != "POST" || parsed["path"] != "/sessions" || parsed["body"] != `{"prompt":"hi"}` {
		t.Fatalf("unexpected proxied payload: %#v", parsed)
	}
}

func TestProxyHandlerOfflineNode(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	server := httptest.NewServer(proxy)
	defer server.Close()

	resp, err := http.Get(server.URL + "/control/nodes/node-ghost/proxy/meta")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for offline node, got %d", resp.StatusCode)
	}
}

func TestProxyHandlerRequiresAuth(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return true })
	proxy := NewProxyHandler(hub, func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer good"
	})
	server := httptest.NewServer(proxy)
	defer server.Close()

	resp, err := http.Get(server.URL + "/control/nodes/node-a/proxy/meta")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestProxyHandlerTimeout(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool { return credential == "ck_secret" })

	// Node never responds.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.Timeout = 150 * time.Millisecond
	server := httptest.NewServer(proxy)
	defer server.Close()

	start := time.Now()
	resp, err := http.Get(server.URL + "/control/nodes/node-a/proxy/hang")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 on timeout, got %d", resp.StatusCode)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("proxy timeout took too long: %v", time.Since(start))
	}
}

// TestProxyHandlerStreamsSSE verifies the center proxy relays an SSE-style
// streaming response (chat events) from the node to the browser in real time
// instead of buffering the whole body.
func TestProxyHandlerStreamsSSE(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: hello\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: world\n\n")
		flusher.Flush()
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL+"/control/nodes/node-a/proxy/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected SSE content type, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), "world") {
		t.Fatalf("expected both SSE events, got %q", body)
	}
}

// TestProxyHandlerStreamsSSEInRealTime proves the proxy does not buffer the
// whole SSE body: the client must receive the first event before the node
// finishes the stream (the handler sleeps between events).
func TestProxyHandlerStreamsSSEInRealTime(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL+"/control/nodes/node-a/proxy/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The first event must arrive well before the handler's 400ms sleep ends.
	// Read it as a stream rather than waiting for the whole body.
	reader := bufio.NewReader(resp.Body)
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first line: %v", err)
	}
	if !strings.Contains(firstLine, "first") {
		t.Fatalf("expected first event, got %q", firstLine)
	}
	elapsed := time.Since(start)
	// If the proxy buffered everything, nothing arrives until the handler
	// returns (~400ms). With streaming, the first event arrives early.
	if elapsed >= 350*time.Millisecond {
		t.Fatalf("first event arrived too late (%v): proxy likely buffered", elapsed)
	}

	// Drain the rest to keep the connection tidy.
	_, _ = io.Copy(io.Discard, reader)
}

func TestProxyHandlerGuardedRemoteBlocksWrites(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	// The node is registered with guarded-remote trust: writes need approval.
	proxy.TrustLevel = func(nodeID string) string {
		return "guarded-remote"
	}
	server := httptest.NewServer(proxy)
	defer server.Close()

	// Write operation without approval is blocked.
	writeResp, err := http.Post(server.URL+"/control/nodes/node-a/proxy/sessions", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	defer writeResp.Body.Close()
	if writeResp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(writeResp.Body)
		t.Fatalf("expected 403 for guarded write, got %d: %s", writeResp.StatusCode, body)
	}

	// Write with explicit approval header passes through.
	approvedReq, err := http.NewRequest("POST", server.URL+"/control/nodes/node-a/proxy/sessions", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("build approved request: %v", err)
	}
	approvedReq.Header.Set("Content-Type", "application/json")
	approvedReq.Header.Set("X-Godex-Trust-Approved", "1")
	approvedResp, err := http.DefaultClient.Do(approvedReq)
	if err != nil {
		t.Fatalf("approved request: %v", err)
	}
	defer approvedResp.Body.Close()
	if approvedResp.StatusCode != 200 {
		body, _ := io.ReadAll(approvedResp.Body)
		t.Fatalf("expected 200 for approved write, got %d: %s", approvedResp.StatusCode, body)
	}

	// Read operations are allowed without approval.
	readResp, err := http.Get(server.URL + "/control/nodes/node-a/proxy/meta")
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	defer readResp.Body.Close()
	if readResp.StatusCode != 200 {
		t.Fatalf("expected 200 for guarded read, got %d", readResp.StatusCode)
	}
}

func TestProxyHandlerTrustLevelUnsetAllowsWrites(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-a", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-a")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.TrustLevel = func(nodeID string) string { return "trusted" }
	server := httptest.NewServer(proxy)
	defer server.Close()

	resp, err := http.Post(server.URL+"/control/nodes/node-a/proxy/sessions", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for trusted write, got %d: %s", resp.StatusCode, body)
	}
}

func waitOnline(t *testing.T, hub *Hub, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline(nodeID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %s did not come online", nodeID)
}
