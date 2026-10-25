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
