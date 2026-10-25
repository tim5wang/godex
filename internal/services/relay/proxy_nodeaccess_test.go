package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
)

// TestProxyHandlerAuthorizeNodeDeniesForward covers the P0 NodeAccess seam: a
// caller that passes the credential check but is not allowed to reach the
// target node must be refused, and the target must never see the request.
func TestProxyHandlerAuthorizeNodeDeniesForward(t *testing.T) {
	reached := false
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})
	agent := NewAgent(AgentConfig{
		CenterURL: wsURL, NodeID: "node-b", Credential: "ck_secret", Version: "v1",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-b")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.AuthorizeNode = func(r *http.Request, nodeID string) identity.Decision {
		return identity.Denied(identity.ReasonCrossTenant)
	}
	server := httptest.NewServer(proxy)
	defer server.Close()

	resp, err := http.Get(server.URL + "/control/nodes/node-b/proxy/api/sessions")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", resp.StatusCode, body)
	}
	if reached {
		t.Fatal("target node received a request the policy denied")
	}
}

// TestProxyHandlerAuthorizeNodeAllowsForward is the counterpart: when the
// policy allows, forwarding still works end to end.
func TestProxyHandlerAuthorizeNodeAllowsForward(t *testing.T) {
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})
	agent := NewAgent(AgentConfig{
		CenterURL: wsURL, NodeID: "node-b", Credential: "ck_secret", Version: "v1",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload, _ := json.Marshal(map[string]string{"path": r.URL.Path})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
		}),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-b")

	var seenNode string
	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.AuthorizeNode = func(r *http.Request, nodeID string) identity.Decision {
		seenNode = nodeID
		return identity.AllowedDecision(identity.ReasonSingleTenant)
	}
	server := httptest.NewServer(proxy)
	defer server.Close()

	resp, err := http.Get(server.URL + "/control/nodes/node-b/proxy/api/sessions")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	if seenNode != "node-b" {
		t.Fatalf("AuthorizeNode saw nodeID %q, want node-b", seenNode)
	}
}

// TestProxyHandlerAuthorizeNodeRunsBeforeLocalDispatch pins the ordering: the
// self node is served in-process, so the check must happen before that branch
// or a denial would be bypassed for local requests.
func TestProxyHandlerAuthorizeNodeRunsBeforeLocalDispatch(t *testing.T) {
	localCalled := false
	proxy := NewProxyHandler(nil, nil)
	proxy.SetLocalHandler("self", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	proxy.AuthorizeNode = func(r *http.Request, nodeID string) identity.Decision {
		return identity.Denied(identity.ReasonCrossTenant)
	}

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/nodes/self/proxy/api/sessions", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if localCalled {
		t.Fatal("local handler ran despite the policy denying access")
	}
}

// TestProxyHandlerWithoutAuthorizeNodeIsUnchanged guards against the seam
// accidentally becoming mandatory: nil must preserve the previous behaviour.
func TestProxyHandlerWithoutAuthorizeNodeIsUnchanged(t *testing.T) {
	localCalled := false
	proxy := NewProxyHandler(nil, nil)
	proxy.SetLocalHandler("self", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/nodes/self/proxy/api/sessions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !localCalled {
		t.Fatal("local handler should still run when no policy is wired")
	}
}
