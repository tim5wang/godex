package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// deleteRecordingRegistry records Delete calls, so a test can assert that a refused
// request changed nothing (the same shape visibilityCapableRegistry uses for
// SetOwnership).
type deleteRecordingRegistry struct {
	fakeControlRegistry
	deleted string
}

func (r *deleteRecordingRegistry) Delete(_ context.Context, id string) (noderegistry.NodeView, error) {
	r.deleted = id
	return noderegistry.NodeView{}, nil
}

// newNodeAdminTestServer wires a control registry holding one node, so the
// node-management routes actually register and resolve.
//
// The registry matters: an earlier sweep reported 404 for these routes because the
// fixture had no registry. A 404 from a route whose dependency was never wired is
// NOT evidence about its gate — and reading it as "nothing to see" is what let the
// holes below stay hidden through one round of review.
func newNodeAdminTestServer(t *testing.T, webToken string) (*httptest.Server, *deleteRecordingRegistry) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry := &deleteRecordingRegistry{fakeControlRegistry: fakeControlRegistry{nodes: []noderegistry.NodeView{
		{ID: "node-owned-by-op", TenantID: "tenant-op", OwnerUserID: "local-operator", Visibility: "private"},
	}}}
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         service,
		Accounts:        accountsService,
		ControlRegistry: registry,
	}))
	t.Cleanup(server.Close)
	return server, registry
}

// nodeAdminWriteCalls are the registry's WRITE routes: they enrol machines, mint
// their credentials and remove nodes.
//
// Measured 2026-11-10 with a TENANT MEMBER's session, before the gate:
//
//	POST   /control/nodes/register         -> 200 (registered a node)
//	POST   /control/nodes/{id}/credential   -> 200, returned a fresh ck_ credential
//	DELETE /control/nodes/{id}              -> 200, removed the operator's node
//	POST   /control/nodes/{id}/heartbeat    -> 200
//	POST   /control/node-proxy-token        -> 200, returned an nk_ proxy credential
//	POST   /control/self/join               -> 502 from the outbound hop it attempted,
//	                                            i.e. the handler already ran, and it
//	                                            rewrites THIS instance's own identity
//	                                            (control.credential / center_token /
//	                                            center_url / node.json)
//
// The READ routes (GET /control/nodes, /control/nodes/{id}, .../overview) are
// deliberately NOT here: they accept a login session BY DESIGN (TestNodeProxyRead
// AcceptsSessionWithoutEscalation pins it) because the Web UI's node pages use one,
// with tenancy enforced by masking inside the handler. "The node surface takes a
// session" is true of reads and false of writes.
func nodeAdminWriteCalls() []struct {
	name   string
	method string
	path   string
	body   any
} {
	return []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"register a node", http.MethodPost, "/control/nodes/register", map[string]any{"id": "tenant-node"}},
		{"heartbeat a node", http.MethodPost, "/control/nodes/node-owned-by-op/heartbeat", map[string]any{}},
		{"issue a node credential", http.MethodPost, "/control/nodes/node-owned-by-op/credential", nil},
		{"issue a node proxy token", http.MethodPost, "/control/node-proxy-token", map[string]any{}},
		{"issue a per-node proxy token", http.MethodPost, "/control/node-proxy-token", map[string]any{"node_id": "node-owned-by-op"}},
		{"delete a node", http.MethodDelete, "/control/nodes/node-owned-by-op", nil},
		// The outbound hop fails in this fixture (502), which is fine: what is
		// asserted is that the GATE answers first.
		{"make this instance join a center", http.MethodPost, "/control/self/join", map[string]any{
			"center_url": "http://127.0.0.1:1", "token": "gjt_probe", "node_id": "probe-node",
		}},
	}
}

// TestTenantUserIsRefusedTheNodeAdminWriteSurfaces is the regression for the holes
// above. A tenant member could enrol machines into the platform registry, mint a
// ck_ credential for the operator's node (i.e. hand themselves a machine's identity),
// obtain an nk_ proxy credential, repoint the instance at a center of their choosing,
// and delete nodes.
func TestTenantUserIsRefusedTheNodeAdminWriteSurfaces(t *testing.T) {
	server, _ := newNodeAdminTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	for _, tc := range nodeAdminWriteCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, tenantToken, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as tenant: status = %d, want 403: %v", tc.name, resp.StatusCode, body)
			}
		})
	}
}

// TestTenantCannotDestroyTheOperatorsNode asserts the OUTCOME, not just the status:
// the delete really removed the node before the gate, so a 403 alone would not prove
// the registry was left alone.
func TestTenantCannotDestroyTheOperatorsNode(t *testing.T) {
	server, registry := newNodeAdminTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")
	registry.deleted = ""

	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/control/nodes/node-owned-by-op", tenantToken, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant delete: status = %d, want 403", resp.StatusCode)
	}
	if registry.deleted != "" {
		t.Fatalf("the registry was asked to delete %q despite the refusal", registry.deleted)
	}
}

// TestOperatorStillReachesTheNodeAdminWriteSurfaces: not 403 for the operator — a
// specific code would be registry/handler lifecycle semantics.
func TestOperatorStillReachesTheNodeAdminWriteSurfaces(t *testing.T) {
	server, _ := newNodeAdminTestServer(t, "web-secret")

	for _, tc := range nodeAdminWriteCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, "web-secret", tc.body)
			if resp.StatusCode == http.StatusForbidden {
				t.Fatalf("operator refused on %s: %v", tc.name, body)
			}
		})
	}
}

// TestUnauthenticatedCallerIsRefusedTheNodeAdminWriteSurfaces: the gate keeps the
// authentication requirement and fails closed (401, not 403).
func TestUnauthenticatedCallerIsRefusedTheNodeAdminWriteSurfaces(t *testing.T) {
	server, _ := newNodeAdminTestServer(t, "web-secret")

	resp, _ := doJSON(t, http.MethodPost, server.URL+"/control/nodes/register",
		"", map[string]any{"id": "anon-node"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated register: status = %d, want 401", resp.StatusCode)
	}
}
