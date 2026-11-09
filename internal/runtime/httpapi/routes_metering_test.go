package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/metering"
)

func newMeteringServer(t *testing.T) (*httptest.Server, *metering.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	meteringService, err := metering.New(t.TempDir())
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	t.Cleanup(func() { _ = meteringService.Close() })
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  backend.NewService(cfg, nil, nil),
		Metering: meteringService,
	}))
	t.Cleanup(server.Close)
	return server, meteringService
}

// TestTenantMeteringIsScopedToTheCaller is the isolation property: forwarded-usage
// counters describe someone else's machines, so a signed-in tenant must see only
// its own bucket — and must not be able to ask for another tenant via a query
// parameter.
func TestTenantMeteringIsScopedToTheCaller(t *testing.T) {
	server, _ := newMeteringServer(t)
	_, sessionA := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "a@example.com", "password": "correct-horse",
	})
	_, sessionB := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "b@example.com", "password": "correct-horse",
	})
	tenantA, _ := (sessionA["tenant"].(map[string]any))["id"].(string)
	tenantB, _ := (sessionB["tenant"].(map[string]any))["id"].(string)
	tokenA, _ := sessionA["token"].(string)

	// A tenant cannot point the query at another tenant, because the route derives
	// the tenant from the principal and ignores any supplied id.
	resp, body := doJSON(t, http.MethodGet,
		server.URL+"/usage/relay?tenant_id="+tenantB, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	summary, _ := body["summary"].(map[string]any)
	if summary["tenant_id"] != tenantA {
		t.Fatalf("summary tenant = %v, want the caller's own tenant %q", summary["tenant_id"], tenantA)
	}
}

func TestTenantMeteringRequiresAuthentication(t *testing.T) {
	server, _ := newMeteringServer(t)
	resp, _ := doJSON(t, http.MethodGet, server.URL+"/usage/relay", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a credential", resp.StatusCode)
	}
}

// TestAdminMeteringCrossesTenants: reconciliation needs every tenant's row, so the
// operator route must cross boundaries — and a tenant user must not reach it.
func TestAdminMeteringCrossesTenants(t *testing.T) {
	server, meteringService := newMeteringServer(t)
	_, sessionA := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "a@example.com", "password": "correct-horse",
	})
	tokenA, _ := sessionA["token"].(string)

	for _, record := range []metering.Record{
		{TenantID: "t1", RequestBytes: 10, ResponseBytes: 20},
		{TenantID: "t2", RequestBytes: 30, ResponseBytes: 40},
	} {
		if err := meteringService.Record(record); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/usage/relay", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	tenants, _ := body["tenants"].([]any)
	if len(tenants) != 2 {
		t.Fatalf("tenants = %d, want 2 rows", len(tenants))
	}
	// The grand total must equal the sum of the rows. A mismatch is how an
	// unattributed row would show up, which is why both are returned.
	total, _ := body["total"].(map[string]any)
	if total["request_bytes"] != float64(40) {
		t.Errorf("total request_bytes = %v, want 40", total["request_bytes"])
	}
	if total["response_bytes"] != float64(60) {
		t.Errorf("total response_bytes = %v, want 60", total["response_bytes"])
	}

	// A signed-in tenant user must not read the platform-wide view.
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/admin/usage/relay", tokenA, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant user status = %d, want 403", resp.StatusCode)
	}
}

func TestAdminMeteringRequiresAuthentication(t *testing.T) {
	server, _ := newMeteringServer(t)
	resp, _ := doJSON(t, http.MethodGet, server.URL+"/admin/usage/relay", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestMeteringWindowRejectsBadInput: a malformed window must be a 400, not a
// silently different period (which would make reconciliation numbers wrong rather
// than missing).
func TestMeteringWindowRejectsBadInput(t *testing.T) {
	server, _ := newMeteringServer(t)
	for _, query := range []string{"?since=not-a-time", "?hours=0", "?hours=-3", "?hours=abc"} {
		resp, _ := doJSON(t, http.MethodGet, server.URL+"/usage/relay"+query, "web-secret", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("query %q status = %d, want 400", query, resp.StatusCode)
		}
	}
}

// TestMeteringUnavailableWithoutStore: an instance whose metering store could not
// be opened must say so, rather than report zero usage (which reads as "no
// traffic" instead of "no accounting").
func TestMeteringUnavailableWithoutStore(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: backend.NewService(cfg, nil, nil),
	}))
	t.Cleanup(server.Close)

	for _, path := range []string{"/usage/relay", "/admin/usage/relay"} {
		resp, _ := doJSON(t, http.MethodGet, server.URL+path, "web-secret", nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503 without a metering store", path, resp.StatusCode)
		}
	}
}

// TestMeteringWindowHoursScopesResult: the window is applied, so a row older than
// the requested period is excluded.
func TestMeteringWindowHoursScopesResult(t *testing.T) {
	server, meteringService := newMeteringServer(t)
	base := time.Now()
	meteringService.SetNow(func() time.Time { return base.Add(-48 * time.Hour) })
	if err := meteringService.Record(metering.Record{TenantID: "t1", RequestBytes: 5}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	meteringService.SetNow(func() time.Time { return base })
	if err := meteringService.Record(metering.Record{TenantID: "t1", RequestBytes: 3}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/usage/relay?hours=24", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	total, _ := body["total"].(map[string]any)
	if total["requests"] != float64(1) {
		t.Errorf("requests = %v, want 1 (only the in-window row)", total["requests"])
	}
}

// TestForwardAuditIsScopedToTheCaller: an audit row names a host:port someone
// tried to reach, which is traffic metadata about another tenant's network — and a
// refusal is even more sensitive than a success. The tenant read must derive the
// tenant from the principal and ignore any supplied id.
func TestForwardAuditIsScopedToTheCaller(t *testing.T) {
	server, meteringService := newMeteringServer(t)
	_, sessionA := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "a@example.com", "password": "correct-horse",
	})
	_, sessionB := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "b@example.com", "password": "correct-horse",
	})
	tenantA, _ := (sessionA["tenant"].(map[string]any))["id"].(string)
	tenantB, _ := (sessionB["tenant"].(map[string]any))["id"].(string)
	tokenA, _ := sessionA["token"].(string)

	_ = meteringService.RecordForwardAudit(metering.ForwardAuditRecord{
		TenantID: tenantA, UserID: "u1", NodeID: "n1", Target: "db:5432", Allowed: true,
	})
	_ = meteringService.RecordForwardAudit(metering.ForwardAuditRecord{
		TenantID: tenantB, UserID: "u2", NodeID: "n2", Target: "secrets:22", Allowed: false, Reason: "cross_tenant",
	})

	// Pointing the query at another tenant must not widen the result.
	resp, body := doJSON(t, http.MethodGet,
		server.URL+"/usage/relay/audit?tenant_id="+tenantB, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	attempts, _ := body["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 (only the caller's own): %v", len(attempts), body)
	}
	first, _ := attempts[0].(map[string]any)
	if first["target"] != "db:5432" {
		t.Errorf("target = %v, want the caller's own target db:5432", first["target"])
	}
	// The denied count is surfaced so a refusal is not skimmed past.
	if body["denied"] != float64(0) {
		t.Errorf("denied = %v, want 0 for this tenant", body["denied"])
	}
}

// TestForwardAuditAdminCrossesTenantsAndCountsDenials: the operator view spans
// tenants (that is its purpose) and reports the denied total, which is the signal
// this whole surface exists to expose.
func TestForwardAuditAdminCrossesTenantsAndCountsDenials(t *testing.T) {
	server, meteringService := newMeteringServer(t)
	_, sessionA := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "a@example.com", "password": "correct-horse",
	})
	tokenA, _ := sessionA["token"].(string)

	_ = meteringService.RecordForwardAudit(metering.ForwardAuditRecord{TenantID: "t1", NodeID: "n1", Target: "a:1", Allowed: true})
	_ = meteringService.RecordForwardAudit(metering.ForwardAuditRecord{TenantID: "t2", NodeID: "n2", Target: "b:2", Allowed: false, Reason: "cross_tenant"})

	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/usage/relay/audit", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	attempts, _ := body["attempts"].([]any)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 across tenants", len(attempts))
	}
	if body["denied"] != float64(1) {
		t.Errorf("denied = %v, want 1", body["denied"])
	}

	// A tenant user must not reach the cross-tenant view.
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/admin/usage/relay/audit", tokenA, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant user status = %d, want 403", resp.StatusCode)
	}
}

func TestForwardAuditRequiresAuthentication(t *testing.T) {
	server, _ := newMeteringServer(t)
	for _, path := range []string{"/usage/relay/audit", "/admin/usage/relay/audit"} {
		resp, _ := doJSON(t, http.MethodGet, server.URL+path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401 without a credential", path, resp.StatusCode)
		}
	}
}
