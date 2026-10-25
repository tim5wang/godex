package identity

import (
	"reflect"
	"testing"
)

func TestPrincipalValidityAndEntitlements(t *testing.T) {
	cases := []struct {
		name         string
		principal    Principal
		wantValid    bool
		wantCenter   bool
		wantTenant   bool
		wantPlatform bool
	}{
		{
			name:       "tenant member without center execution",
			principal:  NewPrincipal("t1", "u1", "", KindUser, EntitlementRelayOnly),
			wantValid:  true,
			wantTenant: true,
		},
		{
			name:         "superadmin holds center execution",
			principal:    NewPrincipal("", "admin", RolePlatformAdmin, KindPlatform, EntitlementCenterExecution),
			wantValid:    true,
			wantCenter:   true,
			wantPlatform: true,
		},
		{
			name:      "empty principal is invalid",
			principal: Principal{},
		},
		{
			name:       "entitlement case and whitespace normalized",
			principal:  NewPrincipal("t1", "u1", "", KindUser, " Center_Execution "),
			wantValid:  true,
			wantCenter: true,
			wantTenant: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.principal.Valid(); got != tc.wantValid {
				t.Errorf("Valid() = %v, want %v", got, tc.wantValid)
			}
			if got := tc.principal.CanExecuteOnCenter(); got != tc.wantCenter {
				t.Errorf("CanExecuteOnCenter() = %v, want %v", got, tc.wantCenter)
			}
			if got := tc.principal.HasTenant(); got != tc.wantTenant {
				t.Errorf("HasTenant() = %v, want %v", got, tc.wantTenant)
			}
			if got := tc.principal.IsPlatformAdmin(); got != tc.wantPlatform {
				t.Errorf("IsPlatformAdmin() = %v, want %v", got, tc.wantPlatform)
			}
		})
	}
}

func TestNormalizeEntitlementsSortsAndDedupes(t *testing.T) {
	got := NewPrincipal("t", "u", "", KindUser, "relay_only", "center_execution", "relay_only", "", "  ").Entitlements
	want := []string{"center_execution", "relay_only"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entitlements = %v, want %v", got, want)
	}
	if got := NormalizeEntitlements(nil); len(got) != 0 {
		t.Errorf("NormalizeEntitlements(nil) = %v, want empty", got)
	}
}

func TestNormalizeVisibilityDefaultsToPrivate(t *testing.T) {
	cases := map[string]string{
		"":         VisibilityPrivate,
		"private":  VisibilityPrivate,
		"tenant":   VisibilityTenant,
		" TENANT ": VisibilityTenant,
		"garbage":  VisibilityPrivate, // unknown input must fail safe
	}
	for in, want := range cases {
		if got := NormalizeVisibility(in); got != want {
			t.Errorf("NormalizeVisibility(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuthorizeNodeAccess(t *testing.T) {
	tenantUser := NewPrincipal("t1", "user-a", "", KindUser, EntitlementRelayOnly)
	tenantPeer := NewPrincipal("t1", "user-b", "", KindUser, EntitlementRelayOnly)
	otherTenant := NewPrincipal("t2", "user-c", "", KindUser, EntitlementRelayOnly)
	admin := NewPrincipal("", "admin", RolePlatformAdmin, KindPlatform, EntitlementCenterExecution)

	privateOwnedByA := NodeRef{ID: "node-a", TenantID: "t1", OwnerUserID: "user-a", Visibility: VisibilityPrivate}
	sharedOwnedByA := NodeRef{ID: "node-shared", TenantID: "t1", OwnerUserID: "user-a", Visibility: VisibilityTenant}
	nodelessTenant := NodeRef{ID: "node-legacy", TenantID: "t1", OwnerUserID: "user-a"}
	// A node owned by the t2 caller: reaching it must be allowed, which is what
	// makes the cross-tenant case below meaningful rather than trivially true.
	t2OwnNode := NodeRef{ID: "node-t2", TenantID: "t2", OwnerUserID: "user-c", Visibility: VisibilityTenant}

	cases := []struct {
		name       string
		policy     Policy
		principal  Principal
		node       NodeRef
		wantAllow  bool
		wantReason string
	}{
		{
			name: "owner reaches own private node", policy: Policy{}, principal: tenantUser, node: privateOwnedByA,
			wantAllow: true, wantReason: ReasonAllowed,
		},
		{
			name: "peer cannot reach private node", policy: Policy{}, principal: tenantPeer, node: privateOwnedByA,
			wantReason: ReasonNodeNotShared,
		},
		{
			name: "peer reaches tenant-visible node", policy: Policy{}, principal: tenantPeer, node: sharedOwnedByA,
			wantAllow: true, wantReason: ReasonAllowed,
		},
		{
			name: "a caller reaches its own tenant's node", policy: Policy{}, principal: otherTenant, node: t2OwnNode,
			wantAllow: true, wantReason: ReasonAllowed,
		},
		{
			name: "cross-tenant is denied even when tenant-visible", policy: Policy{}, principal: otherTenant, node: sharedOwnedByA,
			wantReason: ReasonCrossTenant,
		},
		{
			name: "node without tenant is denied (not implicitly global)", policy: Policy{}, principal: tenantUser, node: NodeRef{ID: "node-x"},
			wantReason: ReasonCrossTenant,
		},
		{
			name: "principal without tenant is denied", policy: Policy{}, principal: NewPrincipal("", "nobody", "", KindUser),
			node: sharedOwnedByA, wantReason: ReasonCrossTenant,
		},
		{
			name: "empty principal is denied", policy: Policy{}, principal: Principal{}, node: sharedOwnedByA,
			wantReason: ReasonNoPrincipal,
		},
		{
			name:   "platform admin does not bypass tenant boundary",
			policy: Policy{}, principal: admin, node: sharedOwnedByA, wantReason: ReasonCrossTenant,
		},
		{
			name:       "sharing disabled blocks peers but not the owner",
			policy:     Policy{TenantSharingEnabled: func(string) bool { return false }},
			principal:  tenantPeer,
			node:       sharedOwnedByA,
			wantReason: ReasonSharingDisabled,
		},
		{
			name:      "sharing disabled still lets the owner in",
			policy:    Policy{TenantSharingEnabled: func(string) bool { return false }},
			principal: tenantUser, node: sharedOwnedByA,
			wantAllow: true, wantReason: ReasonAllowed,
		},
		{
			name:   "empty visibility is treated as private",
			policy: Policy{}, principal: tenantPeer, node: nodelessTenant,
			wantReason: ReasonNodeNotShared,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.policy.Authorize(tc.principal, tc.node)
			if got.Allowed != tc.wantAllow {
				t.Fatalf("Allowed = %v, want %v (reason=%q)", got.Allowed, tc.wantAllow, got.Reason)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestAuthorizeCenterExecutionOnlyForHolder(t *testing.T) {
	t.Run("tenant user is refused", func(t *testing.T) {
		d := AuthorizeCenterExecution(NewPrincipal("t1", "u1", "", KindUser, EntitlementRelayOnly))
		if d.Allowed {
			t.Fatal("tenant user must not execute on the center")
		}
		if d.Reason != ReasonNoCenterExecution {
			t.Errorf("Reason = %q, want %q", d.Reason, ReasonNoCenterExecution)
		}
	})
	t.Run("superadmin is allowed", func(t *testing.T) {
		d := AuthorizeCenterExecution(NewPrincipal("", "admin", RolePlatformAdmin, KindPlatform, EntitlementCenterExecution))
		if !d.Allowed {
			t.Fatalf("superadmin refused: %q", d.Reason)
		}
	})
	t.Run("platform admin role alone is not enough", func(t *testing.T) {
		// The capability bit is the check; the role label must not substitute
		// for it, otherwise a role typo silently grants execution.
		d := AuthorizeCenterExecution(NewPrincipal("", "admin", RolePlatformAdmin, KindPlatform))
		if d.Allowed {
			t.Fatal("role without the entitlement must not execute")
		}
	})
	t.Run("no principal", func(t *testing.T) {
		if d := AuthorizeCenterExecution(Principal{}); d.Reason != ReasonNoPrincipal {
			t.Errorf("Reason = %q, want %q", d.Reason, ReasonNoPrincipal)
		}
	})
}

func TestVisibleNodeIDsMasksForeignNodes(t *testing.T) {
	// Reproduces the §1.3 finding: a bare node list must not enumerate another
	// tenant's nodes.
	user := NewPrincipal("t1", "user-a", "", KindUser, EntitlementRelayOnly)
	nodes := []NodeRef{
		{ID: "own", TenantID: "t1", OwnerUserID: "user-a"},
		{ID: "shared", TenantID: "t1", OwnerUserID: "user-b", Visibility: VisibilityTenant},
		{ID: "peer-private", TenantID: "t1", OwnerUserID: "user-b"},
		{ID: "foreign", TenantID: "t2", OwnerUserID: "user-c", Visibility: VisibilityTenant},
	}
	policy := Policy{}
	got := policy.VisibleNodeIDs(user, nodes)
	want := []string{"own", "shared"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VisibleNodeIDs = %v, want %v", got, want)
	}
	// Parenthesized: a bare `Policy{}` in an if-header is parsed as the start
	// of the block, not a composite literal.
	if out := (Policy{}).VisibleNodeIDs(user, nil); len(out) != 0 {
		t.Errorf("VisibleNodeIDs(nil) = %v, want empty", out)
	}
}
