package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/core/workcontext"
	"github.com/tim5wang/godex/internal/services/backend"
)

func TestWorkContextHTTPMainConversationAndEventDetails(t *testing.T) {
	service := backend.NewService(newTestConfig(t), nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })
	principal := identity.NewPrincipal("tenant-a", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	mux := http.NewServeMux()
	registerWorkContextRoutes(mux, service, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	})
	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	ctx, scope := context.Background(), workcontext.Scope{TenantID: "tenant-a", OwnerID: "alice"}
	store, _ := service.WorkContexts()
	c, err := store.Create(ctx, scope, workcontext.ContextInput{Title: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"first", "second"} {
		if _, err := store.LinkSession(ctx, scope, c.ID, workcontext.SessionLink{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/v1/work-contexts/" + c.ID
	if w := call("POST", base+"/sessions/second/main"); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"role":"lead"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := store.SaveCheckpoint(ctx, scope, c.ID, 0, workcontext.Checkpoint{Summary: "original revision"}); err != nil {
		t.Fatal(err)
	}
	detail, _ := store.Get(ctx, scope, c.ID)
	eventID := ""
	for _, event := range detail.Events {
		if event.Kind == "checkpoint_saved" {
			eventID = event.ID
		}
	}
	if eventID == "" {
		t.Fatal("no checkpoint event")
	}
	path := base + "/events/" + eventID
	if w := call("GET", path); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"summary":"original revision"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	principal = identity.NewPrincipal("tenant-a", "bob", "", identity.KindUser, identity.EntitlementCenterExecution)
	for _, methodPath := range [][2]string{{"POST", base + "/sessions/first/main"}, {"GET", path}} {
		if w := call(methodPath[0], methodPath[1]); w.Code != 404 {
			t.Fatal("foreign access", w.Code, w.Body.String())
		}
	}
}
