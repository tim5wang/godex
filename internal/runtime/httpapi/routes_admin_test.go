package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// newAdminServer wires the real handler with an account store, so the platform
// administration surface is exercised end to end.
func newAdminServer(t *testing.T, webToken string) (*httptest.Server, *accounts.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	handler := NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  backend.NewService(cfg, nil, nil),
		Accounts: accountsService,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, accountsService
}

func doJSON(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp, decoded
}

func TestAdminTenantsListsAcrossTenants(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")

	// Two signups → two tenants. The operator must see both; that is the point of
	// the surface.
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if resp, _ := postAuth(t, server.URL+"/auth/register", map[string]any{
			"email": email, "password": "correct-horse",
		}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed register %s failed: %d", email, resp.StatusCode)
		}
	}

	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	tenants, _ := body["tenants"].([]any)
	if len(tenants) != 2 {
		t.Fatalf("tenants = %d, want 2: %v", len(tenants), body)
	}
	first, _ := tenants[0].(map[string]any)
	// Default sharing is enabled so the switch stays purely subtractive.
	if first["sharing_enabled"] != true {
		t.Errorf("sharing_enabled = %v, want true by default", first["sharing_enabled"])
	}
	if first["member_count"] != float64(1) {
		t.Errorf("member_count = %v, want 1", first["member_count"])
	}
}

// TestNodeProxyReadAcceptsSessionWithoutEscalation locks the two halves of the
// node-read surface together.
//
// The Web UI's node pages authenticate with a login session, so this surface must
// accept one — a tenant user has to see their own nodes. But classification falls
// through to the platform operator when no checker claims the request, so merely
// ACCEPTING the token would classify every signed-in user as the superadmin.
// These two assertions must therefore hold at the same time; either one alone is a
// bug (401 for a legitimate user, or a privilege escalation).
func TestNodeProxyReadAcceptsSessionWithoutEscalation(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	sessionToken, _ := session["token"].(string)
	if sessionToken == "" {
		t.Fatal("no session token issued")
	}

	resp, _ := doJSON(t, http.MethodGet, server.URL+"/control/nodes", sessionToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session cannot read /control/nodes: status = %d (the Web UI's node pages use this route)", resp.StatusCode)
	}

	// The session must resolve to the tenant user, NOT the operator. Asserted via
	// /auth/me, which reports the resolved principal itself.
	_, profile := doJSON(t, http.MethodGet, server.URL+"/auth/me", sessionToken, nil)
	principal, _ := profile["principal"].(map[string]any)
	if principal["platform_role"] != nil {
		t.Fatalf("session was classified as a platform admin: %v", principal)
	}
	if principal["auth_method"] != AuthMethodSession {
		t.Fatalf("auth_method = %v, want %q", principal["auth_method"], AuthMethodSession)
	}
}

// TestAdminRoutesRefuseTenantUser is the boundary that matters: the
// administration surface crosses tenant boundaries, so a signed-in tenant user
// must not reach it — not even their own tenant's row.
func TestAdminRoutesRefuseTenantUser(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantToken, _ := session["token"].(string)
	if tenantToken == "" {
		t.Fatal("no session token issued")
	}

	for _, route := range []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/admin/tenants"},
		{method: http.MethodGet, path: "/admin/policy"},
		{method: http.MethodGet, path: "/admin/tenants/tnt_x/members"},
		{method: http.MethodPut, path: "/admin/tenants/tnt_x/sharing", body: map[string]any{"enabled": false}},
	} {
		resp, _ := doJSON(t, route.method, server.URL+route.path, tenantToken, route.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s status = %d, want 403 for a tenant user", route.method, route.path, resp.StatusCode)
		}
	}
}

func TestAdminRoutesRequireAuthentication(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a credential", resp.StatusCode)
	}
}

func TestAdminMembersListsUsers(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants/"+tenantID+"/members", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	members, _ := body["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("members = %d, want 1: %v", len(members), body)
	}
	member, _ := members[0].(map[string]any)
	if member["role"] != identity.RoleTenantOwner {
		t.Errorf("role = %v, want owner", member["role"])
	}
	// The email is rendered, the password hash must never be.
	if raw, err := json.Marshal(body); err == nil && bytes.Contains(raw, []byte("$2a$")) {
		t.Fatalf("members response leaked a bcrypt hash: %s", raw)
	}
}

// TestAdminSharingRequiresExplicitEnabled: an omitted field must not silently
// disable sharing, which is why the request uses a pointer.
func TestAdminSharingRequiresExplicitEnabled(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	if resp, _ := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatal("seed register failed")
	}

	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/sharing", "web-secret", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when enabled is omitted", resp.StatusCode)
	}
}

func TestAdminSharingTogglePersists(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/sharing",
		"web-secret", map[string]any{"enabled": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["sharing_enabled"] != false {
		t.Fatalf("response = %v, want sharing_enabled false", body)
	}
	// The stored policy must be what the authorization seam reads.
	if accountsService.TenantSharingEnabled(tenantID) {
		t.Error("TenantSharingEnabled still true after being switched off")
	}
	// Listing reflects it too, so the UI does not have to reconcile two sources.
	_, listed := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := listed["tenants"].([]any)
	row, _ := tenants[0].(map[string]any)
	if row["sharing_enabled"] != false {
		t.Errorf("list shows sharing_enabled = %v, want false", row["sharing_enabled"])
	}
}

// TestSharingDisabledBlocksPeerReachButNotSelf is the decision ④⑥ behaviour made
// concrete: the switch removes peer exposure and never the owner's own access.
func TestSharingDisabledBlocksPeerReachButNotSelf(t *testing.T) {
	policy := identity.Policy{TenantSharingEnabled: func(string) bool { return false }}

	owner := identity.NewPrincipal("t1", "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)
	peer := identity.NewPrincipal("t1", "user-b", "", identity.KindUser, identity.EntitlementRelayOnly)
	shared := identity.NodeRef{ID: "n1", TenantID: "t1", OwnerUserID: "user-a", Visibility: identity.VisibilityTenant}
	ownPrivate := identity.NodeRef{ID: "n2", TenantID: "t1", OwnerUserID: "user-a", Visibility: identity.VisibilityPrivate}

	if decision := policy.Authorize(owner, shared); !decision.Allowed {
		t.Errorf("owner refused their own shared node: %q", decision.Reason)
	}
	if decision := policy.Authorize(owner, ownPrivate); !decision.Allowed {
		t.Errorf("owner refused their own private node: %q", decision.Reason)
	}
	decision := policy.Authorize(peer, shared)
	if decision.Allowed {
		t.Fatal("peer reached a tenant-visible node while sharing is disabled")
	}
	if decision.Reason != identity.ReasonSharingDisabled {
		t.Errorf("reason = %q, want %q", decision.Reason, identity.ReasonSharingDisabled)
	}
	if !policy.TenantSharingAllowed("t1") {
		// sanity: the exported method must agree with the seam
	} else {
		t.Error("TenantSharingAllowed disagrees with Authorize")
	}
}

// visibilityCapableRegistry adds the optional nodeOwnerSetter capability that
// fakeControlRegistry deliberately lacks: admin_claim_test.go asserts the 501 path
// for a registry without it, so the visibility endpoint needs its own fixture to
// get PAST the capability check and reach the ownership rule under test.
type visibilityCapableRegistry struct {
	fakeControlRegistry
	// wrote records the last SetOwnership call, so a test can assert that a refused
	// request changed nothing.
	wrote []string
}

func (r *visibilityCapableRegistry) SetOwnership(_ context.Context, id, tenantID, ownerUserID, visibility string) (noderegistry.NodeView, error) {
	r.wrote = []string{id, tenantID, ownerUserID, visibility}
	for i, node := range r.nodes {
		if node.ID == id {
			r.nodes[i].TenantID = tenantID
			r.nodes[i].OwnerUserID = ownerUserID
			r.nodes[i].Visibility = visibility
			return r.nodes[i], nil
		}
	}
	return noderegistry.NodeView{}, fmt.Errorf("node %s not found", id)
}

// TestVisibilityEndpointRequiresOwnership: reach is not ownership. A tenant peer
// may reach a shared node, but only the owner decides how far it is exposed.
func TestVisibilityEndpointRequiresOwnership(t *testing.T) {
	registry := &visibilityCapableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{
		// Owned by user-b; the caller below is user-a of the same tenant.
		{ID: "peer-node", TenantID: "tenant-a", OwnerUserID: "user-b", Visibility: "tenant"},
	}}}
	mux := controlMuxWithPolicy(registry, identity.Policy{})

	req, err := http.NewRequest(http.MethodPost, "/control/nodes/peer-node/visibility",
		bytes.NewReader([]byte(`{"visibility":"private"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Principal", "tenant-a-user")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-owner: %s", rec.Code, rec.Body.String())
	}
	if registry.wrote != nil {
		t.Errorf("a non-owner's request still wrote visibility: %v", registry.wrote)
	}
}

// TestVisibilityEndpointRefusesTenantVisibilityWhenSharingDisabled: storing a node
// as tenant-visible while the switch says peers cannot reach it would advertise a
// node nobody can use, so the request is refused with the reason. The caller here
// IS the owner, so this exercises the sharing rule rather than the ownership one.
func TestVisibilityEndpointRefusesTenantVisibilityWhenSharingDisabled(t *testing.T) {
	registry := &visibilityCapableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{
		{ID: "own", TenantID: "tenant-a", OwnerUserID: "user-a", Visibility: "private"},
	}}}
	policy := identity.Policy{TenantSharingEnabled: func(tenantID string) bool { return tenantID != "tenant-a" }}
	mux := controlMuxWithPolicy(registry, policy)

	req, err := http.NewRequest(http.MethodPost, "/control/nodes/own/visibility",
		bytes.NewReader([]byte(`{"visibility":"tenant"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Principal", "tenant-a-user")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 while sharing is disabled: %s", rec.Code, rec.Body.String())
	}
	if registry.wrote != nil {
		t.Errorf("the refused request still wrote visibility: %v", registry.wrote)
	}
}

// TestVisibilityEndpointAllowsOwnerToGoPrivate is the positive control: without
// it the two refusals above could both pass simply because the route is broken.
func TestVisibilityEndpointAllowsOwnerToGoPrivate(t *testing.T) {
	registry := &visibilityCapableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{
		{ID: "own", TenantID: "tenant-a", OwnerUserID: "user-a", Visibility: "tenant"},
	}}}
	mux := controlMuxWithPolicy(registry, identity.Policy{})

	req, err := http.NewRequest(http.MethodPost, "/control/nodes/own/visibility",
		bytes.NewReader([]byte(`{"visibility":"private"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Principal", "tenant-a-user")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the owner: %s", rec.Code, rec.Body.String())
	}
	// The owner's tenant and owner fields must be preserved: visibility is the
	// only thing this endpoint may change.
	if got := registry.wrote; len(got) != 4 || got[3] != identity.VisibilityPrivate || got[1] != "tenant-a" || got[2] != "user-a" {
		t.Errorf("SetOwnership args = %v, want tenant/owner preserved with private visibility", registry.wrote)
	}
}
