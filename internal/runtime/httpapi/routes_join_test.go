package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// joinTestRegistry actually registers nodes, unlike fakeControlRegistry (whose
// Register is a no-op). The join route reads back the registered node, so a
// no-op Register would make every assertion pass vacuously.
type joinTestRegistry struct {
	ownerSettableRegistry
}

func (r *joinTestRegistry) Register(_ context.Context, input noderegistry.NodeInput) (noderegistry.NodeView, error) {
	if strings.TrimSpace(input.ID) == "" {
		return noderegistry.NodeView{}, fmt.Errorf("missing node id")
	}
	node := noderegistry.NodeView{
		ID:         input.ID,
		Name:       input.Name,
		TrustLevel: input.TrustLevel,
		Status:     "online",
	}
	r.nodes = append(r.nodes, node)
	return node, nil
}

func newJoinServer(t *testing.T) (*httptest.Server, *accounts.Service, *joinTestRegistry) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry := &joinTestRegistry{}
	handler := NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         backend.NewService(cfg, nil, nil),
		Accounts:        accountsService,
		ControlRegistry: registry,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, accountsService, registry
}

func seedJoinToken(t *testing.T, service *accounts.Service, email string) (tenantID, ownerID, plaintext string) {
	t.Helper()
	result, err := service.Register(accounts.RegisterInput{Email: email, Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_, token, err := service.CreateJoinToken(accounts.CreateJoinTokenInput{
		TenantID:  result.Tenant.ID,
		CreatedBy: result.User.ID,
	})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	return result.Tenant.ID, result.User.ID, token
}

// TestJoinRouteEnrolsNodeWithTokenDerivedOwnership is the §3.5 property: a tenant
// can add a node WITHOUT the center's web token, and the resulting ownership comes
// from the join token rather than the request body.
func TestJoinRouteEnrolsNodeWithTokenDerivedOwnership(t *testing.T) {
	server, service, registry := newJoinServer(t)
	tenantID, ownerID, token := seedJoinToken(t, service, "owner@example.com")

	// No Authorization header at all: the join token IS the credential. Requiring
	// the web token here would defeat the point of the endpoint.
	resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "node-1", "name": "Workshop laptop",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, body)
	}
	if body["node_id"] != "node-1" {
		t.Errorf("node_id = %v, want node-1", body["node_id"])
	}
	credential, _ := body["credential"].(string)
	if !strings.HasPrefix(credential, "ck_") {
		t.Errorf("credential = %q, want a ck_ per-node credential", credential)
	}
	proxyToken, _ := body["node_proxy_token"].(string)
	if !strings.HasPrefix(proxyToken, "nk_") {
		t.Errorf("node_proxy_token = %q, want an nk_ restricted credential", proxyToken)
	}
	if body["tenant_id"] != tenantID {
		t.Errorf("tenant_id = %v, want %q (from the token, not the body)", body["tenant_id"], tenantID)
	}
	if body["owner_user_id"] != ownerID {
		t.Errorf("owner_user_id = %v, want %q (token creator fallback)", body["owner_user_id"], ownerID)
	}
	if body["visibility"] != "private" {
		t.Errorf("visibility = %v, want private (decision ⑥ default)", body["visibility"])
	}

	// The registry must actually carry the ownership, not just the response.
	if len(registry.nodes) != 1 {
		t.Fatalf("registry holds %d nodes, want 1", len(registry.nodes))
	}
	stored := registry.nodes[0]
	if stored.TenantID != tenantID || stored.OwnerUserID != ownerID {
		t.Errorf("stored ownership = tenant %q owner %q, want %q/%q", stored.TenantID, stored.OwnerUserID, tenantID, ownerID)
	}
}

func TestJoinRouteRejectsReusedToken(t *testing.T) {
	server, service, _ := newJoinServer(t)
	_, _, token := seedJoinToken(t, service, "owner@example.com")

	if resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "node-1",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first join status = %d, want 201: %v", resp.StatusCode, body)
	}
	// A second device presenting the same code must be refused.
	resp, _ := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "node-2",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused token status = %d, want 401", resp.StatusCode)
	}
}

func TestJoinRouteRejectsUnknownToken(t *testing.T) {
	server, _, _ := newJoinServer(t)
	resp, _ := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": accounts.JoinTokenPrefix + "deadbeef", "node_id": "node-1",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unknown token", resp.StatusCode)
	}
}

func TestJoinRouteRequiresNodeID(t *testing.T) {
	server, service, _ := newJoinServer(t)
	_, _, token := seedJoinToken(t, service, "owner@example.com")
	resp, _ := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{"token": token})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without node_id", resp.StatusCode)
	}
}

// TestJoinRouteRefusesExistingNodeWithoutBurningToken is the takeover guard.
//
// A join token names a tenant. If it could reassign an ALREADY registered node,
// then tenant A holding a code could claim tenant B's machine. The check must run
// before the token is consumed, so the legitimate use of that code still works.
func TestJoinRouteRefusesExistingNodeWithoutBurningToken(t *testing.T) {
	server, service, registry := newJoinServer(t)
	tenantID, ownerID, token := seedJoinToken(t, service, "owner@example.com")

	// A node already on the center, owned by someone else.
	registry.nodes = append(registry.nodes, noderegistry.NodeView{
		ID: "existing-node", TenantID: "tnt_other", OwnerUserID: "usr_other", Visibility: "private",
	})

	resp, _ := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "existing-node",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for an already-registered node", resp.StatusCode)
	}
	// The existing ownership must be untouched.
	for _, node := range registry.nodes {
		if node.ID == "existing-node" && node.TenantID != "tnt_other" {
			t.Fatalf("existing node ownership was reassigned to %q", node.TenantID)
		}
	}

	// The token must NOT have been burned by the refusal: it still enrols a
	// genuinely new node.
	resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "fresh-node",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("token was consumed by the refused attempt: status = %d, %v", resp.StatusCode, body)
	}
	if body["tenant_id"] != tenantID || body["owner_user_id"] != ownerID {
		t.Errorf("fresh enrolment ownership = %v/%v, want %q/%q", body["tenant_id"], body["owner_user_id"], tenantID, ownerID)
	}
}

// TestJoinRouteOwnerFallsBackToTenantOwnerNotCreator is the fix for a real defect
// found by end-to-end testing.
//
// When a platform OPERATOR issues the code (the normal case for the admin UI),
// CreatedBy is the operator — who is not a member of that tenant. Using the creator
// as the owner therefore produced a node owned by a user the tenant cannot reach
// and, being private by default, usable by NOBODY. The fallback must be the
// tenant's owner member.
func TestJoinRouteOwnerFallsBackToTenantOwnerNotCreator(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry := &joinTestRegistry{}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         backend.NewService(cfg, nil, nil),
		Accounts:        accountsService,
		ControlRegistry: registry,
	}))
	t.Cleanup(server.Close)

	// Seed the tenant directly, then issue the token AS THE OPERATOR (CreatedBy is
	// deliberately the platform operator id, mirroring the admin route).
	result, err := accountsService.Register(accounts.RegisterInput{Email: "member@example.com", Password: "correct-horse"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_, token, err := accountsService.CreateJoinToken(accounts.CreateJoinTokenInput{
		TenantID:  result.Tenant.ID,
		CreatedBy: "local-operator",
	})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "node-1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, body)
	}
	if body["owner_user_id"] != result.User.ID {
		t.Fatalf("owner_user_id = %v, want the tenant's owner member %q (not the creator)",
			body["owner_user_id"], result.User.ID)
	}
	if body["visibility"] != "private" {
		t.Errorf("visibility = %v, want private when a real owner exists", body["visibility"])
	}
	// And the stored node must carry that owner, so the tenant can actually reach it.
	if len(registry.nodes) != 1 || registry.nodes[0].OwnerUserID != result.User.ID {
		t.Errorf("stored node = %+v, want owner %q", registry.nodes, result.User.ID)
	}
}

// TestJoinRouteRejectsOwnerOutsideTenant: an owner that is not a member would make
// the node unreachable (private by default, owned by a user the tenant cannot
// reach), so it is refused — and refused at ISSUANCE, so the operator learns
// immediately instead of burning a one-time code at redemption.
func TestJoinRouteRejectsOwnerOutsideTenant(t *testing.T) {
	_, service, _ := newJoinServer(t)
	tenantID, ownerID, _ := seedJoinToken(t, service, "member@example.com")

	if _, _, err := service.CreateJoinToken(accounts.CreateJoinTokenInput{
		TenantID: tenantID, OwnerUserID: "usr_not_a_member",
	}); err == nil {
		t.Fatal("CreateJoinToken accepted an owner outside the tenant")
	}

	// Positive control: the tenant's real owner IS accepted, so the rejection
	// above is about membership rather than "every owner is refused".
	record, token, err := service.CreateJoinToken(accounts.CreateJoinTokenInput{
		TenantID: tenantID, OwnerUserID: ownerID,
	})
	if err != nil {
		t.Fatalf("CreateJoinToken rejected a real member: %v", err)
	}
	if record.OwnerUserID != ownerID {
		t.Errorf("recorded owner = %q, want %q", record.OwnerUserID, ownerID)
	}
	if !strings.HasPrefix(token, accounts.JoinTokenPrefix) {
		t.Errorf("token = %q, missing prefix", token)
	}
}

// TestJoinRouteUnavailableWithoutAccountStore: an instance with no account store
// must answer 503 rather than silently enrolling an ownerless node.
func TestJoinRouteUnavailableWithoutAccountStore(t *testing.T) {
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	handler := NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: backend.NewService(cfg, nil, nil),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, _ := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": "gjt_whatever", "node_id": "node-1",
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 without an account store", resp.StatusCode)
	}
}
