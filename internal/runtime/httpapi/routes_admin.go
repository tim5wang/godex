package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
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
func registerAdminRoutes(mux *http.ServeMux, accountsService *accounts.Service, protected func(http.Handler) http.Handler) {
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
		for _, summary := range tenants {
			out = append(out, adminTenantResponse{
				ID:             summary.Tenant.ID,
				Name:           summary.Tenant.Name,
				Plan:           summary.Tenant.Plan,
				SharingEnabled: summary.SharingEnabled,
				MemberCount:    summary.MemberCount,
				CreatedAt:      summary.Tenant.CreatedAt,
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

	// GET /admin/policy — the effective platform policy, so the superadmin UI can
	// render switches from the same source the authorization seam reads instead of
	// guessing defaults on the client.
	mux.Handle("GET /admin/policy", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !adminAccountsAvailable(w, accountsService) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			// Default visibility for a newly claimed node (decision ⑥).
			"default_visibility": identity.VisibilityPrivate,
			"roles": []string{
				identity.RoleTenantOwner,
				identity.RoleTenantAdmin,
				identity.RoleTenantMember,
			},
		})
	})))
}

func adminAccountsAvailable(w http.ResponseWriter, service *accounts.Service) bool {
	if service != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
		"tenant administration is unavailable on this instance (account store could not be opened)"))
	return false
}
