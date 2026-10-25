package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// newLimitJoinServer wires the join route against the REAL account store and the
// REAL node registry.
//
// A fake that merely returns an error would only prove the handler can forward
// one: the behaviour worth pinning is that the tenant's stored allowance reaches
// the registry and that the registry's refusal becomes a 409, so the whole chain
// is exercised — policy read, atomic check, status mapping.
func newLimitJoinServer(t *testing.T) (*httptest.Server, *accounts.Service, *noderegistry.Registry) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry, err := noderegistry.New(t.TempDir()+"/nodes.json", 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         backend.NewService(cfg, nil, nil),
		Accounts:        accountsService,
		ControlRegistry: registry,
	}))
	t.Cleanup(server.Close)
	return server, accountsService, registry
}

// TestJoinRouteRefusesEnrolmentPastTenantLimit is the basic-tier quota (design §7)
// at the seam that tenant enrolment actually uses.
func TestJoinRouteRefusesEnrolmentPastTenantLimit(t *testing.T) {
	server, service, registry := newLimitJoinServer(t)
	ctx := context.Background()

	result, err := service.Register(accounts.RegisterInput{Email: "owner@example.com", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	tenantID := result.Tenant.ID
	if _, err := service.SetTenantMaxNodes(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}

	join := func(nodeID string) (*http.Response, map[string]any) {
		t.Helper()
		// A fresh token per attempt: codes are one-time, so reusing one would make
		// the second call fail for the wrong reason.
		_, token, err := service.CreateJoinToken(accounts.CreateJoinTokenInput{
			TenantID: tenantID, CreatedBy: result.User.ID,
		})
		if err != nil {
			t.Fatalf("CreateJoinToken: %v", err)
		}
		return doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
			"token": token, "node_id": nodeID,
		})
	}

	resp, body := join("node-1")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first enrolment status = %d, want 201: %v", resp.StatusCode, body)
	}
	if body["tenant_id"] != tenantID {
		t.Fatalf("tenant_id = %v, want %q", body["tenant_id"], tenantID)
	}

	resp, body = join("node-2")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second enrolment status = %d, want 409 (at the allowance): %v", resp.StatusCode, body)
	}
	// The refused node must not exist at all: registering it and then failing the
	// ownership step would leave an unowned node that still looks enrolled.
	if _, err := registry.Get(ctx, "node-2"); err == nil {
		t.Error("a refused enrolment left a registered node behind")
	}

	// Raising the allowance lets the SAME tenant proceed, so the refusal was about
	// the limit rather than about the node id or the token.
	if _, err := service.SetTenantMaxNodes(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxNodes (raise): %v", err)
	}
	if resp, body := join("node-2"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enrolment after raising the allowance = %d, want 201: %v", resp.StatusCode, body)
	}
}

// TestJoinRouteUnlimitedByDefault: a tenant with no policy row keeps enrolling, so
// the quota is purely additive for tenants that existed before it.
func TestJoinRouteUnlimitedByDefault(t *testing.T) {
	server, service, _ := newLimitJoinServer(t)

	result, err := service.Register(accounts.RegisterInput{Email: "owner@example.com", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		_, token, err := service.CreateJoinToken(accounts.CreateJoinTokenInput{
			TenantID: result.Tenant.ID, CreatedBy: result.User.ID,
		})
		if err != nil {
			t.Fatalf("CreateJoinToken: %v", err)
		}
		resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
			"token": token, "node_id": nodeID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("enrolment %s = %d, want 201 with no configured limit: %v", nodeID, resp.StatusCode, body)
		}
	}
}

// TestAdminClaimIgnoresTheAllowance: the operator repair path must not be blocked
// by the quota it may be repairing — otherwise a tenant stuck at its limit with a
// mis-assigned node could never be fixed.
func TestAdminClaimIgnoresTheAllowance(t *testing.T) {
	_, service, registry := newLimitJoinServer(t)
	ctx := context.Background()

	result, err := service.Register(accounts.RegisterInput{Email: "owner@example.com", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	tenantID := result.Tenant.ID
	if _, err := service.SetTenantMaxNodes(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}
	// One node fills the allowance.
	if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: "node-1"}); err != nil {
		t.Fatalf("register node-1: %v", err)
	}
	if _, err := registry.SetOwnership(ctx, "node-1", tenantID, result.User.ID, "private"); err != nil {
		t.Fatalf("assign node-1: %v", err)
	}
	if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: "node-2"}); err != nil {
		t.Fatalf("register node-2: %v", err)
	}

	// The plain setter (what /admin/nodes/{id}/claim uses) stays available.
	if _, err := registry.SetOwnership(ctx, "node-2", tenantID, result.User.ID, "private"); err != nil {
		t.Fatalf("admin claim refused by the allowance: %v", err)
	}
	// And the limit-aware setter still refuses, so the two paths really differ.
	if _, err := registry.SetOwnershipWithin(ctx, "node-2", "tenant-other", "", "private", 0); err != nil {
		t.Fatalf("unlimited assignment failed: %v", err)
	}
}
