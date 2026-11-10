package noderegistry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newLimitRegistry seeds a registry plus the given devices, all already owned by
// (tenantID, ownerUserID) except the last one, which is left unclaimed so a test can
// exercise the "assign into a full account" path.
func newLimitRegistry(t *testing.T, tenantID string, owned int) *Registry {
	return newLimitRegistryForOwner(t, tenantID, "user-1", owned)
}

// newLimitRegistryForOwner is the same fixture with an explicit owner, so a test can
// put several members' devices in ONE tenant and prove the allowance is per member.
func newLimitRegistryForOwner(t *testing.T, tenantID, ownerUserID string, owned int) *Registry {
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
		if _, err := registry.SetOwnership(ctx, id, tenantID, ownerUserID, "private"); err != nil {
			t.Fatalf("assign %s: %v", id, err)
		}
	}
	if _, err := registry.Register(ctx, NodeInput{ID: "fresh"}); err != nil {
		t.Fatalf("register fresh: %v", err)
	}
	return registry
}

// TestSetOwnershipWithinRefusesPastTheAllowance is the basic-tier quota: a member
// at their device limit cannot take on another device.
func TestSetOwnershipWithinRefusesPastTheAllowance(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 2)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", 2); !errors.Is(err, ErrDeviceLimitReached) {
		t.Fatalf("err = %v, want ErrDeviceLimitReached", err)
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

// TestAllowanceIsPerMemberNotPerTenant is why the limit is per user: with a
// tenant-wide count, the second member to join would find the allowance already
// spent by the first, so "2 devices each" would be unimplementable.
func TestAllowanceIsPerMemberNotPerTenant(t *testing.T) {
	ctx := context.Background()
	// user-1 already holds their 2. A colleague in the SAME tenant must still be
	// able to enrol their own first device against the same limit of 2.
	registry := newLimitRegistryForOwner(t, "tenant-a", "user-1", 2)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-2", "private", 2); err != nil {
		t.Fatalf("a second member was blocked by the first member's devices: %v", err)
	}
}

// TestAllowanceRefusesTheSecondDeviceOfTheSameMember is the other half of the same
// property: the limit does bite for the member who actually reached it.
func TestAllowanceRefusesTheSecondDeviceOfTheSameMember(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistryForOwner(t, "tenant-a", "user-1", 1)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-2", "private", 1); err != nil {
		t.Fatalf("user-2's first device was refused: %v", err)
	}
	// A SECOND, DISTINCT device for the same member: refused. (Re-saving "fresh"
	// would prove nothing — the same member re-saving a device they already hold
	// costs no allowance by design, see
	// TestSetOwnershipWithinAllowsReassigningAnOwnedNode.)
	if _, err := registry.Register(ctx, NodeInput{ID: "fresh-2"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := registry.SetOwnershipWithin(ctx, "fresh-2", "tenant-a", "user-2", "private", 1); !errors.Is(err, ErrDeviceLimitReached) {
		t.Fatalf("err = %v, want ErrDeviceLimitReached for user-2's second device", err)
	}
}

// TestSetOwnershipWithinHonoursUnlimited: 0 (and any non-positive value) means
// unlimited, which is what an upgraded center's default resolves to. If this were
// treated as "no devices allowed", every pre-existing tenant would stop enrolling.
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
// NEW enrolments but never strips ownership from devices that already exist. The
// alternative would orphan running machines as a side effect of a policy edit.
func TestSetOwnershipWithinDoesNotEvictOnLoweredLimit(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 3)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-a", "user-1", "private", 1); !errors.Is(err, ErrDeviceLimitReached) {
		t.Fatalf("err = %v, want ErrDeviceLimitReached", err)
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
		t.Errorf("tenant still owns %d devices, want the 3 that were already assigned", owned)
	}
}

// TestSetOwnershipWithinAllowsReassigningAnOwnedNode: re-saving a device the same
// member already holds costs no allowance. Without this, an owner changing their
// device's visibility at their limit would be refused for "adding" one they had.
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

// TestSetOwnershipWithinCountsMovingADeviceToAnotherMember: a device moving to a
// DIFFERENT member changes whose allowance it consumes, so the new owner's headroom
// is what must be checked. Treating it as "already owned, costs nothing" would let
// two members trade one device back and forth past the limit.
func TestSetOwnershipWithinCountsMovingADeviceToAnotherMember(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistryForOwner(t, "tenant-a", "user-1", 1)
	// user-2 is already at their limit of 1 without owning node-a.
	if _, err := registry.Register(ctx, NodeInput{ID: "user2-device"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := registry.SetOwnership(ctx, "user2-device", "tenant-a", "user-2", "private"); err != nil {
		t.Fatalf("assign: %v", err)
	}

	if _, err := registry.SetOwnershipWithin(ctx, "node-a", "tenant-a", "user-2", "private", 1); !errors.Is(err, ErrDeviceLimitReached) {
		t.Fatalf("err = %v, want ErrDeviceLimitReached when moving INTO a full member", err)
	}
}

// TestSetOwnershipWithinIgnoresUnclaimedNodes: unowned devices are nobody's
// occupancy, so a member at their limit is not blocked by machines nobody claimed.
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

// TestSetOwnershipWithinCountsOnlyTheTargetMember: another tenant's (or another
// member's) occupancy must not consume this one's allowance.
func TestSetOwnershipWithinCountsOnlyTheTargetMember(t *testing.T) {
	ctx := context.Background()
	registry := newLimitRegistry(t, "tenant-a", 3)

	if _, err := registry.SetOwnershipWithin(ctx, "fresh", "tenant-b", "user-2", "private", 1); err != nil {
		t.Fatalf("tenant-b was blocked by tenant-a's occupancy: %v", err)
	}
}
