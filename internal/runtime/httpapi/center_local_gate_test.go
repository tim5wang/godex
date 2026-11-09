package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/relay"
)

// newCenterLocalTestServer serves the center-local surfaces with multi-tenant
// sign-in wired, which is what makes the tenant refusal below meaningful: without
// an accounts service a login-session token would fall through to the operator
// principal and every caller would look like the superadmin.
func newCenterLocalTestServer(t *testing.T, webToken string) *httptest.Server {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  service,
		Accounts: accountsService,
	}))
	t.Cleanup(server.Close)
	return server
}

// centerLocalCalls are the requests a tenant user must be refused.
//
// These are the surfaces whose DATA LIVES ON THIS INSTANCE (design §6): the flow
// store, notes, durable memory, capture logs, cron/heartbeat jobs, agent templates,
// node library, MCP / provider config, packages and the skill inventory. Every one
// of them was registered behind bare `protected` — "any authenticated human" — so a
// signed-in tenant user could list, read, and in several cases delete or publish the
// operator's own machine state, while the session and execution surfaces already
// answered 403.
//
// Kept as a table so a route added to one of these surfaces is a one-line addition
// rather than a test that might not be written (same convention as
// sessionSurfaceCalls). Each entry is a READ where a cheap one exists, because the
// claim is about the gate, not the handler; the flows set includes writes because
// that surface is where the leak was found.
//
// NOT covered here, deliberately:
//   - `/v1/mcp/*`: this test server has no MCP manager, so registerMCPRoutes returns
//     early and registers nothing (asserting on an unregistered path would pass or
//     fail for the wrong reason). Its gate is the same wiring line in httpapi.go.
//   - node-local surfaces (/files, /git, /terminal, /control/sandbox, /desktop,
//     /voice, /browser, /preview): reached through /control/nodes/{id}/proxy and
//     gated by NodeAccess, which is the correct multi-tenant seam and is covered by
//     the §2-node-face tests in this package.
//   - `/usage` (node model traffic + tenant-scoped relay metering) and `/config`
//     (gated per ITEM — see config_admin_gate_test.go).
func centerLocalCalls() []struct {
	name   string
	method string
	path   string
	body   any
} {
	return []struct {
		name   string
		method string
		path   string
		body   any
	}{
		// Flows — where the leak was found (the whole surface answered "anyone").
		{"list flows", http.MethodGet, "/v1/flows", nil},
		{"read a flow", http.MethodGet, "/v1/flows/fl_gate_seed", nil},
		{"create a flow", http.MethodPost, "/v1/flows", map[string]any{
			"flow_id": "fl_gate_tenant", "version": "1",
			"definition": map[string]any{"flow_id": "fl_gate_tenant", "version": "1"},
		}},
		{"validate a version", http.MethodPost, "/v1/flows/fl_gate_seed/versions/1/validate", map[string]any{
			"flow_id": "fl_gate_seed", "version": "1",
		}},
		{"publish a version", http.MethodPost, "/v1/flows/fl_gate_seed/versions/1/publish", nil},
		{"delete a version", http.MethodDelete, "/v1/flows/fl_gate_seed/versions/1", nil},
		{"delete a flow", http.MethodDelete, "/v1/flows/fl_gate_seed", nil},
		{"list runs", http.MethodGet, "/v1/flows/fl_gate_seed/runs", nil},
		{"list sessions", http.MethodGet, "/v1/flows/fl_gate_seed/sessions", nil},
		{"read a run", http.MethodGet, "/v1/flow-runs/run_seed?flow_id=fl_gate_seed", nil},
		{"cancel a run", http.MethodPost, "/v1/flow-runs/run_seed/cancel?flow_id=fl_gate_seed", nil},
		{"diagnose a run", http.MethodPost, "/v1/flow-runs/run_seed/diagnose?flow_id=fl_gate_seed", nil},
		{"read run events", http.MethodGet, "/v1/flow-runs/run_seed/events?flow_id=fl_gate_seed&poll=1", nil},
		{"inspection report", http.MethodGet, "/v1/flow-inspection?window_hours=24", nil},
		{"human task queue", http.MethodGet, "/v1/human-tasks", nil},
		{"node library", http.MethodGet, "/v1/node-library", nil},

		// Knowledge surfaces.
		{"list notes", http.MethodGet, "/notes", nil},
		{"read a note", http.MethodGet, "/notes/note_seed", nil},
		{"save a note", http.MethodPost, "/notes", map[string]any{"title": "t", "content": "c"}},
		{"search memory", http.MethodGet, "/memory?q=anything", nil},
		{"memory candidates", http.MethodGet, "/memory/candidates", nil},
		{"memory audit", http.MethodGet, "/memory/audit", nil},

		// Capture logs (full request/response bodies of this instance's traffic).
		{"capture status", http.MethodGet, "/llm-capture/status", nil},
		{"capture records", http.MethodGet, "/llm-capture/records", nil},

		// Scheduled jobs owned by this instance.
		{"list cron jobs", http.MethodGet, "/automation/cron/jobs", nil},
		{"read heartbeat", http.MethodGet, "/automation/heartbeat", nil},

		// Machine configuration and capabilities.
		{"list agent templates", http.MethodGet, "/agent-templates", nil},
		{"template form options", http.MethodGet, "/agent-templates/options", nil},
		{"list providers", http.MethodGet, "/providers", nil},
		{"skill catalog", http.MethodGet, "/skills/catalog", nil},
		{"acp agent models", http.MethodGet, "/acp/agents/acp_seed/models", nil},
		{"acp agent config options", http.MethodGet, "/acp/agents/acp_seed/config-options", nil},

		// Package / model catalog.
		{"list models", http.MethodGet, "/models", nil},
		{"security summary", http.MethodGet, "/security/summary", nil},
		{"security audit", http.MethodGet, "/security/audit", nil},
		{"list packages", http.MethodGet, "/packages", nil},
		{"package quality", http.MethodGet, "/packages/quality", nil},
		{"list prompts", http.MethodGet, "/prompts", nil},
		{"list commands", http.MethodGet, "/commands", nil},
		{"package roles", http.MethodGet, "/packages/roles", nil},
	}
}

// TestTenantUserIsRefusedTheCenterLocalSurface is the hole itself: center-local data
// belongs to the operator (design §6), so no tenant principal may reach it.
//
// Before the gate these routes were behind `protected` only, while the session and
// execution surfaces already answered 403 — the same shape of gap that
// routes_center_local_gate_test.go was written for, a whole family of surfaces over.
func TestTenantUserIsRefusedTheCenterLocalSurface(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	for _, tc := range centerLocalCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, tenantToken, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as tenant: status = %d, want 403: %v", tc.name, resp.StatusCode, body)
			}
		})
	}
}

// TestOperatorStillReachesTheCenterLocalSurface is the other half: the gate must not
// refuse the surfaces' intended user.
//
// It asserts "not 403" rather than a specific success code on purpose: 400/404/409/500
// here would be lifecycle semantics (a seeded flow does not exist, cron is unavailable
// in this fixture, a definition is intentionally minimal) and are unrelated to what
// this test claims — that the GATE does not refuse the operator.
func TestOperatorStillReachesTheCenterLocalSurface(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")

	for _, tc := range centerLocalCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, "web-secret", tc.body)
			if resp.StatusCode == http.StatusForbidden {
				t.Fatalf("operator refused on %s: %v", tc.name, body)
			}
		})
	}
}

// TestNodeActorStillReachesTheCenterLocalFlowSurface pins the exemption that keeps
// remote-node operation working.
//
// When a remote node is active, the web client proxies the Flows/Settings/Memory pages
// to that node, and the request arrives at the node wearing the relay-trust identity.
// Gating on the platform role alone would answer 403 there — the exact trap documented
// for /channels/* (requirePlatformAdminOrNodeActor) and restricted settings
// (authorizeConfigWrite). A node's own flows are center-local to that node.
func TestNodeActorStillReachesTheCenterLocalFlowSurface(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"control.credential": "ck_node_secret",
		"control.node_id":    "my-laptop",
	}}); err != nil {
		t.Fatalf("seed control config: %v", err)
	}
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/flows", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(relay.RelayTrustHeader, relay.SignRelayTrust("my-laptop", "ck_node_secret"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get flows with trust header: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("node actor refused on the flow surface: status = %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node actor list flows: status = %d, want 200", resp.StatusCode)
	}
}

// TestUnauthenticatedCallerIsRefusedTheCenterLocalSurface pins that the gate keeps the
// authentication requirement rather than replacing it, and that a missing principal
// fails closed (401, not 403) — an unauthenticated caller must not be reported as
// "forbidden", which would send the client retrying the wrong way.
func TestUnauthenticatedCallerIsRefusedTheCenterLocalSurface(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")

	resp, _ := doJSON(t, http.MethodGet, server.URL+"/v1/flows", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list flows: status = %d, want 401", resp.StatusCode)
	}
}

// TestRevealSecretIsRefusedToTenantUser covers the disclosure half.
//
// POST /config/reveal returns a credential in plaintext — web.token included — and was
// behind `protected` alone, so a tenant user could read the operator's own secrets.
// The fix reuses requirePlatformAdminOrNodeActor (per-item gating does not fit a
// disclosure endpoint; the node exemption is what remote Settings editing needs).
func TestRevealSecretIsRefusedToTenantUser(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/config/reveal", tenantToken,
		map[string]any{"path": "web.token"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant reveal: status = %d, want 403: %v", resp.StatusCode, body)
	}
	// The credential must not be in the body even as an error detail.
	if value, ok := body["value"].(string); ok && value != "" {
		t.Fatalf("reveal refusal leaked a value: %q", value)
	}

	// The operator must still be able to reveal (it is their own instance).
	resp, body = doJSON(t, http.MethodPost, server.URL+"/config/reveal", "web-secret",
		map[string]any{"path": "web.token"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator reveal: status = %d, want 200: %v", resp.StatusCode, body)
	}

	// And an unauthenticated caller must fail closed.
	resp, _ = doJSON(t, http.MethodPost, server.URL+"/config/reveal", "", map[string]any{"path": "web.token"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated reveal: status = %d, want 401", resp.StatusCode)
	}
}
