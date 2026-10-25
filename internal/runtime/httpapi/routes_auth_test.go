package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
)

// newAuthServer wires the real handler with an explicit account store, so the
// sign-in surface is exercised end to end (route → service → store) rather than
// against a stub that could disagree with production wiring.
func newAuthServer(t *testing.T, webToken string) (*httptest.Server, *accounts.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	service := backend.NewService(cfg, nil, nil)
	handler := NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  service,
		Accounts: accountsService,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, accountsService
}

func postAuth(t *testing.T, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		// A body-less response is fine for some statuses; decode failures here are
		// reported by the assertions on the status code.
		decoded = map[string]any{}
	}
	return resp, decoded
}

func TestAuthRegisterThenLoginRoundTrip(t *testing.T) {
	server, _ := newAuthServer(t, "")

	resp, body := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse", "tenant_name": "Acme",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201: %v", resp.StatusCode, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("register did not return a session token: %v", body)
	}
	tenant, _ := body["tenant"].(map[string]any)
	if tenant == nil || tenant["name"] != "Acme" {
		t.Fatalf("register did not return the tenant: %v", body)
	}
	principal, _ := body["principal"].(map[string]any)
	if principal == nil || principal["role"] != "owner" {
		t.Fatalf("register principal = %v, want owner role", principal)
	}
	// The response must never carry the password hash.
	if raw, err := json.Marshal(body); err == nil && bytes.Contains(raw, []byte("$2a$")) {
		t.Fatalf("response leaked a bcrypt hash: %s", raw)
	}

	// Login with the same credentials yields a new, working session.
	resp, body = postAuth(t, server.URL+"/auth/login", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %v", resp.StatusCode, body)
	}
	loginToken, _ := body["token"].(string)
	if loginToken == "" || loginToken == token {
		t.Fatalf("login token = %q, want a fresh session token", loginToken)
	}
}

func TestAuthRegisterRejectsDuplicateAndWeakInput(t *testing.T) {
	server, _ := newAuthServer(t, "")

	if resp, _ := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "dup@example.com", "password": "correct-horse",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first register status = %d, want 201", resp.StatusCode)
	}

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{
			name: "duplicate email is a conflict",
			body: map[string]any{"email": "dup@example.com", "password": "another-good-one"},
			want: http.StatusConflict,
		},
		{
			name: "malformed email is a bad request",
			body: map[string]any{"email": "not-an-email", "password": "correct-horse"},
			want: http.StatusBadRequest,
		},
		{
			name: "short password is a bad request",
			body: map[string]any{"email": "weak@example.com", "password": "short"},
			want: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := postAuth(t, server.URL+"/auth/register", tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %v", resp.StatusCode, tc.want, body)
			}
		})
	}
}

// TestAuthLoginRejectsBadCredentials: bad credentials are 401, not 403 — the
// caller is unauthenticated, and 403 would push a client to retry unchanged.
func TestAuthLoginRejectsBadCredentials(t *testing.T) {
	server, _ := newAuthServer(t, "")
	if resp, _ := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "real@example.com", "password": "correct-horse",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatal("seed register failed")
	}

	for _, body := range []map[string]any{
		{"email": "real@example.com", "password": "wrong-password"},
		{"email": "ghost@example.com", "password": "correct-horse"},
	} {
		resp, _ := postAuth(t, server.URL+"/auth/login", body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login %v status = %d, want 401", body, resp.StatusCode)
		}
	}
}

// TestAuthSessionTokenAuthenticatesProtectedRoutes is the point of the whole
// surface: a session token must work as a credential on the normal API, not only
// on /auth/*.
func TestAuthSessionTokenAuthenticatesProtectedRoutes(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	// Without any credential the protected surface refuses.
	unauthReq, err := http.NewRequest(http.MethodGet, server.URL+"/auth/me", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauthenticated /auth/me: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /auth/me status = %d, want 401", unauthResp.StatusCode)
	}

	_, body := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("no session token issued")
	}

	meReq, err := http.NewRequest(http.MethodGet, server.URL+"/auth/me", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	meReq.Header.Set("Authorization", "Bearer "+token)
	meResp, err := http.DefaultClient.Do(meReq)
	if err != nil {
		t.Fatalf("session /auth/me: %v", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("session /auth/me status = %d, want 200", meResp.StatusCode)
	}
	var me map[string]any
	if err := json.NewDecoder(meResp.Body).Decode(&me); err != nil {
		t.Fatalf("decode /auth/me: %v", err)
	}
	principal, _ := me["principal"].(map[string]any)
	if principal == nil {
		t.Fatalf("/auth/me returned no principal: %v", me)
	}
	// The decisive assertion: a signed-in tenant user must NOT be classified as
	// the platform superadmin (that would be a privilege escalation).
	if principal["platform_role"] == "admin" {
		t.Fatalf("session principal was classified as the platform admin: %v", principal)
	}
	if principal["user_id"] == "local-operator" {
		t.Fatalf("session principal fell through to the operator default: %v", principal)
	}
}

// TestWebTokenStillWorksAlongsideSessions: adding sign-in must not break the
// existing single-operator deployment.
func TestWebTokenStillWorksAlongsideSessions(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/me", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer web-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("/auth/me with web token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web token status = %d, want 200", resp.StatusCode)
	}
	var me map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	principal, _ := me["principal"].(map[string]any)
	if principal == nil || principal["platform_role"] != "admin" {
		t.Fatalf("web token principal = %v, want the platform admin", principal)
	}
}

func TestAuthLogoutInvalidatesSession(t *testing.T) {
	// A web token is configured deliberately: without one the middleware
	// fail-opens (existing §5 semantics), so a logged-out caller would fall
	// through to the single-tenant operator default and /auth/me would answer 200
	// — which would make this assertion unable to detect a real logout bug.
	server, _ := newAuthServer(t, "web-secret")
	_, body := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	token, _ := body["token"].(string)

	logout := func() *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/auth/logout", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("logout: %v", err)
		}
		return resp
	}
	resp := logout()
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", resp.StatusCode)
	}

	// The session must no longer be accepted anywhere.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/me", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	after, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post-logout /auth/me: %v", err)
	}
	_ = after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout /auth/me status = %d, want 401", after.StatusCode)
	}
}

// TestAuthLogoutRejectsWebToken: the shared web token is not a revocable session,
// so logout must say so instead of silently reporting success.
func TestAuthLogoutRejectsWebToken(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")
	req, err := http.NewRequest(http.MethodPost, server.URL+"/auth/logout", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer web-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout with web token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (web token is not a session)", resp.StatusCode)
	}
}

// TestAuthRoutesUnavailableWithoutAccountStore covers the degraded wiring: a
// store that could not be opened must report 503 with an actionable message, not
// a bare 404 — otherwise "sign-in is off here" is indistinguishable from "no such
// endpoint".
//
// Driven through registerAuthRoutes directly rather than the full handler: the
// handler CREATES the account store on demand (so sign-in works out of the box),
// which means the nil case is a wiring failure that cannot be reproduced by
// simply omitting Dependencies.Accounts.
func TestAuthRoutesUnavailableWithoutAccountStore(t *testing.T) {
	mux := http.NewServeMux()
	registerAuthRoutes(mux, nil, allowAll)
	server := httptest.NewServer(mux)
	defer server.Close()

	for _, route := range []struct {
		method string
		path   string
		body   map[string]any
	}{
		{method: http.MethodPost, path: "/auth/login", body: map[string]any{"email": "a@example.com", "password": "whatever-long"}},
		{method: http.MethodPost, path: "/auth/register", body: map[string]any{"email": "a@example.com", "password": "whatever-long"}},
	} {
		payload, err := json.Marshal(route.body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req, err := http.NewRequest(route.method, server.URL+route.path, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", route.method, route.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s status = %d, want 503", route.method, route.path, resp.StatusCode)
		}
	}
}

// TestAuthRegisterMakesOwnerNotPlatformAdmin pins decision ⑨ through the HTTP
// surface: signing up grants a tenant, never center execution.
func TestAuthRegisterMakesOwnerNotPlatformAdmin(t *testing.T) {
	server, accountsService := newAuthServer(t, "")
	_, body := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	token, _ := body["token"].(string)

	principal, ok := accountsService.Authenticate(token)
	if !ok {
		t.Fatal("issued session must authenticate")
	}
	if principal.IsPlatformAdmin() || principal.CanExecuteOnCenter() {
		t.Fatalf("signup produced a center-executing principal: %#v", principal)
	}
	if !principal.HasTenant() {
		t.Fatalf("signup produced a principal with no tenant: %#v", principal)
	}
}
