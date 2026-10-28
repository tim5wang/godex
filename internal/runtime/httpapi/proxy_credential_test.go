package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// proxyIssuingRegistry adds per-node nk_ storage to the join fixture, mirroring
// what noderegistry.Registry provides in production. Built as a separate type so
// the existing fixtures stay untouched — which also keeps one fixture exercising
// the legacy fallback below.
type proxyIssuingRegistry struct {
	joinTestRegistry
	proxyHashes map[string]string
	credHashes  map[string]string
}

func (r *proxyIssuingRegistry) SetProxyTokenHash(_ context.Context, id, hash string) error {
	if r.proxyHashes == nil {
		r.proxyHashes = map[string]string{}
	}
	r.proxyHashes[id] = hash
	return nil
}

func (r *proxyIssuingRegistry) NodeByProxyTokenHash(hash string) (noderegistry.NodeView, bool) {
	return r.resolve(r.proxyHashes, hash)
}

func (r *proxyIssuingRegistry) NodeByCredentialHash(hash string) (noderegistry.NodeView, bool) {
	return r.resolve(r.credHashes, hash)
}

func (r *proxyIssuingRegistry) resolve(hashes map[string]string, hash string) (noderegistry.NodeView, bool) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return noderegistry.NodeView{}, false
	}
	for id, h := range hashes {
		if h != "" && h == hash {
			for _, node := range r.nodes {
				if node.ID == id {
					return node, true
				}
			}
			return noderegistry.NodeView{ID: id}, true
		}
	}
	return noderegistry.NodeView{}, false
}

func newProxyIssuingServer(t *testing.T) (*httptest.Server, *accounts.Service, *proxyIssuingRegistry) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry := &proxyIssuingRegistry{}
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

// TestJoinRouteIssuesPerNodeTokenThatResolves is acceptance V1 on the enrolment
// path: the token handed to a newly joined node must resolve back to THAT node,
// with its tenant — which is what makes the node's identity real instead of the
// anonymous placeholder.
func TestJoinRouteIssuesPerNodeTokenThatResolves(t *testing.T) {
	server, service, registry := newProxyIssuingServer(t)
	tenantID, _, token := seedJoinToken(t, service, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "node-a", "name": "Workshop laptop",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, body)
	}
	proxyToken, _ := body["node_proxy_token"].(string)
	if !strings.HasPrefix(proxyToken, "nk_") {
		t.Fatalf("node_proxy_token = %q, want an nk_ credential", proxyToken)
	}

	node, ok := registry.NodeByProxyTokenHash(relay.HashCredential(proxyToken))
	if !ok {
		t.Fatal("the issued nk_ must resolve back to its node, not to nothing")
	}
	if node.ID != "node-a" {
		t.Errorf("resolved id = %q, want node-a", node.ID)
	}
	if node.TenantID != tenantID {
		t.Errorf("resolved tenant = %q, want %q", node.TenantID, tenantID)
	}
}

// TestJoinRouteFallsBackToLegacyTokenWhenRegistryCannotStorePerNode keeps the
// dual-accept promise honest on the issuance side: a deployment whose registry
// cannot keep per-node digests must still enrol nodes, using the center-wide
// value. Without this, "no interruption" would only be true for new enrollments.
func TestJoinRouteFallsBackToLegacyTokenWhenRegistryCannotStorePerNode(t *testing.T) {
	// newJoinServer's fixture deliberately does NOT implement the setter.
	server, service, _ := newJoinServer(t)
	_, _, token := seedJoinToken(t, service, "owner@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/control/join", "", map[string]any{
		"token": token, "node_id": "node-1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, body)
	}
	if proxyToken, _ := body["node_proxy_token"].(string); !strings.HasPrefix(proxyToken, "nk_") {
		t.Errorf("node_proxy_token = %q, want a legacy nk_ credential", proxyToken)
	}
}

// TestNodeProxyTokenEndpointIssuesPerNodeForNamedNode covers the exchange
// endpoint both ways: naming a node yields a per-node token, omitting the id
// yields the legacy value (what an older node asks for).
func TestNodeProxyTokenEndpointIssuesPerNodeForNamedNode(t *testing.T) {
	server, _, registry := newProxyIssuingServer(t)
	if _, err := registry.Register(context.Background(), noderegistry.NodeInput{ID: "node-a"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/control/node-proxy-token", "web-secret",
		map[string]any{"node_id": "node-a"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	perNode, _ := body["node_proxy_token"].(string)
	if node, ok := registry.NodeByProxyTokenHash(relay.HashCredential(perNode)); !ok || node.ID != "node-a" {
		t.Fatalf("named-node token resolved to %#v ok=%v, want node-a", node, ok)
	}

	// No node id: the legacy center-wide value, still accepted for old nodes.
	resp, body = doJSON(t, http.MethodPost, server.URL+"/control/node-proxy-token", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	legacy, _ := body["node_proxy_token"].(string)
	if legacy == "" {
		t.Fatal("expected a legacy token when no node id is given")
	}
	if _, ok := registry.NodeByProxyTokenHash(relay.HashCredential(legacy)); ok {
		t.Error("the legacy token must not resolve to a node")
	}

	// An unknown node id is a 404, not a silently minted credential.
	resp, _ = doJSON(t, http.MethodPost, server.URL+"/control/node-proxy-token", "web-secret",
		map[string]any{"node_id": "ghost"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status for unknown node = %d, want 404", resp.StatusCode)
	}
}

// rotationRequest posts to the node-authenticated rotation endpoint with the
// given credential as the Bearer value.
func rotationRequest(t *testing.T, server *httptest.Server, credential string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/control/self/proxy-credential", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestSelfProxyCredentialRotationIsScopedToTheCallingNode is acceptance V5, and
// the mechanism that removes per-machine manual work from the migration: a node
// rotates ITS OWN credential using the ck_ it already holds, and nobody else's
// digest changes.
func TestSelfProxyCredentialRotationIsScopedToTheCallingNode(t *testing.T) {
	server, _, registry := newProxyIssuingServer(t)
	ctx := context.Background()

	for _, id := range []string{"node-a", "node-b"} {
		if _, err := registry.Register(ctx, noderegistry.NodeInput{ID: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	// Give both nodes ownership so the resolved view carries a tenant.
	if _, err := registry.SetOwnership(ctx, "node-a", "tenant-1", "", "private"); err != nil {
		t.Fatalf("own node-a: %v", err)
	}
	ckA, ckB := "ck_node_a", "ck_node_b"
	registry.credHashes = map[string]string{
		"node-a": relay.HashCredential(ckA),
		"node-b": relay.HashCredential(ckB),
	}
	if err := registry.SetProxyTokenHash(ctx, "node-b", "pre-existing-b"); err != nil {
		t.Fatalf("seed node-b token: %v", err)
	}

	resp := rotationRequest(t, server, ckA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		NodeID         string `json:"node_id"`
		NodeProxyToken string `json:"node_proxy_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.NodeID != "node-a" {
		t.Fatalf("rotated node = %q, want node-a (the ck_ holder, not a body-supplied id)", out.NodeID)
	}
	if !strings.HasPrefix(out.NodeProxyToken, "nk_") {
		t.Fatalf("issued token = %q, want an nk_ credential", out.NodeProxyToken)
	}
	node, ok := registry.NodeByProxyTokenHash(relay.HashCredential(out.NodeProxyToken))
	if !ok || node.ID != "node-a" {
		t.Fatalf("new token resolved to %#v ok=%v, want node-a", node, ok)
	}
	if node.TenantID != "tenant-1" {
		t.Errorf("resolved tenant = %q, want tenant-1", node.TenantID)
	}

	// The peer is untouched: rotation is per node, not global.
	peer, ok := registry.NodeByProxyTokenHash("pre-existing-b")
	if !ok || peer.ID != "node-b" {
		t.Errorf("rotating node-a disturbed node-b: %#v ok=%v", peer, ok)
	}
}

// A bad or absent credential must be refused, and the refusal must not reveal
// which node ids exist.
func TestSelfProxyCredentialRotationRejectsUnknownCredential(t *testing.T) {
	server, _, registry := newProxyIssuingServer(t)
	registry.credHashes = map[string]string{"node-a": relay.HashCredential("ck_node_a")}

	resp := rotationRequest(t, server, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status with no credential = %d, want 401", resp.StatusCode)
	}
	resp = rotationRequest(t, server, "ck_wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status with an unknown credential = %d, want 401", resp.StatusCode)
	}
}
