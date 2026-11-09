package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/usage"
)

// newUsageAndAuthTestServer wires BOTH multi-tenant sign-in and the usage store.
//
// Both are required, for reasons that each hid a finding:
//   - without accounts, a login-session token falls through to the operator
//     principal, so every caller would look like the superadmin and a refusal test
//     would pass for the wrong reason;
//   - without a usage store, /usage/* and /v1/biz/keys* do not register at all, so
//     a sweep sees 404 — and a 404 from an unregistered route is NOT evidence that a
//     gate exists. An earlier sweep of mine reported these as 404 and I read it as
//     "nothing to see"; that is how the holes below stayed hidden.
func newUsageAndAuthTestServer(t *testing.T, webToken string) (*httptest.Server, *usage.Service) {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	manager := newTestManager(t, cfg)
	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })
	store, err := usage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("usage store: %v", err)
	}
	usageService := usage.NewService(store)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  service,
		Accounts: accountsService,
		Usage:    usageService,
	}))
	t.Cleanup(server.Close)
	return server, usageService
}

// TestTenantUserIsRefusedTheUsageAndBizKeySurfaces covers the meta LLM gateway's
// key/model/usage administration AND the biz keys (the operator-issued machine
// credentials for the Agent Step Platform).
//
// Measured 2026-11-10 with a TENANT MEMBER's session, before the gate:
//
//	GET    /usage/keys             -> 200
//	GET    /usage/models|summary|calls|sessions|time-series -> 200
//	GET    /v1/biz/keys            -> 200 (the operator's key list)
//	POST   /v1/biz/keys            -> 201 (created one)
//	POST   /v1/biz/keys/{id}/reveal -> 200 WITH THE PLAINTEXT SECRET
//	DELETE /v1/biz/keys/{id}        -> 204, and the key was really gone
//
// So any signed-in user of any tenant could read, mint, reveal and destroy the
// platform's machine credentials.
func TestTenantUserIsRefusedTheUsageAndBizKeySurfaces(t *testing.T) {
	server, _ := newUsageAndAuthTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	calls := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"usage keys", http.MethodGet, "/usage/keys", nil},
		{"usage models", http.MethodGet, "/usage/models", nil},
		{"usage summary", http.MethodGet, "/usage/summary", nil},
		{"usage calls", http.MethodGet, "/usage/calls", nil},
		{"usage sessions", http.MethodGet, "/usage/sessions", nil},
		{"usage time series", http.MethodGet, "/usage/time-series", nil},
		{"biz keys list", http.MethodGet, "/v1/biz/keys", nil},
		{"biz key create", http.MethodPost, "/v1/biz/keys", map[string]any{"name": "tenant-made", "pin": "654321"}},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, tenantToken, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as tenant: status = %d, want 403: %v", tc.name, resp.StatusCode, body)
			}
		})
	}
}

// TestTenantCannotDestroyTheOperatorsBizKey asserts the OUTCOME rather than a
// status code, because the destructive half is what actually mattered: the probe
// showed a tenant member's DELETE removed the operator's key for real.
func TestTenantCannotDestroyTheOperatorsBizKey(t *testing.T) {
	server, usageService := newUsageAndAuthTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/v1/biz/keys", "web-secret", map[string]any{
		"name": "operator-key", "pin": "123456",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator create biz key: %d %v", resp.StatusCode, body)
	}
	keyID, _ := body["id"].(string)
	if keyID == "" {
		if key, ok := body["key"].(map[string]any); ok {
			keyID, _ = key["id"].(string)
		}
	}
	if keyID == "" {
		t.Fatalf("no biz key id in create response: %v", body)
	}

	// Destroying and revealing must both be refused.
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/v1/biz/keys/"+keyID, tenantToken, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant delete biz key: status = %d, want 403", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodPost, server.URL+"/v1/biz/keys/"+keyID+"/reveal", tenantToken, map[string]any{"pin": "123456"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant reveal biz key: status = %d, want 403", resp.StatusCode)
	}

	// The key must still exist: status codes alone would not prove the delete
	// had not gone through.
	if _, err := usageService.GetBizKey(keyID); err != nil {
		t.Fatalf("the operator's biz key did not survive the tenant's delete: %v", err)
	}
}

// TestOperatorStillReachesTheUsageAndBizKeySurfaces is the other half: these are
// the operator's own gateway surfaces, so the gate must not refuse them.
func TestOperatorStillReachesTheUsageAndBizKeySurfaces(t *testing.T) {
	server, _ := newUsageAndAuthTestServer(t, "web-secret")

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"usage keys", http.MethodGet, "/usage/keys"},
		{"usage models", http.MethodGet, "/usage/models"},
		{"usage summary", http.MethodGet, "/usage/summary"},
		{"biz keys list", http.MethodGet, "/v1/biz/keys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, "web-secret", nil)
			if resp.StatusCode == http.StatusForbidden {
				t.Fatalf("operator refused on %s: %v", tc.name, body)
			}
		})
	}
}

// instanceWideCalls are the surfaces whose SUBJECT is the instance answering the
// request: its own service runtime, and which channel bots are bound to it.
//
// Both were readable by a tenant member before 2026-11-10:
//
//	POST /runtime/service/restart -> 400 "service runtime control is unavailable"
//	GET  /channels                -> 200 with the channel list
//
// The 400 is the point worth keeping: it is the handler's own nil-guard, so the
// request had passed authentication and reached the handler. On a configured
// instance the same call would restart the service — letting any signed-in user of
// any tenant bounce the platform and every other tenant's traffic with it.
func instanceWideCalls() []struct {
	name   string
	method string
	path   string
	body   any
} {
	return []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"runtime service status", http.MethodGet, "/runtime/service", nil},
		{"runtime service restart", http.MethodPost, "/runtime/service/restart", nil},
		{"channel status", http.MethodGet, "/channels", nil},
	}
}

// TestTenantUserIsRefusedTheInstanceWideSurfaces covers the two above.
//
// They use requirePlatformAdminOrNodeActor rather than requireCenterLocalData: the
// pass/deny set is identical, and for these two the "an instance-wide setting a node
// must still be able to change about itself" spelling is the accurate intent —
// /channels and /runtime/service are node-scoped in the UI, so a remote node's own
// page arrives as the relay-trust node identity and must keep working.
func TestTenantUserIsRefusedTheInstanceWideSurfaces(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")
	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	for _, tc := range instanceWideCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, tenantToken, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as tenant: status = %d, want 403: %v", tc.name, resp.StatusCode, body)
			}
		})
	}
}

// TestOperatorStillReachesTheInstanceWideSurfaces: not 403 for the operator (a
// specific code would be lifecycle semantics — nil providers answer 400/200).
func TestOperatorStillReachesTheInstanceWideSurfaces(t *testing.T) {
	server := newCenterLocalTestServer(t, "web-secret")

	for _, tc := range instanceWideCalls() {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, "web-secret", tc.body)
			if resp.StatusCode == http.StatusForbidden {
				t.Fatalf("operator refused on %s: %v", tc.name, body)
			}
		})
	}
}

// TestNodeActorStillReachesTheInstanceWideSurfaces pins the exemption. Without it,
// selecting a remote node would break that node's own service/channel pages — the
// same trap documented for /channels/* and restricted config settings.
func TestNodeActorStillReachesTheInstanceWideSurfaces(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"control.credential": "ck_node_secret",
		"control.node_id":    "my-laptop",
	}}); err != nil {
		t.Fatalf("seed control config: %v", err)
	}
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	server := httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
	t.Cleanup(server.Close)
	trust := relay.SignRelayTrust("my-laptop", "ck_node_secret")

	for _, tc := range instanceWideCalls() {
		t.Run(tc.name, func(t *testing.T) {
			var payload []byte
			if tc.body != nil {
				data, err := json.Marshal(tc.body)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				payload = data
			}
			req, err := http.NewRequest(tc.method, server.URL+tc.path, bytes.NewReader(payload))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(relay.RelayTrustHeader, trust)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusForbidden {
				t.Fatalf("node actor refused on %s: remote operation would break", tc.name)
			}
		})
	}
}
