package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/services/backend"
)

func TestWorkContextHTTPMemoryTimeoutAndGroupedResolution(t *testing.T) {
	service := backend.NewService(newTestConfig(t), nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	principal := identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	})
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(raw)))
		return w
	}
	scope := workcontext.Scope{TenantID: "tenant-a", OwnerID: "alice"}
	ctx := context.Background()
	store, _ := service.WorkContexts()
	c, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/work-contexts/" + c.ID
	w := call("PATCH", base+"/memory", map[string]any{"enabled": true, "interval_seconds": 3600, "timeout_seconds": 300})
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"timeout_seconds":300`)) {
		t.Fatalf("timeout: %d %s", w.Code, w.Body.String())
	}
	if w = call("PATCH", base+"/memory", map[string]any{"enabled": true, "interval_seconds": 3600, "timeout_seconds": 1}); w.Code != 400 {
		t.Fatalf("invalid timeout accepted: %d", w.Code)
	}
	var fs []workcontext.Fact
	for _, text := range []string{"Use Vision", "Do not use Vision"} {
		f, e := store.CreateFact(ctx, scope, c.ID, workcontext.FactInput{Title: text, Content: text, Scope: `{"node":"test"}`})
		if e != nil {
			t.Fatal(e)
		}
		fs = append(fs, f)
	}
	in, _, err := store.BeginDream(ctx, scope, c.ID, true, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishDream(ctx, scope, *in, workcontext.DreamResult{Conflicts: [][]string{{fs[0].ID, fs[1].ID}}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = call("GET", base+"/memory/issues", nil)
	var issues []workcontext.MemoryIssue
	if err = json.Unmarshal(w.Body.Bytes(), &issues); err != nil || w.Code != 200 || len(issues) != 1 {
		t.Fatalf("issues: %d %s %v", w.Code, w.Body.String(), err)
	}
	versions := map[string]int{}
	for _, f := range issues[0].Facts {
		versions[f.ID] = f.Version
	}
	path := base + "/memory/issues/" + issues[0].ID + "/resolve"
	principal = identity.NewPrincipal("tenant-a", "bob", "", identity.KindUser, identity.EntitlementCenterExecution)
	if w = call("POST", path, workcontext.ResolveMemoryIssue{KeepID: fs[0].ID, Versions: versions}); w.Code != 404 {
		t.Fatalf("foreign resolution accepted: %d", w.Code)
	}
	principal = identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	if w = call("POST", path, workcontext.ResolveMemoryIssue{KeepID: fs[0].ID, Versions: versions}); w.Code != 200 {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	f, _ := store.Fact(ctx, scope, c.ID, fs[0].ID)
	if f.Status != "observed" {
		t.Fatal("unverified claim was promoted by grouped decision")
	}
}

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

func TestWorkContextHTTPVerificationAuditIsOwnerScopedAndReadOnly(t *testing.T) {
	service := backend.NewService(newTestConfig(t), nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	principal := identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	})
	scope := workcontext.Scope{TenantID: "tenant-a", OwnerID: "alice"}
	ctx := context.Background()
	store, _ := service.WorkContexts()
	c, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "audit"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.CreateFact(ctx, scope, c.ID, workcontext.FactInput{Title: "expired", Content: "old", Scope: `{"env":"test"}`, ValidUntil: "2020-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyMemoryVerification(ctx, scope, c.ID, "session", "turn", []workcontext.VerificationOperation{{Action: "archive", FactID: f.ID, IfVersion: f.Version, Reason: "explicit deadline expired"}}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/work-contexts/" + c.ID + "/memory/verifications"
	call := func(method string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	if w := call("GET"); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("explicit deadline expired")) || !bytes.Contains(w.Body.Bytes(), []byte(`"before"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST"); w.Code != 405 {
		t.Fatal("write endpoint exposed", w.Code)
	}
	principal = identity.NewPrincipal("tenant-a", "bob", "", identity.KindUser, identity.EntitlementCenterExecution)
	if w := call("GET"); w.Code != 404 {
		t.Fatal("audit crossed owners", w.Code)
	}
}
