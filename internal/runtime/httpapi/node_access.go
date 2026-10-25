package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
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
// sharing switch (decision ④) must apply to proxied requests too, and a second
// policy shaped slightly differently on that path would be a silent bypass.
//
// A nil accounts service yields a policy with no sharing override, which means
// sharing stays enabled — the pre-policy behaviour. That is deliberate: nodes are
// private by default on their own (decision ⑥), so sharing policy only ever
// REMOVES reach; its absence must not grant any.
func NewNodeAccessPolicy(accountsService *accounts.Service) identity.Policy {
	if accountsService == nil {
		return identity.Policy{}
	}
	return identity.Policy{
		TenantSharingEnabled: accountsService.TenantSharingEnabled,
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

// filterVisibleNodes drops the nodes the caller may not reach.
//
// Order is preserved so the registry's own sorting (online first, then name)
// still governs the response.
func filterVisibleNodes(policy identity.Policy, r *http.Request, nodes []noderegistry.NodeView) []noderegistry.NodeView {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		// See authorizeNodeAccess: an absent principal is a wiring fault, not a
		// reason to expose the platform's node inventory.
		return []noderegistry.NodeView{}
	}
	visible := policy.VisibleNodeIDs(principal, noderegistry.NodeRefs(nodes))
	allowed := make(map[string]bool, len(visible))
	for _, id := range visible {
		allowed[id] = true
	}
	out := make([]noderegistry.NodeView, 0, len(allowed))
	for _, node := range nodes {
		if allowed[node.ID] {
			out = append(out, node)
		}
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
