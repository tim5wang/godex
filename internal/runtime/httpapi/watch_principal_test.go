package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// The watch family (P0 §7) authenticated the device and checked its scope, but
// attached only the device value — never a Principal — so identity-based
// decisions on every watch route saw nobody.

// pairedWatch issues a real device token with the given scopes, exercising the
// same pairing handshake production uses.
func pairedWatch(t *testing.T, auth *watchauth.Service, scopes ...string) (watchauth.Device, string) {
	t.Helper()
	pairing, err := auth.StartPairing("wrist", "watch")
	if err != nil {
		t.Fatalf("start pairing: %v", err)
	}
	device, err := auth.ApprovePairing(pairing.UserCode, scopes)
	if err != nil {
		t.Fatalf("approve pairing: %v", err)
	}
	result, err := auth.ExchangeToken(pairing.DeviceCode)
	if err != nil {
		t.Fatalf("exchange token: %v", err)
	}
	if result.Token == "" {
		t.Fatalf("no token issued: %#v", result)
	}
	return device, result.Token
}

func watchAuthForTest(t *testing.T) *watchauth.Service {
	t.Helper()
	auth, err := watchauth.New(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	return auth
}

func TestWatchScopeAttachesDevicePrincipal(t *testing.T) {
	auth := watchAuthForTest(t)
	device, token := pairedWatch(t, auth, watchauth.ScopeRead)

	var got identity.Principal
	var ok bool
	handler := withWatchScope(auth, watchauth.ScopeRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/watch/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok {
		t.Fatal("no principal attached for an authenticated watch device")
	}
	if got.Kind != identity.KindWatch {
		t.Errorf("Kind = %q, want %q", got.Kind, identity.KindWatch)
	}
	if got.AuthMethod != AuthMethodWatch {
		t.Errorf("AuthMethod = %q, want %q", got.AuthMethod, AuthMethodWatch)
	}
	// The device id, not a human identity: a paired watch is a device credential.
	if got.UserID != device.DeviceID {
		t.Errorf("UserID = %q, want the device id %q", got.UserID, device.DeviceID)
	}
}

// TestWatchPrincipalHoldsNoCapabilities: a wrist must not inherit operator
// capabilities, so a future gate requiring one denies it by default.
func TestWatchPrincipalHoldsNoCapabilities(t *testing.T) {
	principal := watchDevicePrincipal(watchauth.Device{DeviceID: "wch-1"})

	if principal.IsPlatformAdmin() {
		t.Error("a watch must not be the platform admin")
	}
	if principal.IsNodeActor() {
		t.Error("a watch must not be a node actor")
	}
	if principal.CanExecuteOnCenter() {
		t.Error("a watch must not hold center_execution")
	}
	if len(principal.Entitlements) != 0 {
		t.Errorf("Entitlements = %v, want none", principal.Entitlements)
	}
	if !principal.Valid() {
		t.Error("principal must be valid")
	}
}

// TestWatchPrincipalIsUnscopedAndThereforeCannotReachNodes documents the
// deliberate empty TenantID (watchauth.Device has no tenant binding yet): an
// unscoped non-operator principal authorizes as cross-tenant, so the gap fails
// closed. If this flips to allowed, a missing tenant binding would silently
// become "this wrist reaches every node on the platform".
func TestWatchPrincipalIsUnscopedAndThereforeCannotReachNodes(t *testing.T) {
	principal := watchDevicePrincipal(watchauth.Device{DeviceID: "wch-1"})

	if principal.HasTenant() {
		t.Fatal("this test asserts the unscoped case; the device unexpectedly has a tenant")
	}
	decision := (identity.Policy{}).Authorize(principal, identity.NodeRef{ID: "node-a"})
	if decision.Allowed {
		t.Fatalf("an unscoped watch reached a node: reason=%q detail=%q", decision.Reason, decision.Detail)
	}
}

// TestWatchScopeDenialStillAttachesNoPrincipal guards the other direction: a
// device without the scope is refused, and no identity leaks to the handler.
func TestWatchScopeDenialStillAttachesNoPrincipal(t *testing.T) {
	auth := watchAuthForTest(t)
	_, token := pairedWatch(t, auth, watchauth.ScopeRead) // read only

	called := false
	handler := withWatchScope(auth, watchauth.ScopeCommand)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/watch/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("handler ran for a device lacking the scope")
	}
}

// TestWatchScopeOrRelayNamesTheDeviceOnTheDirectPath: on the direct path the
// device is known, so the principal must name it rather than fall back to the
// relay-vouched placeholder.
func TestWatchScopeOrRelayNamesTheDeviceOnTheDirectPath(t *testing.T) {
	auth := watchAuthForTest(t)
	device, token := pairedWatch(t, auth, watchauth.ScopeRead)

	var got identity.Principal
	var ok bool
	handler := withWatchScopeOrRelay(auth, watchauth.ScopeRead, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/watch/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok || got.UserID != device.DeviceID {
		t.Fatalf("principal = %#v ok=%v, want the named device %q", got, ok, device.DeviceID)
	}
}

// TestWatchScopeOrRelayForwardGetsRelayVouchedPrincipal: a relay forward carries
// scopes, not a device id, so the wrist cannot be named. The identity must still
// be attached (that is the point of the change) and must hold no entitlements —
// withWatchScopeOrRelay exists to grant exactly the scope asked for, so the
// principal must not undo that by handing out capabilities.
func TestWatchScopeOrRelayForwardGetsRelayVouchedPrincipal(t *testing.T) {
	auth := watchAuthForTest(t)

	var got identity.Principal
	var ok bool
	// trust returns true: this request arrives over an authenticated relay channel.
	trust := func(*http.Request) bool { return true }
	handler := withWatchScopeOrRelay(auth, watchauth.ScopeRead, trust)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/watch/sessions", nil)
	req.Header.Set(WatchScopesHeader, watchauth.ScopeRead)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok {
		t.Fatal("no principal attached for a relay-vouched watch forward")
	}
	if got.Kind != identity.KindWatch {
		t.Errorf("Kind = %q, want %q", got.Kind, identity.KindWatch)
	}
	if got.AuthMethod != AuthMethodRelayTrust {
		t.Errorf("AuthMethod = %q, want %q (the forward, not a device)", got.AuthMethod, AuthMethodRelayTrust)
	}
	if len(got.Entitlements) != 0 {
		t.Errorf("Entitlements = %v, want none", got.Entitlements)
	}
}

// TestWatchOrWebReadAttachesDeviceOnTheWatchPath pins the third middleware: the
// web-token branch is covered by `protected`, so only the device branch needs the
// attachment here.
func TestWatchOrWebReadAttachesDeviceOnTheWatchPath(t *testing.T) {
	auth := watchAuthForTest(t)
	device, token := pairedWatch(t, auth, watchauth.ScopeRead)

	var got identity.Principal
	var ok bool
	// protected is never reached on this path; allowAll documents that.
	handler := withWatchOrWebRead(auth, allowAll)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/watch/push/pending", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok || got.UserID != device.DeviceID {
		t.Fatalf("principal = %#v ok=%v, want the named device %q", got, ok, device.DeviceID)
	}
}
