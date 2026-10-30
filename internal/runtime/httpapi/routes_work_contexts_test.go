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

func TestWorkContextHTTPTodosAndFocus(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	principal := identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	})
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(raw)))
		var decoded map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
		return rec.Code, decoded
	}

	code, item := call("POST", "/v1/work-contexts", map[string]any{"title": "plan", "goal": "g"})
	if code != 200 {
		t.Fatalf("create %d: %v", code, item)
	}
	id := item["id"].(string)

	code, todo := call("POST", "/v1/work-contexts/"+id+"/todos", map[string]any{"text": "ship plan board", "owner": "me"})
	if code != 200 {
		t.Fatalf("create todo %d: %v", code, todo)
	}
	todoID := todo["id"].(string)

	code, todo = call("PATCH", "/v1/work-contexts/"+id+"/todos/"+todoID, map[string]any{"status": "doing", "if_version": 1})
	if code != 200 || todo["status"] != "doing" {
		t.Fatalf("patch todo %d: %v", code, todo)
	}
	// A stale version must be reported as a conflict, not applied.
	if code, _ = call("PATCH", "/v1/work-contexts/"+id+"/todos/"+todoID, map[string]any{"text": "stale", "if_version": 1}); code != 409 {
		t.Fatalf("stale patch status %d, want 409", code)
	}

	// Focus reports the plan and the events recorded so far, with no watermark yet.
	code, focus := call("GET", "/v1/work-contexts/"+id+"/focus", nil)
	if code != 200 {
		t.Fatalf("focus %d: %v", code, focus)
	}
	if focus["last_seen_at"] != "" {
		t.Fatalf("fresh focus should have empty last_seen_at: %v", focus["last_seen_at"])
	}
	if todos, _ := focus["todos"].([]any); len(todos) != 1 {
		t.Fatalf("focus should carry the plan item: %v", focus["todos"])
	}
	if changes, _ := focus["changes"].([]any); len(changes) == 0 {
		t.Fatal("focus should carry recorded events")
	}

	// Marking seen advances the watermark, and the next focus is then empty.
	if code, _ = call("POST", "/v1/work-contexts/"+id+"/seen", nil); code != 200 {
		t.Fatalf("seen status %d", code)
	}
	code, focus = call("GET", "/v1/work-contexts/"+id+"/focus", nil)
	if code != 200 || focus["last_seen_at"] == "" {
		t.Fatalf("focus after seen %d: %v", code, focus)
	}
	if changes, _ := focus["changes"].([]any); len(changes) != 0 {
		t.Fatalf("nothing changed since seen: %v", changes)
	}

	if code, _ = call("DELETE", "/v1/work-contexts/"+id+"/todos/"+todoID, nil); code != 200 {
		t.Fatalf("delete status %d", code)
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
