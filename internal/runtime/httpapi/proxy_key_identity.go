package httpapi

import (
	"net/http"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/usage"
)

// proxyKeyPrincipal builds the Principal for an authenticated gdx_ usage-gateway
// key (P0 §7, the last un-wired authentication family).
//
// These routes are OpenAI/Anthropic-compatible gateways whose authorization
// mechanism is the key itself: AllowedModels caps what it may call and
// BudgetCredits caps how much. So, exactly like the biz-key family, adding a
// center_execution gate would not tighten them — it would just remove the
// gateway feature. What was missing is the half that is genuinely absent: the
// caller's identity. Every other authenticated route attaches one; these did not,
// so anything downstream that asks "who is calling?" (audit, future tenant
// binding, budget attribution) saw nobody.
//
// UserID is the key id, not a human identity: a gdx_ key is a machine credential.
// Recording it as its own subject keeps the audit trail from blaming whoever
// created the key.
//
// TenantID is empty because ProxyAPIKey has no tenant binding yet (§3.1 requires
// "key → 租户" later). Empty is the fail-safe reading: Policy.Authorize treats an
// unscoped non-operator principal as cross-tenant, so a key cannot reach
// tenant-scoped nodes by omission — the mistake fails closed, not open.
//
// Entitlements is nil on purpose: a proxy key must not inherit operator
// capabilities, so any future gate requiring one denies it by default.
func proxyKeyPrincipal(key *usage.ProxyAPIKey) identity.Principal {
	id := "proxy-key"
	if key != nil && key.ID != "" {
		id = key.ID
	}
	return identity.Principal{
		UserID:     id,
		Kind:       identity.KindProxyKey,
		AuthMethod: AuthMethodProxyKey,
	}
}

// withProxyKeyPrincipal attaches the proxy-key identity to the request so the
// handler downstream sees it. Returns the request unchanged for a nil key, which
// cannot happen on a route that already authenticated, but keeps the caller from
// having to guard.
func withProxyKeyPrincipal(r *http.Request, key *usage.ProxyAPIKey) *http.Request {
	if key == nil {
		return r
	}
	return r.WithContext(WithPrincipal(r.Context(), proxyKeyPrincipal(key)))
}
