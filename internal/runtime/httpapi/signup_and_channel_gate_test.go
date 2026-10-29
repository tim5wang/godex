package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/runtime/channels/weixin"
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/relay"
)

// Binding the WeChat bot decides which WeChat identity drives an agent on this
// instance. The QR endpoints only sat behind `protected`, so ANY signed-in
// tenant user could start a login and hand the bot to themselves — the same
// class of hole as editing channels.weixin.allow_from, which decision ⑫ already
// restricted to the platform admin.
func TestWeixinAuthBindingRequiresPlatformAdmin(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)

	accountsService, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	t.Cleanup(func() { _ = accountsService.Close() })

	handler := NewHandlerWithDependencies(Dependencies{
		Config:   manager,
		Backend:  backend.NewService(cfg, nil, nil),
		Accounts: accountsService,
		WeixinAuth: stubWeixinAuth{status: weixin.WebAuthStatus{
			AccountID: "default",
			Enabled:   true,
		}},
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// A tenant owner signs up (self-signup is enabled in the shared fixture) and
	// gets a real session token.
	_, body := postAuth(t, server.URL+"/auth/register", map[string]any{
		"email": "tenant-a@example.com", "password": "correct-horse-battery",
	})
	sessionToken, _ := body["token"].(string)
	if sessionToken == "" {
		t.Fatal("expected a session token for the tenant owner")
	}

	// The credential gate alone accepts this session (that is what makes the new
	// gate load-bearing), so what is asserted is the PLATFORM role refusal.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/channels/weixin/auth"},
		{http.MethodPost, "/channels/weixin/auth/start"},
		{http.MethodPost, "/channels/weixin/auth/logout"},
	} {
		resp := doJSONWithToken(t, route.method, server.URL+route.path, map[string]string{"account_id": "default"}, sessionToken)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a tenant session = %d, want 403", route.method, route.path, resp.StatusCode)
		}
	}

	// Positive control: the operator still binds the bot, so the feature works.
	ok := doJSONWithToken(t, http.MethodGet, server.URL+"/channels/weixin/auth", nil, cfg.WebToken)
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("operator GET /channels/weixin/auth = %d, want 200", ok.StatusCode)
	}

	// And an anonymous caller is refused as unauthenticated, not as forbidden.
	anon := doJSONWithToken(t, http.MethodGet, server.URL+"/channels/weixin/auth", nil, "")
	_ = anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous GET = %d, want 401", anon.StatusCode)
	}
}

// Self-service signup mints a NEW tenant, so an open /auth/register lets anyone
// who can reach the instance create one. It is closed by default.
func TestSelfSignupRefusedByDefaultWithoutOperatorCredential(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	// newTestManager opts the shared fixture into self-signup; this case is
	// precisely about the shipped default, so turn it back off.
	if _, err := manager.Update(context.Background(), config.UpdateRequest{
		Values: map[string]any{"web.allow_self_signup": false},
	}); err != nil {
		t.Fatalf("disable self-signup: %v", err)
	}

	handler := NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: backend.NewService(cfg, nil, nil),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// Anonymous caller: refused, and told this is a policy decision.
	anon := doJSONWithToken(t, http.MethodPost, server.URL+"/auth/register", map[string]any{
		"email": "anyone@example.com", "password": "correct-horse-battery",
	}, "")
	raw, _ := io.ReadAll(anon.Body)
	_ = anon.Body.Close()
	if anon.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous signup = %d, want 403 (body=%s)", anon.StatusCode, string(raw))
	}
	if !strings.Contains(string(raw), "signup is disabled") {
		t.Errorf("refusal should name the policy, got %s", string(raw))
	}

	// The platform admin can still create a tenant by presenting the web token,
	// so closing signup does not remove the capability from the operator.
	created := doJSONWithToken(t, http.MethodPost, server.URL+"/auth/register", map[string]any{
		"email": "operator-made@example.com", "password": "correct-horse-battery", "tenant_name": "Made",
	}, cfg.WebToken)
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("operator signup = %d, want 201 (body=%s)", created.StatusCode, string(body))
	}
	var session map[string]any
	if err := json.NewDecoder(created.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if token, _ := session["token"].(string); token == "" {
		t.Fatal("operator-created tenant did not receive a session token")
	}
}

// The closed-signup refusal must not point at a route that cannot exist.
//
// It used to say "ask the platform administrator for an invite", but every invite
// code is a code to JOIN an existing tenant (design §3.6): there is no code that
// creates one. Following that advice reached a dead end, which is how the
// operator came to ask where the tenant-invite codes were issued.
func TestClosedSignupRefusalNamesAReachableRoute(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	if _, err := manager.Update(context.Background(), config.UpdateRequest{
		Values: map[string]any{"web.allow_self_signup": false},
	}); err != nil {
		t.Fatalf("disable self-signup: %v", err)
	}

	handler := NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: backend.NewService(cfg, nil, nil),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/auth/register", map[string]any{
		"email": "anyone@example.com", "password": "correct-horse-battery",
	}, "")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous signup = %d, want 403 (body=%s)", resp.StatusCode, string(raw))
	}
	message := strings.ToLower(string(raw))

	// The wrong advice: an invite cannot create a tenant, so the caller must not be
	// sent looking for one.
	if strings.Contains(message, "for an invite") {
		t.Errorf("refusal still points at an invite code, which cannot create a tenant: %s", string(raw))
	}
	// The right advice: a route that exists.
	if !strings.Contains(message, "create a tenant") {
		t.Errorf("refusal should say the administrator can create the tenant, got %s", string(raw))
	}
}

// The operator can opt back in; when enabled, anonymous signup works exactly as
// it did before (a SaaS surface needs this).
func TestSelfSignupOpenWhenOperatorOptsIn(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg) // seeds web.allow_self_signup = true

	handler := NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: backend.NewService(cfg, nil, nil),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/auth/register", map[string]any{
		"email": "walkin@example.com", "password": "correct-horse-battery",
	}, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("anonymous signup with the setting on = %d, want 201 (body=%s)", resp.StatusCode, string(body))
	}
}

// TestWeixinAuthBindingStillAllowsRelayNodeActor pins WHY the gate is
// "platform admin OR node actor" rather than platform-admin-only.
//
// /channels/* is node-scoped: with a remote node active the client proxies the
// request to that node, where it arrives wearing the relay-trust node identity.
// A platform-only gate would silently make binding a channel bot impossible on
// every remote node while still passing the center case — the kind of regression
// that only shows up when someone actually tries it on a second machine.
func TestWeixinAuthBindingStillAllowsRelayNodeActor(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.WebToken = "web-secret"
	manager := newTestManager(t, cfg)
	// Node side: joined a center with its own credential and id, which is what
	// makes a relay-trust header verifiable.
	if _, err := manager.Update(context.Background(), config.UpdateRequest{Values: map[string]any{
		"control.credential": "ck_node_secret",
		"control.node_id":    "my-laptop",
	}}); err != nil {
		t.Fatalf("seed control config: %v", err)
	}

	handler := NewHandlerWithDependencies(Dependencies{
		Config:  manager,
		Backend: backend.NewService(cfg, nil, nil),
		WeixinAuth: stubWeixinAuth{status: weixin.WebAuthStatus{
			AccountID: "default",
			Enabled:   true,
		}},
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// Sign with the EFFECTIVE node identity, exactly as the node-side relay agent
	// does (relay/agent.go signs with a.cfg.NodeID / a.cfg.Credential). Reading it
	// back instead of reusing the literals above keeps this test independent of
	// ambient state: GODEX_CONTROL_CREDENTIAL is a REAL environment variable in
	// this process, and another test exercising the self-join live-apply path sets
	// it via os.Setenv, which would otherwise win over the value seeded here and
	// make a hardcoded signature fail for reasons unrelated to the gate.
	effective := manager.Current()
	trust := relay.SignRelayTrust(strings.TrimSpace(effective.Control.NodeID), strings.TrimSpace(effective.Control.Credential))
	if trust == "" {
		t.Fatal("no relay trust signature: node id and credential must both be set")
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/channels/weixin/auth"},
		{http.MethodPost, "/channels/weixin/auth/start"},
		{http.MethodPost, "/channels/weixin/auth/logout"},
	} {
		req, err := http.NewRequest(route.method, server.URL+route.path,
			strings.NewReader(`{"account_id":"default"}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(relay.RelayTrustHeader, trust)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", route.method, route.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s as a relay node actor = %d, want 200 (remote bot binding must keep working); body=%s",
				route.method, route.path, resp.StatusCode, string(body))
		}
	}
}

// TestAllowSelfSignupIsNotPlatformAdminRestricted pins that the switch itself is
// a normal operator setting: marking it platform-admin-only would make it
// impossible to change from a node's own Settings surface.
func TestAllowSelfSignupIsNotPlatformAdminRestricted(t *testing.T) {
	manager := configGateManager(t)
	for _, section := range manager.Schema() {
		for _, field := range section.Fields {
			if field.Path == "web.allow_self_signup" {
				if field.PlatformAdmin {
					t.Fatal("web.allow_self_signup must not be platform-admin-only")
				}
				return
			}
		}
	}
	t.Fatal("web.allow_self_signup is missing from the schema")
}
