package accounts

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	service, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func registerUser(t *testing.T, service *Service, email, password string) RegisterResult {
	t.Helper()
	result, err := service.Register(RegisterInput{Email: email, Password: password})
	if err != nil {
		t.Fatalf("Register(%s): %v", email, err)
	}
	return result
}

func TestRegisterCreatesTenantWithOwnerMembership(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "Owner@Example.com", "correct-horse")

	if !result.Created {
		t.Error("new signup must report Created")
	}
	// Email is normalized so a case variant cannot register a second account.
	if result.User.Email != "owner@example.com" {
		t.Errorf("Email = %q, want normalized lowercase", result.User.Email)
	}
	if result.Member.Role != identity.RoleTenantOwner {
		t.Errorf("Role = %q, want %q", result.Member.Role, identity.RoleTenantOwner)
	}
	if result.Member.TenantID != result.Tenant.ID || result.User.ID == "" {
		t.Errorf("membership not linked: %#v", result.Member)
	}
	if result.Token == "" || !hasPrefix(result.Token, SessionTokenPrefix) {
		t.Errorf("Token = %q, want %s prefix", result.Token, SessionTokenPrefix)
	}
	// The password must never be readable back out.
	if result.User.PasswordHash != "" {
		t.Error("Register must not return the password hash")
	}
}

func TestRegisterRejectsDuplicateEmailCaseInsensitively(t *testing.T) {
	service := newTestService(t)
	registerUser(t, service, "dup@example.com", "correct-horse")

	_, err := service.Register(RegisterInput{Email: "DUP@example.com", Password: "another-good-one"})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterValidatesEmailAndPassword(t *testing.T) {
	service := newTestService(t)

	cases := []struct {
		name     string
		email    string
		password string
		want     error
	}{
		{name: "no at sign", email: "nope", password: "correct-horse", want: ErrInvalidEmail},
		{name: "domain without dot", email: "a@localhost", password: "correct-horse", want: ErrInvalidEmail},
		{name: "empty local part", email: "@example.com", password: "correct-horse", want: ErrInvalidEmail},
		{name: "two at signs", email: "a@b@example.com", password: "correct-horse", want: ErrInvalidEmail},
		{name: "whitespace", email: "a b@example.com", password: "correct-horse", want: ErrInvalidEmail},
		{name: "short password", email: "ok@example.com", password: "short", want: ErrWeakPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Register(RegisterInput{Email: tc.email, Password: tc.password})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLoginReturnsTenantScopedPrincipal(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "user@example.com", "correct-horse")

	principal, token, expires, err := service.Login(" User@Example.com ", "correct-horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if principal.Kind != identity.KindUser {
		t.Errorf("Kind = %q, want %q", principal.Kind, identity.KindUser)
	}
	if principal.TenantID != registered.Tenant.ID || principal.UserID != registered.User.ID {
		t.Errorf("principal not tenant-scoped: %#v", principal)
	}
	if principal.AuthMethod != "session" {
		t.Errorf("AuthMethod = %q, want session", principal.AuthMethod)
	}
	// A tenant user forwards to their own nodes and never executes on the center.
	if principal.CanExecuteOnCenter() {
		t.Error("a tenant user must not hold center_execution")
	}
	if !principal.HasEntitlement(identity.EntitlementRelayOnly) {
		t.Error("a tenant user must hold relay_only")
	}
	if token == "" || expires.Before(time.Now()) {
		t.Errorf("session not usable: token=%q expires=%v", token, expires)
	}
}

// TestLoginFailsIdenticallyForUnknownEmailAndWrongPassword pins the deliberate
// single error: distinguishing them tells an attacker which emails are registered.
func TestLoginFailsIdenticallyForUnknownEmailAndWrongPassword(t *testing.T) {
	service := newTestService(t)
	registerUser(t, service, "real@example.com", "correct-horse")

	_, _, _, unknownErr := service.Login("ghost@example.com", "correct-horse")
	_, _, _, wrongErr := service.Login("real@example.com", "wrong-password")

	if !errors.Is(unknownErr, ErrInvalidCredentials) {
		t.Fatalf("unknown email err = %v, want ErrInvalidCredentials", unknownErr)
	}
	if !errors.Is(wrongErr, ErrInvalidCredentials) {
		t.Fatalf("wrong password err = %v, want ErrInvalidCredentials", wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("errors differ and leak account existence: %q vs %q", unknownErr, wrongErr)
	}
}

func TestAuthenticateResolvesSessionToPrincipal(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "user@example.com", "correct-horse")

	principal, ok := service.Authenticate(registered.Token)
	if !ok {
		t.Fatal("a fresh session token must authenticate")
	}
	if principal.TenantID != registered.Tenant.ID {
		t.Errorf("TenantID = %q, want %q", principal.TenantID, registered.Tenant.ID)
	}
	if !principal.HasEntitlement(identity.EntitlementRelayOnly) {
		t.Error("session principal must carry the tenant plan entitlement")
	}

	// Shape check first: something that is not a session token is refused without
	// a lookup, so a web token can never be mistaken for a session.
	for _, bogus := range []string{"", "web-secret", "gdx_abc", "gxs_"} {
		if _, ok := service.Authenticate(bogus); ok {
			t.Errorf("Authenticate(%q) accepted a non-session token", bogus)
		}
	}
}

func TestAuthenticateRejectsExpiredSessionAndCleansItUp(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "user@example.com", "correct-horse")

	// Jump past the TTL.
	service.SetNow(func() time.Time { return time.Now().Add(sessionTTL + time.Hour) })
	if _, ok := service.Authenticate(registered.Token); ok {
		t.Fatal("an expired session must not authenticate")
	}

	// The row must be gone, not just rejected.
	var count int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, hashToken(registered.Token)).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("expired session row not cleaned up: count=%d", count)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "user@example.com", "correct-horse")

	if err := service.Logout(registered.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, ok := service.Authenticate(registered.Token); ok {
		t.Fatal("a logged-out session must not authenticate")
	}
	// Idempotent: logging out twice, or with a bogus token, is not an error.
	if err := service.Logout(registered.Token); err != nil {
		t.Errorf("second Logout = %v, want nil", err)
	}
	if err := service.Logout("not-a-session"); err != nil {
		t.Errorf("Logout(non-session) = %v, want nil", err)
	}
}

// TestRoleChangeAppliesImmediately is why Authenticate rebuilds the principal from
// stored facts instead of trusting the session row: a demotion must not linger
// until the session expires.
func TestRoleChangeAppliesImmediately(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "user@example.com", "correct-horse")

	principal, ok := service.Authenticate(registered.Token)
	if !ok || principal.PlatformRole != "" {
		t.Fatalf("unexpected initial principal: %#v ok=%v", principal, ok)
	}

	// Promote to platform admin (as the operator would, out of band).
	if _, err := service.db.Exec(`UPDATE users SET platform_role = ? WHERE id = ?`, identity.RolePlatformAdmin, registered.User.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	principal, ok = service.Authenticate(registered.Token)
	if !ok {
		t.Fatal("session must still authenticate after a role change")
	}
	if !principal.IsPlatformAdmin() || !principal.CanExecuteOnCenter() {
		t.Errorf("promotion did not take effect on the existing session: %#v", principal)
	}
}

// TestMembershipRevocationInvalidatesSession: removing the membership must end the
// session's access to that tenant rather than leaving a tenant-scoped principal
// pointing at a tenant the user no longer belongs to.
func TestMembershipRevocationInvalidatesSession(t *testing.T) {
	service := newTestService(t)
	registered := registerUser(t, service, "user@example.com", "correct-horse")

	if _, err := service.db.Exec(`DELETE FROM memberships WHERE user_id = ?`, registered.User.ID); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}
	if _, ok := service.Authenticate(registered.Token); ok {
		t.Fatal("a session whose membership was revoked must not authenticate")
	}
}

// TestTenantIsolationBetweenSignups is the SaaS acceptance criterion in miniature:
// two signups land in two tenants and neither can see the other's tenant id.
func TestTenantIsolationBetweenSignups(t *testing.T) {
	service := newTestService(t)
	first := registerUser(t, service, "a@example.com", "correct-horse")
	second := registerUser(t, service, "b@example.com", "correct-horse")

	if first.Tenant.ID == second.Tenant.ID {
		t.Fatal("two signups must not share a tenant")
	}
	firstPrincipal, _, _, err := service.Login("a@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("login a: %v", err)
	}
	secondPrincipal, _, _, err := service.Login("b@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("login b: %v", err)
	}
	if firstPrincipal.TenantID == secondPrincipal.TenantID {
		t.Fatal("logins resolved to the same tenant")
	}
	// Cross-tenant reach must be refused by the shared policy.
	decision := (identity.Policy{}).Authorize(firstPrincipal, identity.NodeRef{ID: "node-b", TenantID: second.Tenant.ID})
	if decision.Allowed {
		t.Fatalf("tenant A reached tenant B's node: %q", decision.Reason)
	}
}

func TestDefaultTenantNameFallsBackToEmailLocalPart(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "acme-team@example.com", "correct-horse")
	if result.Tenant.Name != "acme-team" {
		t.Errorf("Tenant.Name = %q, want the email local part", result.Tenant.Name)
	}
}

func TestServiceRejectsMissingStateDir(t *testing.T) {
	if _, err := New("  "); err == nil {
		t.Fatal("expected an error for an empty state dir")
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	service, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	registered := registerUser(t, service, "user@example.com", "correct-horse")
	token := registered.Token
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, ok := reopened.Authenticate(token); !ok {
		t.Fatal("session must survive a restart")
	}
	principal, _, _, err := reopened.Login("user@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("login after reopen: %v", err)
	}
	if principal.TenantID != registered.Tenant.ID {
		t.Errorf("TenantID = %q, want %q", principal.TenantID, registered.Tenant.ID)
	}
}

// TestStoreLivesUnderStateDir pins the file location: the store must not scatter
// account data outside the state directory.
func TestStoreLivesUnderStateDir(t *testing.T) {
	dir := t.TempDir()
	service, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if service.dir != dir {
		t.Errorf("service.dir = %q, want %q", service.dir, dir)
	}
	if _, err := filepath.Glob(filepath.Join(dir, "accounts.sqlite")); err != nil {
		t.Fatalf("glob: %v", err)
	}
}

func hasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
