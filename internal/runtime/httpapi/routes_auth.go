package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/accounts"
)

// Email + password sign-in (design §3.1/§3.5).
//
// These routes are deliberately NOT all behind `protected`:
//
//   - /auth/register and /auth/login cannot require a credential, because
//     obtaining the credential is the point of calling them;
//   - /auth/logout and /auth/me sit behind the same chain as the rest of the API,
//     which accepts a session token OR the web token.
//
// The web token keeps working unchanged alongside sessions, so an existing
// single-operator deployment is unaffected.

type registerRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	TenantName string `json:"tenant_name,omitempty"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// authSessionResponse is what a successful signup or login returns. It carries
// the session token once, at issue time; no endpoint re-reads it.
type authSessionResponse struct {
	Token     string                `json:"token"`
	ExpiresAt string                `json:"expires_at"`
	Principal authPrincipalResponse `json:"principal"`
	Tenant    *authTenantResponse   `json:"tenant,omitempty"`
}

type authPrincipalResponse struct {
	UserID       string   `json:"user_id"`
	TenantID     string   `json:"tenant_id,omitempty"`
	PlatformRole string   `json:"platform_role,omitempty"`
	Role         string   `json:"role,omitempty"`
	Entitlements []string `json:"entitlements"`
	AuthMethod   string   `json:"auth_method,omitempty"`
}

type authTenantResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan string `json:"plan,omitempty"`
}

// registerAuthRoutes wires the sign-in surface.
//
// Each pattern is registered exactly once, with availability decided INSIDE the
// handlers: the route-ownership gate requires one registration site per pattern,
// and a separate "service unavailable" branch would register every pattern twice.
func registerAuthRoutes(mux *http.ServeMux, manager *config.Manager, accountsService *accounts.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("POST /auth/register", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		// Self-service signup mints a NEW tenant, so leaving it open lets anyone
		// who can reach this instance create their own. It is therefore closed by
		// default and opened only by the operator (web.allow_self_signup). The
		// platform admin can still create a tenant by presenting the web token,
		// which is what keeps single-operator deployments working unchanged.
		//
		// Checked BEFORE decoding the body so an unauthenticated caller learns
		// nothing about which emails exist.
		if !selfSignupAllowed(manager, r) {
			writeError(w, http.StatusForbidden, fmt.Errorf(
				"self-service signup is disabled on this instance; ask the platform administrator for an invite (set web.allow_self_signup to enable it)"))
			return
		}
		var req registerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := accountsService.Register(accounts.RegisterInput{
			Email:      req.Email,
			Password:   req.Password,
			TenantName: req.TenantName,
		})
		if err != nil {
			writeError(w, statusForAuthError(err), err)
			return
		}
		// Consent trail (design §3.6 "用户协议"): signup is where the disclosure is
		// shown, so it is where acceptance is recorded. Written after the account
		// exists — the row needs the tenant id — and deliberately NOT fatal: failing
		// a signup over an audit row would deny service for an accounting problem.
		// The error is logged so a missing trail is visible rather than silent.
		if err := accountsService.RecordConsent(identity.ConsentRecord{
			TenantID:     result.Tenant.ID,
			UserID:       result.User.ID,
			TermsVersion: identity.CurrentTermsVersion,
			SourceIP:     requestSourceIP(r),
		}); err != nil {
			logger.Warnf("record signup consent for tenant %s: %v", result.Tenant.ID, err)
		}
		// The sign-in history (user centre) starts with the first sign-in, so signup
		// records its own event. Same best-effort trade as the consent trail.
		recordLoginEvent(accountsService, identity.Principal{UserID: result.User.ID, TenantID: result.Tenant.ID}, accounts.LoginEventRegister, r)
		writeJSON(w, http.StatusCreated, authSessionResponse{
			Token:     result.Token,
			ExpiresAt: result.Expires.UTC().Format(time.RFC3339Nano),
			Principal: authPrincipalResponse{
				UserID:       result.User.ID,
				TenantID:     result.Tenant.ID,
				Role:         result.Member.Role,
				Entitlements: entitlementsForSignup(result.Member.Role),
				AuthMethod:   AuthMethodSession,
			},
			Tenant: &authTenantResponse{ID: result.Tenant.ID, Name: result.Tenant.Name, Plan: result.Tenant.Plan},
		})
	}))

	mux.Handle("POST /auth/login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		principal, token, expires, err := accountsService.Login(req.Email, req.Password)
		if err != nil {
			// ErrInvalidCredentials covers unknown email and wrong password alike,
			// and the service equalizes timing, so this does not reveal whether the
			// account exists.
			writeError(w, statusForAuthError(err), err)
			return
		}
		recordLoginEvent(accountsService, principal, accounts.LoginEventLogin, r)
		writeJSON(w, http.StatusOK, authSessionResponse{
			Token:     token,
			ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
			Principal: principalResponseBody(principal),
		})
	}))

	// logout and me reuse the shared chain, so they accept a session token or the
	// web token without a second auth path.
	mux.Handle("POST /auth/logout", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		token := sessionTokenFromRequest(r)
		if token == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf(
				"logout requires a session token (the shared web token cannot be logged out)"))
			return
		}
		if err := accountsService.Logout(token); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Recorded AFTER the logout succeeds: an audit row saying "logged out" for a
		// logout that did not happen would be worse than no row.
		if principal, ok := PrincipalFromContext(r.Context()); ok {
			recordLoginEvent(accountsService, principal, accounts.LoginEventLogout, r)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})))

	mux.Handle("GET /auth/me", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authServiceAvailable(w, accountsService) {
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
			return
		}
		response := map[string]any{"principal": principalResponseBody(principal)}
		if tenant, found := accountsService.Tenant(principal.TenantID); found {
			response["tenant"] = authTenantResponse{ID: tenant.ID, Name: tenant.Name, Plan: tenant.Plan}
		}
		writeJSON(w, http.StatusOK, response)
	})))
}

// authServiceAvailable reports whether sign-in is usable, writing an actionable
// 503 when it is not: "sign-in is unavailable here" must be distinguishable from
// "this build has no such endpoint" when someone debugs a node.
func authServiceAvailable(w http.ResponseWriter, service *accounts.Service) bool {
	if service != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
		"email/password sign-in is unavailable on this instance (account store could not be opened)"))
	return false
}

// entitlementsForSignup reports the capabilities a fresh signup holds.
//
// Signup always creates a tenant with the caller as owner, so the entitlement is
// the tenant plan (relay-only) and never center_execution: a new tenant can
// forward to its own nodes but cannot run an agent on the center (decision ⑨).
func entitlementsForSignup(role string) []string {
	if strings.TrimSpace(role) == "" {
		return []string{}
	}
	return []string{identity.EntitlementRelayOnly}
}

func principalResponseBody(principal identity.Principal) authPrincipalResponse {
	return authPrincipalResponse{
		UserID:       principal.UserID,
		TenantID:     principal.TenantID,
		PlatformRole: principal.PlatformRole,
		Entitlements: principal.Entitlements,
		AuthMethod:   principal.AuthMethod,
	}
}

// statusForAuthError maps an accounts error onto an HTTP status.
//
// Bad credentials are 401, not 403: the caller is unauthenticated rather than
// authenticated-but-forbidden, and 403 would push a client to retry with the same
// wrong credentials.
// selfSignupAllowed reports whether this caller may create a NEW tenant through
// /auth/register.
//
// Two ways to be allowed: the setting is on (the operator opted in), or the
// caller presents the web token (the platform admin creating a tenant). The
// second is what lets a fresh instance bootstrap without enabling anonymous
// signup for everyone.
func selfSignupAllowed(manager *config.Manager, r *http.Request) bool {
	if manager == nil {
		return true
	}
	current := manager.Current()
	if current.WebAllowSelfSignup {
		return true
	}
	token := strings.TrimSpace(current.WebToken)
	return token != "" && bearerAuthorized(r, token)
}

func statusForAuthError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, accounts.ErrEmailTaken):
		return http.StatusConflict
	case errors.Is(err, accounts.ErrInvalidCredentials), errors.Is(err, accounts.ErrInvalidSession):
		return http.StatusUnauthorized
	case errors.Is(err, accounts.ErrInvalidEmail), errors.Is(err, accounts.ErrWeakPassword):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// sessionTokenFromRequest extracts a session-shaped credential from the request.
func sessionTokenFromRequest(r *http.Request) string {
	secret := extractProxyKeySecret(r)
	if strings.HasPrefix(secret, accounts.SessionTokenPrefix) {
		return secret
	}
	return ""
}
