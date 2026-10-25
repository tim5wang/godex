package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestJoinTokenRedeemsOnce is the property the credential exists for: a code is
// worth exactly one node enrolment. If this ever regresses, a leaked code can be
// replayed by anyone who saw it.
func TestJoinTokenRedeemsOnce(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "owner@example.com", "correct-horse")

	record, plaintext, err := service.CreateJoinToken(CreateJoinTokenInput{
		TenantID:  registered.Tenant.ID,
		CreatedBy: registered.User.ID,
		Label:     "workshop laptop",
	})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, JoinTokenPrefix) {
		t.Fatalf("token %q is missing the %q prefix", plaintext, JoinTokenPrefix)
	}
	if plaintext == "" || record.ID == "" {
		t.Fatal("empty token or id")
	}

	// The tenant and creator must be recoverable from the code alone: this is what
	// lets the center decide ownership without trusting the node's request body.
	redeemed, err := service.RedeemJoinToken(plaintext, "node-1")
	if err != nil {
		t.Fatalf("first redemption failed: %v", err)
	}
	if redeemed.TenantID != registered.Tenant.ID {
		t.Errorf("tenant = %q, want %q", redeemed.TenantID, registered.Tenant.ID)
	}

	if _, err := service.RedeemJoinToken(plaintext, "node-2"); !errors.Is(err, ErrInvalidJoinToken) {
		t.Fatalf("second redemption error = %v, want ErrInvalidJoinToken", err)
	}

	// The used marker must name the node that consumed it, so the audit trail can
	// say which device enrolled rather than just "someone did".
	listed, err := service.ListJoinTokens(registered.Tenant.ID)
	if err != nil {
		t.Fatalf("ListJoinTokens: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d tokens, want 1", len(listed))
	}
	if listed[0].UsedByNode != "node-1" || listed[0].UsedAt == "" {
		t.Errorf("used markers = %+v, want node-1 with a timestamp", listed[0])
	}
}

func TestJoinTokenRejectsExpired(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "owner@example.com", "correct-horse")

	_, plaintext, err := service.CreateJoinToken(CreateJoinTokenInput{
		TenantID: registered.Tenant.ID,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	service.SetNow(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if _, err := service.RedeemJoinToken(plaintext, "node-1"); !errors.Is(err, ErrInvalidJoinToken) {
		t.Fatalf("expired redemption error = %v, want ErrInvalidJoinToken", err)
	}
}

func TestJoinTokenRevoke(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "owner@example.com", "correct-horse")

	record, plaintext, err := service.CreateJoinToken(CreateJoinTokenInput{TenantID: registered.Tenant.ID})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	if err := service.RevokeJoinToken(record.ID); err != nil {
		t.Fatalf("RevokeJoinToken: %v", err)
	}
	if _, err := service.RedeemJoinToken(plaintext, "node-1"); !errors.Is(err, ErrInvalidJoinToken) {
		t.Fatalf("revoked redemption error = %v, want ErrInvalidJoinToken", err)
	}
	// Revoking twice is quiet (idempotent), but an unknown id is an error so a typo
	// does not look like success.
	if err := service.RevokeJoinToken(record.ID); err != nil {
		t.Errorf("second revoke = %v, want nil", err)
	}
	if err := service.RevokeJoinToken("jt_missing"); err == nil {
		t.Error("revoking an unknown id must fail")
	}
}

func TestJoinTokenUnknownAndMalformedRejected(t *testing.T) {
	service := newTestService(t)
	for _, bogus := range []string{"", "   ", "not-a-token", JoinTokenPrefix + "deadbeef"} {
		if _, err := service.RedeemJoinToken(bogus, "node-1"); !errors.Is(err, ErrInvalidJoinToken) {
			t.Errorf("RedeemJoinToken(%q) error = %v, want ErrInvalidJoinToken", bogus, err)
		}
	}
}

// TestJoinTokenIsScopedToItsTenant: a code issued for tenant A must never resolve
// to tenant B, or tenant isolation is decided by whoever holds a code.
func TestJoinTokenIsScopedToItsTenant(t *testing.T) {
	service := newTestService(t)
	tenantA := registerUser(t, service, "a@example.com", "correct-horse")
	tenantB := registerUser(t, service, "b@example.com", "correct-horse")

	_, tokenA, err := service.CreateJoinToken(CreateJoinTokenInput{TenantID: tenantA.Tenant.ID})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	redeemed, err := service.RedeemJoinToken(tokenA, "node-1")
	if err != nil {
		t.Fatalf("RedeemJoinToken: %v", err)
	}
	if redeemed.TenantID != tenantA.Tenant.ID {
		t.Errorf("token for A resolved to %q", redeemed.TenantID)
	}
	if redeemed.TenantID == tenantB.Tenant.ID {
		t.Error("token for A resolved to tenant B")
	}
	// Listing is tenant-scoped too.
	listedB, err := service.ListJoinTokens(tenantB.Tenant.ID)
	if err != nil {
		t.Fatalf("ListJoinTokens: %v", err)
	}
	if len(listedB) != 0 {
		t.Errorf("tenant B sees %d of tenant A's tokens", len(listedB))
	}
}

// TestJoinTokenPlaintextIsNotStored: the store keeps a hash, so a database leak
// does not hand out enrolment codes.
func TestJoinTokenPlaintextIsNotStored(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "owner@example.com", "correct-horse")

	_, plaintext, err := service.CreateJoinToken(CreateJoinTokenInput{TenantID: registered.Tenant.ID})
	if err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	var stored string
	if err := service.db.QueryRow(`SELECT token_hash FROM join_tokens LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read stored hash: %v", err)
	}
	if stored == plaintext {
		t.Fatal("the plaintext join token is stored verbatim")
	}
	if stored != hashToken(plaintext) {
		t.Errorf("stored value is not the token hash: %q", stored)
	}
}
