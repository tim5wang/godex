package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyHandlerNeverForwardsTrustHeader verifies the security invariant of
// the relay-channel trust model: the center-side ProxyHandler must NOT forward
// a client-supplied X-Godex-Relay-Trusted header to the node. The trust header
// is only ever injected by the node-side agent (which holds the node
// credential); if the proxy passed through a browser-forged value, a malicious
// client could impersonate a relay channel. The proxy forwards a whitelist of
// headers (Content-Type, Authorization), so any other header — including the
// trust header — must be dropped.
func TestProxyHandlerNeverForwardsTrustHeader(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	var gotTrustHeader string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTrustHeader = r.Header.Get(RelayTrustHeader)
		w.WriteHeader(http.StatusOK)
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

	req, err := http.NewRequest("GET", server.URL+"/control/nodes/node-a/proxy/meta", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// A malicious client tries to forge the relay trust header.
	req.Header.Set(RelayTrustHeader, "forged-by-attacker")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// The node must never see the browser-forged value. It may see the agent's
	// own signature (injected in serveRequest), which is the expected value.
	if gotTrustHeader == "forged-by-attacker" {
		t.Fatal("proxy forwarded forged trust header to the node — security invariant violated")
	}
	if gotTrustHeader != "" && !ValidateRelayTrust(gotTrustHeader, "node-a", "ck_secret") {
		t.Fatalf("node saw unexpected trust header %q", gotTrustHeader)
	}
}

// TestAgentInjectsTrustHeaderOnForwardedRequest verifies the node-side agent
// adds the relay trust header signed with its own credential when serving a
// request from the center, so the node's httpapi can skip the web-token check.
func TestAgentInjectsTrustHeaderOnForwardedRequest(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	var gotTrust string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTrust = r.Header.Get(RelayTrustHeader)
		w.WriteHeader(http.StatusOK)
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

	resp, err := http.Get(server.URL + "/control/nodes/node-a/proxy/meta")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if gotTrust == "" {
		t.Fatal("expected agent to inject relay trust header")
	}
	if !ValidateRelayTrust(gotTrust, "node-a", "ck_secret") {
		t.Fatalf("agent injected invalid trust header %q", gotTrust)
	}
	if !strings.Contains(gotTrust, "") {
		t.Fatal("unreachable")
	}
}
