package accounts

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestTenantMaxNodesDefaultsToUnlimited pins the default that keeps the allowance
// purely additive: a tenant with no stored policy row has no limit.
func TestTenantMaxNodesDefaultsToUnlimited(t *testing.T) {
	service := newTestService(t)
	if got := service.TenantMaxNodes("tenant-unknown"); got != 0 {
		t.Fatalf("TenantMaxNodes(unknown) = %d, want 0 (unlimited)", got)
	}
	if got := service.TenantMaxNodes(""); got != 0 {
		t.Fatalf("TenantMaxNodes(\"\") = %d, want 0 (unlimited)", got)
	}
}

func TestSetTenantMaxNodesRoundTrips(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID

	policy, err := service.SetTenantMaxNodes(tenantID, 5)
	if err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}
	if policy.MaxNodes != 5 {
		t.Errorf("returned MaxNodes = %d, want 5", policy.MaxNodes)
	}
	if got := service.TenantMaxNodes(tenantID); got != 5 {
		t.Errorf("TenantMaxNodes = %d, want 5", got)
	}

	// A second write replaces rather than accumulates, and re-reading after a
	// reopen proves it was persisted rather than kept in memory.
	if _, err := service.SetTenantMaxNodes(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxNodes (update): %v", err)
	}
	if got := service.TenantMaxNodes(tenantID); got != 2 {
		t.Errorf("TenantMaxNodes after update = %d, want 2", got)
	}
}

// TestSetTenantMaxNodesDoesNotClobberSharing is the reason the two policies are
// written separately: each endpoint updates only its own column, so setting an
// allowance cannot silently revert a sharing decision the operator made.
func TestSetTenantMaxNodesDoesNotClobberSharing(t *testing.T) {
	service := newTestService(t)
	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID

	if _, err := service.SetTenantSharing(tenantID, false); err != nil {
		t.Fatalf("SetTenantSharing: %v", err)
	}
	if _, err := service.SetTenantMaxNodes(tenantID, 3); err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}
	if service.TenantSharingEnabled(tenantID) {
		t.Error("setting the node allowance re-enabled sharing")
	}

	// And the reverse direction: toggling sharing must not drop the allowance.
	if _, err := service.SetTenantSharing(tenantID, true); err != nil {
		t.Fatalf("SetTenantSharing (re-enable): %v", err)
	}
	if got := service.TenantMaxNodes(tenantID); got != 3 {
		t.Errorf("sharing toggle changed the allowance to %d, want 3", got)
	}
}

// TestSetTenantMaxNodesRejectsMissingTenant: the store still refuses to record
// policy that belongs to nobody.
func TestSetTenantMaxNodesRejectsMissingTenant(t *testing.T) {
	service := newTestService(t)
	if _, err := service.SetTenantMaxNodes("  ", 5); err == nil {
		t.Fatal("expected an error for a missing tenant id")
	}
}

func TestListTenantsReportsMaxNodes(t *testing.T) {
	service := newTestService(t)
	limited := registerUser(t, service, "limited@example.com", "correct-horse")
	unlimited := registerUser(t, service, "free@example.com", "correct-horse")

	if _, err := service.SetTenantMaxNodes(limited.Tenant.ID, 4); err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}
	summaries, err := service.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	got := map[string]int{}
	for _, summary := range summaries {
		got[summary.Tenant.ID] = summary.MaxNodes
	}
	if got[limited.Tenant.ID] != 4 {
		t.Errorf("limited tenant MaxNodes = %d, want 4", got[limited.Tenant.ID])
	}
	if got[unlimited.Tenant.ID] != 0 {
		t.Errorf("unlimited tenant MaxNodes = %d, want 0", got[unlimited.Tenant.ID])
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
	if _, err := service.SetTenantMaxNodes(tenantID, 2); err != nil {
		t.Fatalf("SetTenantMaxNodes: %v", err)
	}
	if _, err := service.SetTenantMaxDailyBytes(tenantID, 4096); err != nil {
		t.Fatalf("SetTenantMaxDailyBytes: %v", err)
	}

	if service.TenantSharingEnabled(tenantID) {
		t.Error("sharing switch was reset by writing the byte allowance")
	}
	if got := service.TenantMaxNodes(tenantID); got != 2 {
		t.Errorf("MaxNodes = %d, want 2 (unchanged by the byte write)", got)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != 4096 {
		t.Errorf("MaxDailyBytes = %d, want 4096", got)
	}
	// And the reverse direction: a node-allowance write must not clear the bytes.
	if _, err := service.SetTenantMaxNodes(tenantID, 3); err != nil {
		t.Fatalf("SetTenantMaxNodes (second): %v", err)
	}
	if got := service.TenantMaxDailyBytes(tenantID); got != 4096 {
		t.Errorf("MaxDailyBytes after a node write = %d, want 4096", got)
	}
}

// TestFreeTierDefaultsAreZeroByDefault: an unconfigured center must behave exactly as
// it did before these knobs existed — unlimited, and no policy row written.
func TestFreeTierDefaultsAreZeroByDefault(t *testing.T) {
	service := newTestService(t)
	maxNodes, maxDailyBytes := service.FreeTierDefaults()
	if maxNodes != 0 || maxDailyBytes != 0 {
		t.Fatalf("defaults = (%d, %d), want (0, 0) = unlimited", maxNodes, maxDailyBytes)
	}

	result := registerUser(t, service, "owner@example.com", "correct-horse")
	tenantID := result.Tenant.ID
	if got := service.TenantMaxNodes(tenantID); got != 0 {
		t.Errorf("fresh signup MaxNodes = %d, want 0 (unlimited)", got)
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
	service.SetFreeTierDefaults(2, hundredMB)

	fresh := registerUser(t, service, "new@example.com", "correct-horse")
	if got := service.TenantMaxNodes(fresh.Tenant.ID); got != 2 {
		t.Errorf("fresh signup MaxNodes = %d, want 2", got)
	}
	if got := service.TenantMaxDailyBytes(fresh.Tenant.ID); got != hundredMB {
		t.Errorf("fresh signup MaxDailyBytes = %d, want %d", got, hundredMB)
	}

	// Defaults apply to SIGNUP only. A tenant that already exists keeps the policy it
	// was created with, so raising a default cannot silently change the terms someone
	// already enrolled under.
	before := registerUser(t, service, "before@example.com", "correct-horse")
	service.SetFreeTierDefaults(99, 999)
	if got := service.TenantMaxNodes(before.Tenant.ID); got != 2 {
		t.Errorf("existing tenant MaxNodes = %d, want 2 (defaults are not retroactive)", got)
	}
	if got := service.TenantMaxDailyBytes(before.Tenant.ID); got != hundredMB {
		t.Errorf("existing tenant MaxDailyBytes = %d, want %d (not retroactive)", got, hundredMB)
	}

	// A later signup gets the NEW default.
	after := registerUser(t, service, "after@example.com", "correct-horse")
	if got := service.TenantMaxNodes(after.Tenant.ID); got != 99 {
		t.Errorf("signup after raising the default: MaxNodes = %d, want 99", got)
	}
}

// TestFreeTierDefaultsOnlyNodesOrOnlyBytes: either knob may be set alone, and 0 for
// the other must stay "unlimited" rather than being read as "none".
func TestFreeTierDefaultsOnlyNodesOrOnlyBytes(t *testing.T) {
	service := newTestService(t)
	service.SetFreeTierDefaults(2, 0)
	onlyNodes := registerUser(t, service, "nodes@example.com", "correct-horse")
	if got := service.TenantMaxNodes(onlyNodes.Tenant.ID); got != 2 {
		t.Errorf("MaxNodes = %d, want 2", got)
	}
	if got := service.TenantMaxDailyBytes(onlyNodes.Tenant.ID); got != 0 {
		t.Errorf("MaxDailyBytes = %d, want 0 (unlimited: only the node knob was set)", got)
	}

	service.SetFreeTierDefaults(0, 4096)
	onlyBytes := registerUser(t, service, "bytes@example.com", "correct-horse")
	if got := service.TenantMaxNodes(onlyBytes.Tenant.ID); got != 0 {
		t.Errorf("MaxNodes = %d, want 0 (unlimited: only the byte knob was set)", got)
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
	service.SetFreeTierDefaults(-5, -4096)
	maxNodes, maxDailyBytes := service.FreeTierDefaults()
	if maxNodes != 0 || maxDailyBytes != 0 {
		t.Fatalf("defaults = (%d, %d), want (0, 0) after clamping negatives", maxNodes, maxDailyBytes)
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

	// Opening through New runs init(), which must add the missing column.
	service, err := New(dir)
	if err != nil {
		t.Fatalf("New on a pre-migration store: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	if got := service.TenantMaxNodes("tenant-legacy"); got != 0 {
		t.Errorf("migrated tenant MaxNodes = %d, want 0 (unlimited)", got)
	}
	// The pre-existing sharing value must survive the migration untouched.
	if service.TenantSharingEnabled("tenant-legacy") {
		t.Error("migration lost the stored sharing_enabled = 0")
	}
	// And the column is now writable.
	if _, err := service.SetTenantMaxNodes("tenant-legacy", 7); err != nil {
		t.Fatalf("SetTenantMaxNodes after migration: %v", err)
	}
	if got := service.TenantMaxNodes("tenant-legacy"); got != 7 {
		t.Errorf("post-migration write = %d, want 7", got)
	}
}
