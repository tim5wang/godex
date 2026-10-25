package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/noderegistry"
)

// NodeAccess seams on the control-plane read surface (§1.3).
//
// The node registry is global: List/Get return every node on the platform. The
// nk_ credential therefore used to enumerate all nodes and reach any of them.
// Masking has to happen here, at the read surface, because the registry itself
// has no notion of the caller.

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
func authorizeNodeAccess(r *http.Request, ref identity.NodeRef) identity.Decision {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		return identity.Denied(identity.ReasonNoPrincipal)
	}
	return identity.Policy{}.Authorize(principal, ref)
}

// filterVisibleNodes drops the nodes the caller may not reach.
//
// Order is preserved so the registry's own sorting (online first, then name)
// still governs the response.
func filterVisibleNodes(r *http.Request, nodes []noderegistry.NodeView) []noderegistry.NodeView {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		// See authorizeNodeAccess: an absent principal is a wiring fault, not a
		// reason to expose the platform's node inventory.
		return []noderegistry.NodeView{}
	}
	visible := identity.Policy{}.VisibleNodeIDs(principal, noderegistry.NodeRefs(nodes))
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
