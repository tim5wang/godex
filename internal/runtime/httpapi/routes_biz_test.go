package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/usage"
)

func mustBizHandler(t *testing.T) (http.Handler, *usage.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	usageService := usage.NewService(store)
	service := backend.NewService(cfg, nil, nil)
	return NewHandler(manager, service, nil, nil, nil, nil, usageService), usageService
}

func TestBizKeyCreateListEndpoint(t *testing.T) {
	handler, _ := mustBizHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Create a biz key via the admin endpoint.
	body := bytes.NewBufferString(`{"name":"sales","mcp_servers":["crm"],"sandbox_tools":["read_file"]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/biz/keys", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testWebToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d: %s", resp.StatusCode, readAll(t, resp))
	}

	var created usage.BizKeyCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !strings.HasPrefix(created.Secret, usage.BizKeyPrefix) {
		t.Fatalf("expected secret prefix %q, got %q", usage.BizKeyPrefix, created.Secret[:8])
	}

	// List returns the created key without hash.
	listResp, err := http.Get(server.URL + "/v1/biz/keys")
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected list status 200, got %d", listResp.StatusCode)
	}
	var keys []usage.BizAPIKey
	if err := json.NewDecoder(listResp.Body).Decode(&keys); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 biz key, got %d", len(keys))
	}
	if keys[0].KeyHash != "" {
		t.Fatal("listed key must not expose hash")
	}
	if keys[0].Name != "sales" {
		t.Fatalf("expected name sales, got %q", keys[0].Name)
	}
}

func TestBizKeyResetRotatesSecret(t *testing.T) {
	_, usageService := mustBizHandler(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "sales"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	// Reset returns a fresh secret and the old one stops authenticating.
	reset, err := usageService.ResetBizKey(created.Key.ID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !strings.HasPrefix(reset.Secret, usage.BizKeyPrefix) {
		t.Fatalf("expected biz_ prefix, got %q", reset.Secret[:8])
	}
	if reset.Secret == created.Secret {
		t.Fatal("reset must return a different secret")
	}
	if _, err := usageService.AuthenticateBizKey(created.Secret); err == nil {
		t.Fatal("old secret must no longer authenticate after reset")
	}
	if _, err := usageService.AuthenticateBizKey(reset.Secret); err != nil {
		t.Fatalf("new secret should authenticate: %v", err)
	}
}

// TestBizKeyAuthMiddlewareAcceptsValidKey verifies a valid key reaches the
// handler context.
func TestBizKeyAuthMiddlewareAcceptsValidKey(t *testing.T) {
	_, usageService := mustBizHandler(t)
	created, err := usageService.CreateBizKey(usage.BizKeyCreateRequest{Name: "crm"})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	// withBizKeyAuth requires an underlying handler; exercise it directly.
	var captured *usage.BizAPIKey
	wrapped := withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = BizKeyFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-steps", nil)
	req.Header.Set("Authorization", "Bearer "+created.Secret)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if captured == nil || captured.ID != created.Key.ID {
		t.Fatalf("expected biz key %s in context, got %+v", created.Key.ID, captured)
	}
}

func TestBizKeyAuthMiddlewareRejectsBadKey(t *testing.T) {
	_, usageService := mustBizHandler(t)

	wrapped := withBizKeyAuth(usageService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []string{
		"",                          // missing
		"gdx_abc123",                // wrong prefix
		"biz_doesnotexist",          // unknown
	}
	for _, secret := range cases {
		req := httptest.NewRequest(http.MethodGet, "/v1/agent-steps", nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("secret %q: expected 401, got %d", secret, rec.Code)
		}
		if got := BizKeyFromContext(req.Context()); got != nil {
			t.Fatalf("secret %q: expected nil key in context", secret)
		}
	}
}

var _ = context.Background
var _ = config.UpdateRequest{}
