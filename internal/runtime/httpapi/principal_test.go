package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/relay"
)

func bearerRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// TestClassifyPrincipalOperatorHoldsCenterExecution pins decision ⑨: the single
// web token holder is the platform superadmin and the only principal allowed to
// execute an agent on the center.
func TestClassifyPrincipalOperatorHoldsCenterExecution(t *testing.T) {
	p := classifyPrincipal(bearerRequest("web-secret"), "", false)
	if p.AuthMethod != AuthMethodWebToken {
		t.Errorf("AuthMethod = %q, want %q", p.AuthMethod, AuthMethodWebToken)
	}
	if !p.IsPlatformAdmin() {
		t.Error("web token holder should be the platform admin (single-tenant default)")
	}
	if !p.CanExecuteOnCenter() {
		t.Error("operator must hold center_execution, otherwise the center could not run any agent")
	}
	if d := identity.AuthorizeCenterExecution(p); !d.Allowed {
		t.Errorf("AuthorizeCenterExecution denied the operator: %q", d.Reason)
	}
}

// TestClassifyPrincipalNodeProxyTokenIsNotOperator is the boundary that keeps a
// joined node from gaining center execution: presenting the nk_ credential must
// not be mistaken for the operator.
func TestClassifyPrincipalNodeProxyTokenIsNotOperator(t *testing.T) {
	p := classifyPrincipal(bearerRequest("nk_node"), "nk_node", false)
	if p.AuthMethod != AuthMethodNodeProxyToken {
		t.Errorf("AuthMethod = %q, want %q", p.AuthMethod, AuthMethodNodeProxyToken)
	}
	if p.Kind != identity.KindNode {
		t.Errorf("Kind = %q, want %q", p.Kind, identity.KindNode)
	}
	if p.CanExecuteOnCenter() {
		t.Fatal("a node proxy credential must NOT grant center execution")
	}
	if p.IsPlatformAdmin() {
		t.Fatal("a node proxy credential must NOT be the platform admin")
	}
	if d := identity.AuthorizeCenterExecution(p); d.Allowed {
		t.Fatal("AuthorizeCenterExecution allowed a node credential")
	}
}

// TestClassifyPrincipalRelayTrustWinsOverBearerOrder guards the ordering: a
// relay-originated call must be classified as a node even if it also carries a
// Bearer header, otherwise the node could inherit operator capabilities.
func TestClassifyPrincipalRelayTrustWinsOverBearerOrder(t *testing.T) {
	p := classifyPrincipal(bearerRequest("nk_node"), "nk_node", true)
	if p.AuthMethod != AuthMethodRelayTrust {
		t.Errorf("AuthMethod = %q, want %q", p.AuthMethod, AuthMethodRelayTrust)
	}
	if p.CanExecuteOnCenter() {
		t.Fatal("relay-trusted call must not grant center execution")
	}
}

func TestClassifyPrincipalNodeProxyTokenRejectsWrongBearer(t *testing.T) {
	// A request that presents a bearer that does NOT match the nk_ token must
	// not be classified as a node on the strength of the token merely existing.
	p := classifyPrincipal(bearerRequest("something-else"), "nk_node", false)
	if p.AuthMethod != AuthMethodWebToken {
		t.Errorf("AuthMethod = %q, want %q (unmatched bearer falls through)", p.AuthMethod, AuthMethodWebToken)
	}
	if p.Kind != identity.KindPlatform {
		t.Errorf("Kind = %q, want %q", p.Kind, identity.KindPlatform)
	}
}

func TestWithPrincipalAuthAttachesPrincipalOnlyWhenAllowed(t *testing.T) {
	allow := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) })
	}
	var got identity.Principal
	var ok bool
	handler := withPrincipalAuth(allow, func(*http.Request) identity.Principal {
		return identity.NewPrincipal("t1", "u1", "", identity.KindUser, identity.EntitlementRelayOnly)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), bearerRequest("t"))

	if !ok {
		t.Fatal("principal not attached downstream of an allowed request")
	}
	if got.UserID != "u1" || got.TenantID != "t1" {
		t.Fatalf("principal = %#v", got)
	}
}

// TestWithPrincipalAuthDoesNotRunHandlerWhenDenied proves the layering keeps
// authorization in charge: classification must never widen access.
func TestWithPrincipalAuthDoesNotRunHandlerWhenDenied(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
	called := false
	handler := withPrincipalAuth(deny, func(*http.Request) identity.Principal {
		return identity.Principal{UserID: "should-not-matter"}
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest("bad"))

	if called {
		t.Fatal("handler ran despite the middleware denying the request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPrincipalFromContextAbsent(t *testing.T) {
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Fatal("expected no principal on a bare context")
	}
}

// TestProtectedChainAttachesOperatorPrincipal runs the real middleware pair
// used for `protected`, so the wiring itself (not just the pure classifier) is
// covered: a valid web token must arrive downstream with a principal attached.
func TestProtectedChainAttachesOperatorPrincipal(t *testing.T) {
	protected := withPrincipalAuth(
		withBearerAuthProvider(func() string { return "web-secret" }),
		func(*http.Request) identity.Principal {
			return classifyPrincipal(bearerRequest("web-secret"), "", false)
		},
	)
	var got identity.Principal
	var ok bool
	protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), bearerRequest("web-secret"))

	if !ok || !got.CanExecuteOnCenter() {
		t.Fatalf("principal = %#v ok=%v; want the operator with center_execution", got, ok)
	}
}

// TestRelayTrustCheckerAndPrincipalAgree keeps the two uses of the trust header
// from drifting: whatever the middleware trusts must classify as a node.
func TestRelayTrustCheckerAndPrincipalAgree(t *testing.T) {
	r := bearerRequest("")
	r.Header.Set(relay.RelayTrustHeader, relay.SignRelayTrust("node-x", "ck_secret"))
	if !relay.ValidateRelayTrust(r.Header.Get(relay.RelayTrustHeader), "node-x", "ck_secret") {
		t.Fatal("test fixture signature should validate")
	}
	p := classifyPrincipal(r, "", true)
	if p.AuthMethod != AuthMethodRelayTrust || p.CanExecuteOnCenter() {
		t.Fatalf("relay-trusted request classified as %#v", p)
	}
}
