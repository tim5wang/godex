package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/backend"
)

func TestWorkContextHTTPPersonalIsolationAndPersistence(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	principal := identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	protected := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, protected)
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, request)
		var decoded map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
		return rec.Code, decoded
	}
	code, item := call("POST", "/v1/work-contexts", map[string]any{"title": "服务长期维护", "goal": "重启可恢复", "tenant_id": "forged", "owner_id": "mallory"})
	if code != 200 {
		t.Fatalf("create %d: %v", code, item)
	}
	id := item["id"].(string)
	version := int(item["version"].(float64))
	code, item = call("POST", "/v1/work-contexts/"+id+"/checkpoints", map[string]any{"if_version": version, "checkpoint": map[string]any{"summary": "待验证", "next_actions": []string{"跑回归"}}})
	if code != 200 {
		t.Fatalf("checkpoint %d: %v", code, item)
	}
	code, _ = call("POST", "/v1/work-contexts/"+id+"/checkpoints", map[string]any{"if_version": version, "checkpoint": map[string]any{"summary": "旧内容"}})
	if code != 409 {
		t.Fatalf("stale version status %d", code)
	}
	principal = identity.NewPrincipal("tenant-a", "bob", "", identity.KindUser, identity.EntitlementCenterExecution)
	if code, _ = call("GET", "/v1/work-contexts/"+id, nil); code != 404 {
		t.Fatalf("owner leak: %d", code)
	}
	principal = identity.NewPrincipal("tenant-b", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	if code, _ = call("GET", "/v1/work-contexts/"+id, nil); code != 404 {
		t.Fatalf("tenant leak: %d", code)
	}
	principal = identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	if code, item = call("GET", "/v1/work-contexts/"+id+"/brief", nil); code != 200 || item["production_verified"] != false {
		t.Fatalf("brief %d: %v", code, item)
	}
	// A fresh backend handle reopens the same SQLite records.
	if err := service.CloseWorkContexts(); err != nil {
		t.Fatal(err)
	}
	restarted := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = restarted.CloseWorkContexts() })
	mux = http.NewServeMux()
	registerWorkContextRoutes(mux, restarted, protected)
	code, item = call("GET", "/v1/work-contexts/"+id, nil)
	if code != 200 {
		t.Fatalf("reopen %d: %v", code, item)
	}
	saved := item["context"].(map[string]any)["checkpoint"].(map[string]any)
	if saved["summary"] != "待验证" {
		t.Fatalf("lost checkpoint: %v", saved)
	}
}

func TestWorkContextHTTPRejectsMissingPrincipalAndCenterRelayOnly(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	for _, tc := range []struct {
		name string
		p    identity.Principal
		want int
	}{
		{"missing", identity.Principal{}, 403},
		{"relay-only", identity.NewPrincipal("tenant", "user", "", identity.KindUser, identity.EntitlementRelayOnly), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), tc.p)))
				})
			})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/work-contexts", nil))
			if rec.Code != tc.want {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
