package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/runtime/httpapi"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// The managed-forward REST surface (design §5's consumer #4, /control/forwards)
// was a THIRD face of the §1.3 hole: its credential gate accepts any login
// session, and registerForwardRoutes had no registry or policy to check the
// target node with. So a member of tenant A could open a tunnel into tenant B's
// node, and the listen-then-bridge meant the exposure happened before any
// response said no.
//
// These pin the fix. The credential half is asserted too (a session IS accepted),
// because that is what makes the node check load-bearing rather than incidental.

type forwardTestEnv struct {
	mux       *http.ServeMux
	server    *relay.ForwardServer
	tokenA    string
	tokenB    string
	nodeIDOwn string
	nodeIDB   string
}

func newForwardTestEnv(t *testing.T) forwardTestEnv {
	t.Helper()

	// --- accounts: two tenants, each with a signed-in user ---
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })

	regA, err := accountsService.Register(accounts.RegisterInput{
		Email: "a@example.com", Password: "correct-horse-battery", TenantName: "tenant-a",
	})
	if err != nil {
		t.Fatalf("register tenant a: %v", err)
	}
	regB, err := accountsService.Register(accounts.RegisterInput{
		Email: "b@example.com", Password: "correct-horse-battery", TenantName: "tenant-b",
	})
	if err != nil {
		t.Fatalf("register tenant b: %v", err)
	}

	// --- registry: one node owned by each tenant, both private (the default) ---
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"a-own", "b-node"} {
		if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: id, Name: id}); err != nil {
			t.Fatalf("register node %s: %v", id, err)
		}
	}
	// Ownership is written only by trusted paths; here we stand in for the admin
	// claim. Visibility stays private, exactly as a fresh node defaults to.
	if _, err := registry.SetOwnership(ctx, "a-own", regA.Tenant.ID, regA.User.ID, "private"); err != nil {
		t.Fatalf("own a-own: %v", err)
	}
	if _, err := registry.SetOwnership(ctx, "b-node", regB.Tenant.ID, regB.User.ID, "private"); err != nil {
		t.Fatalf("own b-node: %v", err)
	}

	// --- forward server + the real wiring under test ---
	hub := relay.NewHub(func(string, string) bool { return false })
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })
	forwardServer := relay.NewForwardServer(hub)
	if forwardServer == nil {
		t.Fatal("expected a forward server")
	}
	t.Cleanup(forwardServer.Shutdown)

	manager, err := config.NewManager(config.Options{
		WorkspaceDir: t.TempDir(),
		HomeDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}

	// A configured web token makes the credential gate real (it is skipped
	// entirely when no credential exists), so the node check is what the
	// cross-tenant cases actually exercise.
	cfg := &config.Config{WebToken: "web-secret", Control: config.ControlConfig{}}
	authorizeNode := newNodeAccessAuthorizer(
		httpapi.NewPrincipalResolver(manager, accountsService),
		registry,
		httpapi.NewNodeAccessPolicy(accountsService),
		accountsService,
	)

	mux := http.NewServeMux()
	registerForwardRoutes(mux, forwardServer, manager, nodeProxyAuthorize(cfg, accountsService), authorizeNode("forward"))

	return forwardTestEnv{
		mux:       mux,
		server:    forwardServer,
		tokenA:    regA.Token,
		tokenB:    regB.Token,
		nodeIDOwn: "a-own",
		nodeIDB:   "b-node",
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// doForward issues a request carrying a login session as the credential.
func doForward(t *testing.T, env forwardTestEnv, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	return rec
}

func createForward(t *testing.T, env forwardTestEnv, token, nodeID string, port int) *httptest.ResponseRecorder {
	t.Helper()
	return doForward(t, env, http.MethodPost, "/control/forwards", token, map[string]any{
		"name":       "probe",
		"node_id":    nodeID,
		"local_port": port,
		"target":     "127.0.0.1:9",
	})
}

func listForwards(t *testing.T, env forwardTestEnv, token string) []relay.ForwardStatus {
	t.Helper()
	rec := doForward(t, env, http.MethodGet, "/control/forwards", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out []relay.ForwardStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func TestForwardCreateRejectsCrossTenantNode(t *testing.T) {
	env := newForwardTestEnv(t)

	// The §1.3 regression: tenant A names tenant B's node.
	rec := createForward(t, env, env.tokenA, env.nodeIDB, freeLocalPort(t))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant create = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}

	// And nothing was opened: the refusal must happen BEFORE the listener binds,
	// otherwise the exposure exists even though the response says no.
	if got := len(env.server.List()); got != 0 {
		t.Fatalf("tunnels after a refused create = %d, want 0", got)
	}
}

func TestForwardCreateStillAllowsOwnNode(t *testing.T) {
	// The guard against the regression must not break the operator's own workflow:
	// a tenant creating a tunnel into its OWN node is the feature.
	env := newForwardTestEnv(t)

	rec := createForward(t, env, env.tokenA, env.nodeIDOwn, freeLocalPort(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("own-node create = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := len(env.server.List()); got != 1 {
		t.Fatalf("tunnels after an allowed create = %d, want 1", got)
	}
}

func TestForwardListMasksOtherTenants(t *testing.T) {
	// Masking rather than refusing: a tenant sees its own tunnels, and the list
	// does not double as a way to learn another tenant's node ids.
	env := newForwardTestEnv(t)
	if rec := createForward(t, env, env.tokenA, env.nodeIDOwn, freeLocalPort(t)); rec.Code != http.StatusCreated {
		t.Fatalf("seed create = %d (body=%s)", rec.Code, rec.Body.String())
	}

	if got := listForwards(t, env, env.tokenA); len(got) != 1 {
		t.Fatalf("tenant A sees %d tunnels, want 1", len(got))
	}
	if got := listForwards(t, env, env.tokenB); len(got) != 0 {
		t.Fatalf("tenant B sees %d of tenant A's tunnels, want 0", len(got))
	}
}

func TestForwardDeleteAndCheckRejectCrossTenantNode(t *testing.T) {
	// Delete and check are writes into the node's exposure too: deleting removes
	// the tenant's control of it, and check opens a real connection over the
	// node's network. Both must be gated like create.
	env := newForwardTestEnv(t)
	rec := createForward(t, env, env.tokenA, env.nodeIDOwn, freeLocalPort(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed create = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var spec struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if spec.ID == "" {
		t.Fatal("expected the created tunnel to carry an id")
	}

	if got := doForward(t, env, http.MethodDelete, "/control/forwards/"+spec.ID, env.tokenB, nil); got.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant delete = %d, want 403", got.Code)
	}
	if got := doForward(t, env, http.MethodPost, "/control/forwards/"+spec.ID+"/check", env.tokenB, nil); got.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant check = %d, want 403", got.Code)
	}
	// The tunnel survived the foreign delete attempt.
	if got := len(env.server.List()); got != 1 {
		t.Fatalf("tunnels after refused delete = %d, want 1", got)
	}
}

func TestForwardGateStillAcceptsTenantSession(t *testing.T) {
	// Pins WHY the node check is load-bearing: the credential gate accepts a
	// plain tenant login session. If this ever becomes false the node check is
	// still correct, but a reader should not have to rediscover that the
	// credential layer alone was never the boundary.
	env := newForwardTestEnv(t)
	if got := doForward(t, env, http.MethodGet, "/control/forwards", env.tokenA, nil); got.Code != http.StatusOK {
		t.Fatalf("tenant session list = %d, want 200 (the gate accepts sessions)", got.Code)
	}
	if got := doForward(t, env, http.MethodGet, "/control/forwards", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", got.Code)
	}
}

// TestForwardAuthorizerAgreesWithProxySeam pins the structural fix: the surface
// label must affect only the audit row, never the decision. If someone later
// gives the forward path its own policy or a looser check, this fails even
// though every single-surface test above would still pass.
func TestForwardAuthorizerAgreesWithProxySeam(t *testing.T) {
	// Build the two surfaces from the SAME factory inputs, as production does.
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"a-own", "b-node"} {
		if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: id, Name: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if _, err := registry.SetOwnership(ctx, "a-own", "tenant-a", "user-tenant-a", "private"); err != nil {
		t.Fatalf("own a-own: %v", err)
	}
	if _, err := registry.SetOwnership(ctx, "b-node", "tenant-b", "user-tenant-b", "private"); err != nil {
		t.Fatalf("own b-node: %v", err)
	}

	cases := []struct{ tenantID, nodeID string }{
		{"tenant-a", "a-own"},
		{"tenant-a", "b-node"},
		{"tenant-b", "a-own"},
	}
	for _, surface := range []string{"proxy", "forward"} {
		build := newNodeAccessAuthorizer(
			func(r *http.Request) identity.Principal {
				return identity.NewPrincipal(r.Header.Get("X-Test-Tenant"), "user-"+r.Header.Get("X-Test-Tenant"), "", identity.KindUser, identity.EntitlementRelayOnly)
			},
			registry,
			identity.Policy{},
			nil,
		)
		decs := build(surface)
		for _, tc := range cases {
			req := httptest.NewRequest(http.MethodPost, "/control/forwards", nil)
			req.Header.Set("X-Test-Tenant", tc.tenantID)
			got := decs(req, tc.nodeID)
			want := tc.tenantID == "tenant-a" && tc.nodeID == "a-own"
			if got.Allowed != want {
				t.Errorf("surface %s: %s -> %s allowed=%v, want %v", surface, tc.tenantID, tc.nodeID, got.Allowed, want)
			}
		}
	}
}
