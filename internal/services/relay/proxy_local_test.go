package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyHandlerLocalDirect verifies that when the proxy is wired with a
// local handler for the self node (center server acting as its own node), a
// request to /control/nodes/{self}/proxy/{path...} is served locally instead
// of being forwarded over the relay channel (which would fail offline).
func TestProxyHandlerLocalDirect(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	var localCalls int
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls++
		body, _ := io.ReadAll(r.Body)
		payload, _ := json.Marshal(map[string]string{
			"local": "yes",
			"path":  r.URL.Path,
			"body":  string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.SetLocalHandler("self-node", local)
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest("POST", server.URL+"/control/nodes/self-node/proxy/sessions", strings.NewReader(`{"prompt":"hi"}`))
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
	if localCalls != 1 {
		t.Fatalf("expected local handler to be called once, got %d", localCalls)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]string
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if parsed["local"] != "yes" {
		t.Fatalf("expected local direct response, got %#v", parsed)
	}
	// The local handler must see the relay-stripped path (node-local httpapi path).
	if parsed["path"] != "/sessions" {
		t.Fatalf("expected local path /sessions, got %q", parsed["path"])
	}
	if parsed["body"] != `{"prompt":"hi"}` {
		t.Fatalf("expected body preserved, got %q", parsed["body"])
	}
}

// TestProxyHandlerLocalDirectOtherNodeStillForwards verifies that setting a
// local handler only affects the self node id; other nodes keep going over
// relay (and therefore fail offline with 503, proving no local shortcut).
func TestProxyHandlerLocalDirectOtherNodeStillForwards(t *testing.T) {
	hub, _ := serveRelayHub(t, func(nodeID, credential string) bool { return true })

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.SetLocalHandler("self-node", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	server := httptest.NewServer(proxy)
	defer server.Close()

	resp, err := http.Get(server.URL + "/control/nodes/other-node/proxy/meta")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for offline non-self node, got %d", resp.StatusCode)
	}
	_ = context.Background()
}
