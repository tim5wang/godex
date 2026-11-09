package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/services/metering"
)

// Relay metering surface (design §7 basic tier: "forward requests/bytes", and the
// P3 acceptance criterion "metering can be reconciled").
//
// Three audiences, deliberately separate routes:
//
//	GET /usage/relay            a tenant reads ITS OWN forwarded usage
//	GET /usage/relay/audit      a tenant reads ITS OWN forward attempts
//	GET /admin/usage/relay      the operator reconciles EVERY tenant
//	GET /admin/usage/relay/audit the operator reviews every tenant's attempts
//
// The tenant routes derive the tenant from the authenticated principal rather
// than from a query parameter: accepting a tenant id would let any signed-in user
// read another tenant's volumes, which is traffic metadata about someone else's
// machines. The same reasoning applies twice over to the audit trail, which names
// hosts and ports a tenant tried to reach.
func registerMeteringRoutes(mux *http.ServeMux, meteringService *metering.Service, protected func(http.Handler) http.Handler) {
	mux.Handle("GET /usage/relay", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if meteringService == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
				"relay metering is unavailable on this instance (metering store could not be opened)"))
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing identity"))
			return
		}
		// A platform operator has no tenant of its own, so the tenant view reports
		// the unscoped bucket — the operator's own direct traffic. Totals across
		// tenants are the admin route's job, not this one's.
		since, err := meteringWindow(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		summary, err := meteringService.Summary(strings.TrimSpace(principal.TenantID), since)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"summary":     summary,
			"window_from": since.UTC().Format(time.RFC3339),
			"window_to":   time.Now().UTC().Format(time.RFC3339),
		})
	})))

	mux.Handle("GET /admin/usage/relay", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if meteringService == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
				"relay metering is unavailable on this instance (metering store could not be opened)"))
			return
		}
		since, err := meteringWindow(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		summaries, err := meteringService.TenantSummaries(since)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Grand total alongside the per-tenant rows: reconciliation compares the
		// two, and a total that does not equal the sum of its rows means traffic
		// was recorded that no tenant owns (the operator's own direct usage, or an
		// unattributed row) — which is exactly the kind of gap this surface exists
		// to make visible.
		var total metering.Summary
		for _, item := range summaries {
			total.Requests += item.Requests
			total.RequestBytes += item.RequestBytes
			total.ResponseBytes += item.ResponseBytes
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenants":     summaries,
			"total":       total,
			"window_from": since.UTC().Format(time.RFC3339),
			"window_to":   time.Now().UTC().Format(time.RFC3339),
		})
	})))

	// Forward AUDIT (2026-11-10).
	//
	// A separate list from the usage summary because it answers a different
	// question and has a different shape: relay_usage is a billing counter
	// (bytes per request, only for requests that were forwarded), while this is a
	// security record of ATTEMPTS — the refused ones included, which never
	// produced any usage at all. A refused forward was previously invisible
	// everywhere, so "did anyone try to reach a service they may not touch?" had
	// no answer.
	mux.Handle("GET /usage/relay/audit", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if meteringService == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
				"relay metering is unavailable on this instance (metering store could not be opened)"))
			return
		}
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("missing identity"))
			return
		}
		since, err := meteringWindow(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		attempts, err := meteringService.ForwardAudits(strings.TrimSpace(principal.TenantID), since, meteringLimit(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"attempts":    attempts,
			"denied":      countDenied(attempts),
			"window_from": since.UTC().Format(time.RFC3339),
			"window_to":   time.Now().UTC().Format(time.RFC3339),
		})
	})))

	mux.Handle("GET /admin/usage/relay/audit", requirePlatformAdmin(protected)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if meteringService == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf(
				"relay metering is unavailable on this instance (metering store could not be opened)"))
			return
		}
		since, err := meteringWindow(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		attempts, err := meteringService.AllForwardAudits(since, meteringLimit(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"attempts":    attempts,
			"denied":      countDenied(attempts),
			"window_from": since.UTC().Format(time.RFC3339),
			"window_to":   time.Now().UTC().Format(time.RFC3339),
		})
	})))
}

// countDenied is surfaced explicitly rather than left to the caller: the denied
// rows are the reason this list exists, and making a client filter for them is
// how a refusal gets skimmed past.
func countDenied(attempts []metering.ForwardAuditRecord) int {
	denied := 0
	for _, attempt := range attempts {
		if !attempt.Allowed {
			denied++
		}
	}
	return denied
}

// meteringLimit reads an optional row bound. 0 keeps the store's bounded default.
func meteringLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// meteringWindow resolves the reconciliation window from `since` or `hours`.
//
// Defaults to 24 hours rather than "all time": an unbounded aggregate is a
// reporting query that grows with retention, and the common reconciliation
// question is about a recent period.
func meteringWindow(r *http.Request) (time.Time, error) {
	query := r.URL.Query()
	if raw := strings.TrimSpace(query.Get("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid since %q: use RFC3339 (e.g. 2026-10-25T00:00:00Z)", raw)
		}
		return parsed, nil
	}
	hours := 24
	if raw := strings.TrimSpace(query.Get("hours")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return time.Time{}, fmt.Errorf("invalid hours %q: use a positive integer", raw)
		}
		hours = parsed
	}
	return time.Now().Add(-time.Duration(hours) * time.Hour), nil
}
