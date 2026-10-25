package identity

import "strings"

// Visibility values for a node's reach within its tenant (design §4.2).
const (
	// VisibilityPrivate is the default (decision ⑥): only the owner reaches it.
	VisibilityPrivate = "private"
	// VisibilityTenant exposes the node to every member of the owner's tenant.
	VisibilityTenant = "tenant"
)

// NormalizeVisibility validates a declared visibility, defaulting to private.
//
// Private is the default because it is the safer failure mode: a node that
// forgets to declare visibility stays reachable only by its owner, rather than
// silently becoming tenant-visible.
func NormalizeVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case VisibilityTenant:
		return VisibilityTenant
	default:
		return VisibilityPrivate
	}
}

// Decision reasons. They are stable identifiers so callers can log and audit
// them without string matching on prose.
const (
	// ReasonAllowed — the request may proceed.
	ReasonAllowed = "allowed"
	// ReasonNoPrincipal — the caller carries no identity (→ 401).
	ReasonNoPrincipal = "no_principal"
	// ReasonCrossTenant — the target node belongs to another tenant (→ 403).
	ReasonCrossTenant = "cross_tenant"
	// ReasonNodeNotShared — same tenant, but the node is private to its owner
	// and the caller is not the owner (→ 403).
	ReasonNodeNotShared = "node_not_shared"
	// ReasonSharingDisabled — the superadmin disabled tenant sharing for this
	// tenant, so even a tenant-visible node is not reachable by peers (→ 403).
	ReasonSharingDisabled = "sharing_disabled"
	// ReasonNoCenterExecution — the caller may not run an agent on the center
	// (decision ⑨) (→ 403).
	ReasonNoCenterExecution = "no_center_execution"
	// ReasonPlatformAdmin — a platform admin reached this via an explicit
	// delegation rather than tenant membership. Recorded so the audit trail
	// shows WHY access was granted.
	ReasonPlatformAdmin = "platform_admin"
)

// Decision is the result of an authorization check.
//
// It carries a reason and the facts it was made from, mirroring
// channels.AccessDecision: "a decision, a reason, and something auditable" is
// the shape every authorization seam should produce, so a denial can be
// explained after the fact instead of being an anonymous 403.
type Decision struct {
	Allowed  bool
	Reason   string
	TenantID string
	UserID   string
	Method   string // auth method that produced the principal
	// Detail is optional human-readable context (e.g. which node, which
	// delegation). Never contains credentials.
	Detail string
}

// Denied returns a decision with Allowed=false.
func Denied(reason string) Decision { return Decision{Allowed: false, Reason: reason} }

// AllowedDecision returns a decision with Allowed=true.
func AllowedDecision(reason string) Decision { return Decision{Allowed: true, Reason: reason} }

// NodeRef is the subset of a registered node that authorization depends on.
//
// It is a plain value rather than a *noderegistry.NodeView so the decision
// function stays reusable from every seam (relay proxy, registry reads, watch
// forwarding) without those packages importing the registry.
type NodeRef struct {
	ID          string
	TenantID    string
	OwnerUserID string
	Visibility  string
}

// Normalized returns a copy with visibility normalized (default private).
func (n NodeRef) Normalized() NodeRef {
	n.Visibility = NormalizeVisibility(n.Visibility)
	return n
}

// IsTenantVisible reports whether the node is exposed to its whole tenant.
func (n NodeRef) IsTenantVisible() bool {
	return NormalizeVisibility(n.Visibility) == VisibilityTenant
}

// Policy carries the operator-controlled switches that modify authorization.
type Policy struct {
	// TenantSharingEnabled is the superadmin switch (decision ④): when false,
	// no node in this deployment may be reached by tenant peers, regardless of
	// its own visibility setting. Expressed as a predicate so a deployment can
	// scope it per tenant later without changing this signature.
	TenantSharingEnabled func(tenantID string) bool
}

func (p Policy) sharingAllowed(tenantID string) bool {
	if p.TenantSharingEnabled == nil {
		// No override configured: sharing is permitted, and each node's own
		// visibility still decides the outcome.
		return true
	}
	return p.TenantSharingEnabled(tenantID)
}

// Authorize decides whether principal may reach the node (design §4.3).
//
// Rules, in order:
//  1. no identity            → denied (no_principal)
//  2. different tenant       → denied (cross_tenant)
//  3. same tenant, node private and caller is not the owner → denied (node_not_shared)
//  4. sharing disabled by the superadmin → denied (sharing_disabled)
//  5. otherwise              → allowed
//
// A platform admin does NOT bypass these rules: cross-tenant access is read
// access to another tenant's data, which requires an explicit delegation, not a
// role. Delegation is layered on top by the caller (see Delegation).
func (p Policy) Authorize(principal Principal, node NodeRef) Decision {
	if !principal.Valid() {
		return Denied(ReasonNoPrincipal)
	}
	node = node.Normalized()

	principalTenant := strings.TrimSpace(principal.TenantID)
	nodeTenant := strings.TrimSpace(node.TenantID)
	if principalTenant == "" || nodeTenant == "" || principalTenant != nodeTenant {
		return Decision{
			Allowed: false, Reason: ReasonCrossTenant,
			TenantID: principalTenant, UserID: principal.UserID,
			Method: principal.AuthMethod, Detail: "node " + node.ID,
		}
	}

	owner := strings.TrimSpace(node.OwnerUserID)
	isOwner := owner != "" && owner == strings.TrimSpace(principal.UserID)
	if !isOwner && !node.IsTenantVisible() {
		return Decision{
			Allowed: false, Reason: ReasonNodeNotShared,
			TenantID: principalTenant, UserID: principal.UserID,
			Method: principal.AuthMethod, Detail: "node " + node.ID,
		}
	}
	// The owner always reaches their own node; the sharing switch governs
	// exposure to peers, not self-access.
	if !isOwner && !p.sharingAllowed(principalTenant) {
		return Decision{
			Allowed: false, Reason: ReasonSharingDisabled,
			TenantID: principalTenant, UserID: principal.UserID,
			Method: principal.AuthMethod, Detail: "node " + node.ID,
		}
	}

	return Decision{
		Allowed: true, Reason: ReasonAllowed,
		TenantID: principalTenant, UserID: principal.UserID,
		Method: principal.AuthMethod, Detail: "node " + node.ID,
	}
}

// AuthorizeCenterExecution decides whether principal may execute an agent on
// the center instance itself (decision ⑨).
//
// This is the single check for the "only one account runs an agent on the
// server" property. Every center execution entry point (sessions, turns,
// workflow runs, agent-steps, gateway chat completions) calls it, so the rule
// lives in one place rather than in each handler.
func AuthorizeCenterExecution(principal Principal) Decision {
	if !principal.Valid() {
		return Denied(ReasonNoPrincipal)
	}
	if !principal.CanExecuteOnCenter() {
		return Decision{
			Allowed: false, Reason: ReasonNoCenterExecution,
			TenantID: principal.TenantID, UserID: principal.UserID,
			Method: principal.AuthMethod,
		}
	}
	return Decision{
		Allowed: true, Reason: ReasonAllowed,
		TenantID: principal.TenantID, UserID: principal.UserID,
		Method: principal.AuthMethod,
	}
}

// VisibleNodeIDs filters a node list down to the ids the principal may reach.
//
// Registry list endpoints must apply this before returning, otherwise a tenant
// can enumerate another tenant's node ids (the §1.3 finding, where a global nk_
// credential listed every node).
func (p Policy) VisibleNodeIDs(principal Principal, nodes []NodeRef) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if p.Authorize(principal, node).Allowed {
			out = append(out, node.ID)
		}
	}
	return out
}
