package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// NodeAccess seams on the control-plane read surface (§1.3).
//
// The node registry is global: List/Get return every node on the platform. The
// nk_ credential therefore used to enumerate all nodes and reach any of them.
// Masking has to happen here, at the read surface, because the registry itself
// has no notion of the caller.

// NewNodeAccessPolicy builds the authorization policy used by every NodeAccess
// seam (registry reads here, the relay proxy and TCP forward server in main).
//
// Exported so the relay side can build the SAME policy: the superadmin's tenant
// sharing switch (decision ④) and the tenant-granted operator delegation (§3.6)
// must apply to proxied requests too, and a second policy shaped slightly
// differently on that path would be a silent bypass.
//
// A nil accounts service yields a policy with no sharing override and no
// delegation lookup. Sharing then stays enabled — the pre-policy behaviour, and
// safe because nodes are private by default on their own (decision ⑥), so sharing
// policy only ever REMOVES reach. Delegation does the opposite: its absence means a
// platform admin gets no cross-tenant read, which is the correct failure direction
// for a grant.
func NewNodeAccessPolicy(accountsService *accounts.Service) identity.Policy {
	if accountsService == nil {
		return identity.Policy{}
	}
	return identity.Policy{
		TenantSharingEnabled: accountsService.TenantSharingEnabled,
		DelegatedRead:        activeDelegationLookup(accountsService),
	}
}

// activeDelegationLookup adapts the account store to the policy's delegation seam.
//
// The account store applies expiry and revocation, so this adapter does no
// clock logic: keeping that in one place is what stops a seam from honouring a
// grant the tenant already withdrew.
func activeDelegationLookup(accountsService *accounts.Service) func(operatorUserID, tenantID string) (string, bool) {
	return func(operatorUserID, tenantID string) (string, bool) {
		delegation, ok := accountsService.ActiveDelegationFor(operatorUserID, tenantID)
		if !ok {
			return "", false
		}
		return delegation.ID, true
	}
}

// authorizeNodeAccess decides whether the caller of r may reach the given node.
//
// Takes the node's identity.NodeRef rather than a bare id on purpose: an id-only
// signature cannot know the node's tenant, so every node would look unclaimed
// and a tenant member would be denied access to their OWN node. Callers already
// hold the NodeView (they fetched it to answer the request); pass node.NodeRef().
//
// Fails closed when no principal is attached: the route is registered behind
// nodeProxyRead (which attaches one), so a missing principal means the wiring
// changed — and "the wiring changed" must not silently mean "see everything".
func authorizeNodeAccess(policy identity.Policy, r *http.Request, ref identity.NodeRef) identity.Decision {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		return identity.Denied(identity.ReasonNoPrincipal)
	}
	return policy.Authorize(principal, ref)
}

// filterVisibleNodes drops the nodes the caller may not reach, and audits a
// platform operator's read when it happened through a tenant grant (§3.6).
//
// Order is preserved so the registry's own sorting (online first, then name)
// still governs the response.
//
// The per-node decision loop replaces a call to policy.VisibleNodeIDs so the
// DECISION is available here: auditing needs to know not just "allowed" but "
// allowed *because the tenant granted it*", and an id-only result cannot say that.
func filterVisibleNodes(policy identity.Policy, r *http.Request, nodes []noderegistry.NodeView, accountsService *accounts.Service) []noderegistry.NodeView {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		// See authorizeNodeAccess: an absent principal is a wiring fault, not a
		// reason to expose the platform's node inventory.
		return []noderegistry.NodeView{}
	}
	out := make([]noderegistry.NodeView, 0, len(nodes))
	var delegated *identity.Decision
	for _, node := range nodes {
		decision := policy.Authorize(principal, node.NodeRef())
		if !decision.Allowed {
			continue
		}
		out = append(out, node)
		if delegated == nil && decision.DelegationID != "" {
			captured := decision
			delegated = &captured
		}
	}
	if delegated != nil {
		// One row for the list read rather than one per node: a list is a single
		// act, and N rows per refresh would bury the trail it exists to provide.
		recordDelegatedRead(accountsService, r, *delegated,
			fmt.Sprintf("node list (%d visible)", len(out)))
	}
	return out
}

// maySetNodeVisibility reports whether the caller owns the node (or administers
// the platform).
//
// Deliberately NOT the same check as "may reach it": a tenant peer may reach a
// tenant-visible node, but visibility is the OWNER's decision (design §4.2), so
// a peer must not be able to flip a node private or expose it further.
func maySetNodeVisibility(r *http.Request, node noderegistry.NodeView) bool {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		return false
	}
	if principal.IsPlatformAdmin() {
		return true
	}
	owner := node.OwnerUserID
	return owner != "" && owner == principal.UserID
}

// RecordDelegatedRead writes the §3.6 audit row for a decision made OUTSIDE the
// HTTP read routes.
//
// Exported for cmd/godex's relay proxy seam: it shares this policy and therefore
// this delegation lookup, but not these handlers. Leaving that path unaudited would
// mean the audit trail depends on which surface the operator happened to use — and
// the proxy is the one the web UI actually drives, so the trail would be empty in
// exactly the case it exists for.
func RecordDelegatedRead(accountsService *accounts.Service, r *http.Request, decision identity.Decision, target string) {
	recordDelegatedRead(accountsService, r, decision, target)
}

// recordDelegatedRead writes an audit row when a decision was made THROUGH a
// tenant-granted delegation (design §3.6 "审计").
//
// Only delegated access is recorded, on purpose. A tenant's own members reading
// their own nodes are not "代看" — logging those would bury the handful of rows
// the tenant actually needs to inspect (who from the platform looked, and when)
// under its own normal traffic. Filtering on the decision's DelegationID also keeps
// the trail honest: the row names the exact grant that was exercised.
//
// Failures are logged, not returned: the request already succeeded, and turning an
// audit-write problem into a 500 would deny a tenant access its own grant allows.
func recordDelegatedRead(accountsService *accounts.Service, r *http.Request, decision identity.Decision, target string) {
	if accountsService == nil || decision.DelegationID == "" {
		return
	}
	err := accountsService.RecordDelegationUse(accounts.DelegationAudit{
		DelegationID:   decision.DelegationID,
		TenantID:       decision.TenantID,
		OperatorUserID: decision.UserID,
		Action:         strings.ToUpper(r.Method) + " " + r.URL.Path,
		Target:         target,
		Allowed:        decision.Allowed,
		Reason:         decision.Reason,
	})
	if err != nil {
		logger.Warnf("delegation audit: %v", err)
	}
}
