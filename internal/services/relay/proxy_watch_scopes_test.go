package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyForwardsWatchScopesHeader covers the hop that makes multi-node wrist
// control work: the node the watch is paired with sends the scopes it already
// verified, and the center must deliver them to the target node.
//
// This is not incidental plumbing. The target node has never seen this wrist, so
// it cannot validate a wch_ credential; the forwarded scope list is the only
// thing that authorises the request there. When the proxy forwarded just
// Content-Type and Authorization, every multi-node wrist call arrived with no
// authority and was refused — while each side's own unit tests still passed,
// because the header was only dropped in the middle.
func TestProxyForwardsWatchScopesHeader(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	var gotScopes string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScopes = r.Header.Get(WatchScopesHeader)
		w.WriteHeader(http.StatusOK)
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-b", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-b")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest("POST", server.URL+"/control/nodes/node-b/proxy/watch/command",
		strings.NewReader(`{"session_id":"s","text":"hi"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(WatchScopesHeader, "watch:command")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if gotScopes != "watch:command" {
		t.Fatalf("target node saw %s=%q, want watch:command", WatchScopesHeader, gotScopes)
	}
}

// TestProxyStillForwardsTrustHeaderAllowlistNarrowly pins the boundary next to the
// new header: adding the watch-scope header must not have widened the allowlist
// into "forward everything". A caller-supplied relay-trust header is the one that
// matters — honouring it would let any client claim relay trust and skip the
// target node's own credential checks.
func TestProxyStillForwardsTrustHeaderAllowlistNarrowly(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})

	got := map[string]string{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got[RelayTrustHeader] = r.Header.Get(RelayTrustHeader)
		got["X-Evil"] = r.Header.Get("X-Evil")
		got[WatchScopesHeader] = r.Header.Get(WatchScopesHeader)
		w.WriteHeader(http.StatusOK)
	})
	agent := NewAgent(AgentConfig{CenterURL: wsURL, NodeID: "node-b", Credential: "ck_secret", Version: "v1.2.0", Handler: handler})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-b")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest("POST", server.URL+"/control/nodes/node-b/proxy/watch/command", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// A forged trust value and an arbitrary header must both be dropped.
	req.Header.Set(RelayTrustHeader, "forged-by-client")
	req.Header.Set("X-Evil", "should-not-arrive")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	if got[RelayTrustHeader] == "forged-by-client" {
		// The node's own agent legitimately injects a signed value on every relayed
		// request, so non-empty is expected; what must never happen is the caller's
		// forged string surviving the hop.
		t.Fatalf("client-forged trust header reached the node verbatim: %q", got[RelayTrustHeader])
	}
	if want := SignRelayTrust("node-b", "ck_secret"); got[RelayTrustHeader] != want {
		t.Fatalf("trust header = %q, want the node's own signature", got[RelayTrustHeader])
	}
	if got["X-Evil"] != "" {
		t.Fatalf("arbitrary header reached the node: %q", got["X-Evil"])
	}
}
