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

// newCenterLocalTestServer serves the flow surface with multi-tenant sign-in
// wired, which is what makes the tenant refusal below meaningful: without an
// accounts service a login-session token would fall through to the operator
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

// centerLocalFlowCalls are the flow-surface requests a tenant user must be refused.
//
// The list is deliberately the READ surface plus the definition writes: these were
// the routes registered behind bare `protected`, so "any signed-in user" reached
// them — the flow store is global, so a tenant could list every flow on the center,
// read any version, and delete or publish anything. Kept as a table so a route
// added to this surface is a one-line addition rather than a test that might not be
// written (same convention as sessionSurfaceCalls).
func centerLocalFlowCalls() []struct {
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
		{"list flows", http.MethodGet, "/v1/flows", nil},
		{"read a flow", http.MethodGet, "/v1/flows/fl_gate_seed", nil},
		{"create a flow", http.MethodPost, "/v1/flows", map[string]any{
			"flow_id": "fl_gate_tenant", "version": "1", "definition": map[string]any{
				"flow_id": "fl_gate_tenant", "version": "1",
			},
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
	}
}

// TestTenantUserIsRefusedTheCenterLocalFlowSurface is the hole itself: the flow
// store is center-local data (design §6), so no tenant principal may reach it.
//
// Before the gate these routes were behind `protected` only, while the session and
// execution surfaces already answered 403 — the same shape of gap that
// routes_center_local_gate_test.go was written for, one surface over.
func TestTenantUserIsRefusedTheCenterLocalFlowSurface(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	for _, tc := range centerLocalFlowCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, tenantToken, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as tenant: status = %d, want 403: %v", tc.name, resp.StatusCode, body)
			}
		})
	}
}

// TestOperatorStillReachesTheCenterLocalFlowSurface is the other half: the gate
// must not refuse the surface's intended user.
//
// It asserts "not 403" rather than a specific success code on purpose: 400/404/409
// here would be flow-store lifecycle semantics (the seeded flow does not exist, a
// definition is intentionally minimal), which is unrelated to what this test
// claims — that the GATE does not refuse the operator.
func TestOperatorStillReachesTheCenterLocalFlowSurface(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")

	for _, tc := range centerLocalFlowCalls() {
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
// When a remote node is active, the web client proxies the Flows page to that node,
// and the request arrives at the node wearing the relay-trust identity. Gating on
// the platform role alone would answer 403 there — the exact trap documented for
// /channels/* (requirePlatformAdminOrNodeActor) and restricted settings
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

// TestUnauthenticatedCallerIsRefusedTheCenterLocalFlowSurface pins that the gate
// keeps the authentication requirement rather than replacing it, and that a missing
// principal fails closed (401, not 403) — an unauthenticated caller must not be
// reported as "forbidden", which would send the client retrying the wrong way.
func TestUnauthenticatedCallerIsRefusedTheCenterLocalFlowSurface(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")

	resp, _ := doJSON(t, http.MethodGet, server.URL+"/v1/flows", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list flows: status = %d, want 401", resp.StatusCode)
	}
}
