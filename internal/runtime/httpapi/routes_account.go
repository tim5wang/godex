// godex-feature: accounts
// 用户中心 + 成员管理 + 人员邀请码（设计 §3.6）。
// 入口：/account/*（本人账户）、/tenant/members|invites（租户管理员）、
//
//	/admin/tenants/{id}/members|invites（超管）、/auth/accept-invite（凭码加入）
//
// 文档：docs/multi-tenant-commercialization-design.md §3.6
package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/accounts"
)

// accountProfileResponse is the user centre's own-view profile.
type accountProfileResponse struct {
	UserID       string `json:"user_id"`
	Email        string `json:"email,omitempty"`
	PlatformRole string `json:"platform_role,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
	TenantName   string `json:"tenant_name,omitempty"`
	TenantPlan   string `json:"tenant_plan,omitempty"`
	Role         string `json:"role,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	// SharedToken is true when the caller is the single web-token holder rather
	// than a signed-in account. That caller has no user row, so the account
	// surfaces that need one report this instead of pretending to be a user.
	SharedToken bool `json:"shared_token,omitempty"`
}

type memberResponse struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at,omitempty"`
}

// registerAccountRoutes wires the self-service account surface (design §3.6
// "用户中心"). Every route is scoped to the CALLER: the user id comes from the
// authenticated principal, never from the request, so there is no id to tamper
// with and no way to address another account.
func registerAccountRoutes(mux *http.ServeMux, accountsService *accounts.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /account", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
			return
		}
		writeJSON(w, http.StatusOK, accountProfile(accountsService, principal))
	})))

	mux.Handle("GET /account/sessions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
			return
		}
		// The shared web token is not an account, so it has no session list. Said
		// plainly rather than answered with an empty list, which would read as
		// "no devices signed in" — the opposite of the truth.
		if accountUserID(principal) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf(
				"the shared web token has no account: sign in with email and password to manage sessions"))
			return
		}
		items, err := accountsService.ListSessions(principal.UserID, sessionTokenFromRequest(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
	})))

	mux.Handle("DELETE /account/sessions/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || accountUserID(principal) == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated account"))
			return
		}
		id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid session id"))
			return
		}
		if err := accountsService.RevokeSession(principal.UserID, id); err != nil {
			if errors.Is(err, accounts.ErrSessionNotFound) {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		recordLoginEvent(accountsService, principal, accounts.LoginEventSessionRevoked, r)
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})))

	// Revoke every OTHER session. The current one survives, so pressing "sign out
	// everywhere else" does not sign the user out of the device they are on.
	mux.Handle("POST /account/sessions/revoke-others", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || accountUserID(principal) == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated account"))
			return
		}
		count, err := accountsService.RevokeOtherSessions(principal.UserID, sessionTokenFromRequest(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		recordLoginEvent(accountsService, principal, accounts.LoginEventSessionsRevoked, r)
		writeJSON(w, http.StatusOK, map[string]any{"revoked": count})
	})))

	mux.Handle("GET /account/login-events", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || accountUserID(principal) == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated account"))
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid limit %q: use a positive integer", raw))
				return
			}
			limit = parsed
		}
		items, err := accountsService.LoginEvents(principal.UserID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": items})
	})))

	// Changing the password requires the CURRENT password. The caller is already
	// authenticated, but requiring the old password bounds a stolen session: the
	// thief can act as the user for the session's lifetime, but cannot lock the
	// real owner out by changing the credential.
	mux.Handle("POST /account/password", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || accountUserID(principal) == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated account"))
			return
		}
		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		err := accountsService.ChangePassword(
			principal.UserID, sessionTokenFromRequest(r), req.CurrentPassword, req.NewPassword)
		switch {
		case err == nil:
		case errors.Is(err, accounts.ErrWrongPassword), errors.Is(err, accounts.ErrPasswordUnchanged):
			writeError(w, http.StatusBadRequest, err)
			return
		case errors.Is(err, accounts.ErrWeakPassword):
			writeError(w, http.StatusBadRequest, err)
			return
		default:
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		recordLoginEvent(accountsService, principal, accounts.LoginEventPasswordChanged, r)
		writeJSON(w, http.StatusOK, map[string]any{"changed": true})
	})))
}

// accountProfile assembles the caller's own profile. The email comes from the
// user row (the principal does not carry it), so a shared-token caller — who has
// no row — is reported as such instead of appearing to be a user with no email.
func accountProfile(accountsService *accounts.Service, principal identity.Principal) accountProfileResponse {
	response := accountProfileResponse{
		UserID:       principal.UserID,
		PlatformRole: principal.PlatformRole,
		TenantID:     principal.TenantID,
		SharedToken:  accountUserID(principal) == "",
	}
	if user, ok := accountsService.User(principal.UserID); ok {
		response.Email = user.Email
		response.CreatedAt = user.CreatedAt
	}
	if tenant, ok := accountsService.Tenant(principal.TenantID); ok {
		response.TenantName = tenant.Name
		response.TenantPlan = tenant.Plan
	}
	if principal.TenantID != "" {
		for _, member := range memberViewsOrNil(accountsService, principal.TenantID) {
			if member.UserID == principal.UserID {
				response.Role = member.Role
				break
			}
		}
	}
	return response
}

// accountUserID reports the user row this principal corresponds to, or "" for the
// shared web token (which is a credential, not an account).
//
// Keyed on the auth method rather than on the user id: "local-operator" is a
// fabricated id with no row, and a node actor reuses a placeholder id too, so
// checking the id would be a guess. The credential that authenticated is the fact.
func accountUserID(principal identity.Principal) string {
	if principal.AuthMethod != AuthMethodSession {
		return ""
	}
	return strings.TrimSpace(principal.UserID)
}

// registerMemberRoutes wires member management (design §3.6).
//
// Two route families, one implementation: a tenant owner/admin manages their own
// tenant through /tenant/*, and the platform superadmin manages any tenant through
// /admin/tenants/{id}/*. The difference is only which actor is passed down, so the
// ownership rules (no owner removal, no owner grant) live in one place and cannot
// drift between the two surfaces.
func registerMemberRoutes(mux *http.ServeMux, accountsService *accounts.Service, protected func(http.Handler) http.Handler) {
	tenantAdmin := requirePlatformAdmin(protected)

	// ---- tenant side: the caller's own tenant ----
	mux.Handle("GET /tenant/members", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID, actor, ok := tenantActor(w, r, accountsService)
		if !ok {
			return
		}
		if !accountsService.IsTenantAdmin(tenantID, actor.UserID) {
			writeError(w, http.StatusForbidden, accounts.ErrNotMemberActor)
			return
		}
		members, err := accountsService.MemberViews(tenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": memberResponses(members)})
	})))

	mux.Handle("POST /tenant/invites", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID, actor, ok := tenantActor(w, r, accountsService)
		if !ok {
			return
		}
		var req struct {
			Role     string `json:"role"`
			TTLHours int    `json:"ttl_hours"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		invite, token, err := accountsService.CreateUserInvite(accounts.CreateUserInviteInput{
			TenantID: tenantID, InvitedBy: actor.UserID, Role: req.Role,
			TTL: time.Duration(req.TTLHours) * time.Hour,
		})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		// The plaintext code is returned exactly once, here. It is not retrievable
		// afterwards, which is the point of storing only a hash.
		writeJSON(w, http.StatusCreated, map[string]any{"invite": invite, "code": token})
	})))

	mux.Handle("GET /tenant/invites", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID, actor, ok := tenantActor(w, r, accountsService)
		if !ok {
			return
		}
		invites, err := accountsService.ListUserInvites(tenantID, accounts.MemberActor{UserID: actor.UserID})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
	})))

	mux.Handle("DELETE /tenant/invites/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID, actor, ok := tenantActor(w, r, accountsService)
		if !ok {
			return
		}
		invites, err := accountsService.ListUserInvites(tenantID, accounts.MemberActor{UserID: actor.UserID})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		found := false
		for _, invite := range invites {
			if invite.ID == id {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, fmt.Errorf("invite not found: %s", id))
			return
		}
		if err := accountsService.RevokeUserInvite(id, accounts.MemberActor{UserID: actor.UserID}); err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})))

	mux.Handle("PUT /tenant/members/{userID}/role", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID, actor, ok := tenantActor(w, r, accountsService)
		if !ok {
			return
		}
		var req struct {
			Role string `json:"role"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		member, err := accountsService.SetMemberRole(tenantID, r.PathValue("userID"), req.Role,
			accounts.MemberActor{UserID: actor.UserID})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"member": memberResponse{
			UserID: member.UserID, Role: member.Role, JoinedAt: member.JoinedAt}})
	})))

	mux.Handle("DELETE /tenant/members/{userID}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID, actor, ok := tenantActor(w, r, accountsService)
		if !ok {
			return
		}
		if err := accountsService.RemoveMember(tenantID, r.PathValue("userID"),
			accounts.MemberActor{UserID: actor.UserID}); err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": true})
	})))

	// ---- admin side: any tenant, by id ----
	// The same operations, with the platform admin as the actor. Kept separate
	// routes rather than one polymorphic handler so the two authorization stories
	// stay legible: here the gate is requirePlatformAdmin, above it is a tenant role.
	mux.Handle("POST /admin/tenants/{id}/invites", tenantAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		var req struct {
			Role     string `json:"role"`
			TTLHours int    `json:"ttl_hours"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		invite, token, err := accountsService.CreateUserInvite(accounts.CreateUserInviteInput{
			TenantID: r.PathValue("id"), Role: req.Role,
			TTL: time.Duration(req.TTLHours) * time.Hour, PlatformAdmin: true,
		})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"invite": invite, "code": token})
	})))

	mux.Handle("GET /admin/tenants/{id}/invites", tenantAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		invites, err := accountsService.ListUserInvites(r.PathValue("id"), accounts.MemberActor{PlatformAdmin: true})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
	})))

	// The invite id is checked against THIS tenant's list before revoking, exactly
	// like the tenant surface above. RevokeUserInvite resolves the tenant from the id
	// itself and would happily revoke another tenant's code under this URL, which
	// would make `/admin/tenants/A/invites/<B's id>` silently mean B — the admin may
	// revoke anything, but the route must still mean what it says.
	mux.Handle("DELETE /admin/tenants/{id}/invites/{inviteID}", tenantAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		tenantID := r.PathValue("id")
		invites, err := accountsService.ListUserInvites(tenantID, accounts.MemberActor{PlatformAdmin: true})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		id := strings.TrimSpace(r.PathValue("inviteID"))
		found := false
		for _, invite := range invites {
			if invite.ID == id {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, fmt.Errorf("invite not found: %s", id))
			return
		}
		if err := accountsService.RevokeUserInvite(id, accounts.MemberActor{PlatformAdmin: true}); err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})))

	mux.Handle("DELETE /admin/tenants/{id}/members/{userID}", tenantAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		if err := accountsService.RemoveMember(r.PathValue("id"), r.PathValue("userID"),
			accounts.MemberActor{PlatformAdmin: true}); err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": true})
	})))

	mux.Handle("PUT /admin/tenants/{id}/members/{userID}/role", tenantAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		var req struct {
			Role string `json:"role"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		member, err := accountsService.SetMemberRole(r.PathValue("id"), r.PathValue("userID"), req.Role,
			accounts.MemberActor{PlatformAdmin: true})
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"member": memberResponse{
			UserID: member.UserID, Role: member.Role, JoinedAt: member.JoinedAt}})
	})))

	// Accepting an invite creates the caller's session, so it is NOT behind
	// `protected`: the one-time code IS the credential. Requiring a login as well
	// would deadlock the invite for a person who has no account yet — exactly the
	// case it exists for.
	mux.Handle("POST /auth/accept-invite", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		var req struct {
			Code     string `json:"code"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := accountsService.AcceptUserInvite(req.Code, req.Email, req.Password)
		if err != nil {
			writeError(w, statusForMemberError(err), err)
			return
		}
		recordLoginEvent(accountsService, identity.Principal{UserID: result.User.ID, TenantID: result.Tenant.ID},
			accounts.LoginEventJoinedTenant, r)
		writeJSON(w, http.StatusCreated, authSessionResponse{
			Token:     result.Token,
			ExpiresAt: result.Expires.UTC().Format(time.RFC3339Nano),
			Principal: authPrincipalResponse{
				UserID:       result.User.ID,
				TenantID:     result.Tenant.ID,
				Role:         result.Member.Role,
				Entitlements: identity.EntitlementsFor("", result.Member.Role, true),
				AuthMethod:   AuthMethodSession,
			},
			Tenant: &authTenantResponse{ID: result.Tenant.ID, Name: result.Tenant.Name, Plan: result.Tenant.Plan},
		})
	}))
}

// tenantActor resolves the caller's tenant and their identity as an actor.
//
// A caller with no tenant cannot manage members; the answer is a plain 400
// ("this is a tenant surface") rather than a 403, because there is nothing to be
// forbidden FROM — the same shape the delegation surface uses for the operator.
func tenantActor(w http.ResponseWriter, r *http.Request, accountsService *accounts.Service) (string, accounts.MemberActor, bool) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
		return "", accounts.MemberActor{}, false
	}
	if strings.TrimSpace(principal.TenantID) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf(
			"member management belongs to a tenant; this principal has none"))
		return "", accounts.MemberActor{}, false
	}
	return principal.TenantID, accounts.MemberActor{
		UserID:        principal.UserID,
		PlatformAdmin: principal.IsPlatformAdmin(),
	}, true
}

func memberResponses(views []accounts.MemberView) []memberResponse {
	out := make([]memberResponse, 0, len(views))
	for _, view := range views {
		out = append(out, memberResponse{
			UserID: view.UserID, Email: view.Email, Role: view.Role, JoinedAt: view.JoinedAt})
	}
	return out
}

func memberViewsOrNil(accountsService *accounts.Service, tenantID string) []accounts.MemberView {
	views, err := accountsService.MemberViews(tenantID)
	if err != nil {
		return nil
	}
	return views
}

// statusForMemberError maps the member-management errors onto the status that
// tells the caller what to change. Owner protection is a 409 rather than a 403:
// the caller IS allowed to manage members, they just cannot do this to the owner.
func statusForMemberError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, accounts.ErrNotMemberActor):
		return http.StatusForbidden
	case errors.Is(err, accounts.ErrInvalidInvite), errors.Is(err, accounts.ErrInvalidCredentials),
		errors.Is(err, accounts.ErrInvalidSession):
		return http.StatusUnauthorized
	case errors.Is(err, accounts.ErrOwnershipProtected):
		return http.StatusConflict
	case errors.Is(err, accounts.ErrMemberNotFound):
		return http.StatusNotFound
	case errors.Is(err, accounts.ErrInvalidMemberRole), errors.Is(err, accounts.ErrCannotGrantOwner),
		errors.Is(err, accounts.ErrInvalidEmail), errors.Is(err, accounts.ErrWeakPassword):
		return http.StatusBadRequest
	case errors.Is(err, accounts.ErrEmailTaken):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// recordLoginEvent writes one audit row, logging rather than failing on error:
// refusing a completed action over an audit row would deny service for an
// accounting problem (the same trade the consent trail makes).
func recordLoginEvent(accountsService *accounts.Service, principal identity.Principal, event string, r *http.Request) {
	if accountsService == nil {
		return
	}
	if err := accountsService.RecordLoginEvent(principal.UserID, principal.TenantID, event, requestSourceIP(r)); err != nil {
		logger.Warnf("record login event %s for user %s: %v", event, principal.UserID, err)
	}
}
