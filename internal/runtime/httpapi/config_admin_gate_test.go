package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/identity"
)

func configGateManager(t *testing.T) *config.Manager {
	t.Helper()
	cfg := newTestConfig(t)
	return newTestManager(t, cfg)
}

// TestSchemaMarksWeixinAllowFromPlatformAdmin pins the tag that makes decision ⑫
// enforceable. Without it the allowlist could be edited by anyone holding the web
// token, and "only the superadmin decides who may drive the bot" would be a
// slogan.
//
// The Feishu credentials are in the same list for the same reason: that bot has
// no separate binding step, so its app id/secret ARE which bot runs here.
func TestSchemaMarksWeixinAllowFromPlatformAdmin(t *testing.T) {
	manager := configGateManager(t)
	restricted := platformAdminOnlyPaths(manager)
	for _, path := range []string{
		"channels.weixin.allow_from",
		"channels.feishu.app_id",
		"channels.feishu.app_secret",
		"web.token",
	} {
		if !restricted[path] {
			t.Errorf("%s must be marked platform-admin-only; restricted set = %v", path, restricted)
		}
	}
	// A node's own settings must NOT be restricted, or remote Settings editing
	// (which is node-scoped) would break.
	for _, path := range []string{"logging.level", "api.timeout_seconds"} {
		if restricted[path] {
			t.Errorf("%s must remain editable by a node operator", path)
		}
	}
}

func TestRestrictedConfigKeysInspectsValuesAndClearSecrets(t *testing.T) {
	manager := configGateManager(t)

	if got := restrictedConfigKeys(manager, map[string]interface{}{"logging.level": "debug"}, nil); len(got) != 0 {
		t.Errorf("unrestricted write flagged: %v", got)
	}

	got := restrictedConfigKeys(manager, map[string]interface{}{
		"channels.weixin.allow_from": []string{"wx-user"},
		"logging.level":              "debug",
	}, nil)
	if len(got) != 1 || got[0] != "channels.weixin.allow_from" {
		t.Errorf("restricted keys = %v, want [channels.weixin.allow_from]", got)
	}

	// Clearing a restricted secret is as much a change as setting it.
	got = restrictedConfigKeys(manager, nil, []string{"web.token"})
	if len(got) != 1 || got[0] != "web.token" {
		t.Errorf("clear_secrets not inspected: %v", got)
	}

	// Case and surrounding whitespace must not be a way around the gate.
	got = restrictedConfigKeys(manager, map[string]interface{}{" Web.Token ": "x"}, nil)
	if len(got) != 1 || got[0] != "web.token" {
		t.Errorf("normalization bypass: %v", got)
	}
}

func TestAuthorizeConfigWriteAllowsOperatorAndNodeActor(t *testing.T) {
	manager := configGateManager(t)
	restricted := map[string]interface{}{"channels.weixin.allow_from": []string{"wx-user"}}

	operator := identity.NewPrincipal("", "local-operator", identity.RolePlatformAdmin, identity.KindPlatform,
		identity.EntitlementCenterExecution)
	if err := authorizeConfigWrite(reqWithPrincipal(operator), manager, restricted, nil); err != nil {
		t.Errorf("operator refused: %v", err)
	}

	// A node actor writes the node's OWN config (this is the remote Settings
	// path); it must not be blocked, or multi-node settings editing breaks.
	node := identity.NewPrincipal("", "relay-node", "", identity.KindNode, identity.EntitlementRelayOnly)
	if err := authorizeConfigWrite(reqWithPrincipal(node), manager, restricted, nil); err != nil {
		t.Errorf("node actor refused: %v", err)
	}
}

func TestAuthorizeConfigWriteRefusesTenantUser(t *testing.T) {
	manager := configGateManager(t)
	tenantUser := identity.NewPrincipal("tenant-1", "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)

	err := authorizeConfigWrite(reqWithPrincipal(tenantUser), manager,
		map[string]interface{}{"channels.weixin.allow_from": []string{"wx-user"}}, nil)
	if err == nil {
		t.Fatal("a tenant user must not change the weixin allowlist")
	}
	if status := statusForConfigAuthError(err); status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
	if !strings.Contains(err.Error(), "channels.weixin.allow_from") {
		t.Errorf("error should name the offending setting, got %q", err.Error())
	}

	// ...but the same user may change an ordinary setting.
	if err := authorizeConfigWrite(reqWithPrincipal(tenantUser), manager,
		map[string]interface{}{"logging.level": "debug"}, nil); err != nil {
		t.Errorf("tenant user refused an unrestricted setting: %v", err)
	}
}

// TestAuthorizeConfigWriteFailsClosedWithoutPrincipal covers a route not behind
// authentication: editing the settings that govern who may use the instance must
// not be possible.
func TestAuthorizeConfigWriteFailsClosedWithoutPrincipal(t *testing.T) {
	manager := configGateManager(t)
	req := httptest.NewRequest(http.MethodPut, "/config", nil)

	err := authorizeConfigWrite(req, manager, map[string]interface{}{"web.token": "x"}, nil)
	if err == nil {
		t.Fatal("expected a refusal without an authenticated principal")
	}
	if status := statusForConfigAuthError(err); status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (unauthenticated, not forbidden)", status)
	}
}

// TestStatusForConfigAuthErrorIsNotForbiddenByDefault guards the 401/403 split:
// reporting an unauthenticated caller as forbidden misleads clients into
// retrying with the same absent credentials.
func TestStatusForConfigAuthErrorIsNotForbiddenByDefault(t *testing.T) {
	if status := statusForConfigAuthError(configWriteAuthError{msg: "x"}); status != http.StatusForbidden {
		t.Errorf("plain refusal status = %d, want 403", status)
	}
	if status := statusForConfigAuthError(nil); status != http.StatusOK {
		t.Errorf("nil error status = %d, want 200", status)
	}
}

// TestSchemaMarksCenterNodeProxyTokenPlatformAdmin pins the tag for the setting
// P4a clears.
//
// The center-wide nk_ is a credential that unlocks the node proxy surface, so
// whoever may write it can choose a node credential of their own: set the value,
// present it, and the resolver reports a legacy match with no node and no tenant —
// which the single-tenant policy branch allows through. Leaving it unrestricted
// hands a tenant user the access the per-node migration exists to remove.
func TestSchemaMarksCenterNodeProxyTokenPlatformAdmin(t *testing.T) {
	manager := configGateManager(t)
	if restricted := platformAdminOnlyPaths(manager); !restricted["control.node_proxy_token"] {
		t.Errorf("control.node_proxy_token must be platform-admin-only; restricted set = %v", restricted)
	}
}

// TestAuthorizeConfigWriteRefusesTenantUserRewritingCenterNodeProxyToken is the
// behavioural half: a tenant user must be refused both when setting the
// center-wide nk_ and when CLEARING it (clearing ends the legacy path, so it
// changes the same setting and must be gated identically).
func TestAuthorizeConfigWriteRefusesTenantUserRewritingCenterNodeProxyToken(t *testing.T) {
	manager := configGateManager(t)
	tenantUser := identity.NewPrincipal("tenant-1", "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)

	err := authorizeConfigWrite(reqWithPrincipal(tenantUser), manager,
		map[string]interface{}{"control.node_proxy_token": "nk_attacker_chosen"}, nil)
	if err == nil {
		t.Fatal("a tenant user must not choose the center-wide node proxy credential")
	}
	if status := statusForConfigAuthError(err); status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
	if !strings.Contains(err.Error(), "control.node_proxy_token") {
		t.Errorf("error should name the offending setting, got %q", err.Error())
	}

	if err := authorizeConfigWrite(reqWithPrincipal(tenantUser), manager, nil,
		[]string{"control.node_proxy_token"}); err == nil {
		t.Fatal("a tenant user must not be able to clear the center-wide node proxy credential")
	}

	// The operator still manages it — P4a is exactly this call.
	operator := identity.NewPrincipal("", "local-operator", identity.RolePlatformAdmin, identity.KindPlatform,
		identity.EntitlementCenterExecution)
	if err := authorizeConfigWrite(reqWithPrincipal(operator), manager, nil,
		[]string{"control.node_proxy_token"}); err != nil {
		t.Errorf("the operator must be able to clear the legacy credential: %v", err)
	}
}

// The Feishu bot has no QR-binding endpoint: its app credentials ARE the binding,
// so whoever may write them decides which Feishu app drives an agent here. They
// were not marked platform-admin-only, which left the same class of hole the
// WeChat binding gate just closed — a tenant user could repoint the bot at their
// own app and then message it with an operator-capability agent behind it.
func TestAuthorizeConfigWriteRefusesTenantUserWritingChannelCredentials(t *testing.T) {
	manager := configGateManager(t)
	tenantUser := identity.NewPrincipal("tenant-1", "user-a", "", identity.KindUser, identity.EntitlementRelayOnly)

	for _, key := range []string{"channels.feishu.app_id", "channels.feishu.app_secret"} {
		err := authorizeConfigWrite(reqWithPrincipal(tenantUser), manager,
			map[string]interface{}{key: "attacker-supplied"}, nil)
		if err == nil {
			t.Errorf("a tenant user must not be able to rebind a bot through %s", key)
			continue
		}
		if status := statusForConfigAuthError(err); status != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", key, status)
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error should name %s, got %q", key, err.Error())
		}
	}

	// Clearing the credential is the same act as replacing it.
	err := authorizeConfigWrite(reqWithPrincipal(tenantUser), manager, nil,
		[]string{"channels.feishu.app_secret"})
	if err == nil {
		t.Error("a tenant user must not be able to clear the bot credential")
	}

	// The operator and a node acting for it still manage their own channel config.
	operator := identity.NewPrincipal("", "local-operator", identity.RolePlatformAdmin, identity.KindPlatform,
		identity.EntitlementCenterExecution)
	if err := authorizeConfigWrite(reqWithPrincipal(operator), manager,
		map[string]interface{}{"channels.feishu.app_id": "cli_x"}, nil); err != nil {
		t.Errorf("operator refused: %v", err)
	}
	node := identity.NewPrincipal("", "relay-node", "", identity.KindNode, identity.EntitlementRelayOnly)
	if err := authorizeConfigWrite(reqWithPrincipal(node), manager,
		map[string]interface{}{"channels.feishu.app_id": "cli_x"}, nil); err != nil {
		t.Errorf("node actor refused: %v", err)
	}
}

// reqWithPrincipal attaches a principal the way the protected chain does.
func reqWithPrincipal(principal identity.Principal) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/config", nil)
	return req.WithContext(WithPrincipal(req.Context(), principal))
}
