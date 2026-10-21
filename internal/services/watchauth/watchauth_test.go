package watchauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readFile is a tiny helper so assertions read cleanly.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "watch", "devices.json"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// pairDevice walks the full handshake and returns the device plus its token.
func pairDevice(t *testing.T, s *Service, scopes []string) (Device, string) {
	t.Helper()
	p, err := s.StartPairing("Pixel Watch", "watch")
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	// Before approval the watch must not get a token.
	res, err := s.ExchangeToken(p.DeviceCode)
	if err != nil {
		t.Fatalf("ExchangeToken(pending): %v", err)
	}
	if res.Status != "pending" {
		t.Fatalf("expected pending before approval, got %q", res.Status)
	}
	if res.Token != "" {
		t.Fatal("token must not be issued before approval")
	}

	device, err := s.ApprovePairing(p.UserCode, scopes)
	if err != nil {
		t.Fatalf("ApprovePairing: %v", err)
	}
	res, err = s.ExchangeToken(p.DeviceCode)
	if err != nil {
		t.Fatalf("ExchangeToken(approved): %v", err)
	}
	if res.Status != "approved" {
		t.Fatalf("expected approved, got %q", res.Status)
	}
	if res.Token == "" {
		t.Fatal("expected a token after approval")
	}
	if res.Device == nil || res.Device.DeviceID != device.DeviceID {
		t.Fatalf("exchange returned wrong device: %+v", res.Device)
	}
	return device, res.Token
}

func TestPairingRequiresApprovalBeforeIssuingToken(t *testing.T) {
	s := newTestService(t)
	_, token := pairDevice(t, s, nil)

	device, ok := s.Authenticate(token)
	if !ok {
		t.Fatal("freshly paired token should authenticate")
	}
	if device.Kind != "watch" {
		t.Fatalf("expected kind=watch, got %q", device.Kind)
	}
	if !device.HasScope(ScopeRead) || !device.HasScope(ScopeCommand) || !device.HasScope(ScopeApprove) {
		t.Fatalf("default scopes missing: %v", device.Scopes)
	}
}

func TestUserCodeIsAcceptedWithSeparators(t *testing.T) {
	s := newTestService(t)
	p, err := s.StartPairing("Watch", "watch")
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	// The watch renders the code as "1234 5678"; normalisation must cope.
	spaced := p.UserCode[:4] + " " + p.UserCode[4:]
	if _, err := s.ApprovePairing(spaced, nil); err != nil {
		t.Fatalf("ApprovePairing with spaced code: %v", err)
	}
}

func TestPairingCodeIsSingleUse(t *testing.T) {
	s := newTestService(t)
	p, err := s.StartPairing("Watch", "watch")
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	if _, err := s.ApprovePairing(p.UserCode, nil); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := s.ApprovePairing(p.UserCode, nil); err == nil {
		t.Fatal("second approve with the same code must fail")
	}
}

func TestPairingCodeExpires(t *testing.T) {
	s := newTestService(t)
	now := time.Now()
	s.SetNow(func() time.Time { return now })

	p, err := s.StartPairing("Watch", "watch")
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	// Jump past the pairing window.
	s.SetNow(func() time.Time { return now.Add(defaultPairTTL + time.Second) })

	if _, err := s.ApprovePairing(p.UserCode, nil); err == nil {
		t.Fatal("expired pairing code must not be approved")
	}
	res, err := s.ExchangeToken(p.DeviceCode)
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if res.Status != "expired" {
		t.Fatalf("expected expired, got %q", res.Status)
	}
}

func TestExchangeTokenIsSingleUse(t *testing.T) {
	s := newTestService(t)
	p, err := s.StartPairing("Watch", "watch")
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	if _, err := s.ApprovePairing(p.UserCode, nil); err != nil {
		t.Fatalf("ApprovePairing: %v", err)
	}
	first, err := s.ExchangeToken(p.DeviceCode)
	if err != nil || first.Status != "approved" {
		t.Fatalf("first exchange: %+v err=%v", first, err)
	}
	second, err := s.ExchangeToken(p.DeviceCode)
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	if second.Token != "" {
		t.Fatal("device code must not mint a second token")
	}
}

func TestScopesRejectAdminAndUnknown(t *testing.T) {
	cases := [][]string{
		{"watch:admin"},
		{"watch:read", "watch:admin"},
		{"totally-made-up"},
	}
	for _, scopes := range cases {
		if _, err := NormalizeScopes(scopes); err == nil {
			t.Fatalf("scopes %v must be rejected", scopes)
		}
	}
}

func TestScopesDefaultAndDedupe(t *testing.T) {
	got, err := NormalizeScopes(nil)
	if err != nil {
		t.Fatalf("NormalizeScopes(nil): %v", err)
	}
	if len(got) != len(DefaultScopes) {
		t.Fatalf("expected default scopes, got %v", got)
	}

	got, err = NormalizeScopes([]string{"watch:approve", "watch:read", "watch:read", " watch:approve "})
	if err != nil {
		t.Fatalf("NormalizeScopes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected dedupe to 2 entries, got %v", got)
	}
	if got[0] != ScopeApprove || got[1] != ScopeRead {
		t.Fatalf("expected sorted scopes, got %v", got)
	}
}

func TestRevokedTokenStopsAuthenticating(t *testing.T) {
	s := newTestService(t)
	device, token := pairDevice(t, s, nil)

	if _, ok := s.Authenticate(token); !ok {
		t.Fatal("token should work before revocation")
	}
	if _, err := s.Revoke(device.DeviceID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := s.Authenticate(token); ok {
		t.Fatal("revoked token must not authenticate")
	}
	survived, ok := s.Get(device.DeviceID)
	if !ok || !survived.Revoked {
		t.Fatalf("device should be marked revoked: %+v ok=%v", survived, ok)
	}
}

func TestRevokeUnknownDeviceFails(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Revoke("wch_nope"); err == nil {
		t.Fatal("revoking an unknown device must fail")
	}
}

func TestExpiredTokenStopsAuthenticating(t *testing.T) {
	s := newTestService(t)
	now := time.Now()
	s.SetNow(func() time.Time { return now })
	_, token := pairDevice(t, s, nil)

	if _, ok := s.Authenticate(token); !ok {
		t.Fatal("token should work inside its lifetime")
	}
	s.SetNow(func() time.Time { return now.Add(defaultTokenTTL + time.Hour) })
	if _, ok := s.Authenticate(token); ok {
		t.Fatal("token past its lifetime must not authenticate")
	}
}

func TestDevicesPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch", "devices.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	device, token := pairDevice(t, s, []string{ScopeRead})

	reloaded, err := New(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.Authenticate(token)
	if !ok {
		t.Fatal("token should survive a restart")
	}
	if got.DeviceID != device.DeviceID {
		t.Fatalf("device id changed across restart: %q vs %q", got.DeviceID, device.DeviceID)
	}
	if got.HasScope(ScopeCommand) {
		t.Fatalf("scope narrowing lost across restart: %v", got.Scopes)
	}
}

func TestTokenIsNeverStoredInPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch", "devices.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, token := pairDevice(t, s, nil)

	raw := readFile(t, path)
	if strings.Contains(raw, token) {
		t.Fatal("plaintext token leaked into the on-disk registry")
	}
	if !strings.Contains(raw, TokenPrefix) {
		// The hash of a wk_ token must not contain the prefix either; the
		// registry stores only hex digests.
		if strings.Contains(raw, "wk_") {
			t.Fatal("token prefix leaked into the on-disk registry")
		}
	}
}

func TestListReturnsPairedDevices(t *testing.T) {
	s := newTestService(t)
	pairDevice(t, s, nil)
	pairDevice(t, s, nil)

	devices := s.List(context.Background())
	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devices))
	}
}

func TestUnknownDeviceCodeIsExpired(t *testing.T) {
	s := newTestService(t)
	res, err := s.ExchangeToken("dc_does-not-exist")
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if res.Status != "expired" {
		t.Fatalf("unknown device code should read as expired, got %q", res.Status)
	}
}

func TestAuthenticateRejectsEmptyToken(t *testing.T) {
	s := newTestService(t)
	if _, ok := s.Authenticate("   "); ok {
		t.Fatal("blank token must not authenticate")
	}
}
