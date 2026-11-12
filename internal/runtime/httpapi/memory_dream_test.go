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

func TestMemoryDreamHTTPNoChangeScheduleHistoryAndOwnerIsolation(t *testing.T) {
	service := backend.NewService(newTestConfig(t), nil, nil)
	defer service.CloseWorkContexts()
	store, err := service.WorkContexts()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := workcontext.Scope{TenantID: "tenant-a", OwnerID: "alice"}
	desktop, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "Memory", Goal: "isolation"})
	if err != nil {
		t.Fatal(err)
	}
	in, _, err := store.BeginDream(ctx, scope, desktop.ID, false, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishDream(ctx, scope, *in, workcontext.DreamResult{Summary: "derived reference"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	principal := identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	})
	call := func(method, path string, body any) (int, json.RawMessage) {
		t.Helper()
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(raw)))
		return rec.Code, rec.Body.Bytes()
	}
	base := "/v1/work-contexts/" + desktop.ID
	status, raw := call("POST", base+"/organize", map[string]any{})
	var skipped map[string]any
	_ = json.Unmarshal(raw, &skipped)
	if status != 200 || skipped["queued"] != false || skipped["reason"] != "unchanged" {
		t.Fatalf("no-change job created: %d %s", status, raw)
	}
	status, raw = call("PATCH", base+"/memory", map[string]any{"enabled": false, "interval_seconds": 3600})
	var state workcontext.MemoryState
	_ = json.Unmarshal(raw, &state)
	if status != 200 || state.Enabled || state.IntervalSeconds != 3600 {
		t.Fatalf("schedule: %d %s", status, raw)
	}
	if status, _ = call("PATCH", base+"/memory", map[string]any{"enabled": true, "interval_seconds": 1}); status != 400 {
		t.Fatal("invalid interval accepted")
	}
	if status, _ = call("GET", base+"/memory/runs", nil); status != 200 {
		t.Fatal("history unavailable")
	}
	principal = identity.NewPrincipal("tenant-a", "bob", "", identity.KindUser, identity.EntitlementCenterExecution)
	for _, op := range []struct {
		method, path string
		body         any
	}{
		{"GET", base + "/memory", nil}, {"GET", base + "/memory/runs", nil}, {"POST", base + "/organize", map[string]any{"force": true}}, {"PATCH", base + "/memory", map[string]any{"enabled": false, "interval_seconds": 3600}}, {"POST", base + "/memory/runs/" + in.RunID + "/revert", nil},
	} {
		if status, raw = call(op.method, op.path, op.body); status != 404 {
			t.Fatalf("owner bypass: %s %d %s", op.path, status, raw)
		}
	}
	principal = identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	status, raw = call("POST", base+"/memory/runs/"+in.RunID+"/revert", nil)
	if status != 200 {
		t.Fatalf("revert %d %s", status, raw)
	}
	_ = json.Unmarshal(raw, &state)
	if state.Summary != "" || state.Status != "reverted" {
		t.Fatalf("revert lost semantics: %+v", state)
	}
}
