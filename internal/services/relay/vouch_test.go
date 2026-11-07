package relay

import "testing"

func TestEncodeDecodeVouchRoundTrip(t *testing.T) {
	header := EncodeVouch("tenant-a", "alice")
	id, ok := DecodeVouch(header)
	if !ok {
		t.Fatalf("DecodeVouch(%q) not ok", header)
	}
	if id.UserID != "alice" || id.TenantID != "tenant-a" {
		t.Fatalf("round trip = %#v", id)
	}
}

// TestEncodeVouchOmitsEmptyOwner pins the fail-safe: with nobody to name, the
// header is absent so the node keeps its placeholder identity (which owns
// nothing) instead of scoping a per-owner surface to "".
func TestEncodeVouchOmitsEmptyOwner(t *testing.T) {
	if got := EncodeVouch("tenant-a", ""); got != "" {
		t.Fatalf("EncodeVouch with empty user = %q, want empty", got)
	}
	if got := EncodeVouch("", "   "); got != "" {
		t.Fatalf("EncodeVouch with blank user = %q, want empty", got)
	}
}

func TestEncodeVouchAllowsEmptyTenant(t *testing.T) {
	// Single-tenant deployments have no tenant on either side; that is the common
	// case today and must still carry the owner.
	id, ok := DecodeVouch(EncodeVouch("", "local-operator"))
	if !ok || id.UserID != "local-operator" || id.TenantID != "" {
		t.Fatalf("tenant-less round trip = %#v ok=%v", id, ok)
	}
}

// TestDecodeVouchRejectsUnusableValues pins that a half-parsed assertion is not
// trusted: the node must fall back to the placeholder rather than acting on a
// value it could not fully read.
func TestDecodeVouchRejectsUnusableValues(t *testing.T) {
	for name, raw := range map[string]string{
		"absent":       "",
		"blank":        "   ",
		"not json":     "alice",
		"ownerless":    `{"tenant":"t1"}`,
		"blank owner":  `{"tenant":"t1","user":"  "}`,
		"wrong shape":  `["alice"]`,
		"empty object": `{}`,
	} {
		if _, ok := DecodeVouch(raw); ok {
			t.Errorf("%s: DecodeVouch(%q) reported ok, want rejected", name, raw)
		}
	}
}
