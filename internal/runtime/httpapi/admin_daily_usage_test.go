package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/metering"
)

// newAdminMeteringServer wires the operator surface together with a metering store, which
// is what lets the tenant list report TODAY'S usage.
func newAdminMeteringServer(t *testing.T) (*httptest.Server, *accounts.Service, *metering.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	meteringService, err := metering.New(t.TempDir())
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	t.Cleanup(func() { _ = meteringService.Close() })
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  backend.NewService(cfg, nil, nil),
		Accounts: accountsService,
		Metering: meteringService,
	}))
	t.Cleanup(server.Close)
	return server, accountsService, meteringService
}

// TestAdminTenantListReportsTodaysUsage: a limit whose usage the operator cannot see is
// one they cannot decide about, so the list carries how much of the daily allowance is
// already spent.
//
// The window matters more than the number: it must be the SAME day window the quota
// refuses on (relay.QuotaDayWindow), or the operator would be reading a different
// measurement than the one that just refused a user.
func TestAdminTenantListReportsTodaysUsage(t *testing.T) {
	server, accountsService, meteringService := newAdminMeteringServer(t)
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)
	if tenantID == "" {
		t.Fatalf("no tenant from signup: %v", session)
	}

	const hundredMB = int64(100 * 1024 * 1024)
	if _, err := accountsService.SetTenantMaxDailyBytes(tenantID, hundredMB); err != nil {
		t.Fatalf("SetTenantMaxDailyBytes: %v", err)
	}

	// Today's traffic counts (both directions, like the quota's own read).
	if err := meteringService.Record(metering.Record{
		TenantID: tenantID, RequestBytes: 10, ResponseBytes: 90,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A row from BEFORE today must not: otherwise the figure would be a lifetime total
	// wearing the label "today", and it would never fall back after a reset.
	base := time.Now()
	meteringService.SetNow(func() time.Time { return base.Add(-48 * time.Hour) })
	if err := meteringService.Record(metering.Record{
		TenantID: tenantID, RequestBytes: 5000, ResponseBytes: 5000,
	}); err != nil {
		t.Fatalf("record old: %v", err)
	}
	meteringService.SetNow(func() time.Time { return base })

	_, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := body["tenants"].([]any)
	if len(tenants) != 1 {
		t.Fatalf("tenants = %d, want 1", len(tenants))
	}
	row, _ := tenants[0].(map[string]any)
	if row["max_daily_bytes"] != float64(hundredMB) {
		t.Errorf("max_daily_bytes = %v, want %d", row["max_daily_bytes"], hundredMB)
	}
	if row["daily_used_bytes"] != float64(100) {
		t.Errorf("daily_used_bytes = %v, want 100 (today's 10+90; yesterday excluded)", row["daily_used_bytes"])
	}
}

// TestAdminTenantListWithoutMeteringStillAnswers: the usage figure is best effort, like the
// node counts. A deployment with no metering store (or a store that failed to open) must
// still list its tenants — hiding every customer because accounting is unavailable would be
// the worse failure.
func TestAdminTenantListWithoutMeteringStillAnswers(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	if session == nil {
		t.Fatal("register failed")
	}
	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	tenants, _ := body["tenants"].([]any)
	if len(tenants) != 1 {
		t.Fatalf("tenants = %d, want 1", len(tenants))
	}
	row, _ := tenants[0].(map[string]any)
	if row["daily_used_bytes"] != float64(0) {
		t.Errorf("daily_used_bytes = %v, want 0 with no metering store", row["daily_used_bytes"])
	}
}
