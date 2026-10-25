package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
)

// TestAnonymousGuardRefusesProxiedRequestsWithoutToken covers the gap the startup
// guard cannot see (design §5): an instance bound to 127.0.0.1 behind nginx or a
// tunnel looks loopback to the kernel while being publicly reachable. With no web
// token, an anonymous caller is the platform operator, so the whole management API
// would be served to the internet as the operator.
func TestAnonymousGuardRefusesProxiedRequestsWithoutToken(t *testing.T) {
	manager := anonymousGuardManager(t, "")

	for _, header := range []string{
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"X-Real-Ip", "Forwarded", "Cf-Connecting-Ip", "X-Client-Ip",
	} {
		handler := rejectProxiedAnonymous(manager)(func(next http.Handler) http.Handler { return next })(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

		req := httptest.NewRequest(http.MethodGet, "/config", nil)
		req.Header.Set(header, "203.0.113.9")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("header %s: status = %d, want 403 (proxied anonymous request must be refused)", header, rec.Code)
		}
	}
}

// TestAnonymousGuardAllowsDirectCallerWithoutToken: a developer running the
// instance on loopback and opening it directly must keep working. That is the
// behaviour the fail-open semantics exist for, so the guard must not break it.
func TestAnonymousGuardAllowsDirectCallerWithoutToken(t *testing.T) {
	manager := anonymousGuardManager(t, "")
	handler := rejectProxiedAnonymous(manager)(func(next http.Handler) http.Handler { return next })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a direct caller with no token", rec.Code)
	}
}

// TestAnonymousGuardStepsAsideWhenTokenConfigured: with a token, the auth
// middleware decides on the credential, so a proxied request is normal and must
// not be refused — otherwise every properly-authenticated deployment behind a
// proxy would break.
func TestAnonymousGuardStepsAsideWhenTokenConfigured(t *testing.T) {
	manager := anonymousGuardManager(t, "web-secret")
	handler := rejectProxiedAnonymous(manager)(func(next http.Handler) http.Handler { return next })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when a token is configured", rec.Code)
	}
}

// TestAnonymousGuardRefusalNamesTheHeader: an unexplainable 403 is exactly what
// this codebase avoids; the refusal must say which header proved the request came
// through a proxy.
func TestAnonymousGuardRefusalNamesTheHeader(t *testing.T) {
	manager := anonymousGuardManager(t, "")
	handler := rejectProxiedAnonymous(manager)(func(next http.Handler) http.Handler { return next })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.Header.Set("X-Real-Ip", "203.0.113.9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "X-Real-Ip") {
		t.Errorf("refusal did not name the header: %s", body)
	}
}

func TestAnonymousGuardNilManagerIsSafe(t *testing.T) {
	handler := rejectProxiedAnonymous(nil)(func(next http.Handler) http.Handler { return next })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when no manager is wired", rec.Code)
	}
}

// anonymousGuardManager builds a manager whose config carries (or omits) a web
// token. The token is what the guard switches on.
func anonymousGuardManager(t *testing.T, webToken string) *config.Manager {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	return newTestManager(t, cfg)
}
