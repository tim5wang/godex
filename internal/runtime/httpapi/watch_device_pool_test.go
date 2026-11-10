package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// newWatchPoolServer wires the watch surface together with the account store and the
// node registry, which is what makes a paired wrist consume a device slot.
//
// A web token is configured (unlike newWatchTestServer): these tests need a REAL
// principal at pairing time, because the device's owner comes from the approving
// principal and nothing else.
func newWatchPoolServer(t *testing.T) (*httptest.Server, *watchauth.Service, *noderegistry.Registry, *accounts.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))

	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	registry, err := noderegistry.New(filepath.Join(t.TempDir(), "nodes.json"), 0)
	if err != nil {
		t.Fatalf("noderegistry.New: %v", err)
	}
	auth, err := watchauth.New(filepath.Join(cfg.StateDir, "watch", "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:          manager,
		Backend:         service,
		WatchAuth:       auth,
		Accounts:        accountsService,
		ControlRegistry: registry,
	}))
	t.Cleanup(server.Close)
	return server, auth, registry, accountsService
}

// startPairing begins a pairing and returns both codes.
func startPairing(t *testing.T, server *httptest.Server, name string) (userCode, deviceCode string) {
	t.Helper()
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{"name": name}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/start status=%d body=%s", resp.StatusCode, body)
	}
	var pairing struct {
		UserCode   string `json:"user_code"`
		DeviceCode string `json:"device_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode pairing: %v (%s)", err, body)
	}
	if pairing.UserCode == "" || pairing.DeviceCode == "" {
		t.Fatalf("pairing missing codes: %s", body)
	}
	return pairing.UserCode, pairing.DeviceCode
}

// approvePairing approves a pending code with the given credential, which is what
// decides the device's owner.
func approvePairing(t *testing.T, server *httptest.Server, userCode, credential string) (*http.Response, []byte) {
	t.Helper()
	return postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{"user_code": userCode}, credential)
}

// pairWatchAs runs the whole handshake with the given credential and returns the
// device plus the watch credential the wrist would then poll for.
func pairWatchAs(t *testing.T, server *httptest.Server, credential string) (watchauth.Device, string) {
	t.Helper()
	userCode, deviceCode := startPairing(t, server, "Wrist")
	resp, approved := approvePairing(t, server, userCode, credential)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/approve status=%d body=%s", resp.StatusCode, approved)
	}
	var device watchauth.Device
	if err := json.Unmarshal(approved, &device); err != nil {
		t.Fatalf("decode device: %v (%s)", err, approved)
	}
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/token", map[string]any{"device_code": deviceCode}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token status=%d body=%s", resp.StatusCode, body)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode token: %v (%s)", err, body)
	}
	if result.Token == "" {
		t.Fatalf("no watch credential issued: %s", body)
	}
	return device, result.Token
}

// delWatch issues an authenticated DELETE and reads the body.
func delWatch(t *testing.T, url, credential string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// TestTenantWatchConsumesADeviceSlot is the point of merging watches into the device
// pool: the plan's unit is devices PER MEMBER, so a wrist has to occupy one of its
// owner's slots — otherwise a user could pair unlimited watches and the "2 devices"
// allowance would describe only some of their hardware.
func TestTenantWatchConsumesADeviceSlot(t *testing.T) {
	server, _, registry, accountsService := newWatchPoolServer(t)
	token, tenantID, userID := registerTenantOwner(t, server, "owner@example.com")
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}

	device, _ := pairWatchAs(t, server, token)
	if device.OwnerUserID != userID || device.TenantID != tenantID {
		t.Fatalf("device = (owner %q, tenant %q), want (%q, %q)",
			device.OwnerUserID, device.TenantID, userID, tenantID)
	}
	// And it really reached the device account, as a watch.
	nodes, err := registry.List(context.Background())
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Kind != noderegistry.KindWatch || nodes[0].OwnerUserID != userID {
		t.Fatalf("registry = %+v, want one watch owned by %s", nodes, userID)
	}

	// The owner's second device: the wrist already took the only slot.
	userCode, _ := startPairing(t, server, "Second")
	resp, body := approvePairing(t, server, userCode, token)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second device status=%d body=%s, want 409 at the allowance", resp.StatusCode, body)
	}

	// A refused pairing must not have consumed its code: the user deletes a device and
	// retries with the SAME code rather than having to start over.
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 2); err != nil {
		t.Fatalf("raise allowance: %v", err)
	}
	resp, body = approvePairing(t, server, userCode, token)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry after raising the allowance status=%d body=%s, want 201", resp.StatusCode, body)
	}
}

// TestRevokingAWatchReleasesTheSlot: without this the allowance is a ratchet, and at
// the limit the user's only remedy (delete the old device) would be the action that
// fails to help.
func TestRevokingAWatchReleasesTheSlot(t *testing.T) {
	server, _, registry, accountsService := newWatchPoolServer(t)
	token, tenantID, _ := registerTenantOwner(t, server, "owner@example.com")
	if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 1); err != nil {
		t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
	}

	device, _ := pairWatchAs(t, server, token)
	resp, body := delWatch(t, server.URL+"/watch/devices/"+device.DeviceID, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", resp.StatusCode, body)
	}
	// The registry row is gone, so the slot is free again.
	nodes, err := registry.List(context.Background())
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("registry = %+v, want the revoked watch removed", nodes)
	}
	// Pairing again succeeds, which is the property the user actually needs.
	pairWatchAs(t, server, token)
}

// TestWatchDevicesAreScopedToTheirOwner: the registry holds every wrist on this node,
// so an unfiltered list handed one user another's devices — and revoking by id accepted
// anyone's, which is a way to cut off a stranger's hardware.
func TestWatchDevicesAreScopedToTheirOwner(t *testing.T) {
	server, _, _, accountsService := newWatchPoolServer(t)
	tokenA, tenantA, _ := registerTenantOwner(t, server, "a@example.com")
	tokenB, tenantB, _ := registerTenantOwner(t, server, "b@example.com")
	for _, tenantID := range []string{tenantA, tenantB} {
		if _, err := accountsService.SetTenantMaxDevicesPerUser(tenantID, 1); err != nil {
			t.Fatalf("SetTenantMaxDevicesPerUser: %v", err)
		}
		_ = tenantID
	}
	deviceA, _ := pairWatchAs(t, server, tokenA)

	resp, body := getWatch(t, server.URL+"/watch/devices", tokenB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list as B: status=%d body=%s", resp.StatusCode, body)
	}
	var devices []watchauth.Device
	if err := json.Unmarshal(body, &devices); err != nil {
		t.Fatalf("decode devices: %v (%s)", err, body)
	}
	for _, device := range devices {
		if device.DeviceID == deviceA.DeviceID {
			t.Fatalf("tenant B saw tenant A's watch: %+v", device)
		}
	}
	// And A does see its own, so the filter is not a blanket empty list.
	resp, body = getWatch(t, server.URL+"/watch/devices", tokenA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list as A: status=%d body=%s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &devices); err != nil {
		t.Fatalf("decode devices: %v (%s)", err, body)
	}
	seen := false
	for _, device := range devices {
		if device.DeviceID == deviceA.DeviceID {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("owner could not see its own watch: %+v", devices)
	}

	// B must not be able to revoke A's device either.
	resp, _ = delWatch(t, server.URL+"/watch/devices/"+deviceA.DeviceID, tokenB)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B revoking A's watch: status=%d, want 404", resp.StatusCode)
	}
	// A still can, so the refusal was about ownership rather than a broken route.
	resp, body = delWatch(t, server.URL+"/watch/devices/"+deviceA.DeviceID, tokenA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("A revoking its own watch: status=%d body=%s", resp.StatusCode, body)
	}
}
