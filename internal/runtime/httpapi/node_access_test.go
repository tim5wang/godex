package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// fakeControlRegistry is a minimal controlNodeRegistry with prepared nodes.
type fakeControlRegistry struct {
	nodes []noderegistry.NodeView
}

func (f *fakeControlRegistry) Register(context.Context, noderegistry.NodeInput) (noderegistry.NodeView, error) {
	return noderegistry.NodeView{}, nil
}
func (f *fakeControlRegistry) Heartbeat(context.Context, string, noderegistry.NodeInput) (noderegistry.NodeView, error) {
	return noderegistry.NodeView{}, nil
}
func (f *fakeControlRegistry) List(context.Context) ([]noderegistry.NodeView, error) {
	return append([]noderegistry.NodeView{}, f.nodes...), nil
}
func (f *fakeControlRegistry) Get(_ context.Context, id string) (noderegistry.NodeView, error) {
	for _, node := range f.nodes {
		if node.ID == id {
			return node, nil
		}
	}
	return noderegistry.NodeView{}, context.Canceled
}
func (f *fakeControlRegistry) SetCredentialHash(context.Context, string, string) error { return nil }
func (f *fakeControlRegistry) Delete(context.Context, string) (noderegistry.NodeView, error) {
	return noderegistry.NodeView{}, nil
}

// tenantScopedNodes mirrors the §1.3 scenario: nodes belonging to two tenants
// plus one unclaimed legacy node.
func tenantScopedNodes() []noderegistry.NodeView {
	return []noderegistry.NodeView{
		{ID: "a-own", TenantID: "tenant-a", OwnerUserID: "user-a", Visibility: "private"},
		{ID: "a-shared", TenantID: "tenant-a", OwnerUserID: "user-b", Visibility: "tenant"},
		{ID: "a-peer-private", TenantID: "tenant-a", OwnerUserID: "user-b", Visibility: "private"},
		{ID: "b-node", TenantID: "tenant-b", OwnerUserID: "user-c", Visibility: "tenant"},
	}
}

func controlMux(registry controlNodeRegistry) *http.ServeMux {
	return controlMuxWithPolicy(registry, identity.Policy{})
}

// controlMuxWithPolicy is controlMux with an explicit policy, so tests can drive
// the superadmin's tenant sharing switch through the same wiring production uses.
func controlMuxWithPolicy(registry controlNodeRegistry, policy identity.Policy) *http.ServeMux {
	mux := http.NewServeMux()
	// Identity standing in for the protected middleware: the point under test is
	// the masking, not authentication.
	attach := func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := principalFromHeader(r)
			handler.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
	registerControlNodeRoutes(mux, registry, nil, attach, attach, policy, nil)
	return mux
}

// principalFromHeader lets each test name the principal it is exercising.
func principalFromHeader(r *http.Request) identity.Principal {
	switch r.Header.Get("X-Test-Principal") {
	case "tenant-a-user":
		return identity.NewPrincipal("tenant-a", "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)
	case "tenant-b-user":
		return identity.NewPrincipal("tenant-b", "user-c", "", identity.KindUser, identity.EntitlementRelayOnly)
	case "node-actor":
		return identity.NewPrincipal("", "relay-node", "", identity.KindNode, identity.EntitlementRelayOnly)
	case "none":
		return identity.Principal{}
	default:
		return identity.NewPrincipal("", "local-operator", identity.RolePlatformAdmin, identity.KindPlatform,
			identity.EntitlementCenterExecution, identity.EntitlementRelayOnly)
	}
}

func getNodeList(t *testing.T, mux *http.ServeMux, as string) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/control/nodes", nil)
	if as != "" {
		req.Header.Set("X-Test-Principal", as)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var nodes []noderegistry.NodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

// TestNodeListMasksOtherTenants is the §1.3 regression: a tenant member must not
// be able to enumerate another tenant's nodes.
func TestNodeListMasksOtherTenants(t *testing.T) {
	mux := controlMux(&fakeControlRegistry{nodes: tenantScopedNodes()})
	got := getNodeList(t, mux, "tenant-a-user")
	want := map[string]bool{"a-own": true, "a-shared": true}
	if len(got) != len(want) {
		t.Fatalf("visible nodes = %v, want only %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("node %q leaked to tenant-a caller", id)
		}
	}
	for _, forbidden := range []string{"b-node", "a-peer-private"} {
		if listContains(got, forbidden) {
			t.Errorf("node %q must not be listed", forbidden)
		}
	}
}

// TestNodeListPreservesRegistryOrder guards the filtering from reordering the
// registry's own sort (online first, then by name).
func TestNodeListPreservesRegistryOrder(t *testing.T) {
	nodes := []noderegistry.NodeView{
		{ID: "a-own", TenantID: "tenant-a", OwnerUserID: "user-a"},
		{ID: "a-own-2", TenantID: "tenant-a", OwnerUserID: "user-a"},
	}
	got := getNodeList(t, controlMux(&fakeControlRegistry{nodes: nodes}), "tenant-a-user")
	if len(got) != 2 || got[0] != "a-own" || got[1] != "a-own-2" {
		t.Fatalf("order not preserved: %v", got)
	}
}

// TestNodeListEmptyWithoutPrincipal fails closed: an absent principal means the
// wiring changed, and must not expose the platform's node inventory.
func TestNodeListEmptyWithoutPrincipal(t *testing.T) {
	mux := controlMux(&fakeControlRegistry{nodes: tenantScopedNodes()})
	if got := getNodeList(t, mux, "none"); len(got) != 0 {
		t.Fatalf("list = %v, want empty when no principal is attached", got)
	}
}

// TestNodeListSingleTenantStillWorks is the compatibility check: with no tenant
// system in use, the operator still sees every (unclaimed) node.
func TestNodeListSingleTenantStillWorks(t *testing.T) {
	nodes := []noderegistry.NodeView{{ID: "legacy-1"}, {ID: "legacy-2"}}
	got := getNodeList(t, controlMux(&fakeControlRegistry{nodes: nodes}), "")
	if len(got) != 2 {
		t.Fatalf("operator saw %v, want both unclaimed nodes", got)
	}
}

func getNode(t *testing.T, mux *http.ServeMux, id, as string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/control/nodes/"+id, nil)
	if as != "" {
		req.Header.Set("X-Test-Principal", as)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// TestNodeGetForbidsCrossTenant pins that masking the list is not cosmetic: the
// same node must also be unreadable by id.
func TestNodeGetForbidsCrossTenant(t *testing.T) {
	mux := controlMux(&fakeControlRegistry{nodes: tenantScopedNodes()})
	if code := getNode(t, mux, "b-node", "tenant-a-user"); code != http.StatusForbidden {
		t.Fatalf("cross-tenant by id = %d, want 403", code)
	}
	if code := getNode(t, mux, "a-peer-private", "tenant-a-user"); code != http.StatusForbidden {
		t.Fatalf("peer-private by id = %d, want 403", code)
	}
	if code := getNode(t, mux, "a-shared", "tenant-a-user"); code != http.StatusOK {
		t.Fatalf("visible node by id = %d, want 200", code)
	}
	if code := getNode(t, mux, "a-own", "tenant-a-user"); code != http.StatusOK {
		t.Fatalf("own node by id = %d, want 200", code)
	}
}

// TestNodeOverviewForbidsCrossTenant covers the sibling read route: the overview
// carries the node plus its activity, so it must be gated identically.
func TestNodeOverviewForbidsCrossTenant(t *testing.T) {
	mux := controlMux(&fakeControlRegistry{nodes: tenantScopedNodes()})

	denied := httptest.NewRequest(http.MethodGet, "/control/nodes/b-node/overview", nil)
	denied.Header.Set("X-Test-Principal", "tenant-a-user")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant overview = %d, want 403", rec.Code)
	}

	allowed := httptest.NewRequest(http.MethodGet, "/control/nodes/a-shared/overview", nil)
	allowed.Header.Set("X-Test-Principal", "tenant-a-user")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, allowed)
	if rec.Code != http.StatusOK {
		t.Fatalf("permitted overview = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

func listContains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
