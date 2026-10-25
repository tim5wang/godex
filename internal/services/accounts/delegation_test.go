package accounts

import (
	"errors"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
)

// addMemberLocked inserts a membership directly, standing in for an invitation
// flow: several tests need a NON-owner member, and Register always creates an
// owner in a fresh tenant.
func addMember(t *testing.T, service *Service, tenantID, userID, role string) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, err := service.db.Exec(
		`INSERT INTO memberships (tenant_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`,
		tenantID, userID, role, service.timestamp()); err != nil {
		t.Fatalf("insert member: %v", err)
	}
}

func TestCreateDelegationRequiresATenantAdmin(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")
	outsider := registerUser(t, service, "outsider@example.com", "correct-horse")

	// The outsider owns a DIFFERENT tenant, so this also proves the check is
	// "admin of THAT tenant" rather than "admin of any tenant".
	_, err := service.CreateDelegation(CreateDelegationInput{
		TenantID:       owner.Tenant.ID,
		OperatorUserID: "local-operator",
		GrantedBy:      outsider.User.ID,
	})
	if !errors.Is(err, ErrNotTenantAdmin) {
		t.Fatalf("err = %v, want ErrNotTenantAdmin", err)
	}

	// A plain member of the tenant must not be able to authorize either: the
	// grant is the tenant's decision, and that is expressed by its admins.
	member := registerUser(t, service, "member@example.com", "correct-horse")
	addMember(t, service, owner.Tenant.ID, member.User.ID, identity.RoleTenantMember)
	_, err = service.CreateDelegation(CreateDelegationInput{
		TenantID:       owner.Tenant.ID,
		OperatorUserID: "local-operator",
		GrantedBy:      member.User.ID,
	})
	if !errors.Is(err, ErrNotTenantAdmin) {
		t.Fatalf("plain member err = %v, want ErrNotTenantAdmin", err)
	}
}

func TestCreateDelegationCapsWindowAndDefaults(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	// Zero means "use the default", not "no expiry": absent is the common case and
	// must not be the most dangerous one.
	record, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}
	if record.Scope != identity.ScopeReadNodes {
		t.Errorf("Scope = %q, want the read-only default %q", record.Scope, identity.ScopeReadNodes)
	}
	if !record.Active(time.Now().UTC()) {
		t.Errorf("a fresh default grant is not active: %+v", record)
	}

	// Over the cap must be refused rather than silently clamped: a tenant that
	// asked for a year should learn the grant is shorter, not discover it later.
	if _, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
		TTL: 365 * 24 * time.Hour,
	}); err == nil {
		t.Fatal("a window beyond MaxDelegationTTL was accepted")
	}

	maxed, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
		TTL: identity.MaxDelegationTTL,
	})
	if err != nil {
		t.Fatalf("the documented maximum must be accepted: %v", err)
	}
	if !maxed.Active(time.Now().UTC()) {
		t.Errorf("grant at the cap is not active: %+v", maxed)
	}
}

func TestCreateDelegationRequiresOperatorIdentity(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	// A grant that names nobody would be exactly the "any operator, any time"
	// bypass the design rejects, so it must not be creatable.
	if _, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, GrantedBy: owner.User.ID,
	}); err == nil {
		t.Fatal("a delegation without operator_user_id was accepted")
	}
}

func TestActiveDelegationForAppliesExpiryAndRevocation(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	record, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}

	if _, ok := service.ActiveDelegationFor("local-operator", owner.Tenant.ID); !ok {
		t.Fatal("a live grant was not found")
	}
	// Another operator must not be covered by it.
	if _, ok := service.ActiveDelegationFor("other-operator", owner.Tenant.ID); ok {
		t.Error("a grant covered an operator it does not name")
	}
	// Nor another tenant.
	if _, ok := service.ActiveDelegationFor("local-operator", "tnt_other"); ok {
		t.Error("a grant covered a tenant it does not name")
	}

	// Revocation takes effect immediately — that is the tenant's whole point.
	if err := service.RevokeDelegation(record.ID, owner.User.ID); err != nil {
		t.Fatalf("RevokeDelegation: %v", err)
	}
	if _, ok := service.ActiveDelegationFor("local-operator", owner.Tenant.ID); ok {
		t.Error("a revoked grant still authorized access")
	}

	// Expiry is honoured too: move the clock past the window on a fresh grant.
	second, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}
	service.SetNow(func() time.Time { return time.Now().UTC().Add(3 * time.Hour) })
	if _, ok := service.ActiveDelegationFor("local-operator", owner.Tenant.ID); ok {
		t.Errorf("an expired grant (%s) still authorized access", second.ID)
	}
}

func TestRevokeDelegationRequiresTenantAdminAndKnownID(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")
	outsider := registerUser(t, service, "outsider@example.com", "correct-horse")

	record, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}

	// Revocation reads the tenant from the record, so an unrelated admin cannot
	// revoke a grant by asserting someone else's tenant id.
	if err := service.RevokeDelegation(record.ID, outsider.User.ID); !errors.Is(err, ErrNotTenantAdmin) {
		t.Fatalf("err = %v, want ErrNotTenantAdmin", err)
	}
	if err := service.RevokeDelegation("dlg_missing", owner.User.ID); !errors.Is(err, ErrDelegationNotFound) {
		t.Fatalf("err = %v, want ErrDelegationNotFound", err)
	}
}

func TestListOperatorDelegationsIsOperatorScoped(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	if _, err := service.CreateDelegation(CreateDelegationInput{
		TenantID: owner.Tenant.ID, OperatorUserID: "local-operator", GrantedBy: owner.User.ID,
	}); err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}

	mine, err := service.ListOperatorDelegations("local-operator")
	if err != nil {
		t.Fatalf("ListOperatorDelegations: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("got %d grants for the named operator, want 1", len(mine))
	}
	// The operator view must not enumerate other operators' grants: that would turn
	// the console into a map of who is inspecting whom.
	others, err := service.ListOperatorDelegations("somebody-else")
	if err != nil {
		t.Fatalf("ListOperatorDelegations: %v", err)
	}
	if len(others) != 0 {
		t.Errorf("another operator saw %d grants, want 0", len(others))
	}
}

func TestDelegationAuditIsTenantScopedAndOrderedNewestFirst(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	for _, entry := range []DelegationAudit{
		{DelegationID: "dlg_1", TenantID: owner.Tenant.ID, OperatorUserID: "local-operator",
			Action: "GET /control/nodes", Target: "node list (1 visible)", Allowed: true, Reason: identity.ReasonPlatformAdmin},
		{DelegationID: "dlg_1", TenantID: owner.Tenant.ID, OperatorUserID: "local-operator",
			Action: "GET /control/nodes/{id}", Target: "node a-own", Allowed: true, Reason: identity.ReasonPlatformAdmin},
		{DelegationID: "dlg_2", TenantID: "tnt_other", OperatorUserID: "local-operator",
			Action: "GET /control/nodes", Target: "other tenant", Allowed: true},
	} {
		if err := service.RecordDelegationUse(entry); err != nil {
			t.Fatalf("RecordDelegationUse: %v", err)
		}
	}

	entries, err := service.ListDelegationAudit(owner.Tenant.ID, 100)
	if err != nil {
		t.Fatalf("ListDelegationAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d audit rows, want only this tenant's 2 (another tenant leaked in)", len(entries))
	}
	if entries[0].Target != "node a-own" {
		t.Errorf("newest-first ordering not applied: first row = %+v", entries[0])
	}
	if !entries[0].Allowed {
		t.Error("the allowed flag was not round-tripped")
	}
}

func TestConsentRecordsCaptureTheAgreementTrail(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	if err := service.RecordConsent(identity.ConsentRecord{
		TenantID: owner.Tenant.ID, UserID: owner.User.ID,
		TermsVersion: identity.CurrentTermsVersion, SourceIP: "203.0.113.7",
	}); err != nil {
		t.Fatalf("RecordConsent: %v", err)
	}

	// A record without a version cannot be tied to the text agreed to, so it must
	// not be storable.
	if err := service.RecordConsent(identity.ConsentRecord{TenantID: owner.Tenant.ID}); err == nil {
		t.Error("a consent record without a terms version was accepted")
	}

	records, err := service.ConsentRecords(owner.Tenant.ID)
	if err != nil {
		t.Fatalf("ConsentRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d consent records, want 1", len(records))
	}
	if records[0].TermsVersion != identity.CurrentTermsVersion || records[0].SourceIP != "203.0.113.7" {
		t.Errorf("consent record = %+v, want version+source preserved", records[0])
	}
	if records[0].AcceptedAt == "" {
		t.Error("AcceptedAt was not filled in; the agreement has no timestamp")
	}
}

// TestIsTenantAdminDistinguishesOwnerAdminAndMember documents the role boundary the
// grant surface depends on.
func TestIsTenantAdminDistinguishesOwnerAdminAndMember(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")

	if !service.IsTenantAdmin(owner.Tenant.ID, owner.User.ID) {
		t.Error("the signup owner is not treated as a tenant admin")
	}

	admin := registerUser(t, service, "admin@example.com", "correct-horse")
	addMember(t, service, owner.Tenant.ID, admin.User.ID, identity.RoleTenantAdmin)
	if !service.IsTenantAdmin(owner.Tenant.ID, admin.User.ID) {
		t.Error("a tenant admin is not treated as a tenant admin")
	}

	member := registerUser(t, service, "member@example.com", "correct-horse")
	addMember(t, service, owner.Tenant.ID, member.User.ID, identity.RoleTenantMember)
	if service.IsTenantAdmin(owner.Tenant.ID, member.User.ID) {
		t.Error("a plain member is treated as a tenant admin")
	}
	// A user with no membership at all must not pass either.
	if service.IsTenantAdmin(owner.Tenant.ID, "usr_absent") {
		t.Error("an unknown user is treated as a tenant admin")
	}
}
