package noderegistry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newLimitRegistry seeds a registry plus the given nodes, all already owned by
// tenantID except the last one, which is left unclaimed so a test can exercise
// the "assign into a full tenant" path.
func newLimitRegistry(t *testing.T, tenantID string, owned int) *Registry {
	t.Helper()
	registry, err := New(filepath.Join(t.TempDir(), "nodes.json"), time.Minute)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < owned; i++ {
		id := "node-" + string(rune('a'+i))
		if _, err := registry.Register(ctx, NodeInput{ID: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if _, err := registry.SetOwnership(ctx, id, tenantID, "user-1", "private"); err != nil {
			t.Fatalf("assign %s: %v", id, err)
		}
	}
	if _, err := registry.Register(ctx, NodeInput{ID: "fresh"}); err != nil {
		t.Fatalf("register fresh: %v", err)
	}
	return registry
}

// TestSetOwnershipWithinRefusesPastTheAllowance is the basic-tier quota (design
// §7): a tenant at its limit cannot take on another node.
func TestSetOwnershipWithinRefusesPastTheAllowance(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 2)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", 2); !errors.Is(err, ErrNodeLimitReached) {
		t.Fatalf("err = %v, want ErrNodeLimitReached", err)
	}
	// The refusal must leave the node untouched: a rejected enrolment that still
	// wrote the tenant would silently consume the allowance it just refused.
	node, err := registry.Get(ctx, "fresh")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if node.TenantID != "" {
		t.Errorf("refused assignment still wrote tenant %q", node.TenantID)
	}

	// Positive control: under the limit the same call succeeds, so the refusal
	// above is about the allowance rather than about the call being invalid.
	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", 3); err != nil {
		t.Fatalf("assignment under the limit failed: %v", err)
	}
}

// TestSetOwnershipWithinHonoursUnlimited: 0 (and any non-positive value) means
// unlimited, which is what an upgraded center's default resolves to. If this were
// treated as "no nodes allowed", every pre-existing tenant would stop enrolling.
func TestSetOwnershipWithinHonoursUnlimited(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 3)

	for _, limit := range []int{0, -1} {
		if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", limit); err != nil {
			t.Fatalf("limit %d refused an assignment; 0 must mean unlimited: %v", limit, err)
		}
		// Reset so the second iteration tests the same starting condition.
		if _, err := registry.SetOwnership(ctx, "fresh", "", "", "private"); err != nil {
			t.Fatalf("reset: %v", err)
		}
	}
}

// TestSetOwnershipWithinDoesNotEvictOnLoweredLimit: lowering an allowance refuses
// NEW enrolments but never strips ownership from nodes that already exist. The
// alternative would orphan running machines as a side effect of a policy edit.
func TestSetOwnershipWithinDoesNotEvictOnLoweredLimit(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 3)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", 1); !errors.Is(err, ErrNodeLimitReached) {
		t.Fatalf("err = %v, want ErrNodeLimitReached", err)
	}
	nodes, err := registry.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	owned := 0
	for _, node := range nodes {
		if node.TenantID == "tenant-a" {
			owned++
		}
	}
	if owned != 3 {
		t.Errorf("tenant still owns %d nodes, want the 3 that were already assigned", owned)
	}
}

// TestSetOwnershipWithinAllowsReassigningAnOwnedNode: re-saving a node the tenant
// already holds costs no allowance. Without this, an owner changing their node's
// visibility on a full tenant would be refused for "adding" a node it already had.
func TestSetOwnershipWithinAllowsReassigningAnOwnedNode(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 2)

	if _, err := registry.SetOwnershipWithin(ctx, "node-a", "tenant-a", "user-1", "tenant", 2); err != nil {
		t.Fatalf("re-saving an already-owned node was refused: %v", err)
	}
	node, err := registry.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The literal, matching the sibling ownership tests: this package wraps
	// identity.NormalizeVisibility and exports no visibility constants.
	if node.Visibility != "tenant" {
		t.Errorf("visibility = %q, want tenant", node.Visibility)
	}
}

// TestSetOwnershipWithinIgnoresUnclaimedNodes: unowned nodes are nobody's
// occupancy, so a tenant at its limit is not blocked by machines nobody claimed.
func TestSetOwnershipWithinIgnoresUnclaimedNodes(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 1)
	for i := 0; i < 3; i++ {
		if _, err := registry.Register(ctx, NodeInput{ID: "loose-" + string(rune('a'+i))}); err != nil {
			t.Fatalf("register loose: %v", err)
		}
	}
	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", 2); err != nil {
		t.Fatalf("unclaimed nodes consumed the allowance: %v", err)
	}
}

// TestPlainSetOwnershipIgnoresTheAllowance covers the admin repair path: an
// operator fixing a mis-assigned node must not be blocked by the quota they may be
// repairing. Enforcement lives in SetOwnershipWithin, which only tenant-facing
// enrolment calls.
func TestPlainSetOwnershipIgnoresTheAllowance(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 3)

	if _, err := registry.SetOwnership(ctx, "fresh", "tenant-a", "user-1", "private"); err != nil {
		t.Fatalf("admin claim refused by the allowance: %v", err)
	}
}

// TestSetOwnershipWithinCountsOnlyTheTargetTenant: one tenant's occupancy must not
// consume another's allowance.
func TestSetOwnershipWithinCountsOnlyTheTargetTenant(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 3)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-b", "user-2", "private", 1); err != nil {
		t.Fatalf("tenant-b was blocked by tenant-a's occupancy: %v", err)
	}
}
