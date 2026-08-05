package relay

import (
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
