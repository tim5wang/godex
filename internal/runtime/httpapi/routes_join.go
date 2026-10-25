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

		node, err := controlRegistry.Register(r.Context(), noderegistry.NodeInput{
			ID:         req.NodeID,
			Name:       req.Name,
			TrustLevel: req.TrustLevel,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("register node: %w", err))
			return
		}

		// Ownership comes from the TOKEN, never from the request body: the body is
		// attacker-controlled, and self-reported ownership is the §1.2 hole.
		owner := strings.TrimSpace(record.OwnerUserID)
		// Validate the requested owner against the tenant's membership. An owner id
		// that belongs to no member produces a node nobody in the tenant can reach
		// (private by default), which looks like a successful enrolment until someone
		// tries to use it.
		if owner != "" && !accountsService.IsMember(record.TenantID, owner) {
			writeError(w, http.StatusBadRequest, fmt.Errorf(
				"owner_user_id is not a member of this tenant"))
			return
		}
		if owner == "" {
			// Fall back to the tenant's OWNER member, not to the token's creator:
			// the creator is the platform operator whenever an operator issued the
			// code, and the operator is not a member of that tenant, so the node would
			// end up owned by nobody reachable.
			if tenantOwner, ok := accountsService.TenantOwnerUserID(record.TenantID); ok {
				owner = tenantOwner
			} else {
				// A tenant with no owner member (delete path, or a manually seeded
				// tenant): enroll the node unowned and visible to the tenant, so it is
				// usable and the operator can claim it precisely later.
				owner = ""
			}
		}
		visibility := identity.VisibilityPrivate
		if owner == "" {
			visibility = identity.VisibilityTenant
		}
		node, err = setter.SetOwnership(r.Context(), node.ID, record.TenantID, owner, visibility)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("assign node ownership: %w", err))
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
