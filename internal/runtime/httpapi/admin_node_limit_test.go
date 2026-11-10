package httpapi

import (
	"net/http"
	"testing"
)

// TestAdminDeviceAllowanceRequiresExplicitValue: 0 means unlimited, so an omitted
// field must NOT be read as 0 — that would silently lift a limit the operator was
// trying to enforce. Same reasoning as the sharing switch's explicit false.
func TestAdminDeviceAllowanceRequiresExplicitValue(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	if resp, _ := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatal("seed register failed")
	}

	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-devices-per-user", "web-secret", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when max_devices_per_user is omitted", resp.StatusCode)
	}
	// The member limit is a separate endpoint and needs the same explicitness, or an
	// empty body would quietly mean "unlimited people".
	resp, _ = doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-users", "web-secret", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when max_users is omitted", resp.StatusCode)
	}
}

// TestAdminDeviceAllowanceRejectsNegative: a negative allowance has no meaning, and
// silently clamping it to unlimited would turn a typo into a policy change.
func TestAdminDeviceAllowanceRejectsNegative(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-devices-per-user", "web-secret",
		map[string]any{"max_devices_per_user": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a negative allowance", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-users", "web-secret",
		map[string]any{"max_users": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a negative member allowance", resp.StatusCode)
	}
}

// TestAdminDeviceAllowancePersists: the value must reach the seam that enforces it
// (the policy read) AND appear in the tenant list, so the console renders from one
// source rather than re-deriving it.
func TestAdminDeviceAllowancePersists(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-devices-per-user",
		"web-secret", map[string]any{"max_devices_per_user": 2})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["max_devices_per_user"] != float64(2) {
		t.Fatalf("response = %v, want max_devices_per_user 2", body)
	}
	if got := accountsService.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("TenantMaxDevicesPerUser = %d, want 2", got)
	}
	// Writing the device allowance must not disturb the member allowance.
	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-users",
		"web-secret", map[string]any{"max_users": 5}); resp.StatusCode != http.StatusOK {
		t.Fatalf("max-users: %d %v", resp.StatusCode, body)
	}
	if got := accountsService.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("member write changed the device allowance to %d, want 2", got)
	}

	// The tenant list carries both, so the console renders from one source.
	_, listed := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := listed["tenants"].([]any)
	row, _ := tenants[0].(map[string]any)
	if row["max_devices_per_user"] != float64(2) {
		t.Errorf("list shows max_devices_per_user = %v, want 2", row["max_devices_per_user"])
	}
	if row["max_users"] != float64(5) {
		t.Errorf("list shows max_users = %v, want 5", row["max_users"])
	}
	// And the device count is reported alongside (0 here: no device is enrolled).
	if row["node_count"] != float64(0) {
		t.Errorf("list shows node_count = %v, want 0", row["node_count"])
	}
}

// TestAdminAllowanceWritesDoNotClobberSharing: the policies are separate columns and
// separate endpoints, so an operator changing one must not revert another. Each
// direction is asserted, because a single-column UPDATE that forgets the other column
// shows up differently depending on which write lands second.
func TestAdminAllowanceWritesDoNotClobberSharing(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/sharing",
		"web-secret", map[string]any{"enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("sharing off: %d %v", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-devices-per-user",
		"web-secret", map[string]any{"max_devices_per_user": 2}); resp.StatusCode != http.StatusOK {
		t.Fatalf("max devices: %d %v", resp.StatusCode, body)
	}
	if accountsService.TenantSharingEnabled(tenantID) {
		t.Error("setting the allowance re-enabled sharing")
	}

	// Reverse: toggling sharing must leave the allowance in place.
	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/sharing",
		"web-secret", map[string]any{"enabled": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("sharing on: %d %v", resp.StatusCode, body)
	}
	if got := accountsService.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("sharing toggle changed the allowance to %d, want 2", got)
	}
}

// TestAdminDeviceAllowanceRequiresPlatformAdmin: the allowance is a commercial
// control, so a tenant must not be able to raise its own limit.
func TestAdminDeviceAllowanceRequiresPlatformAdmin(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-devices-per-user", "", map[string]any{"max_devices_per_user": 9})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without an operator credential", resp.StatusCode)
	}
}

// TestAdminMaxDailyBytesRequiresExplicitValue: 0 means unlimited, so an omitted field
// must NOT be read as 0 — that would silently lift the allowance the operator was
// trying to enforce.
func TestAdminMaxDailyBytesRequiresExplicitValue(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-daily-bytes", "web-secret", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when max_daily_bytes is omitted", resp.StatusCode)
	}
}

func TestAdminMaxDailyBytesRejectsNegative(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-daily-bytes", "web-secret",
		map[string]any{"max_daily_bytes": -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a negative allowance", resp.StatusCode)
	}
}

// TestAdminMaxDailyBytesPersistsAndIsSeparateFromTheDeviceAllowance: the value must
// reach the seam that enforces it (the policy read), and writing it must not revert
// the device allowance — the two answer different questions and share a row.
func TestAdminMaxDailyBytesPersistsAndIsSeparateFromTheDeviceAllowance(t *testing.T) {
	server, accountsService := newAdminServer(t, "web-secret")
	_, session := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "founder@example.com", "password": "correct-horse",
	})
	tenantID, _ := (session["tenant"].(map[string]any))["id"].(string)

	const hundredMB = 100 * 1024 * 1024
	// Set the device allowance first so the byte write has something to clobber.
	if resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-devices-per-user",
		"web-secret", map[string]any{"max_devices_per_user": 2}); resp.StatusCode != http.StatusOK {
		t.Fatalf("max-devices-per-user: %d %v", resp.StatusCode, body)
	}

	resp, body := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/"+tenantID+"/max-daily-bytes",
		"web-secret", map[string]any{"max_daily_bytes": hundredMB})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["max_daily_bytes"] != float64(hundredMB) {
		t.Fatalf("response = %v, want max_daily_bytes %d", body, hundredMB)
	}
	// The stored policy is what the forwarding seam reads.
	if got := accountsService.TenantMaxDailyBytes(tenantID); got != hundredMB {
		t.Errorf("TenantMaxDailyBytes = %d, want %d", got, hundredMB)
	}
	if got := accountsService.TenantMaxDevicesPerUser(tenantID); got != 2 {
		t.Errorf("the byte write changed the device allowance to %d, want 2", got)
	}

	// The tenant list carries it too, so the console renders from one source.
	_, listed := doJSON(t, http.MethodGet, server.URL+"/admin/tenants", "web-secret", nil)
	tenants, _ := listed["tenants"].([]any)
	row, _ := tenants[0].(map[string]any)
	if row["max_daily_bytes"] != float64(hundredMB) {
		t.Errorf("list shows max_daily_bytes = %v, want %d", row["max_daily_bytes"], hundredMB)
	}
	// The admin list must expose the field, or the console cannot render it at all.
	if _, ok := row["max_daily_bytes"]; !ok {
		t.Errorf("tenant list omits max_daily_bytes: %v", row)
	}
}

// TestAdminMaxDailyBytesRequiresPlatformAdmin: the traffic allowance is a commercial
// control, so a tenant must not be able to raise its own.
func TestAdminMaxDailyBytesRequiresPlatformAdmin(t *testing.T) {
	server, _ := newAdminServer(t, "web-secret")
	resp, _ := doJSON(t, http.MethodPut, server.URL+"/admin/tenants/tnt_x/max-daily-bytes", "",
		map[string]any{"max_daily_bytes": 1})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without an operator credential", resp.StatusCode)
	}
}
