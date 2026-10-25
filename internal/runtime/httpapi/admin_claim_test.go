package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// ownerSettableRegistry adds the optional nodeOwnerSetter capability to the
// fake, mirroring how the real registry satisfies it.
type ownerSettableRegistry struct {
	fakeControlRegistry
	setCalls []string
}

func (f *ownerSettableRegistry) SetOwnership(_ context.Context, id, tenantID, ownerUserID, visibility string) (noderegistry.NodeView, error) {
	f.setCalls = append(f.setCalls, id+"|"+tenantID+"|"+ownerUserID+"|"+visibility)
	for i, node := range f.nodes {
		if node.ID == id {
			node.TenantID = tenantID
			node.OwnerUserID = ownerUserID
			if visibility != "" {
				node.Visibility = noderegistry.NormalizeVisibility(visibility)
			}
			f.nodes[i] = node
			return node, nil
		}
	}
	return noderegistry.NodeView{}, context.Canceled
}

func claimRequest(id, body, as string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/nodes/"+id+"/claim", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if as != "" {
		req.Header.Set("X-Test-Principal", as)
	}
	return req
}

// TestAdminClaimAssignsOwnership is the point of the route: it is the only
// writer for TenantID/OwnerUserID, so without it the ownership fields would stay
// empty forever and visibility could never be changed off its default.
func TestAdminClaimAssignsOwnership(t *testing.T) {
	registry := &ownerSettableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{{ID: "node-1"}}}}
	mux := controlMux(registry)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimRequest("node-1", `{"tenant_id":"tenant-a","owner_user_id":"user-a","visibility":"tenant"}`, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(registry.setCalls) != 1 || registry.setCalls[0] != "node-1|tenant-a|user-a|tenant" {
		t.Fatalf("SetOwnership calls = %v", registry.setCalls)
	}
	var node noderegistry.NodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if node.TenantID != "tenant-a" || node.OwnerUserID != "user-a" || node.Visibility != "tenant" {
		t.Fatalf("claimed node = %#v", node)
	}
}

// TestAdminClaimRequiresPlatformAdmin pins that a tenant user cannot reassign
// node ownership — that would be a way to steal another tenant's node.
func TestAdminClaimRequiresPlatformAdmin(t *testing.T) {
	registry := &ownerSettableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{{ID: "node-1"}}}}
	mux := controlMux(registry)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimRequest("node-1", `{"tenant_id":"tenant-a","owner_user_id":"user-a"}`, "tenant-a-user"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(registry.setCalls) != 0 {
		t.Fatalf("ownership was changed by a non-admin: %v", registry.setCalls)
	}
}

// TestAdminClaimRefusesNodeActor is the reason this route uses
// requirePlatformAdmin rather than requireCenterExecution: the latter exempts
// node actors (so the center can drive remote execution), and reusing it here
// would let a joined node claim other nodes.
func TestAdminClaimRefusesNodeActor(t *testing.T) {
	registry := &ownerSettableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{{ID: "node-1"}}}}
	mux := controlMux(registry)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimRequest("node-1", `{"tenant_id":"tenant-a","owner_user_id":"user-a"}`, "node-actor"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a node actor", rec.Code)
	}
	if len(registry.setCalls) != 0 {
		t.Fatalf("a node actor changed ownership: %v", registry.setCalls)
	}
}

// TestAdminClaimRequiresTenantAndOwner: a half-specified claim would create a
// node nobody can reach (the policy compares tenants first), so reject it up
// front instead of persisting an unreachable ownership row.
func TestAdminClaimRequiresTenantAndOwner(t *testing.T) {
	registry := &ownerSettableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{{ID: "node-1"}}}}
	mux := controlMux(registry)

	for _, body := range []string{
		`{"owner_user_id":"user-a"}`,
		`{"tenant_id":"tenant-a"}`,
		`{"tenant_id":" ","owner_user_id":"user-a"}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, claimRequest("node-1", body, ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s -> status %d, want 400", body, rec.Code)
		}
	}
	if len(registry.setCalls) != 0 {
		t.Fatalf("incomplete claims were persisted: %v", registry.setCalls)
	}
}

// TestAdminClaimUnknownNode: an unknown id must 404 rather than invent a node.
func TestAdminClaimUnknownNode(t *testing.T) {
	registry := &ownerSettableRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{{ID: "node-1"}}}}
	mux := controlMux(registry)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimRequest("ghost", `{"tenant_id":"tenant-a","owner_user_id":"user-a"}`, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestAdminClaimUnsupportedRegistry: a registry without the optional capability
// must answer 501 rather than panic on a nil type assertion.
func TestAdminClaimUnsupportedRegistry(t *testing.T) {
	mux := controlMux(&fakeControlRegistry{nodes: []noderegistry.NodeView{{ID: "node-1"}}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimRequest("node-1", `{"tenant_id":"tenant-a","owner_user_id":"user-a"}`, ""))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

// TestRequirePlatformAdminFailsClosedWithoutPrincipal mirrors the
// center-execution gate: a missing principal means the route is not behind
// authentication, which must refuse rather than assume an operator.
func TestRequirePlatformAdminFailsClosedWithoutPrincipal(t *testing.T) {
	called := false
	handler := requirePlatformAdmin(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/nodes/x/claim", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler ran without an authenticated principal")
	}
}

// TestRequirePlatformAdminRefusesTenantAdmin: a tenant admin is privileged in
// their tenant but must not administer the platform.
func TestRequirePlatformAdminRefusesTenantAdmin(t *testing.T) {
	handler := requirePlatformAdmin(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}))
	rec := serveWithPrincipal(identity.NewPrincipal("tenant-1", "admin-a", "", identity.KindUser, identity.EntitlementRelayOnly), handler)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
