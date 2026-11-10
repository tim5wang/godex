package httpapi

import (
	"net/http"
	"testing"

	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// TestTenantAdminIssuesOwnDeviceCode is the point of this surface: a TENANT ADMIN
// issues an enrolment code for their own devices without ever holding the center's
// web token.
//
// Before it existed, the only issuer was /admin/tenants/{id}/join-tokens behind
// requirePlatformAdmin, so "the same user on several devices" required the operator
// to mint a code for every machine — the one thing self-service is supposed to
// remove. Note the request carries NO web token: the tenant session is the whole
// credential.
func TestTenantAdminIssuesOwnDeviceCode(t *testing.T) {
	server, service, registry := newJoinServer(t)
	adminToken, tenantID, adminUserID := registerTenantOwner(t, server, "admin@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", adminToken, map[string]any{
		"label": "my laptop",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("issue: status = %d, want 201: %v", resp.StatusCode, body)
	}
	code, _ := body["token"].(string)
	if code == "" {
		t.Fatal("no plaintext code returned; it is shown exactly once")
	}
	record, _ := body["join_token"].(map[string]any)
	if record["tenant_id"] != tenantID {
		t.Fatalf("code tenant = %v, want the issuer's own %q", record["tenant_id"], tenantID)
	}
	// The owner defaults to the CALLER. Leaving it empty would fall back to the
	// tenant's owner member at redemption, and since default visibility is private,
	// an admin who is not the owner would end up with a device they cannot see.
	if record["owner_user_id"] != adminUserID {
		t.Fatalf("code owner = %v, want the issuer %q", record["owner_user_id"], adminUserID)
	}

	// The code enrols a device, and ownership comes from the TOKEN, not the body.
	resp, body = doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": code, "node_id": "laptop-2",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("enrol: status = %d, want 201: %v", resp.StatusCode, body)
	}
	if body["tenant_id"] != tenantID || body["owner_user_id"] != adminUserID {
		t.Errorf("enrolled ownership = %v/%v, want %q/%q",
			body["tenant_id"], body["owner_user_id"], tenantID, adminUserID)
	}
	if len(registry.nodes) != 1 || registry.nodes[0].ID != "laptop-2" {
		t.Fatalf("registry nodes = %+v, want the enrolled device", registry.nodes)
	}
	// The allowance read is reported alongside the codes so the UI can warn before
	// the user clicks issue (0 = unlimited, the encoding the policy uses).
	_, listed := doJSON(t, http.MethodGet, server.URL+"/tenant/join-tokens", adminToken, nil)
	if _, ok := listed["max_nodes"]; !ok {
		t.Errorf("list response has no max_nodes: %v", listed)
	}
	if _, ok := listed["used_nodes"]; !ok {
		t.Errorf("list response has no used_nodes: %v", listed)
	}
	_ = service
}

// TestTenantPlainMemberCannotIssueCodes: issuing an enrolment code adds a machine
// to the tenant, so it belongs to an admin — a plain member must not be able to
// extend the tenant's footprint.
func TestTenantPlainMemberCannotIssueCodes(t *testing.T) {
	server, _, _ := newJoinServer(t)
	adminToken, _, _ := registerTenantOwner(t, server, "admin@example.com")
	memberToken, _ := inviteMember(t, server, adminToken, "member")

	for _, call := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"issue", http.MethodPost, "/tenant/join-tokens", map[string]any{"label": "x"}},
		{"list", http.MethodGet, "/tenant/join-tokens", nil},
	} {
		t.Run(call.name, func(t *testing.T) {
			resp, body := doJSON(t, call.method, server.URL+call.path, memberToken, call.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as member: status = %d, want 403: %v", call.name, resp.StatusCode, body)
			}
		})
	}
}

// TestTenantJoinTokenSurfaceRefusesPrincipalsWithoutATenant: the operator holds the
// web token but belongs to no tenant, and the tenant comes from the PRINCIPAL (never
// from a path or body), so there is nothing to issue for. The admin surface is the
// operator's route.
func TestTenantJoinTokenSurfaceRefusesPrincipalsWithoutATenant(t *testing.T) {
	server, _, _ := newJoinServer(t)

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", "web-secret", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("operator on the tenant surface: status = %d, want 400 (no tenant to act for): %v", resp.StatusCode, body)
	}
}

// TestTenantJoinTokenRequiresAuthentication pins that the surface did not become
// open by being moved under the tenant path.
func TestTenantJoinTokenRequiresAuthentication(t *testing.T) {
	server, _, _ := newJoinServer(t)

	resp, _ := doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", "", map[string]any{})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated issue: status = %d, want 401", resp.StatusCode)
	}
}

// TestTenantJoinTokenRevokeIsScopedToTheCallersTenant is the cross-tenant guard.
//
// RevokeJoinToken resolves a token by id ALONE and is not tenant-scoped, so without
// the membership check in front of it `/tenant/join-tokens/{A's id}` — called by
// tenant B — would silently revoke A's code. The id must be verified against the
// CALLER's list first.
func TestTenantJoinTokenRevokeIsScopedToTheCallersTenant(t *testing.T) {
	server, _, _ := newJoinServer(t)
	adminA, _, _ := registerTenantOwner(t, server, "a@example.com")
	adminB, _, _ := registerTenantOwner(t, server, "b@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", adminA, map[string]any{"label": "a-device"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("A issue: status = %d: %v", resp.StatusCode, body)
	}
	record, _ := body["join_token"].(map[string]any)
	idA, _ := record["id"].(string)
	if idA == "" {
		t.Fatalf("no code id: %v", body)
	}

	// B must not be able to revoke A's code by naming its id.
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/tenant/join-tokens/"+idA, adminB, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B revoking A's code: status = %d, want 404", resp.StatusCode)
	}
	// A's code must still redeem: the refused revoke changed nothing.
	codeA, _ := body["token"].(string)
	if codeA == "" {
		t.Fatalf("no plaintext code from A's issue: %v", body)
	}
	if resp, joinBody := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": codeA, "node_id": "a-node",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("A's code did not survive B's revoke attempt: status = %d: %v", resp.StatusCode, joinBody)
	}

	// A can revoke its own.
	resp, body = doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", adminA, map[string]any{"label": "second"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("A second issue: status = %d: %v", resp.StatusCode, body)
	}
	second, _ := body["join_token"].(map[string]any)
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/tenant/join-tokens/"+second["id"].(string), adminA, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("A revoking its own code: status = %d, want 200", resp.StatusCode)
	}
}

// TestTenantJoinTokenIssueRefusesPastAllowance: the code is ONE-TIME, so handing one
// out that cannot be redeemed burns it and leaves the user with a failure they cannot
// explain. At the allowance the issue is refused up front with the numbers.
//
// Enforcement itself stays at enrolment (RegisterOwned → checkAllowance), which is
// the only atomic place; this is the advisory pre-check.
func TestTenantJoinTokenIssueRefusesPastAllowance(t *testing.T) {
	server, service, registry := newJoinServer(t)
	adminToken, tenantID, adminUserID := registerTenantOwner(t, server, "admin@example.com")

	if _, err := service.SetTenantMaxNodes(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}
	// The tenant already holds its one node.
	registry.nodes = append(registry.nodes, noderegistry.NodeView{
		ID: "existing-device", TenantID: tenantID, OwnerUserID: adminUserID, Visibility: "private",
	})

	resp, body := doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", adminToken, map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("issue at the allowance: status = %d, want 409: %v", resp.StatusCode, body)
	}

	// Raising the allowance makes issuance work again, so the refusal tracked policy
	// rather than being a blanket block.
	if _, err := service.SetTenantMaxNodes(tenantID, 2); err != nil {
		t.Fatalf("raise allowance: %v", err)
	}
	resp, body = doJSON(t, http.MethodPost, server.URL+"/tenant/join-tokens", adminToken, map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("issue after raising the allowance: status = %d, want 201: %v", resp.StatusCode, body)
	}
}
