package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/metering"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// Platform administration surface (design §3.6). Everything here crosses tenant
// boundaries BY DESIGN — listing every tenant is the operator's job — so every
// route is requirePlatformAdmin with no exemptions.
//
// requirePlatformAdmin rather than requireCenterExecution: center execution
// exempts node actors so the center can drive remote execution, and reusing that
// gate here would let a joined node read every tenant's membership.

type adminTenantResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Plan           string `json:"plan,omitempty"`
	SharingEnabled bool   `json:"sharing_enabled"`
	MemberCount    int    `json:"member_count"`
	CreatedAt      string `json:"created_at,omitempty"`
	// MaxUsers is the tenant's member allowance; 0 means unlimited. Reported next to
	// MemberCount so the operator sees "3 of 5 people".
	MaxUsers int `json:"max_users"`
	// MaxDevicesPerUser is how many devices EACH MEMBER may own; 0 means unlimited.
	// The name keeps the unit visible: a tenant-wide device cap cannot express
	// "2 devices each", which is why this is per user and why it is not called
	// max_nodes.
	MaxDevicesPerUser int `json:"max_devices_per_user"`
	// MaxDailyBytes is the tenant's daily forwarded-byte allowance; 0 means
	// unlimited. Reported alongside the other limits so the console renders them all
	// from one source rather than reading the policy per tenant.
	MaxDailyBytes int64 `json:"max_daily_bytes"`
	// DailyUsedBytes is how much of that allowance the tenant has spent so far in
	// the CURRENT quota day. Reported so the console can show "8.2 MB of 100 MB" —
	// a limit the operator cannot see the usage of is one they cannot decide about.
	//
	// Counted in the SAME window and from the SAME counters the quota enforces
	// (relay.QuotaDayWindow over relay_usage), so the number here is the number the
	// refusal is based on. A rolling 24h figure would drift from it and be worse than
	// showing nothing: the operator would be reading a different measurement than the
	// one that just refused a user.
	DailyUsedBytes int64 `json:"daily_used_bytes"`
	// NodeCount is how many devices the tenant owns ACROSS ALL MEMBERS, so the
	// operator sees the estate at a glance. It is reported even when
	// MaxDevicesPerUser is 0, because the count is useful on its own — and it is
	// deliberately not compared against the per-user limit: the ratio an operator
	// needs ("does this member have room?") is per member, which this list does not
	// model.
	NodeCount int `json:"node_count"`
}

type adminMemberResponse struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at,omitempty"`
}

// registerAdminRoutes wires the superadmin tenant management API.
//
// A nil accounts service registers the same patterns with a 503 inside the
// handler rather than a second registration branch: the route-ownership gate
// requires exactly one registration site per pattern.
func registerAdminRoutes(mux *http.ServeMux, accountsService *accounts.Service, controlRegistry controlNodeRegistry, meteringService *metering.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /admin/tenants", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		tenants, err := accountsService.ListTenants()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]adminTenantResponse, 0, len(tenants))
		// Node counts come from the registry because this store does not own it. A
		// registry read failure is tolerated (counts stay 0) rather than failing the
		// whole list: the tenant/membership data is still correct, and an operator
		// who cannot see any tenant because the node registry hiccuped is worse off.
		nodeCounts := map[string]int{}
		if controlRegistry != nil {
			if nodes, err := controlRegistry.List(r.Context()); err == nil {
				for _, node := range nodes {
					if tenantID := strings.TrimSpace(node.TenantID); tenantID != "" {
						nodeCounts[tenantID]++
					}
				}
			}
		}
		// Today's usage comes from the same counters and the same UTC day window the
		// quota enforces (relay.QuotaDayWindow), read ONCE for the whole list rather
		// than per tenant: one grouped query instead of N. Best effort like the node
		// counts — a metering failure leaves 0, which reads as "no traffic recorded",
		// and hiding the whole tenant list over it would be worse.
		dailyUsed := dailyTenantBytes(r.Context(), meteringService)
		for _, summary := range tenants {
			out = append(out, adminTenantResponse{
				ID:                summary.Tenant.ID,
				Name:              summary.Tenant.Name,
				Plan:              summary.Tenant.Plan,
				SharingEnabled:    summary.SharingEnabled,
				MemberCount:       summary.MemberCount,
				CreatedAt:         summary.Tenant.CreatedAt,
				MaxUsers:          summary.MaxUsers,
				MaxDevicesPerUser: summary.MaxDevicesPerUser,
				MaxDailyBytes:     summary.MaxDailyBytes,
				DailyUsedBytes:    dailyUsed[summary.Tenant.ID],
				NodeCount:         nodeCounts[summary.Tenant.ID],
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
	})))

	mux.Handle("GET /admin/tenants/{id}/members", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		members, err := accountsService.Members(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		out := make([]adminMemberResponse, 0, len(members))
		for _, member := range members {
			entry := adminMemberResponse{UserID: member.UserID, Role: member.Role, JoinedAt: member.JoinedAt}
			// Best effort: a member row whose user row is gone is still reported,
			// because hiding it would make the membership look repaired.
			if user, ok := accountsService.User(member.UserID); ok {
				entry.Email = user.Email
			}
			out = append(out, entry)
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": out})
	})))

	// PUT /admin/tenants/{id}/sharing — the superadmin's sharing switch
	// (decision ④⑥). Turning it off does not rewrite node visibility: nodes stay
	// whatever their owners set, and the authorization seam simply stops honouring
	// "tenant" while the switch is off. That keeps the switch reversible without a
	// migration — turning it back on restores the previous exposure exactly.
	mux.Handle("PUT /admin/tenants/{id}/sharing", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Enabled == nil {
			// A pointer so an explicit false is distinguishable from an omitted
			// field: defaulting to false would silently disable sharing on a
			// malformed request.
			writeError(w, http.StatusBadRequest, fmt.Errorf("enabled is required"))
			return
		}
		policy, err := accountsService.SetTenantSharing(strings.TrimSpace(r.PathValue("id")), *req.Enabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id":       policy.TenantID,
			"sharing_enabled": policy.SharingEnabled,
			"updated_at":      policy.UpdatedAt,
		})
	})))
	registerTenantAllowanceRoutes(mux, accountsService, protected)

	// GET /admin/nodes — every node with its ownership, unfiltered.
	//
	// The tenant-scoped read path (/control/nodes) applies the NodeAccess policy,
	// which deliberately does NOT let a platform admin cross a tenant boundary
	// (design §4.3: cross-tenant reads need an explicit delegation, not a role).
	// Administration therefore needs its own explicitly cross-tenant surface rather
	// than a weakening of the general one — otherwise the superadmin could not even
	// see the nodes whose ownership they are supposed to manage.
	mux.Handle("GET /admin/nodes", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if controlRegistry == nil {
			writeJSON(w, http.StatusOK, map[string]any{"nodes": []noderegistry.NodeView{}})
			return
		}
		nodes, err := controlRegistry.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
	})))

	// GET /admin/tenants/{id}/join-tokens — issued enrolment codes for a tenant.
	//
	// Token hashes are never returned, so a listed record cannot be replayed; only
	// the issuance metadata is shown. The plaintext exists once, in the create
	// response.
	mux.Handle("GET /admin/tenants/{id}/join-tokens", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		tokens, err := accountsService.ListJoinTokens(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"join_tokens": tokens})
	})))

	// POST /admin/tenants/{id}/join-tokens — issue a one-time enrolment code
	// (design §3.5). This is what replaces handing out the center's full web token.
	mux.Handle("POST /admin/tenants/{id}/join-tokens", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		var req struct {
			OwnerUserID string `json:"owner_user_id,omitempty"`
			Label       string `json:"label,omitempty"`
			TTLHours    int    `json:"ttl_hours,omitempty"`
		}
		if err := decodeJSONAllowEmpty(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.TTLHours < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("ttl_hours must not be negative"))
			return
		}
		record, plaintext, err := accountsService.CreateJoinToken(accounts.CreateJoinTokenInput{
			TenantID:    r.PathValue("id"),
			OwnerUserID: req.OwnerUserID,
			Label:       req.Label,
			CreatedBy:   callerUserID(r),
			TTL:         time.Duration(req.TTLHours) * time.Hour,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// The plaintext token is returned here and NOWHERE else: the store keeps only
		// a hash, so the operator must copy it now or issue another.
		writeJSON(w, http.StatusCreated, map[string]any{
			"join_token": record,
			"token":      plaintext,
		})
	})))

	// DELETE /admin/join-tokens/{id} — revoke an unused code.
	mux.Handle("DELETE /admin/join-tokens/{id}", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		if err := accountsService.RevokeJoinToken(r.PathValue("id")); err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})))

	// GET /admin/policy — the effective platform policy, so the superadmin UI can
	// render switches from the same source the authorization seam reads instead of
	// guessing defaults on the client.
	mux.Handle("GET /admin/policy", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		// What a FRESH signup starts with (0 = unlimited). Reported here so the console
		// can show the operator the actual terms new users get, instead of the client
		// guessing from a config file it cannot read.
		freeTierMaxUsers, freeTierMaxDevices, freeTierDailyBytes := accountsService.FreeTierDefaults()
		writeJSON(w, http.StatusOK, map[string]any{
			// Default visibility for a newly claimed node (decision ⑥).
			"default_visibility": identity.VisibilityPrivate,
			"roles": []string{
				identity.RoleTenantOwner,
				identity.RoleTenantAdmin,
				identity.RoleTenantMember,
			},
			"free_tier_max_users":            freeTierMaxUsers,
			"free_tier_max_devices_per_user": freeTierMaxDevices,
			"free_tier_max_daily_bytes":      freeTierDailyBytes,
		})
	})))
}

// registerTenantAllowanceRoutes wires the per-tenant allowance endpoints.
//
// Split out of registerAdminRoutes to stay inside the function-complexity budget
// (the same reason registerMemberRoutes was split): each of these is a single-field
// write, and inlining all of them pushed the caller over the limit. They belong
// together because they are one shape — one column, one pointer field, one 200 — and
// an operator reads them as a set.
func registerTenantAllowanceRoutes(mux *http.ServeMux, accountsService *accounts.Service, protected func(http.Handler) http.Handler) {
	// PUT /admin/tenants/{id}/max-users — how many people may belong to the tenant.
	//
	// A route of its own because these two limits are two different products of the
	// plan ("how many people" vs "how many machines each"), and because writing one
	// column per endpoint is what stops setting a member limit from silently resetting
	// a machine limit or the sharing switch an operator set separately.
	mux.Handle("PUT /admin/tenants/{id}/max-users", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		var req struct {
			// A pointer so an omitted field is distinguishable from an explicit 0:
			// 0 means "unlimited", so defaulting a malformed request to 0 would
			// silently lift the limit the operator was trying to enforce.
			MaxUsers *int `json:"max_users"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.MaxUsers == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_users is required"))
			return
		}
		if *req.MaxUsers < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_users must not be negative"))
			return
		}
		policy, err := accountsService.SetTenantMaxUsers(strings.TrimSpace(r.PathValue("id")), *req.MaxUsers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id":  policy.TenantID,
			"max_users":  policy.MaxUsers,
			"updated_at": policy.UpdatedAt,
		})
	})))

	// PUT /admin/tenants/{id}/max-devices-per-user — how many devices EACH MEMBER may
	// own (pc / cloud / phone / watch all count against the same number).
	mux.Handle("PUT /admin/tenants/{id}/max-devices-per-user", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		var req struct {
			MaxDevicesPerUser *int `json:"max_devices_per_user"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.MaxDevicesPerUser == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_devices_per_user is required"))
			return
		}
		if *req.MaxDevicesPerUser < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_devices_per_user must not be negative"))
			return
		}
		policy, err := accountsService.SetTenantMaxDevicesPerUser(strings.TrimSpace(r.PathValue("id")), *req.MaxDevicesPerUser)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id":            policy.TenantID,
			"max_devices_per_user": policy.MaxDevicesPerUser,
			"updated_at":           policy.UpdatedAt,
		})
	})))

	// PUT /admin/tenants/{id}/max-daily-bytes — the tenant's daily forwarded-byte
	// allowance (design §7, basic tier).
	//
	// Its own route for the same reason /max-users is one: writing only this field
	// means setting a traffic allowance cannot silently reset the node allowance or
	// the sharing switch. A pointer distinguishes an omitted field from an explicit
	// 0, because 0 means "unlimited" and defaulting a malformed request to 0 would
	// silently lift the limit the operator was trying to enforce.
	mux.Handle("PUT /admin/tenants/{id}/max-daily-bytes", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		var req struct {
			MaxDailyBytes *int64 `json:"max_daily_bytes"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.MaxDailyBytes == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_daily_bytes is required"))
			return
		}
		if *req.MaxDailyBytes < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_daily_bytes must not be negative"))
			return
		}
		policy, err := accountsService.SetTenantMaxDailyBytes(strings.TrimSpace(r.PathValue("id")), *req.MaxDailyBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id":       policy.TenantID,
			"max_daily_bytes": policy.MaxDailyBytes,
			"updated_at":      policy.UpdatedAt,
		})
	})))
}

// dailyTenantBytes returns every tenant's forwarded bytes so far in the CURRENT quota
// day, keyed by tenant id.
//
// The window is relay.QuotaDayWindow — the same one the quota refuses on — so the figure
// the console shows is the figure that decides. A rolling 24h number would be a different
// measurement, and an operator reading it would be looking at something that did not
// cause the refusal they are investigating.
//
// One grouped query for the whole list rather than a query per tenant: the admin list is a
// scan, and N+1 reads there would make it cost more the more customers exist.
func dailyTenantBytes(ctx context.Context, meteringService *metering.Service) map[string]int64 {
	out := map[string]int64{}
	if meteringService == nil {
		return out
	}
	windowStart, _ := relay.QuotaDayWindow(time.Now())
	summaries, err := meteringService.TenantSummaries(windowStart)
	if err != nil {
		return out
	}
	for _, summary := range summaries {
		out[summary.TenantID] = summary.RequestBytes + summary.ResponseBytes
	}
	return out
}

func adminAccountsAvailable(w http.ResponseWriter, service *accounts.Service) bool {
	if service != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
		"tenant administration is unavailable on this instance (account store could not be opened)"))
	return false
}

// callerUserID returns the authenticated principal's user id, or empty when the
// route is not behind authentication.
//
// Used as the join token's creator so the record names who issued it. Empty is
// acceptable rather than an error: the enrolment path falls back to assigning the
// node to the tenant without an owner, which the operator can claim later.
func callerUserID(r *http.Request) string {
	if principal, ok := PrincipalFromContext(r.Context()); ok {
		return strings.TrimSpace(principal.UserID)
	}
	return ""
}
