package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/relay"
)

// TestProtectedEndpointAcceptsRelayTrustHeader verifies that a request
// carrying a valid relay-channel trust header (HMAC signed with this node's
// own credential) bypasses the web-token check — this is what makes remote
// operations (chat/terminal/files) work through the center proxy without the
// browser knowing each node's local web token.
func TestProtectedEndpointAcceptsRelayTrustHeader(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	// Node side: the node has joined a center with its own credential and id.
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"control.credential": "ck_node_secret",
		"control.node_id":    "my-laptop",
	}}); err != nil {
		t.Fatalf("seed control config: %v", err)
	}
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	// Without any auth header the request must still be rejected.
	resp, err := http.Get(server.URL + "/sessions")
	if err != nil {
		t.Fatalf("get sessions without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// With a valid relay trust header (only the node can produce it) the
	// request must be accepted even without the web token.
	trust := relay.SignRelayTrust("my-laptop", "ck_node_secret")
	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(relay.RelayTrustHeader, trust)
	trustResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get sessions with trust header: %v", err)
	}
	defer trustResp.Body.Close()
	if trustResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(trustResp.Body)
		t.Fatalf("expected 200 with relay trust header, got %d: %s", trustResp.StatusCode, body)
	}
}

// TestProtectedEndpointRejectsForgedRelayTrust verifies that a garbage or
// mismatched trust header does not bypass auth.
func TestProtectedEndpointRejectsForgedRelayTrust(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"control.credential": "ck_node_secret",
		"control.node_id":    "my-laptop",
	}}); err != nil {
		t.Fatalf("seed control config: %v", err)
	}
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	// Wrong credential / wrong node id / random value must all be rejected.
	for _, value := range []string{
		relay.SignRelayTrust("my-laptop", "ck_wrong"),
		relay.SignRelayTrust("other-node", "ck_node_secret"),
		"forged-garbage",
	} {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set(relay.RelayTrustHeader, value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get sessions: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for forged trust header %q, got %d", value, resp.StatusCode)
		}
	}
}

// TestProtectedEndpointNoCredentialStillWebTokenOnly verifies that when the
// node has no control.credential configured (center mode), the trust header
// does nothing and web-token auth still applies.
func TestProtectedEndpointNoCredentialStillWebTokenOnly(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "secret-token"
	manager := newTestManager(t, cfg)
	// No control.credential seeded: this instance acts as a center.
	caller := &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("done")}}}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(relay.RelayTrustHeader, relay.SignRelayTrust("any-node", "any-cred"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get sessions: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without web token even with trust header, got %d", resp.StatusCode)
	}

	// With the correct web token it works.
	authed, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	authed.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	authedResp, err := http.DefaultClient.Do(authed)
	if err != nil {
		t.Fatalf("get sessions: %v", err)
	}
	defer authedResp.Body.Close()
	if authedResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with web token, got %d", authedResp.StatusCode)
	}
	_ = strings.TrimSpace // keep strings import
}
