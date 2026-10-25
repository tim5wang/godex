package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// Tenant enrolment surface (design §3.5).
//
// Before this, adding a node to the center required the center's FULL web token,
// which in a SaaS deployment means handing the platform master key to every
// tenant. A one-time, tenant-scoped join token replaces it: the code is worth
// exactly one node enrolment in one tenant, and the center — not the node —
// decides the ownership it produces.
//
// Deliberately NOT behind `protected`: the join token IS the credential, and
// requiring the web token as well would reintroduce the very thing this exists to
// remove.

type joinRequest struct {
	Token      string `json:"token"`
	NodeID     string `json:"node_id,omitempty"`
	Name       string `json:"name,omitempty"`
	TrustLevel string `json:"trust_level,omitempty"`
}

// registerJoinRoute wires the tenant enrolment endpoint.
func registerJoinRoute(mux *http.ServeMux, manager *config.Manager, controlRegistry controlNodeRegistry, accountsService *accounts.Service) {
	mux.Handle("POST /control/join", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accountsService == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
				"tenant enrolment is unavailable on this instance (account store could not be opened)"))
			return
		}
		if controlRegistry == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("control node registry is unavailable"))
			return
		}
		var req joinRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		req.Token = strings.TrimSpace(req.Token)
		req.NodeID = strings.TrimSpace(req.NodeID)
		req.Name = strings.TrimSpace(req.Name)
		req.TrustLevel = strings.TrimSpace(req.TrustLevel)
		if req.Token == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("token is required"))
			return
		}
		if req.NodeID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("node_id is required"))
			return
		}
		if req.TrustLevel == "" {
			req.TrustLevel = "trusted"
		}
		setter, ok := controlRegistry.(nodeOwnerSetter)
		if !ok {
			writeError(w, http.StatusNotImplemented, fmt.Errorf("node registry does not support ownership changes"))
			return
		}

		// An already-registered id is refused BEFORE the token is spent. Without
		// this, a join token would be able to reassign an existing node's tenant
		// through SetOwnership — i.e. tenant A could claim tenant B's machine.
		if existing, err := controlRegistry.Get(r.Context(), req.NodeID); err == nil && existing.ID != "" {
			writeError(w, http.StatusConflict, fmt.Errorf(
				"node %q is already registered on this center; a join token cannot take over an existing node", req.NodeID))
			return
		}

		// The token is consumed first so "one-time" is a real property under
		// concurrency. The cost is that a failure AFTER this point burns the code
		// and the operator issues a new one; that is the right trade, because the
		// alternative (validate now, consume later) lets two devices enrol with the
		// same code, which is the failure this credential exists to prevent.
		record, err := accountsService.RedeemJoinToken(req.Token, req.NodeID)
		if err != nil {
			if errors.Is(err, accounts.ErrInvalidJoinToken) {
				// One message for unknown/expired/used/revoked: a holder of a bad
				// code learns nothing about which issued codes exist.
				writeError(w, http.StatusUnauthorized, accounts.ErrInvalidJoinToken)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		// Ownership comes from the TOKEN, never from the request body: the body is
		// attacker-controlled, and self-reported ownership is the §1.2 hole.
		//
		// Everything is resolved BEFORE anything is written. Combining register and
		// assign into one call (see enrollNode) is what keeps a refusal — at the
		// allowance, or against an id taken in the meantime — from leaving a
		// registered but unowned node behind, which would both look enrolled and make
		// the id unusable for the device's retry.
		owner, err := resolveJoinOwner(accountsService, record.TenantID, record.OwnerUserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		visibility := identity.VisibilityPrivate
		if owner == "" {
			visibility = identity.VisibilityTenant
		}
		// The tenant's node allowance is read here and enforced inside the registry's
		// lock (see RegisterOwned). It is deliberately NOT enforced on the admin repair
		// path: an operator fixing a mis-assigned node must not be blocked by the quota
		// they may be repairing.
		maxNodes := accountsService.TenantMaxNodes(record.TenantID)
		node, err := enrollNode(r.Context(), controlRegistry, setter, noderegistry.NodeInput{
			ID:         req.NodeID,
			Name:       req.Name,
			TrustLevel: req.TrustLevel,
		}, record.TenantID, owner, visibility, maxNodes)
		if err != nil {
			switch {
			case errors.Is(err, noderegistry.ErrNodeLimitReached):
				// 409 rather than 400: the request is well-formed and the caller is
				// authorized; the tenant is simply at its allowance. The message names the
				// allowance so the operator need not go read the console.
				writeError(w, http.StatusConflict, err)
			case errors.Is(err, noderegistry.ErrNodeAlreadyRegistered):
				// The pre-check above covers the ordinary case; a concurrent enrolment
				// can still win the race, and 409 keeps the answer honest rather than
				// blaming the request.
				writeError(w, http.StatusConflict, err)
			default:
				writeError(w, http.StatusBadRequest, fmt.Errorf("register node: %w", err))
			}
			return
		}

		credential, err := relay.GenerateCredential()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := controlRegistry.SetCredentialHash(r.Context(), node.ID, relay.HashCredential(credential)); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		proxyToken, err := ensureNodeProxyToken(r.Context(), manager)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"node_id":          node.ID,
			"credential":       credential,
			"node_proxy_token": proxyToken,
			"tenant_id":        record.TenantID,
			"owner_user_id":    owner,
			"visibility":       visibility,
		})
	}))
}

// assignNodeOwnership applies ownership, using the limit-aware setter when the
// registry provides one.
//
// The capability is optional on purpose: /control/join is wired against the
// controlNodeRegistry interface, and requiring the quota everywhere would force
// every implementer (including test fakes) to grow a method for a rule that only
// tenant enrolment cares about.
func assignNodeOwnership(ctx context.Context, setter nodeOwnerSetter, id, tenantID, owner, visibility string, maxNodes int) (noderegistry.NodeView, error) {
	if within, ok := setter.(nodeOwnerSetterWithin); ok {
		return within.SetOwnershipWithin(ctx, id, tenantID, owner, visibility, maxNodes)
	}
	return setter.SetOwnership(ctx, id, tenantID, owner, visibility)
}

// resolveJoinOwner turns the token's optional owner into the owner the node will be
// written with, validating it against the tenant's membership.
//
// An owner id that belongs to no member produces a node nobody in the tenant can
// reach (private by default), which looks like a successful enrolment until someone
// tries to use it.
func resolveJoinOwner(accountsService *accounts.Service, tenantID, requested string) (string, error) {
	owner := strings.TrimSpace(requested)
	if owner != "" {
		if !accountsService.IsMember(tenantID, owner) {
			return "", fmt.Errorf("owner_user_id is not a member of this tenant")
		}
		return owner, nil
	}
	// Fall back to the tenant's OWNER member, not to the token's creator: the creator
	// is the platform operator whenever an operator issued the code, and the operator
	// is not a member of that tenant, so the node would end up owned by nobody
	// reachable.
	if tenantOwner, ok := accountsService.TenantOwnerUserID(tenantID); ok {
		return tenantOwner, nil
	}
	// A tenant with no owner member (delete path, or a manually seeded tenant):
	// enroll the node unowned and visible to the tenant, so it is usable and the
	// operator can claim it precisely later.
	return "", nil
}

// enrollNode registers a node and assigns its ownership as one operation.
//
// The atomic capability is preferred when the registry provides it. Registering
// first and assigning afterwards leaves a registered, unowned node behind whenever
// the assignment is refused — and because the id is then taken, the same device can
// never retry. The fallback exists only so a registry without the capability keeps
// working; it enforces no allowance, which is why the real registry implements it.
func enrollNode(ctx context.Context, registry controlNodeRegistry, setter nodeOwnerSetter, input noderegistry.NodeInput, tenantID, owner, visibility string, maxNodes int) (noderegistry.NodeView, error) {
	if enroller, ok := registry.(nodeEnroller); ok {
		return enroller.RegisterOwned(ctx, input, tenantID, owner, visibility, maxNodes)
	}
	node, err := registry.Register(ctx, input)
	if err != nil {
		return noderegistry.NodeView{}, err
	}
	return assignNodeOwnership(ctx, setter, node.ID, tenantID, owner, visibility, maxNodes)
}

// ensureNodeProxyToken returns the center's restricted nk_ credential, creating
// and persisting it on first use.
//
// Shared with registerNodeProxyTokenRoute so the enrolment path and the explicit
// exchange endpoint cannot mint two different proxy credentials for one center.
func ensureNodeProxyToken(ctx context.Context, manager *config.Manager) (string, error) {
	if current := strings.TrimSpace(manager.Current().Control.NodeProxyToken); current != "" {
		return current, nil
	}
	proxyToken, err := relay.GenerateNodeProxyToken()
	if err != nil {
		return "", fmt.Errorf("generate node proxy token: %w", err)
	}
	if _, err := manager.Update(ctx, config.UpdateRequest{Values: map[string]any{
		"control.node_proxy_token": proxyToken,
	}}); err != nil {
		return "", fmt.Errorf("persist node proxy token: %w", err)
	}
	return proxyToken, nil
}
