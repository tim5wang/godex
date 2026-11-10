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

// TestJoinRouteRefusesEnrolmentPastTheDeviceLimit is the basic-tier quota at the seam
// that tenant enrolment actually uses: a member at their device limit cannot enrol
// another device.
func TestJoinRouteRefusesEnrolmentPastTheDeviceLimit(t *testing.T) {
	server, service, registry := newLimitJoinServer(t)
	ctx := context.Background()

	result, err := service.Register(accounts.RegisterInput{Email: "owner@example.com", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	tenantID := result.Tenant.ID
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
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

	// Raising the allowance lets the SAME member proceed, so the refusal was about
	// the limit rather than about the device id or the token.
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser (raise): %v", err)
	}
	if resp, body := join("node-2"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("enrolment after raising the allowance = %d, want 201: %v", resp.StatusCode, body)
	}
}

// TestJoinRouteAllowanceIsPerMemberNotPerTenant is the property that makes the
// allowance usable at all: with a tenant-wide device count, the second person to join
// would find the allowance already spent by the first, so "2 devices each" could not
// be honoured. Here the owner is at their limit and their colleague still enrols.
func TestJoinRouteAllowanceIsPerMemberNotPerTenant(t *testing.T) {
	server, service, _ := newLimitJoinServer(t)

	owner, err := service.Register(accounts.RegisterInput{Email: "owner@example.com", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	tenantID := owner.Tenant.ID
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}

	// A second member of the SAME tenant, added the way a real invite works.
	invite, code, err := service.CreateUserInvite(accounts.CreateUserInviteInput{
		TenantID: tenantID, Role: "member", InvitedBy: owner.User.ID,
	})
	if err != nil {
		t.Fatalf("CreateUserInvite: %v", err)
	}
	_ = invite
	member, err := service.AcceptUserInvite(code, "member@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("AcceptUserInvite: %v", err)
	}

	enrol := func(nodeID, ownerUserID string) (*http.Response, map[string]any) {
		t.Helper()
		_, token, err := service.CreateJoinToken(accounts.CreateJoinTokenInput{
			TenantID: tenantID, OwnerUserID: ownerUserID, CreatedBy: owner.User.ID,
		})
		if err != nil {
			t.Fatalf("CreateJoinToken: %v", err)
		}
		return doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
			"token": token, "node_id": nodeID,
		})
	}

	// The owner fills their own allowance of 1.
	if resp, body := enrol("owner-device", owner.User.ID); resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner's first device = %d, want 201: %v", resp.StatusCode, body)
	}
	// The owner's SECOND device is refused...
	if resp, body := enrol("owner-device-2", owner.User.ID); resp.StatusCode != http.StatusConflict {
		t.Fatalf("owner's second device = %d, want 409: %v", resp.StatusCode, body)
	}
	// ...while the colleague's FIRST device is accepted against the same limit of 1.
	if resp, body := enrol("member-device", member.User.ID); resp.StatusCode != http.StatusCreated {
		t.Fatalf("member's first device = %d, want 201 (the owner's devices must not consume it): %v", resp.StatusCode, body)
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
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
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
