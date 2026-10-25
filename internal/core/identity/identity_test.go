package identity

import (
	"reflect"
	"testing"
	"time"
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

func TestDelegationActiveRequiresUnrevokedUnexpiredGrant(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	base := Delegation{
		ID: "dlg_1", TenantID: "t1", OperatorUserID: "local-operator",
		Scope: ScopeReadNodes, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
	}

	cases := []struct {
		name       string
		mutate     func(Delegation) Delegation
		wantActive bool
	}{
		{name: "live grant", mutate: func(d Delegation) Delegation { return d }, wantActive: true},
		{
			name: "expired grant",
			mutate: func(d Delegation) Delegation {
				d.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
				return d
			},
		},
		{
			name: "revoked grant",
			mutate: func(d Delegation) Delegation {
				d.RevokedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
				return d
			},
		},
		{
			// An unparseable expiry must mean "no access", not "never expires":
			// reading garbage as perpetual access is the one direction that cannot
			// be recovered from.
			name:   "unparseable expiry is treated as expired",
			mutate: func(d Delegation) Delegation { d.ExpiresAt = "not-a-time"; return d },
		},
		{
			name:   "missing expiry is treated as expired",
			mutate: func(d Delegation) Delegation { d.ExpiresAt = ""; return d },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mutate(base).Active(now); got != tc.wantActive {
				t.Errorf("Active = %v, want %v", got, tc.wantActive)
			}
		})
	}
}

func TestNormalizeScopeDefaultsToReadOnly(t *testing.T) {
	// An unknown scope must collapse to the read-only default rather than being
	// stored verbatim: a stored scope the authorization code does not understand
	// would be a grant whose limits nobody can reason about.
	for _, in := range []string{"", ScopeReadNodes, "READ_NODES", "  read_nodes  ", "write_everything", "admin"} {
		if got := NormalizeScope(in); got != ScopeReadNodes {
			t.Errorf("NormalizeScope(%q) = %q, want %q", in, got, ScopeReadNodes)
		}
	}
}

// TestAuthorizeDelegationCoversOnlyActiveReadGrants pins the §3.6 seam: a platform
// operator crosses a tenant boundary ONLY through an active, tenant-granted,
// read-scoped delegation, and never merely by holding the platform role.
func TestAuthorizeDelegationCoversOnlyActiveReadGrants(t *testing.T) {
	admin := NewPrincipal("", "local-operator", RolePlatformAdmin, KindPlatform, EntitlementCenterExecution)
	tenantNode := NodeRef{ID: "node-b", TenantID: "tenant-b", OwnerUserID: "user-c", Visibility: VisibilityTenant}

	live := Policy{DelegatedRead: func(operatorUserID, tenantID string) (string, bool) {
		if operatorUserID == "local-operator" && tenantID == "tenant-b" {
			return "dlg_live", true
		}
		return "", false
	}}

	cases := []struct {
		name       string
		policy     Policy
		principal  Principal
		node       NodeRef
		wantAllow  bool
		wantReason string
		wantID     string
	}{
		{
			name: "active grant permits cross-tenant read", policy: live, principal: admin, node: tenantNode,
			wantAllow: true, wantReason: ReasonPlatformAdmin, wantID: "dlg_live",
		},
		{
			// The default posture: a platform role is not a read license.
			name:   "no delegation system means no cross-tenant read",
			policy: Policy{}, principal: admin, node: tenantNode, wantReason: ReasonCrossTenant,
		},
		{
			name:      "a lookup that finds nothing denies",
			policy:    Policy{DelegatedRead: func(string, string) (string, bool) { return "", false }},
			principal: admin, node: tenantNode, wantReason: ReasonCrossTenant,
		},
		{
			// A grant names one operator. Another operator must not ride along on it.
			name:   "a grant issued to one operator does not cover another",
			policy: live, principal: NewPrincipal("", "someone-else", RolePlatformAdmin, KindPlatform, EntitlementCenterExecution),
			node: tenantNode, wantReason: ReasonCrossTenant,
		},
		{
			// Granting read to tenant B must not leak tenant A.
			name:   "a grant for another tenant does not apply",
			policy: live, principal: admin,
			node:       NodeRef{ID: "node-a", TenantID: "tenant-a", OwnerUserID: "user-a", Visibility: VisibilityTenant},
			wantReason: ReasonCrossTenant,
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
			if got.DelegationID != tc.wantID {
				t.Errorf("DelegationID = %q, want %q (the audit seam reads this field)", got.DelegationID, tc.wantID)
			}
		})
	}
}

// TestAuthorizeDelegationDoesNotExtendToNonAdmins: a tenant member cannot use
// someone else's delegation to reach a third tenant.
func TestAuthorizeDelegationDoesNotExtendToNonAdmins(t *testing.T) {
	policy := Policy{DelegatedRead: func(string, string) (string, bool) { return "dlg_live", true }}
	member := NewPrincipal("tenant-a", "user-a", "", KindUser, EntitlementRelayOnly)

	got := policy.Authorize(member, NodeRef{ID: "node-b", TenantID: "tenant-b", Visibility: VisibilityTenant})
	if got.Allowed {
		t.Fatalf("a tenant member reached another tenant through a delegation: %+v", got)
	}
	if got.Reason != ReasonCrossTenant {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonCrossTenant)
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
		{
			// Deployment with no tenant system in use: the operator must still
			// reach unclaimed nodes, otherwise every existing installation would
			// break the moment this policy is wired in.
			name: "operator reaches an unscoped node", policy: Policy{}, principal: admin,
			node: NodeRef{ID: "node-legacy"}, wantAllow: true, wantReason: ReasonSingleTenant,
		},
		{
			// An unscoped principal that is NOT the operator must not inherit that
			// reach: if a tenant member's tenant id ever failed to load, an
			// unconditional allow would turn the bug into platform-wide access.
			name: "non-operator unscoped principal is refused", policy: Policy{},
			principal:  NewPrincipal("", "nobody", "", KindUser),
			node:       NodeRef{ID: "node-legacy", OwnerUserID: "nobody"},
			wantReason: ReasonCrossTenant,
		},
		{
			name: "tenant member does not reach an unscoped node", policy: Policy{},
			principal: tenantUser, node: NodeRef{ID: "node-legacy", OwnerUserID: "user-a"},
			wantReason: ReasonCrossTenant,
		},
		{
			// Node-to-node forwarding through the center runs as a node actor,
			// which has no tenant. Refusing it here would silently break the
			// documented multi-node capability.
			name: "node actor reaches an unscoped node", policy: Policy{},
			principal: NewPrincipal("", "relay-node", "", KindNode, EntitlementRelayOnly),
			node:      NodeRef{ID: "node-legacy"},
			wantAllow: true, wantReason: ReasonSingleTenant,
		},
		{
			name: "watch principal is not a node actor", policy: Policy{},
			principal: NewPrincipal("", "wrist-1", "", KindWatch),
			node:      NodeRef{ID: "node-legacy"}, wantReason: ReasonCrossTenant,
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

func TestNodeActorIsNotOperator(t *testing.T) {
	node := NewPrincipal("", "relay-node", "", KindNode, EntitlementRelayOnly)
	if !node.IsNodeActor() {
		t.Fatal("KindNode must be a node actor")
	}
	if node.IsPlatformAdmin() {
		t.Fatal("a node actor must not be the platform admin")
	}
	if node.CanExecuteOnCenter() {
		t.Fatal("a node actor must not hold center_execution")
	}
	// The other kinds must not be mistaken for a node actor.
	for _, p := range []Principal{
		NewPrincipal("", "admin", RolePlatformAdmin, KindPlatform, EntitlementCenterExecution),
		NewPrincipal("", "wrist", "", KindWatch),
		NewPrincipal("t1", "u1", "", KindUser, EntitlementRelayOnly),
		NewPrincipal("t1", "biz", "", KindBizKey),
	} {
		if p.IsNodeActor() {
			t.Errorf("kind %q must not be a node actor", p.Kind)
		}
	}
	if (Principal{}).IsNodeActor() {
		t.Error("empty kind must not be a node actor")
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
