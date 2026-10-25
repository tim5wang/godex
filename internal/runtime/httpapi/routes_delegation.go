package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/accounts"
)

// Operating-access surface (design §3.6, decision ⑪).
//
// Two audiences, deliberately separate route prefixes:
//
//	/tenant/delegations*   the TENANT's view: what it granted, what was read
//	/delegations/mine      the OPERATOR's view: what it is currently allowed to read
//
// The tenant's prefix is what makes the model legible in an API shape: the grant
// belongs to the tenant, so the tenant's own console is where it is created,
// listed and withdrawn. The operator cannot create a grant for itself — there is
// no route for that, which is a design statement rather than a missing feature.
func registerTenantDelegationRoutes(mux *http.ServeMux, accountsService *accounts.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("POST /tenant/delegations", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := delegationCaller(w, r, accountsService)
		if !ok {
			return
		}
		var req tenantDelegationRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ttl, err := delegationTTL(req.TTLHours)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		record, err := accountsService.CreateDelegation(accounts.CreateDelegationInput{
			TenantID:       principal.TenantID,
			OperatorUserID: req.OperatorUserID,
			GrantedBy:      principal.UserID,
			Scope:          req.Scope,
			Reason:         req.Reason,
			TTL:            ttl,
		})
		if err != nil {
			writeError(w, statusForDelegationError(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"delegation": delegationResponseBody(record)})
	})))

	mux.Handle("GET /tenant/delegations", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := delegationCaller(w, r, accountsService)
		if !ok {
			return
		}
		records, err := accountsService.ListDelegations(principal.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"delegations": delegationResponseList(records),
			// Stated in the response so the tenant console does not have to
			// re-derive the cap and risk advertising a window the server refuses.
			"max_ttl_hours": int(identity.MaxDelegationTTL / time.Hour),
		})
	})))

	mux.Handle("DELETE /tenant/delegations/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := delegationCaller(w, r, accountsService)
		if !ok {
			return
		}
		if err := accountsService.RevokeDelegation(r.PathValue("id"), principal.UserID); err != nil {
			writeError(w, statusForDelegationError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})))

	// The audit trail is the tenant's guarantee that operating access is not
	// silent (design §3.6 "可见性"): the rows exist whether or not the operator
	// tells anyone, because they are written at the authorization seam.
	mux.Handle("GET /tenant/delegations/audit", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := delegationCaller(w, r, accountsService)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
		entries, err := accountsService.ListDelegationAudit(principal.TenantID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	})))

	mux.Handle("GET /tenant/consent", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := delegationCaller(w, r, accountsService)
		if !ok {
			return
		}
		records, err := accountsService.ConsentRecords(principal.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"records": records,
			// An empty list means no agreement is on file — reported plainly
			// rather than filled in with a default, because inventing an
			// acceptance is exactly what this record exists to prevent.
			"current_terms_version": identity.CurrentTermsVersion,
		})
	})))

	// POST /tenant/consent records a member's acceptance, for the subscription-time
	// case: signup covers registration, and this covers agreeing later to a new
	// revision.
	mux.Handle("POST /tenant/consent", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := delegationCaller(w, r, accountsService)
		if !ok {
			return
		}
		var req tenantConsentRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		version := strings.TrimSpace(req.TermsVersion)
		if version == "" {
			version = identity.CurrentTermsVersion
		}
		if !req.Accepted {
			writeError(w, http.StatusBadRequest, fmt.Errorf(
				"consent requires \"accepted\": true — an unaccepted revision must not produce a record of agreement"))
			return
		}
		if err := accountsService.RecordConsent(identity.ConsentRecord{
			TenantID:     principal.TenantID,
			UserID:       principal.UserID,
			TermsVersion: version,
			SourceIP:     requestSourceIP(r),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"recorded": true, "terms_version": version})
	})))

	// GET /delegations/mine — the operator's own active grants.
	//
	// This is the operator-side half of "no silent access": a console can show
	// exactly which tenants it may read and until when, so an operator does not
	// have to guess whether a grant is still live. Scoped to the caller, so it
	// cannot be used to enumerate who else holds grants.
	mux.Handle("GET /delegations/mine", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
			return
		}
		if accountsService == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("account store is unavailable"))
			return
		}
		if strings.TrimSpace(principal.UserID) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("the caller has no user identity"))
			return
		}
		records, err := accountsService.ListOperatorDelegations(principal.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"delegations": delegationResponseList(records)})
	})))
}

// tenantDelegationRequest is the grant body. TTLHours is expressed in hours rather
// than a duration string so the tenant console renders a number input and cannot
// send a unit the server reads differently.
type tenantDelegationRequest struct {
	OperatorUserID string `json:"operator_user_id"`
	Scope          string `json:"scope,omitempty"`
	Reason         string `json:"reason,omitempty"`
	TTLHours       int    `json:"ttl_hours,omitempty"`
}

type tenantConsentRequest struct {
	TermsVersion string `json:"terms_version,omitempty"`
	Accepted     bool   `json:"accepted"`
}

type tenantDelegationResponse struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	OperatorUserID string `json:"operator_user_id"`
	GrantedBy      string `json:"granted_by,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Reason         string `json:"reason,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	RevokedAt      string `json:"revoked_at,omitempty"`
	// Active is computed server-side so both consoles agree on liveness. A client
	// comparing timestamps itself would be a second implementation of the expiry
	// rule, and the two would eventually disagree.
	Active bool `json:"active"`
}

func delegationResponseBody(record identity.Delegation) tenantDelegationResponse {
	return delegationResponseList([]identity.Delegation{record})[0]
}

func delegationResponseList(records []identity.Delegation) []tenantDelegationResponse {
	now := time.Now().UTC()
	out := make([]tenantDelegationResponse, 0, len(records))
	for _, record := range records {
		out = append(out, tenantDelegationResponse{
			ID:             record.ID,
			TenantID:       record.TenantID,
			OperatorUserID: record.OperatorUserID,
			GrantedBy:      record.GrantedBy,
			Scope:          record.Scope,
			Reason:         record.Reason,
			CreatedAt:      record.CreatedAt,
			ExpiresAt:      record.ExpiresAt,
			RevokedAt:      record.RevokedAt,
			Active:         record.Active(now) && record.AllowsRead(),
		})
	}
	return out
}

// delegationCaller resolves the principal and refuses callers that have no tenant.
//
// A platform operator has no tenant of its own, so it has no grant surface: it
// cannot authorize being looked at, and it cannot read any tenant's grants or
// audit trail. Returning 400 with the reason (rather than falling through to an
// empty list) keeps "no tenant" from looking like "no grants".
func delegationCaller(w http.ResponseWriter, r *http.Request, accountsService *accounts.Service) (identity.Principal, bool) {
	if accountsService == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
			"operating-access grants are unavailable on this instance (account store could not be opened)"))
		return identity.Principal{}, false
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated principal"))
		return identity.Principal{}, false
	}
	if strings.TrimSpace(principal.TenantID) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf(
			"operating-access grants belong to a tenant, and this caller holds no tenant membership"))
		return identity.Principal{}, false
	}
	return principal, true
}

// delegationTTL converts the requested window, rejecting negative values.
//
// Zero is allowed and means "use the default": absent is the common case, and
// treating it as an error would make the simplest call the hardest one.
func delegationTTL(hours int) (time.Duration, error) {
	if hours < 0 {
		return 0, fmt.Errorf("invalid ttl_hours %d: use a positive number of hours", hours)
	}
	return time.Duration(hours) * time.Hour, nil
}

// statusForDelegationError maps store errors to status codes.
//
// The tenant-admin refusal is a 403 rather than a 400: the request is well-formed
// and the caller is authenticated — it is the authority that is missing, and the
// two are worth distinguishing when reading logs.
func statusForDelegationError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case isDelegationError(err, accounts.ErrNotTenantAdmin):
		return http.StatusForbidden
	case isDelegationError(err, accounts.ErrDelegationNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// isDelegationError matches store sentinels, including when they are wrapped.
func isDelegationError(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// requestSourceIP records where an acceptance came from.
//
// The transport's peer address, not X-Forwarded-For: when a proxy is involved the
// forwarded value is client-supplied and therefore not evidence. Recording the
// proxy address is honest (and the operator identity in the audit trail, not an
// address, is what actually identifies a reader).
func requestSourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
