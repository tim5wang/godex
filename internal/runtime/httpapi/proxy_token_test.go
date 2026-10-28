package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// fakeProxyLookup stands in for the registry's per-node nk_ index.
type fakeProxyLookup struct {
	byHash map[string]noderegistry.NodeView
}

func (f fakeProxyLookup) NodeByProxyTokenHash(hash string) (noderegistry.NodeView, bool) {
	node, ok := f.byHash[hash]
	return node, ok
}

// TestResolveNodeProxyTokenPrefersPerNodeOverLegacy is the migration's core
// ordering property: when the SAME string is both a per-node token and the
// center-wide legacy value, the per-node reading must win, because only it names
// a node. Getting this backwards would silently leave every converged node
// indistinguishable — the exact state the migration exists to end.
func TestResolveNodeProxyTokenPrefersPerNodeOverLegacy(t *testing.T) {
	token := "nk_same_value"
	lookup := fakeProxyLookup{byHash: map[string]noderegistry.NodeView{
		relay.HashCredential(token): {ID: "node-a", TenantID: "tenant-1"},
	}}

	match := resolveNodeProxyToken(bearerRequest(token), token, lookup)
	if !match.Matched {
		t.Fatal("a per-node token must match")
	}
	if match.NodeID != "node-a" || match.TenantID != "tenant-1" {
		t.Errorf("match = %+v, want node-a/tenant-1", match)
	}
	if match.Legacy {
		t.Error("a per-node match must not also be reported as legacy")
	}
}

// TestResolveNodeProxyTokenAcceptsLegacyDuringMigration pins the dual-accept
// window. Without it, rolling out per-node tokens would disconnect every node
// that has not rotated yet, which is the interruption the design's R2 feared.
func TestResolveNodeProxyTokenAcceptsLegacyDuringMigration(t *testing.T) {
	match := resolveNodeProxyToken(bearerRequest("nk_legacy"), "nk_legacy", nil)
	if !match.Matched || !match.Legacy {
		t.Fatalf("match = %+v, want a legacy match", match)
	}
	if match.NodeID != "" || match.TenantID != "" {
		t.Errorf("the center-wide token names no node, got %+v", match)
	}
}

// A non-nk_ bearer must never be read as a node credential, even when it equals
// the legacy value. Only nk_-prefixed strings are node credentials, which also
// keeps ordinary web-token traffic from hashing and scanning the registry.
func TestResolveNodeProxyTokenIgnoresNonNodeCredential(t *testing.T) {
	if match := resolveNodeProxyToken(bearerRequest("web-secret"), "web-secret", nil); match.Matched {
		t.Error("a web token must not be accepted as a node proxy credential")
	}
	if match := resolveNodeProxyToken(bearerRequest("nk_x"), "", nil); match.Matched {
		t.Error("an unknown nk_ token must not match")
	}
}

// TestClassifyPrincipalPerNodeTokenNamesNodeAndTenant is acceptance V1, and the
// mechanism behind V3: the principal now carries the node's id and tenant
// instead of the anonymous "relay-node" placeholder.
func TestClassifyPrincipalPerNodeTokenNamesNodeAndTenant(t *testing.T) {
	match := nodeProxyMatch{Matched: true, NodeID: "node-a", TenantID: "tenant-1"}
	p := classifyPrincipal(bearerRequest("nk_x"), match, false, nil)

	if p.AuthMethod != AuthMethodNodeProxyToken {
		t.Errorf("AuthMethod = %q, want %q", p.AuthMethod, AuthMethodNodeProxyToken)
	}
	if p.UserID != "node-a" {
		t.Errorf("UserID = %q, want the node id node-a", p.UserID)
	}
	if p.TenantID != "tenant-1" {
		t.Errorf("TenantID = %q, want tenant-1", p.TenantID)
	}
	if p.Kind != identity.KindNode {
		t.Errorf("Kind = %q, want %q", p.Kind, identity.KindNode)
	}
	// Naming the node must not promote it: the nk_ boundary is unchanged.
	if p.CanExecuteOnCenter() || p.IsPlatformAdmin() {
		t.Fatal("a per-node credential must still not be the operator")
	}
}

// TestClassifyPrincipalLegacyTokenKeepsPlaceholderIdentity pins the other half of
// the dual-accept window: a not-yet-converged node keeps exactly today's
// identity (placeholder id, no tenant), so single-tenant behaviour is unchanged.
func TestClassifyPrincipalLegacyTokenKeepsPlaceholderIdentity(t *testing.T) {
	p := classifyPrincipal(bearerRequest("nk_legacy"), nodeProxyMatch{Matched: true, Legacy: true}, false, nil)

	if p.UserID != "relay-node" {
		t.Errorf("UserID = %q, want the legacy placeholder relay-node", p.UserID)
	}
	if p.TenantID != "" {
		t.Errorf("TenantID = %q, want empty for the center-wide token", p.TenantID)
	}
	if p.AuthMethod != AuthMethodNodeProxyToken {
		t.Errorf("AuthMethod = %q, want %q", p.AuthMethod, AuthMethodNodeProxyToken)
	}
}

// TestPerNodeIdentityFixesTenantScopedNodeAccess is acceptance V3, stated as the
// policy sees it. Before the migration the node actor carried NO tenant, so any
// tenant-owned target fell into the cross-tenant branch and was refused — a node
// could not reach even its own tenant's nodes. With the tenant attached, the
// target is now judged on its merits.
func TestPerNodeIdentityFixesTenantScopedNodeAccess(t *testing.T) {
	policy := identity.Policy{}
	principal := classifyPrincipal(bearerRequest("nk_x"),
		nodeProxyMatch{Matched: true, NodeID: "node-a", TenantID: "tenant-1"}, false, nil)

	sameTenant := identity.NodeRef{ID: "node-b", TenantID: "tenant-1", Visibility: identity.VisibilityTenant}
	if d := policy.Authorize(principal, sameTenant); !d.Allowed {
		t.Errorf("a node must reach its own tenant's shared node, got %q (%s)", d.Reason, d.Detail)
	}

	// The isolation this migration exists to preserve: another tenant stays out.
	otherTenant := identity.NodeRef{ID: "node-c", TenantID: "tenant-2", Visibility: identity.VisibilityTenant}
	if d := policy.Authorize(principal, otherTenant); d.Allowed {
		t.Error("a node must NOT reach another tenant's node")
	}

	// The legacy placeholder still reaches nothing tenant-owned, which is the
	// pre-migration behaviour and therefore the safe reading of "not converged".
	legacy := classifyPrincipal(bearerRequest("nk_legacy"), nodeProxyMatch{Matched: true, Legacy: true}, false, nil)
	if d := policy.Authorize(legacy, sameTenant); d.Allowed {
		t.Error("the center-wide token must not gain tenant-scoped access")
	}
}

// TestNodeProxyTokenCheckerAgreesWithResolver keeps the authenticator and the
// classifier on one implementation: whatever the middleware lets in must also be
// the thing that gets named.
func TestNodeProxyTokenCheckerAgreesWithResolver(t *testing.T) {
	token := "nk_node_token"
	lookup := fakeProxyLookup{byHash: map[string]noderegistry.NodeView{
		relay.HashCredential(token): {ID: "node-a", TenantID: "tenant-1"},
	}}
	manager := newTestManager(t, newTestConfig(t))
	checker := nodeProxyTokenChecker(manager, lookup)

	if !checker(bearerRequest(token)) {
		t.Fatal("a valid per-node token must authenticate")
	}
	if checker(bearerRequest("nk_unknown")) {
		t.Error("an unknown nk_ must not authenticate")
	}
	if checker(bearerRequest("web-secret")) {
		t.Error("a web token must not authenticate as a node")
	}
}

// resolvingLookup is a control registry that also resolves per-node tokens, so a
// server-level test can prove the wiring (not just the helpers).
type resolvingLookup struct {
	ownerSettableRegistry
	hashes map[string]string
}

func (r *resolvingLookup) SetProxyTokenHash(_ context.Context, id, hash string) error {
	if r.hashes == nil {
		r.hashes = map[string]string{}
	}
	r.hashes[id] = hash
	return nil
}

func (r *resolvingLookup) NodeByProxyTokenHash(hash string) (noderegistry.NodeView, bool) {
	for id, h := range r.hashes {
		if h == strings.TrimSpace(hash) && h != "" {
			for _, node := range r.nodes {
				if node.ID == id {
					return node, true
				}
			}
			// A node registered without a stored view still resolves by id.
			return noderegistry.NodeView{ID: id}, true
		}
	}
	return noderegistry.NodeView{}, false
}

// TestNodeProxyReadAcceptsPerNodeTokenThroughFullChain runs the real middleware
// chain: a per-node nk_ must be authenticatable AND classified as its node, so a
// joined node can read the node list while remaining a non-operator.
func TestNodeProxyReadAcceptsPerNodeTokenThroughFullChain(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)

	registry := &resolvingLookup{}
	registry.nodes = []noderegistry.NodeView{{ID: "node-a", TenantID: "tenant-1", Status: "online"}}

	token := "nk_node_a_token"
	if err := registry.SetProxyTokenHash(context.Background(), "node-a", relay.HashCredential(token)); err != nil {
		t.Fatalf("set proxy hash: %v", err)
	}

	handler := NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		ControlRegistry: registry,
	})

	// A valid per-node credential must be accepted by the real chain and attributed
	// to the node rather than refused; 401 here would mean the per-node index is
	// not wired into the authenticator.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(token))
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("a valid per-node token must not be rejected by the full middleware chain: %s", rec.Body.String())
	}
}
