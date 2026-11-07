package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/relay"
)

// This file reproduces the reported bug end to end at the route layer, because
// the failure was a SCOPE mismatch rather than a missing route: the work desktop
// stores rows per (tenant_id, owner_id) derived from the principal, so a remote
// caller that resolved to the anonymous placeholder owned none of them and saw an
// empty wall while the same rows were plainly on the node itself.
//
// The test drives the REAL relay-trust resolution (classifyPrincipal over a
// relay-vouched request) rather than hand-building the principal, so it fails if
// any link in the chain regresses: the vouch header not being read, being read
// with the wrong field, or the scope not being derived from it.

// workContextCaller installs a relay-trusted principal (optionally vouched as a
// specific owner) on every request, the way a relay hop would.
func workContextCaller(principal identity.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

func listWorkContexts(t *testing.T, mux *http.ServeMux) (int, []map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/work-contexts", nil))
	var items []map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode list: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec.Code, items
}

func createWorkContext(t *testing.T, mux *http.ServeMux, title string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"title": title, "goal": "远程可见"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/work-contexts", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create %q: %d %s", title, rec.Code, rec.Body.String())
	}
}

// TestRelayVouchedCallerSeesOwnerDesktop is the fix's regression test: the owner
// creates streams locally, and a caller arriving over the relay channel with the
// center's vouch for THAT owner must see them.
func TestRelayVouchedCallerSeesOwnerDesktop(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })

	owner := identity.NewPrincipal("", "local-operator", "", identity.KindUser, identity.EntitlementCenterExecution)
	ownerMux := http.NewServeMux()
	registerWorkContextRoutes(ownerMux, service, workContextCaller(owner))
	createWorkContext(t, ownerMux, "Godex工作桌面完善")
	createWorkContext(t, ownerMux, "Godex产品化")

	// The remote caller: relay-trusted, vouched as the owner by the center.
	vouched := classifyPrincipal(relayVouchRequest(relay.EncodeVouch("", "local-operator")), nodeProxyMatch{}, true, nil)
	if vouched.UserID != "local-operator" {
		t.Fatalf("precondition: relay principal resolved to %q, not the vouched owner", vouched.UserID)
	}
	remoteMux := http.NewServeMux()
	registerWorkContextRoutes(remoteMux, service, workContextCaller(vouched))

	code, items := listWorkContexts(t, remoteMux)
	if code != http.StatusOK {
		t.Fatalf("list status %d", code)
	}
	if len(items) != 2 {
		t.Fatalf("vouched relay caller saw %d streams, want 2 (this is the reported bug)", len(items))
	}
}

// TestRelayUnvouchedCallerSeesEmptyDesktop pins the chosen安全语义: without a
// vouch the caller keeps the placeholder identity, which owns nothing, so a
// per-owner surface is EMPTY rather than showing another user's work.
func TestRelayUnvouchedCallerSeesEmptyDesktop(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })

	owner := identity.NewPrincipal("", "local-operator", "", identity.KindUser, identity.EntitlementCenterExecution)
	ownerMux := http.NewServeMux()
	registerWorkContextRoutes(ownerMux, service, workContextCaller(owner))
	createWorkContext(t, ownerMux, "只属于 local-operator 的桌面")

	unvouched := classifyPrincipal(relayVouchRequest(""), nodeProxyMatch{}, true, nil)
	remoteMux := http.NewServeMux()
	registerWorkContextRoutes(remoteMux, service, workContextCaller(unvouched))

	code, items := listWorkContexts(t, remoteMux)
	if code != http.StatusOK {
		t.Fatalf("list status %d (want 200 with an empty list, not an error)", code)
	}
	if len(items) != 0 {
		t.Fatalf("unvouched relay caller saw %d streams, want 0", len(items))
	}
}

// TestRelayVouchDoesNotCrossOwners guards the isolation half: a vouch naming one
// owner must not expose a different owner's streams on the same node.
func TestRelayVouchDoesNotCrossOwners(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, nil, nil)
	t.Cleanup(func() { _ = service.CloseWorkContexts() })

	alice := identity.NewPrincipal("", "alice", "", identity.KindUser, identity.EntitlementCenterExecution)
	aliceMux := http.NewServeMux()
	registerWorkContextRoutes(aliceMux, service, workContextCaller(alice))
	createWorkContext(t, aliceMux, "alice 的桌面")

	mallory := classifyPrincipal(relayVouchRequest(relay.EncodeVouch("", "mallory")), nodeProxyMatch{}, true, nil)
	malloryMux := http.NewServeMux()
	registerWorkContextRoutes(malloryMux, service, workContextCaller(mallory))

	code, items := listWorkContexts(t, malloryMux)
	if code != http.StatusOK {
		t.Fatalf("list status %d", code)
	}
	if len(items) != 0 {
		t.Fatalf("a caller vouched as mallory saw %d of alice's streams", len(items))
	}
}
