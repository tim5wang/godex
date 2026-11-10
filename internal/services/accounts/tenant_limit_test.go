package accounts

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
)

// TestPolicyDefaultsToUnlimited pins the default that keeps every allowance purely
// additive: a tenant with no stored policy row has no limit at all.
func TestPolicyDefaultsToUnlimited(t *testing.T) {
	service := newTestService(t)
	for _, tenantID := range []string{"tenant-unknown", ""} {
		if got := service.TenantMaxDevicesPerUser(tenantID); got != 0 {
			t.Errorf("TenantMaxDevicesPerUser(%q) = %d, want 0 (unlimited)", tenantID, got)
		}
		if got := service.TenantMaxUsers(tenantID); got != 0 {
			t.Errorf("TenantMaxUsers(%q) = %d, want 0 (unlimited)", tenantID, got)
		}
	}
}

// TestSetPolicyLimitsRoundTrip covers both knobs: they are two numbers an operator
// sets independently, so each must survive a write/read cycle on its own and neither
// may disturb the other.
func TestSetPolicyLimitsRoundTrip(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID

	policy, err := service.SetTenantMaxDevicesPerUser(tenantID, 2)
	if err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}
	if policy.MaxDevicesPerUser != 2 {
		t.Errorf("returned MaxDevicesPerUser = %d, want 2", policy.MaxDevicesPerUser)
	}
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("TenantMaxDevicesPerUser = %d, want 2", got)
	}
	// Setting the member limit must not disturb the device limit.
	if _, err := service.SetTenantMaxUsers(tenantID, 5); err != nil {
		t.Fatalf("SetTenantMaxUsers: %v", err)
	}
	if got := service.TenantMaxUsers(tenantID); got != 5 {
		t.Errorf("TenantMaxUsers = %d, want 5", got)
	}
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("member write changed the device allowance to %d, want 2", got)
	}

	// A second write replaces rather than accumulates, and a non-positive value clears
	// the limit rather than meaning "none" — the same encoding the default uses.
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 3); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser (update): %v", err)
	}
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 3 {
		t.Errorf("after update = %d, want 3", got)
	}
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, -1); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 0 {
		t.Errorf("after clearing = %d, want 0 (unlimited)", got)
	}
}

// TestPolicyWritesDoNotClobberSharing is the reason the policies are written
// separately: each endpoint updates only its own column, so setting an allowance
// cannot silently revert a sharing decision the operator made.
func TestPolicyWritesDoNotClobberSharing(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID

	if _, err := service.SetTenantSharing(tenantID, false); err != nil {
		t.Fatalf("SetTenantSharing: %v", err)
	}
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 3); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}
	if _, err := service.SetTenantMaxUsers(tenantID, 4); err != nil {
		t.Fatalf("SetTenantMaxUsers: %v", err)
	}
	if service.TenantSharingEnabled(tenantID) {
		t.Error("setting an allowance re-enabled sharing")
	}

	// And the reverse direction: toggling sharing must not drop either allowance.
	if _, err := service.SetTenantSharing(tenantID, true); err != nil {
		t.Fatalf("SetTenantSharing (re-enable): %v", err)
	}
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 3 {
		t.Errorf("sharing toggle changed the device allowance to %d, want 3", got)
	}
	if got := service.TenantMaxUsers(tenantID); got != 4 {
		t.Errorf("sharing toggle changed the member allowance to %d, want 4", got)
	}
}

// TestSetPolicyLimitsRejectMissingTenant: the store still refuses to record policy
// that belongs to nobody.
func TestSetPolicyLimitsRejectMissingTenant(t *testing.T) {
	service := newTestService(t)
	if _, err := service.SetTenantMaxDevicesPerUser("  ", 2); err == nil {
		t.Fatal("expected an error for a missing tenant id (devices)")
	}
	if _, err := service.SetTenantMaxUsers("  ", 5); err == nil {
		t.Fatal("expected an error for a missing tenant id (members)")
	}
}

func TestListTenantsReportsTheAllowances(t *testing.T) {
	service := newTestService(t)
	limited := registerUser(t, service, "limited@example.com", "correct-horse")
	unlimited := registerUser(t, service, "free@example.com", "correct-horse")

	if _, err := service.SetTenantMaxDevicesPerUser(limited.Tenant.ID, 4); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}
	if _, err := service.SetTenantMaxUsers(limited.Tenant.ID, 3); err != nil {
		t.Fatalf("SetTenantMaxUsers: %v", err)
	}
	summaries, err := service.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	gotDevices := map[string]int{}
	gotUsers := map[string]int{}
	for _, summary := range summaries {
		gotDevices[summary.Tenant.ID] = summary.MaxDevicesPerUser
		gotUsers[summary.Tenant.ID] = summary.MaxUsers
	}
	if gotDevices[limited.Tenant.ID] != 4 || gotUsers[limited.Tenant.ID] != 3 {
		t.Errorf("limited tenant = (%d devices, %d users), want (4, 3)",
			gotDevices[limited.Tenant.ID], gotUsers[limited.Tenant.ID])
	}
	if gotDevices[unlimited.Tenant.ID] != 0 || gotUsers[unlimited.Tenant.ID] != 0 {
		t.Errorf("unlimited tenant = (%d devices, %d users), want (0, 0)",
			gotDevices[unlimited.Tenant.ID], gotUsers[unlimited.Tenant.ID])
	}
}

// TestTenantMaxDailyBytesDefaultsToUnlimited mirrors the node-allowance default:
// a tenant with no stored policy row has no traffic limit, so adding the column
// cannot silently start refusing traffic for tenants that predate it.
func TestTenantMaxDailyBytesDefaultsToUnlimited(t *testing.T) {
	service := newTestService(t)
	if got := service.TenantMaxDailyBytes("tenant-unknown"); got != 0 {
		t.Fatalf("TenantMaxDailyBytes(unknown) = %d, want 0 (unlimited)", got)
	}
	if got := service.TenantMaxDailyBytes(""); got != 0 {
		t.Fatalf("TenantMaxDailyBytes(\"\") = %d, want 0 (unlimited)", got)
	}
}

func TestSetTenantMaxDailyBytesRoundTrips(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID

	const hundredMB = 100 * 1024 * 1024
	policy, err := service.SetTenantMaxDailyBytes(tenantID, hundredMB)
	if err != nil {
		t.Fatalf("SetTenantMaxDailyBytes: %v", err)
	}
	if policy.MaxDailyBytes != hundredMB {
		t.Errorf("returned MaxDailyBytes = %d, want %d", policy.MaxDailyBytes, hundredMB)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != hundredMB {
		t.Errorf("TenantMaxDailyBytes = %d, want %d", got, hundredMB)
	}

	// A negative value clears the limit rather than meaning "no traffic", the same
	// encoding the node allowance uses.
	if _, err := service.SetTenantMaxDailyBytes(tenantID, -1); err != nil {
		t.Fatalf("clear limit: %v", err)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != 0 {
		t.Errorf("after clearing, MaxDailyBytes = %d, want 0 (unlimited)", got)
	}
}

// TestDailyByteLimitDoesNotClobberTheOtherPolicies: each endpoint writes only its
// own column, so setting a traffic allowance cannot silently revert a node
// allowance or a sharing decision the operator made separately.
func TestDailyByteLimitDoesNotClobberTheOtherPolicies(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID

	if _, err := service.SetTenantSharing(tenantID, false); err != nil {
		t.Fatalf("SetTenantSharing: %v", err)
	}
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}
	if _, err := service.SetTenantMaxDailyBytes(tenantID, 4096); err != nil {
		t.Fatalf("SetTenantMaxDailyBytes: %v", err)
	}

	if service.TenantSharingEnabled(tenantID) {
		t.Error("sharing switch was reset by writing the byte allowance")
	}
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("MaxDevicesPerUser = %d, want 2 (unchanged by the byte write)", got)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != 4096 {
		t.Errorf("MaxDailyBytes = %d, want 4096", got)
	}
	// And the reverse direction: a device write must not clear the bytes.
	if _, err := service.SetTenantMaxDevicesPerUser(tenantID, 3); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser (second): %v", err)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != 4096 {
		t.Errorf("MaxDailyBytes after a device write = %d, want 4096", got)
	}
}

// TestFreeTierDefaultsAreZeroByDefault: an unconfigured center must behave exactly as
// it did before these knobs existed — unlimited, and no policy row written.
func TestFreeTierDefaultsAreZeroByDefault(t *testing.T) {
	service := newTestService(t)
	maxUsers, maxDevices, maxDailyBytes := service.FreeTierDefaults()
	if maxUsers != 0 || maxDevices != 0 || maxDailyBytes != 0 {
		t.Fatalf("defaults = (%d, %d, %d), want all 0 = unlimited", maxUsers, maxDevices, maxDailyBytes)
	}

	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID
	if got := service.TenantMaxDevicesPerUser(tenantID); got != 0 {
		t.Errorf("fresh signup MaxDevicesPerUser = %d, want 0 (unlimited)", got)
	}
	if got := service.TenantMaxUsers(tenantID); got != 0 {
		t.Errorf("fresh signup MaxUsers = %d, want 0 (unlimited)", got)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != 0 {
		t.Errorf("fresh signup MaxDailyBytes = %d, want 0 (unlimited)", got)
	}
}

// TestFreeTierDefaultsApplyToNewSignups is the point of the feature: a self-serve
// signup starts with the operator's free allowance instead of being unlimited.
func TestFreeTierDefaultsApplyToNewSignups(t *testing.T) {
	service := newTestService(t)
	const hundredMB = 100 * 1024 * 1024
	service.SetFreeTierDefaults(2, 2, hundredMB)

	fresh := registerUser(t, service, "new@example.com", "correct-horse")
	if got := service.TenantMaxUsers(fresh.Tenant.ID); got != 2 {
		t.Errorf("fresh signup MaxUsers = %d, want 2", got)
	}
	if got := service.TenantMaxDevicesPerUser(fresh.Tenant.ID); got != 2 {
		t.Errorf("fresh signup MaxDevicesPerUser = %d, want 2", got)
	}
	if got := service.TenantMaxDailyBytes(fresh.Tenant.ID); got != hundredMB {
		t.Errorf("fresh signup MaxDailyBytes = %d, want %d", got, hundredMB)
	}

	// Defaults apply to SIGNUP only. A tenant that already exists keeps the policy it
	// was created with, so raising a default cannot silently change the terms someone
	// already enrolled under.
	before := registerUser(t, service, "before@example.com", "correct-horse")
	service.SetFreeTierDefaults(99, 99, 999)
	if got := service.TenantMaxDevicesPerUser(before.Tenant.ID); got != 2 {
		t.Errorf("existing tenant MaxDevicesPerUser = %d, want 2 (defaults are not retroactive)", got)
	}
	if got := service.TenantMaxUsers(before.Tenant.ID); got != 2 {
		t.Errorf("existing tenant MaxUsers = %d, want 2 (not retroactive)", got)
	}
	if got := service.TenantMaxDailyBytes(before.Tenant.ID); got != hundredMB {
		t.Errorf("existing tenant MaxDailyBytes = %d, want %d (not retroactive)", got, hundredMB)
	}

	// A later signup gets the NEW default.
	after := registerUser(t, service, "after@example.com", "correct-horse")
	if got := service.TenantMaxDevicesPerUser(after.Tenant.ID); got != 99 {
		t.Errorf("signup after raising the default: MaxDevicesPerUser = %d, want 99", got)
	}
}

// TestFreeTierDefaultsAreIndependent: each knob may be set alone, and 0 for the
// others must stay "unlimited" rather than being read as "none". The three are
// separate products of the plan, so collapsing them (setting one and clearing the
// rest) would silently change terms an operator never touched.
func TestFreeTierDefaultsAreIndependent(t *testing.T) {
	service := newTestService(t)
	service.SetFreeTierDefaults(0, 2, 0)
	onlyDevices := registerUser(t, service, "devices@example.com", "correct-horse")
	if got := service.TenantMaxDevicesPerUser(onlyDevices.Tenant.ID); got != 2 {
		t.Errorf("MaxDevicesPerUser = %d, want 2", got)
	}
	if got := service.TenantMaxUsers(onlyDevices.Tenant.ID); got != 0 {
		t.Errorf("MaxUsers = %d, want 0 (unlimited: only the device knob was set)", got)
	}
	if got := service.TenantMaxDailyBytes(onlyDevices.Tenant.ID); got != 0 {
		t.Errorf("MaxDailyBytes = %d, want 0 (unlimited: only the device knob was set)", got)
	}

	service.SetFreeTierDefaults(0, 0, 4096)
	onlyBytes := registerUser(t, service, "bytes@example.com", "correct-horse")
	if got := service.TenantMaxDevicesPerUser(onlyBytes.Tenant.ID); got != 0 {
		t.Errorf("MaxDevicesPerUser = %d, want 0 (unlimited: only the byte knob was set)", got)
	}
	if got := service.TenantMaxDailyBytes(onlyBytes.Tenant.ID); got != 4096 {
		t.Errorf("MaxDailyBytes = %d, want 4096", got)
	}
}

// TestFreeTierDefaultsClampNegatives: a negative allowance has no meaning, and
// silently storing it would read back as "unlimited" anyway (the getters clamp), so
// clamping at the setter keeps what is stored and what is enforced consistent.
func TestFreeTierDefaultsClampNegatives(t *testing.T) {
	service := newTestService(t)
	service.SetFreeTierDefaults(-5, -3, -4096)
	maxUsers, maxDevices, maxDailyBytes := service.FreeTierDefaults()
	if maxUsers != 0 || maxDevices != 0 || maxDailyBytes != 0 {
		t.Fatalf("defaults = (%d, %d, %d), want all 0 after clamping negatives", maxUsers, maxDevices, maxDailyBytes)
	}
}

// TestMemberLimitRefusesInviteWhenFull: the member allowance is the tenant-level half
// of the plan (how many PEOPLE may join). The owner is already one member, so a limit
// of 1 leaves no room and the invite is refused up front — the code is ONE-TIME, so
// issuing one that cannot be redeemed would burn it and give the invited person a
// failure they cannot explain.
func TestMemberLimitRefusesInviteWhenFull(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := owner.Tenant.ID

	if _, err := service.SetTenantMaxUsers(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxUsers: %v", err)
	}
	if _, _, err := service.CreateUserInvite(CreateUserInviteInput{
		TenantID: tenantID, InvitedBy: owner.User.ID, Role: identity.RoleTenantMember,
	}); !errors.Is(err, ErrMemberLimitReached) {
		t.Fatalf("err = %v, want ErrMemberLimitReached", err)
	}

	// Raising the allowance lets the same call through, so the refusal tracked the
	// allowance rather than being a blanket block.
	if _, err := service.SetTenantMaxUsers(tenantID, 2); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if _, _, err := service.CreateUserInvite(CreateUserInviteInput{
		TenantID: tenantID, InvitedBy: owner.User.ID, Role: identity.RoleTenantMember,
	}); err != nil {
		t.Fatalf("invite under the allowance was refused: %v", err)
	}
}

// TestMemberLimitRefusesRedemptionPastTheLimit is the authoritative check.
//
// The issue-time refusal above is ADVISORY: the allowance counts MEMBERSHIPS, not
// outstanding invites, so two codes can legitimately be issued while there is room for
// one. Whichever redemption lands second must be refused, or the limit would be
// decorative.
func TestMemberLimitRefusesRedemptionPastTheLimit(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := owner.Tenant.ID
	if _, err := service.SetTenantMaxUsers(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxUsers: %v", err)
	}

	// Both codes are issued while the tenant has room (1 member of 2).
	_, first, err := service.CreateUserInvite(CreateUserInviteInput{
		TenantID: tenantID, InvitedBy: owner.User.ID, Role: identity.RoleTenantMember,
	})
	if err != nil {
		t.Fatalf("first invite: %v", err)
	}
	_, second, err := service.CreateUserInvite(CreateUserInviteInput{
		TenantID: tenantID, InvitedBy: owner.User.ID, Role: identity.RoleTenantMember,
	})
	if err != nil {
		t.Fatalf("second invite: %v", err)
	}

	if _, err := service.AcceptUserInvite(first, "a@example.com", "correct-horse"); err != nil {
		t.Fatalf("first redemption (there was room): %v", err)
	}
	if _, err := service.AcceptUserInvite(second, "b@example.com", "correct-horse"); !errors.Is(err, ErrMemberLimitReached) {
		t.Fatalf("second redemption err = %v, want ErrMemberLimitReached", err)
	}

	// The refused redemption must not have created the account: a half-applied
	// redemption would leave a user row nobody can attach to a tenant.
	if _, _, _, err := service.Login("b@example.com", "correct-horse"); err == nil {
		t.Error("the refused redemption still created an account")
	}
}

// TestMemberLimitUnlimitedByDefault: a tenant with no policy row keeps accepting
// members, so the limit is purely additive for tenants that predate it.
func TestMemberLimitUnlimitedByDefault(t *testing.T) {
	service := newTestService(t)
	owner := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := owner.Tenant.ID

	for i, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		_, code, err := service.CreateUserInvite(CreateUserInviteInput{
			TenantID: tenantID, InvitedBy: owner.User.ID, Role: identity.RoleTenantMember,
		})
		if err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
		if _, err := service.AcceptUserInvite(code, email, "correct-horse"); err != nil {
			t.Fatalf("redemption %d with no configured limit: %v", i, err)
		}
	}
}

// TestInitMigratesTenantPoliciesWithoutMaxNodes is the upgrade path that matters
// most for this feature.
//
// A center that has already been running has a tenant_policies table WITHOUT
// max_nodes. The migration must add the column defaulting to 0 = unlimited: if it
// defaulted to 0-means-zero, every existing tenant would silently lose the ability
// to enrol any node the moment the server was upgraded.
func TestInitMigratesTenantPoliciesWithoutMaxNodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.sqlite")

	// Build the pre-migration schema by hand, exactly as an older release left it.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE tenant_policies (
		tenant_id TEXT PRIMARY KEY,
		sharing_enabled INTEGER NOT NULL DEFAULT 1,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO tenant_policies (tenant_id, sharing_enabled, updated_at) VALUES (?, ?, ?)`,
		"tenant-legacy", 0, "2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seed legacy policy: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Opening through New runs init(), which must add the missing columns.
	service, err := New(dir)
	if err != nil {
		t.Fatalf("New on a pre-migration store: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	if got := service.TenantMaxDevicesPerUser("tenant-legacy"); got != 0 {
		t.Errorf("migrated tenant MaxDevicesPerUser = %d, want 0 (unlimited)", got)
	}
	if got := service.TenantMaxUsers("tenant-legacy"); got != 0 {
		t.Errorf("migrated tenant MaxUsers = %d, want 0 (unlimited)", got)
	}
	// The pre-existing sharing value must survive the migration untouched.
	if service.TenantSharingEnabled("tenant-legacy") {
		t.Error("migration lost the stored sharing_enabled = 0")
	}
	// And both columns are now writable.
	if _, err := service.SetTenantMaxDevicesPerUser("tenant-legacy", 7); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser after migration: %v", err)
	}
	if got := service.TenantMaxDevicesPerUser("tenant-legacy"); got != 7 {
		t.Errorf("post-migration write = %d, want 7", got)
	}
	if _, err := service.SetTenantMaxUsers("tenant-legacy", 3); err != nil {
		t.Fatalf("SetTenantMaxUsers after migration: %v", err)
	}
	if got := service.TenantMaxUsers("tenant-legacy"); got != 3 {
		t.Errorf("post-migration member write = %d, want 3", got)
	}
}

// TestInitRenamesLegacyMaxNodesColumn is the upgrade path for a center that already
// ran the PREVIOUS shape of this feature.
//
// That release stored a column literally named max_nodes, meaning "devices this
// tenant may own". The limit is now per MEMBER, so the column is renamed rather than
// redefined in place: a column named max_nodes holding a per-user limit is a name that
// lies, and the next reader would enforce it tenant-wide again.
//
// The VALUE is carried over on purpose. For the single-member tenants that make up
// every existing deployment the two readings agree, so keeping the number preserves
// exactly the policy the operator set. Dropping it back to 0 would silently lift a
// limit in force; treating a missing rename as "add a fresh 0 column" would do the
// same, which is why this has its own regression.
func TestInitRenamesLegacyMaxNodesColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.sqlite")

	// The previous release's schema: max_nodes present, per-member column absent.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE tenant_policies (
		tenant_id TEXT PRIMARY KEY,
		sharing_enabled INTEGER NOT NULL DEFAULT 1,
		max_nodes INTEGER NOT NULL DEFAULT 0,
		max_daily_bytes INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO tenant_policies (tenant_id, sharing_enabled, max_nodes, max_daily_bytes, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"tenant-legacy", 1, 2, 104857600, "2026-11-10T00:00:00Z",
	); err != nil {
		t.Fatalf("seed legacy policy: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	service, err := New(dir)
	if err != nil {
		t.Fatalf("New on a pre-rename store: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	// The operator's 2 survived the rename.
	if got := service.TenantMaxDevicesPerUser("tenant-legacy"); got != 2 {
		t.Errorf("renamed MaxDevicesPerUser = %d, want the stored 2", got)
	}
	// ...and the column that was NOT part of the rename is untouched.
	if got := service.TenantMaxDailyBytes("tenant-legacy"); got != 104857600 {
		t.Errorf("MaxDailyBytes = %d, want 104857600 (unrelated column must survive)", got)
	}
	// The new member limit starts at the permissive default for an upgraded center.
	if got := service.TenantMaxUsers("tenant-legacy"); got != 0 {
		t.Errorf("MaxUsers = %d, want 0 (unlimited) for a tenant that predates the column", got)
	}
}
