package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

// newFreeTierServer wires the account store the same way production does — the
// httpapi creates it on demand — so the configured defaults are applied by the code
// path under test rather than by the test itself.
//
// Accounts is deliberately left nil. Passing a pre-built store would skip
// SetFreeTierDefaults and the assertion would pass vacuously.
func newFreeTierServer(t *testing.T, values map[string]any) *httptest.Server {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	if len(values) > 0 {
		if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: values}); err != nil {
			t.Fatalf("seed free-tier values: %v", err)
		}
	}
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: service,
	}))
	t.Cleanup(server.Close)
	return server
}

// TestFreshSignupGetsTheConfiguredFreeTier is the end-to-end property: an operator
// configures the free tier, and a self-serve signup starts under it instead of being
// unlimited. Before this, "2 devices / 100 MB a day" existed only in prose — nothing
// wrote a policy for a new tenant at all.
func TestFreshSignupGetsTheConfiguredFreeTier(t *testing.T) {
	server := newFreeTierServer(t, map[string]any{
		"web.allow_self_signup":              true,
		"web.free_tier_max_users":            3,
		"web.free_tier_max_devices_per_user": 2,
		"web.free_tier_max_daily_mb":         100,
	})

	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "new@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)
	if tenantID == "" {
		t.Fatalf("signup returned no tenant: %v", session)
	}

	// Read it back through the operator surface, which is what the console shows.
	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tenants: status = %d: %v", resp.StatusCode, body)
	}
	tenants, _ := body["tenants"].([]any)
	if len(tenants) != 1 {
		t.Fatalf("tenants = %d, want 1", len(tenants))
	}
	row, _ := tenants[0].(map[string]any)
	if row["max_devices_per_user"] != float64(2) {
		t.Errorf("fresh signup max_devices_per_user = %v, want 2 (the configured free tier)", row["max_devices_per_user"])
	}
	if row["max_users"] != float64(3) {
		t.Errorf("fresh signup max_users = %v, want 3 (the configured free tier)", row["max_users"])
	}
	const hundredMB = float64(100 * 1024 * 1024)
	if row["max_daily_bytes"] != hundredMB {
		t.Errorf("fresh signup max_daily_bytes = %v, want %v (100 MB as binary MB)", row["max_daily_bytes"], hundredMB)
	}
}

// TestFreshSignupUnlimitedWithoutConfiguration: the default must stay "unlimited", or
// upgrading would silently cap every tenant that had no policy before — the same
// "absence is permissive" rule the node allowance uses.
func TestFreshSignupUnlimitedWithoutConfiguration(t *testing.T) {
	server := newFreeTierServer(t, map[string]any{"web.allow_self_signup": true})

	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "new@example.com", "password": "correct-horse",
	})
	if tenantID, _ := (session["tenant"].(map[string]any))["id"].(string); tenantID == "" {
		t.Fatalf("signup returned no tenant: %v", session)
	}

	_, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := body["tenants"].([]any)
	row, _ := tenants[0].(map[string]any)
	if row["max_devices_per_user"] != float64(0) || row["max_daily_bytes"] != float64(0) {
		t.Fatalf("unconfigured signup got a limit: devices=%v bytes=%v, want 0/0 (unlimited)",
			row["max_devices_per_user"], row["max_daily_bytes"])
	}
	if row["max_users"] != float64(0) {
		t.Fatalf("unconfigured signup got a member limit: %v, want 0 (unlimited)", row["max_users"])
	}
}

// TestAdminPolicyReportsTheFreeTier: the console must be able to show what a new
// signup will actually get, from one source, rather than guessing from a config file
// the browser cannot read.
func TestAdminPolicyReportsTheFreeTier(t *testing.T) {
	server := newFreeTierServer(t, map[string]any{
		"web.allow_self_signup":              true,
		"web.free_tier_max_users":            3,
		"web.free_tier_max_devices_per_user": 2,
		"web.free_tier_max_daily_mb":         100,
	})

	resp, body := doJSON(t, http.MethodGet, server.URL+"/admin/policy", "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["free_tier_max_users"] != float64(3) {
		t.Errorf("free_tier_max_users = %v, want 3", body["free_tier_max_users"])
	}
	if body["free_tier_max_devices_per_user"] != float64(2) {
		t.Errorf("free_tier_max_devices_per_user = %v, want 2", body["free_tier_max_devices_per_user"])
	}
	if body["free_tier_max_daily_bytes"] != float64(100*1024*1024) {
		t.Errorf("free_tier_max_daily_bytes = %v, want %d", body["free_tier_max_daily_bytes"], 100*1024*1024)
	}
}

// TestFreshSignupFreeTierDefaultsToUnlimitedWhenAccountsInjected pins the deliberate
// boundary: an INJECTED account store keeps its own defaults, because its owner (a
// test, or an embedder) decides them and this package must not overwrite them.
//
// This is asserted rather than left implicit because the opposite choice would look
// harmless and would silently change the terms of every embedded deployment.
func TestFreshSignupFreeTierDefaultsToUnlimitedWhenAccountsInjected(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	if got := accountsService.TenantMaxDevicesPerUser("anything"); got != 0 {
		t.Fatalf("injected store has a device limit %d, want 0", got)
	}
	// And the injected store's signup path writes no policy row.
	if _, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "injected@example.com", "password": "correct-horse",
	}); session == nil {
		t.Fatal("register failed")
	}
	_, body := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := body["tenants"].([]any)
	row, _ := tenants[0].(map[string]any)
	if row["max_devices_per_user"] != float64(0) || row["max_daily_bytes"] != float64(0) {
		t.Fatalf("injected store signup got a limit: %v/%v, want 0/0",
			row["max_devices_per_user"], row["max_daily_bytes"])
	}
}
