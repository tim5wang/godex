package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

// TestDesktopEventsStreamHandshake verifies the desktop event bridge accepts
// an SSE subscription when no web token is configured.
func TestDesktopEventsStreamHandshake(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/desktop/events")
	if err != nil {
		t.Fatalf("open desktop events stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
}

// TestDesktopEventsStreamRequiresToken verifies the bridge is protected by the
// web token when one is configured.
func TestDesktopEventsStreamRequiresToken(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "desktop-test-token"
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	defer server.Close()

	// Missing token must be rejected.
	resp, err := http.Get(server.URL + "/desktop/events")
	if err != nil {
		t.Fatalf("open desktop events stream without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// Correct token must pass.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/desktop/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.WebToken)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open desktop events stream with token: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
}
