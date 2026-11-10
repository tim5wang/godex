package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
)

// quotaProxy builds a proxy whose only target is served in-process, so the quota
// seam can be exercised without a hub or a node.
func quotaProxy(gate func(string) QuotaDecision) (*ProxyHandler, *bool) {
	called := false
	proxy := NewProxyHandler(nil, nil)
	proxy.SetLocalHandler("self", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	// AuthorizeNode supplies the tenant the gate is asked about. In production the
	// tenant comes from the NodeAccess decision, never from a header.
	proxy.AuthorizeNode = func(*http.Request, string) identity.Decision {
		return identity.Decision{Allowed: true, TenantID: "t1", UserID: "u1"}
	}
	proxy.QuotaGate = gate
	return proxy, &called
}

// TestProxyQuotaRefusesWith429AndExplains is the enforcement contract: an exhausted
// allowance answers 429 and says WHICH allowance, how much was used, against what
// limit, and when it resets — a bare "Too Many Requests" would not let a client tell
// a quota from a rate limit or know that waiting helps.
func TestProxyQuotaRefusesWith429AndExplains(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	proxy, called := quotaProxy(func(tenantID string) QuotaDecision {
		if tenantID != "t1" {
			t.Errorf("gate saw tenant %q, want the NodeAccess tenant t1", tenantID)
		}
		return QuotaDecision{Allowed: false, Kind: "daily_bytes", Used: 104857600, Limit: 104857600, ResetAt: reset}
	})

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/nodes/self/proxy/api/sessions", nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if *called {
		t.Fatal("the target was served despite the quota refusal")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body["quota"] != "daily_bytes" {
		t.Errorf("quota = %v, want daily_bytes", body["quota"])
	}
	if body["used_bytes"] != float64(104857600) || body["limit_bytes"] != float64(104857600) {
		t.Errorf("used/limit = %v/%v, want 104857600 both", body["used_bytes"], body["limit_bytes"])
	}
	if body["reset_at"] != reset.Format(time.RFC3339) {
		t.Errorf("reset_at = %v, want %s", body["reset_at"], reset.Format(time.RFC3339))
	}
}

// TestProxyQuotaAllowsUnderTheAllowance: the gate must not become a blanket block.
func TestProxyQuotaAllowsUnderTheAllowance(t *testing.T) {
	proxy, called := quotaProxy(func(string) QuotaDecision {
		return QuotaDecision{Allowed: true, Kind: "daily_bytes", Used: 10, Limit: 100, ResetAt: time.Now()}
	})

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/nodes/self/proxy/api/sessions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !*called {
		t.Fatal("the request was not served although it was within the allowance")
	}
}

// TestProxyQuotaRunsAfterNodeAccess pins the ordering: an unauthorised caller must be
// told 403, not 429. Reporting a quota problem for a request that was never allowed
// would both leak that the node exists and send the caller off to wait for a reset
// that changes nothing.
func TestProxyQuotaRunsAfterNodeAccess(t *testing.T) {
	gateRan := false
	proxy := NewProxyHandler(nil, nil)
	proxy.SetLocalHandler("self", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the target was served for a denied caller")
	}))
	proxy.AuthorizeNode = func(*http.Request, string) identity.Decision {
		return identity.Denied(identity.ReasonCrossTenant)
	}
	proxy.QuotaGate = func(string) QuotaDecision {
		gateRan = true
		return QuotaDecision{Allowed: false, Kind: "daily_bytes", Limit: 1}
	}

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/nodes/self/proxy/api/sessions", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if gateRan {
		t.Error("the quota gate ran for a request NodeAccess had already refused")
	}
}

// TestProxyWithoutQuotaGateIsUnchanged guards the compatibility contract: nil must
// preserve the previous behaviour, or every deployment that never sets an allowance
// would start failing.
func TestProxyWithoutQuotaGateIsUnchanged(t *testing.T) {
	proxy, called := quotaProxy(nil)

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/nodes/self/proxy/api/sessions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 without a gate", rec.Code)
	}
	if !*called {
		t.Fatal("the request was not served although no gate is wired")
	}
}

// TestQuotaDayWindowIsUTCMidnightToMidnight pins the reset boundary: one unambiguous
// instant for every tenant. A local-day boundary would reset the same tenant's
// allowance at different times depending on where the center happens to run.
func TestQuotaDayWindowIsUTCMidnightToMidnight(t *testing.T) {
	// 23:59:59 UTC is still the same day; one second later is the next.
	start, reset := QuotaDayWindow(time.Date(2026, 11, 10, 23, 59, 59, 0, time.UTC))
	if got := start.Format(time.RFC3339); got != "2026-11-10T00:00:00Z" {
		t.Errorf("window start = %s, want 2026-11-10T00:00:00Z", got)
	}
	if got := reset.Format(time.RFC3339); got != "2026-11-11T00:00:00Z" {
		t.Errorf("reset = %s, want 2026-11-11T00:00:00Z", got)
	}

	// A non-UTC input is normalised to the same UTC day rather than to its own.
	zone := time.FixedZone("UTC+13", 13*60*60)
	start, reset = QuotaDayWindow(time.Date(2026, 11, 10, 13, 0, 0, 0, zone)) // 2026-11-10T00:00Z
	if got := start.Format(time.RFC3339); got != "2026-11-10T00:00:00Z" {
		t.Errorf("window start for a +13 input = %s, want 2026-11-10T00:00:00Z", got)
	}
	if got := reset.Sub(start); got != 24*time.Hour {
		t.Errorf("window length = %v, want 24h", got)
	}
	_ = strings.TrimSpace
}
