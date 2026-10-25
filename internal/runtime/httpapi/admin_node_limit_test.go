package httpapi

import (
	"net/http"
	"testing"
)

// TestAdminMaxNodesRequiresExplicitValue: 0 means unlimited, so an omitted field
// must NOT be read as 0 — that would silently lift a limit the operator was trying
// to enforce. Same reasoning as the sharing switch's explicit false.
func TestAdminMaxNodesRequiresExplicitValue(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	if resp, _ := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatal("seed register failed")
	}

	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-nodes", "web-secret", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when max_nodes is omitted", resp.StatusCode)
	}
}

// TestAdminMaxNodesRejectsNegative: a negative allowance has no meaning, and
// silently clamping it to unlimited would turn a typo into a policy change.
func TestAdminMaxNodesRejectsNegative(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-nodes", "web-secret",
		map[string]any{"max_nodes": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a negative allowance", resp.StatusCode)
	}
}

func TestAdminMaxNodesPersists(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-nodes",
		"web-secret", map[string]any{"max_nodes": 6})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["max_nodes"] != float64(6) {
		t.Fatalf("response = %v, want max_nodes 6", body)
	}
	// The stored policy must be what the enrolment seam reads.
	if got := accountsService.TenantMaxNodes(tenantID); got != 6 {
		t.Errorf("TenantMaxNodes = %d, want 6", got)
	}

	// The tenant list carries it too, so the console renders from one source.
	_, listed := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := listed["tenants"].([]any)
	row, _ := tenants[0].(map[string]any)
	if row["max_nodes"] != float64(6) {
		t.Errorf("list shows max_nodes = %v, want 6", row["max_nodes"])
	}
	// And the node count is reported alongside it (0 here: no node is enrolled).
	if row["node_count"] != float64(0) {
		t.Errorf("list shows node_count = %v, want 0", row["node_count"])
	}
}

// TestAdminMaxNodesDoesNotClobberSharing: the two policies are separate columns and
// separate endpoints, so an operator changing one must not revert the other. Each
// direction is asserted, because a single-column UPDATE that forgets the other
// column shows up differently depending on which write lands second.
func TestAdminMaxNodesDoesNotClobberSharing(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/sharing",
		"web-secret", map[string]any{"enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("sharing off: %d %v", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-nodes",
		"web-secret", map[string]any{"max_nodes": 2}); resp.StatusCode != http.StatusOK {
		t.Fatalf("max nodes: %d %v", resp.StatusCode, body)
	}
	if accountsService.TenantSharingEnabled(tenantID) {
		t.Error("setting the allowance re-enabled sharing")
	}

	// Reverse: toggling sharing must leave the allowance in place.
	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/sharing",
		"web-secret", map[string]any{"enabled": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("sharing on: %d %v", resp.StatusCode, body)
	}
	if got := accountsService.TenantMaxNodes(tenantID); got != 2 {
		t.Errorf("sharing toggle changed the allowance to %d, want 2", got)
	}
}

// TestAdminMaxNodesRequiresPlatformAdmin: the allowance is a commercial control, so
// a tenant must not be able to raise its own limit.
func TestAdminMaxNodesRequiresPlatformAdmin(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-nodes", "", map[string]any{"max_nodes": 99})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without an operator credential", resp.StatusCode)
	}
}
