package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
)

// The query-parameter token family (?token=...) exists because WebSocket and
// iframe clients cannot attach an Authorization header. Those paths bypass
// `protected`, so they must attach the Principal themselves — otherwise the same
// web token yields "the operator" over a header and "no identity at all" over a
// query parameter, and every identity-based gate silently stops working there.

func TestPreviewAuthQueryTokenAttachesOperatorPrincipal(t *testing.T) {
	var got identity.Principal
	var ok bool
	handler := withPreviewAuthProvider(func() string { return "web-secret" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok = PrincipalFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodGet, "/preview/static/index.html?token=web-secret", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok {
		t.Fatal("no principal attached on the query-token path")
	}
	if !got.CanExecuteOnCenter() || !got.IsPlatformAdmin() {
		t.Fatalf("query-token principal = %#v, want the operator", got)
	}
}

func TestPreviewAuthBearerAttachesOperatorPrincipal(t *testing.T) {
	var got identity.Principal
	var ok bool
	handler := withPreviewAuthProvider(func() string { return "web-secret" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok = PrincipalFromContext(r.Context())
		}))

	req := httptest.NewRequest(http.MethodGet, "/preview/static/index.html", nil)
	req.Header.Set("Authorization", "Bearer web-secret")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !ok || !got.IsPlatformAdmin() {
		t.Fatalf("bearer principal = %#v ok=%v; want the operator", got, ok)
	}
}

// TestPreviewAuthCookieAttachesOperatorPrincipal covers the third entry: the
// cookie is only set after a successful token check, so it proves the same
// thing and must yield the same identity.
func TestPreviewAuthCookieAttachesOperatorPrincipal(t *testing.T) {
	var got identity.Principal
	var ok bool
	handler := withPreviewAuthProvider(func() string { return "web-secret" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok = PrincipalFromContext(r.Context())
		}))

	req := httptest.NewRequest(http.MethodGet, "/preview/static/index.html", nil)
	req.AddCookie(&http.Cookie{Name: previewTokenCookie, Value: "web-secret"})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !ok || !got.IsPlatformAdmin() {
		t.Fatalf("cookie principal = %#v ok=%v; want the operator", got, ok)
	}
}

// TestPreviewAuthWrongQueryTokenIsRejected guards against the identity change
// loosening authentication: a wrong token must still be refused.
func TestPreviewAuthWrongQueryTokenIsRejected(t *testing.T) {
	called := false
	handler := withPreviewAuthProvider(func() string { return "web-secret" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/static/i.html?token=nope", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler ran for a wrong query token")
	}
}

func TestVoiceQueryTokenAttachesOperatorPrincipal(t *testing.T) {
	// Build the REAL protected chain (authentication + principal attachment),
	// not a bare withBearerAuthProvider: passing the raw middleware would test a
	// wiring that does not exist in production, and the header path would look
	// broken when it is not.
	protected := withPrincipalAuth(
		withBearerAuthProvider(func() string { return "web-secret" }),
		func(*http.Request) identity.Principal { return operatorPrincipal() },
	)
	var got identity.Principal
	var ok bool
	auth := voiceQueryTokenAuth(protected, func() string { return "web-secret" })
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// The header path goes through `protected`, which attaches on its own.
	req := httptest.NewRequest(http.MethodGet, "/v1/voice", nil)
	req.Header.Set("Authorization", "Bearer web-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !ok || !got.IsPlatformAdmin() {
		t.Fatalf("header path: status=%d principal=%#v ok=%v", rec.Code, got, ok)
	}

	// The query path bypasses `protected`, so this is the case that used to have
	// no identity at all.
	got, ok = identity.Principal{}, false
	req = httptest.NewRequest(http.MethodGet, "/v1/voice?token=web-secret", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query path status = %d, want 200", rec.Code)
	}
	if !ok {
		t.Fatal("no principal attached on the voice query-token path")
	}
	if !got.CanExecuteOnCenter() || !got.IsPlatformAdmin() {
		t.Fatalf("voice query principal = %#v, want the operator", got)
	}
}

// TestVoiceQueryTokenWrongTokenFallsThroughToProtected: a wrong query token must
// not short-circuit authentication; it falls through to the normal check, which
// then rejects it.
func TestVoiceQueryTokenWrongTokenFallsThroughToProtected(t *testing.T) {
	called := false
	protected := withPrincipalAuth(
		withBearerAuthProvider(func() string { return "web-secret" }),
		func(*http.Request) identity.Principal { return operatorPrincipal() },
	)
	auth := voiceQueryTokenAuth(protected, func() string { return "web-secret" })
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/voice?token=nope", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler ran for a wrong voice query token")
	}
}

// TestOperatorPrincipalIsTheSameEverywhere pins the extracted constructor: the
// three paths that converge on "the operator presented the web token" must not
// drift in capabilities.
func TestOperatorPrincipalIsTheSameEverywhere(t *testing.T) {
	fromHeader := classifyPrincipal(bearerRequest("web-secret"), "", false)
	direct := operatorPrincipal()
	if fromHeader.UserID != direct.UserID ||
		fromHeader.PlatformRole != direct.PlatformRole ||
		fromHeader.Kind != direct.Kind ||
		fromHeader.AuthMethod != direct.AuthMethod ||
		len(fromHeader.Entitlements) != len(direct.Entitlements) {
		t.Fatalf("operator principal drifted: header=%#v direct=%#v", fromHeader, direct)
	}
	for i := range direct.Entitlements {
		if fromHeader.Entitlements[i] != direct.Entitlements[i] {
			t.Fatalf("entitlements drifted: %v vs %v", fromHeader.Entitlements, direct.Entitlements)
		}
	}
}
