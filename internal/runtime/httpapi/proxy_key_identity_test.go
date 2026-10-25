package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/usage"
)

// The gdx_ usage-gateway family (P0 §7) authenticated callers but attached no
// Principal, so every identity-based decision saw nobody on those routes.
//
// Scope note: these tests cover the principal construction and the attachment
// plumbing directly. They are deliberately NOT written as HTTP round trips
// claiming to observe the identity, because no handler on these routes consumes
// the Principal yet — a round-trip assertion here would pass regardless and would
// be a test that cannot fail.

func TestProxyKeyPrincipalIdentifiesMachineCredential(t *testing.T) {
	key := &usage.ProxyAPIKey{ID: "key-123", Name: "pipeline", Enabled: true}
	principal := proxyKeyPrincipal(key)

	if principal.Kind != identity.KindProxyKey {
		t.Errorf("Kind = %q, want %q", principal.Kind, identity.KindProxyKey)
	}
	if principal.AuthMethod != AuthMethodProxyKey {
		t.Errorf("AuthMethod = %q, want %q", principal.AuthMethod, AuthMethodProxyKey)
	}
	// The key id, not a human identity: a gdx_ key is a machine credential, and
	// attributing its calls to whoever created it would corrupt the audit trail.
	if principal.UserID != "key-123" {
		t.Errorf("UserID = %q, want the key id", principal.UserID)
	}
	if !principal.Valid() {
		t.Error("principal must be valid (Policy.Authorize denies invalid principals outright)")
	}
}

// TestProxyKeyPrincipalHoldsNoCapabilities is the fail-safe property: a proxy key
// must not inherit the operator's capabilities, so a future gate that requires an
// entitlement denies it by default instead of letting the key through.
func TestProxyKeyPrincipalHoldsNoCapabilities(t *testing.T) {
	principal := proxyKeyPrincipal(&usage.ProxyAPIKey{ID: "key-1"})

	if principal.IsPlatformAdmin() {
		t.Error("a proxy key must not be the platform admin")
	}
	if principal.IsNodeActor() {
		t.Error("a proxy key must not be a node actor")
	}
	if principal.CanExecuteOnCenter() {
		t.Error("a proxy key must not hold center_execution")
	}
	if len(principal.Entitlements) != 0 {
		t.Errorf("Entitlements = %v, want none", principal.Entitlements)
	}
}

// TestProxyKeyPrincipalIsUnscopedAndThereforeCannotReachNodes documents the
// deliberate empty TenantID (ProxyAPIKey has no tenant binding yet, §3.1): an
// unscoped non-operator principal authorizes as cross-tenant, so the gap fails
// closed. If this ever flips to allowed, a missing tenant binding would silently
// become "this key reaches every node on the platform".
func TestProxyKeyPrincipalIsUnscopedAndThereforeCannotReachNodes(t *testing.T) {
	principal := proxyKeyPrincipal(&usage.ProxyAPIKey{ID: "key-1"})

	if principal.HasTenant() {
		t.Fatal("this test asserts the unscoped case; the key unexpectedly has a tenant")
	}
	decision := (identity.Policy{}).Authorize(principal, identity.NodeRef{ID: "node-a"})
	if decision.Allowed {
		t.Fatalf("an unscoped proxy key reached a node: reason=%q detail=%q", decision.Reason, decision.Detail)
	}
}

// TestProxyKeyPrincipalToleratesNilKey: the helper is called after a successful
// authentication, so a nil key should not panic and should still yield a usable
// (valid) principal.
func TestProxyKeyPrincipalToleratesNilKey(t *testing.T) {
	principal := proxyKeyPrincipal(nil)
	if !principal.Valid() {
		t.Fatalf("nil key produced an invalid principal: %#v", principal)
	}
}

func TestWithProxyKeyPrincipalAttachesToContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	key := &usage.ProxyAPIKey{ID: "key-abc"}

	got, ok := PrincipalFromContext(withProxyKeyPrincipal(req, key).Context())
	if !ok {
		t.Fatal("no principal attached")
	}
	if got.UserID != "key-abc" || got.Kind != identity.KindProxyKey {
		t.Fatalf("principal = %#v, want the proxy key identity", got)
	}

	// A nil key must leave the request untouched rather than installing an empty
	// identity that downstream code would read as "authenticated".
	if _, ok := PrincipalFromContext(withProxyKeyPrincipal(req, nil).Context()); ok {
		t.Fatal("a nil key must not attach a principal")
	}
}
