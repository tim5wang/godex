package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// newWatchTenantNodeServer wires the watch surface with a configurable instance tenant.
//
// control.tenant_id is what a node records when it enrols into a tenant (see
// routes_self_join.go); empty means this instance IS the operator's own machine, the
// center. That single value is what the tenant comparison turns on, so tests set it
// rather than standing up a whole second deployment.
func newWatchTenantNodeServer(t *testing.T) (*httptest.Server, *watchauth.Service, *noderegistry.Registry, *accounts.Service, *config.Manager) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	auth, err := watchauth.New(filepath.Join(cfg.StateDir, "watch", "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         service,
		WatchAuth:       auth,
		Accounts:        accountsService,
		ControlRegistry: registry,
	}))
	t.Cleanup(server.Close)
	return server, auth, registry, accountsService, manager
}

// enrollInstanceInto points this instance at a tenant, i.e. "this node joined that
// tenant". nil/"" makes it the center again.
func enrollInstanceInto(t *testing.T, manager *config.Manager, tenantID string) {
	t.Helper()
	if _, err := manager.Update(context.Background(), config.UpdateRequest{
		Values: map[string]any{"control.tenant_id": tenantID},
	}); err != nil {
		t.Fatalf("set control.tenant_id: %v", err)
	}
}

// TestTenantWatchIsRefusedTheCentersOwnWorkspace is the residual this closes: the read
// bodies answer for THIS instance's own workspace — the operator's conversations — and a
// wrist belonging to a customer must not read it, however the request is authenticated.
// The binding is on the device, so the comparison is the device's tenant against the
// instance's own (empty = the center).
func TestTenantWatchIsRefusedTheCentersOwnWorkspace(t *testing.T) {
	server, _, _, accountsService, _ := newWatchTenantNodeServer(t)
	token, tenantID, _ := registerTenantOwner(t, server, "owner@example.com")
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}

	_, credential := pairWatchAs(t, server, token)
	for _, path := range []string{
		"/watch/sessions",
		"/watch/nodes",
		"/watch/overview",
		"/watch/approvals",
		"/watch/cross-device",
		"/watch/templates",
		"/watch/push/pending",
	} {
		resp, body := getWatch(t, server.URL+path, credential)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as a tenant's watch: status=%d body=%s, want 403", path, resp.StatusCode, body)
		}
	}
}

// TestTenantWatchIsServedByItsOwnTenantsNode is the other half, and the reason the rule is
// a comparison rather than a blanket refusal: a node enrolled into tenant T serves tenant
// T's wrists, because that node's workspace IS that tenant's data — the whole product.
// Wrapping this surface in requireCenterLocalData would have refused it.
func TestTenantWatchIsServedByItsOwnTenantsNode(t *testing.T) {
	server, _, _, accountsService, manager := newWatchTenantNodeServer(t)
	token, tenantID, _ := registerTenantOwner(t, server, "owner@example.com")
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}

	_, credential := pairWatchAs(t, server, token)
	// As the center, the same wrist is refused...
	if resp, body := getWatch(t, server.URL+"/watch/sessions", credential); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the center should refuse a tenant's watch: status=%d body=%s", resp.StatusCode, body)
	}
	// ...and once this instance IS that tenant's node, it is served.
	enrollInstanceInto(t, manager, tenantID)
	if resp, body := getWatch(t, server.URL+"/watch/sessions", credential); resp.StatusCode != http.StatusOK {
		t.Fatalf("a tenant's own node refused its own wrist: status=%d body=%s", resp.StatusCode, body)
	}
}

// TestOperatorOwnWatchIsUnaffected: a wrist with NO tenant is the operator's own device,
// and the center is the operator's own machine — so the previous behaviour must hold.
// Without this the tenant check would have become a blanket refusal of every wrist.
func TestOperatorOwnWatchIsUnaffected(t *testing.T) {
	server, _, _, _, _ := newWatchTenantNodeServer(t)
	// Approving with the web token classifies as the operator, which records no tenant.
	device, credential := pairWatchAs(t, server, "web-secret")
	if device.TenantID != "" {
		t.Fatalf("operator-paired device has tenant %q, want none", device.TenantID)
	}
	if resp, body := getWatch(t, server.URL+"/watch/sessions", credential); resp.StatusCode != http.StatusOK {
		t.Fatalf("the operator's own wrist was refused: status=%d body=%s", resp.StatusCode, body)
	}
}

// TestTenantWatchCannotReachAnotherTenantsNode is the forwarding half.
//
// The target node has never seen the wrist, so the only credentials available on the hop
// are the ORIGIN node's own — which is why a forwarded wrist request used to arrive as the
// operator and be admitted anywhere. The authority has to be established where the device
// IS known, so it is asserted here, at the origin.
func TestTenantWatchCannotReachAnotherTenantsNode(t *testing.T) {
	server, _, registry, accountsService, _ := newWatchTenantNodeServer(t)
	token, tenantID, userID := registerTenantOwner(t, server, "owner@example.com")
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}
	_, credential := pairWatchAs(t, server, token)

	ctx := context.Background()
	seed := func(id, tenant string) {
		t.Helper()
		if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if _, err := registry.SetOwnership(ctx, id, tenant, userID, "private"); err != nil {
			t.Fatalf("own %s: %v", id, err)
		}
	}
	seed("node-mine", tenantID)
	seed("node-theirs", "tnt_someone_else")
	// An unowned node is nobody's to be reached by a customer.
	if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: "node-unowned"}); err != nil {
		t.Fatalf("register unowned: %v", err)
	}

	resp, body := getWatch(t, server.URL+"/watch/sessions?node_id=node-theirs", credential)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("reaching another tenant's node: status=%d body=%s, want 403", resp.StatusCode, body)
	}
	resp, body = getWatch(t, server.URL+"/watch/sessions?node_id=node-unowned", credential)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("reaching an unowned node: status=%d body=%s, want 403", resp.StatusCode, body)
	}
	// Naming its OWN tenant's node passes the ownership check and is forwarded; the result
	// is whatever the transport says (this fixture wires no relay, so 503) — the point is
	// that it is NOT the 403 the two above got, so those were about ownership rather than
	// about forwarding being impossible at all.
	resp, body = getWatch(t, server.URL+"/watch/sessions?node_id=node-mine", credential)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("its own tenant's node was refused as foreign: status=%d body=%s", resp.StatusCode, body)
	}
}

// TestOwnedDeviceCarriesTheApprovingPrincipal: the device's tenant and owner come from the
// approving PRINCIPAL, never from the request body. A pairing that could name its own owner
// would let one member enrol a device into someone else's allowance — and, with the tenant
// comparison above, into someone else's workspace.
func TestOwnedDeviceCarriesTheApprovingPrincipal(t *testing.T) {
	server, _, _, accountsService, _ := newWatchTenantNodeServer(t)
	token, tenantID, userID := registerTenantOwner(t, server, "owner@example.com")
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}

	userCode, _ := startPairing(t, server, "Wrist")
	// A body naming a different owner/tenant must not be honoured.
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code":     userCode,
		"owner_user_id": "usr_somebody_else",
		"tenant_id":     "tnt_somebody_else",
	}, token)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/approve status=%d body=%s", resp.StatusCode, body)
	}
	var device watchauth.Device
	if err := json.Unmarshal(body, &device); err != nil {
		t.Fatalf("decode device: %v (%s)", err, body)
	}
	if device.OwnerUserID != userID || device.TenantID != tenantID {
		t.Fatalf("device = (owner %q, tenant %q), want the approver's (%q, %q)",
			device.OwnerUserID, device.TenantID, userID, tenantID)
	}
}
