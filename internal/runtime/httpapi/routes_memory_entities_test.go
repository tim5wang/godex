package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/services/backend"
)

func TestMemoryEntityHTTPDirectoryWritesAndOwnerIsolation(t *testing.T) {
	service := backend.NewService(newTestConfig(t), nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	principal := identity.NewPrincipal("tenant", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	})
	call := func(method, url string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, url, bytes.NewReader(raw)))
		return w
	}
	scope := workcontext.Scope{TenantID: "tenant", OwnerID: "alice"}
	ctx := context.Background()
	store, _ := service.WorkContexts()
	c, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "entities"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.CreateFact(ctx, scope, c.ID, workcontext.FactInput{Title: "截图识别模块", Content: "截图识别模块使用当前节点的原生能力", Kind: "fact", Scope: `{"node":"fixture"}`, Source: "fixture://ocr", ObservedAt: "2026-11-12T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	f, err = store.ReviewFact(ctx, scope, c.ID, f.ID, workcontext.FactReview{Status: "confirmed", IfVersion: f.Version})
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/work-contexts/" + c.ID + "/memory/"
	body := workcontext.EntityInput{Kind: "module", Name: "截图识别模块", Aliases: []string{"vision_ocr"}, FactID: f.ID, FactVersion: f.Version, Reason: "curated equivalent name"}
	w := call("POST", base+"entities", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var e workcontext.MemoryEntity
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	w = call("GET", base+"entities?q=vision_ocr", nil)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(e.ID)) || !bytes.Contains(w.Body.Bytes(), []byte(`"evidence"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("GET", base+"entities?cursor=bad", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	body.ID = e.ID
	body.IfVersion = e.Version - 1
	if w = call("POST", base+"entities", body); w.Code != 409 {
		t.Fatal("stale metadata CAS accepted", w.Code, w.Body.String())
	}
	if w = call("GET", base+"entity-audits", nil); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("curated equivalent name")) {
		t.Fatal(w.Code, w.Body.String())
	}
	principal = identity.NewPrincipal("tenant", "bob", "", identity.KindUser, identity.EntitlementCenterExecution)
	for _, suffix := range []string{"entities", "entity-relations", "entity-audits"} {
		if w = call("GET", base+suffix, nil); w.Code != 404 {
			t.Fatal("catalog metadata crossed owners", suffix, w.Code)
		}
	}
	if w = call("POST", base+"entities", body); w.Code != 404 {
		t.Fatal("foreign entity write accepted", w.Code)
	}
}
