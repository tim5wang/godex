package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
)

// allowAll stands in for the protected middleware: it runs the handler, which
// is all requireCenterExecution needs from it (authentication already decided).
func allowAll(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	})
}

func serveWithPrincipal(principal identity.Principal, handler http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sessions", nil)
	req = req.WithContext(WithPrincipal(req.Context(), principal))
	handler.ServeHTTP(rec, req)
	return rec
}

// TestRequireCenterExecutionAllowsOperator pins decision ⑨: the operator (the
// single web token holder) may run an agent on the center.
func TestRequireCenterExecutionAllowsOperator(t *testing.T) {
	called := false
	handler := requireCenterExecution(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	operator := identity.NewPrincipal("", "local-operator", identity.RolePlatformAdmin, identity.KindPlatform,
		identity.EntitlementCenterExecution, identity.EntitlementRelayOnly)

	rec := serveWithPrincipal(operator, handler)
	if !called || rec.Code != http.StatusAccepted {
		t.Fatalf("operator denied: status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

// TestRequireCenterExecutionRefusesTenantUser is the requirement made concrete:
// a tenant user must NOT be able to run an agent on the center; they are
// relayed to their own nodes instead.
func TestRequireCenterExecutionRefusesTenantUser(t *testing.T) {
	called := false
	handler := requireCenterExecution(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	tenantUser := identity.NewPrincipal("tenant-1", "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)

	rec := serveWithPrincipal(tenantUser, handler)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("handler ran for a tenant user without center_execution")
	}
}

// TestRequireCenterExecutionRefusesTenantAdmin covers the subtle case: a tenant
// admin is still scoped to their own tenant and gets no center execution. This
// is what keeps "only one account can run an agent on the server" true even
// though tenant admins are privileged within their tenant.
func TestRequireCenterExecutionRefusesTenantAdmin(t *testing.T) {
	handler := requireCenterExecution(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}))
	tenantAdmin := identity.NewPrincipal("tenant-1", "admin-a", "", identity.KindUser, identity.EntitlementRelayOnly)
	if rec := serveWithPrincipal(tenantAdmin, handler); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestRequireCenterExecutionAllowsNodeActor pins the exemption that keeps the
// documented remote-node path working: driving a node from the center arrives
// as a relay-trust (node) identity and must not be refused.
func TestRequireCenterExecutionAllowsNodeActor(t *testing.T) {
	called := false
	handler := requireCenterExecution(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	node := identity.NewPrincipal("", "relay-node", "", identity.KindNode, identity.EntitlementRelayOnly)

	rec := serveWithPrincipal(node, handler)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("node actor refused: status=%d called=%v", rec.Code, called)
	}
}

// TestRequireCenterExecutionRefusesBareRole asserts the gate reads the
// capability, not the role label: a "platform admin" string without the
// entitlement must not pass, otherwise a role typo would silently grant
// execution on the server.
func TestRequireCenterExecutionRefusesBareRole(t *testing.T) {
	handler := requireCenterExecution(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}))
	bare := identity.NewPrincipal("", "admin", identity.RolePlatformAdmin, identity.KindPlatform)
	if rec := serveWithPrincipal(bare, handler); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestRequireCenterExecutionFailsClosedWithoutPrincipal covers a mis-wired
// route (gate without authentication in front of it): it must refuse rather
// than fall through as if an operator were calling.
func TestRequireCenterExecutionFailsClosedWithoutPrincipal(t *testing.T) {
	called := false
	handler := requireCenterExecution(allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler ran without an authenticated principal")
	}
}

// TestCenterExecutionGateRunsAfterAuthentication checks the ordering the wiring
// depends on: authentication decides first, so an unauthenticated request is
// rejected as 401 (not 403) by the real protected chain.
func TestCenterExecutionGateRunsAfterAuthentication(t *testing.T) {
	protected := withPrincipalAuth(
		withBearerAuthProvider(func() string { return "web-secret" }),
		func(*http.Request) identity.Principal {
			return identity.Principal{UserID: "local-operator", PlatformRole: identity.RolePlatformAdmin,
				Kind: identity.KindPlatform, Entitlements: []string{identity.EntitlementCenterExecution}}
		},
	)
	handler := requireCenterExecution(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No token: authentication must answer 401 before the gate is consulted.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}

	// With the token: the gate passes the operator through.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest("web-secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}
