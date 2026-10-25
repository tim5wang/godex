// godex-feature: identity
// 身份底座：Principal（租户 + 用户 + 能力位）、节点归属与 NodeAccess 唯一授权入口。
// 入口：各授权接缝（relay 代理、节点注册表读取、watch 转发、gateway）
// 文档：docs/multi-tenant-commercialization-design.md、docs/p0-principal-migration-inventory.md

// Package identity defines the P0 identity foundation for multi-tenant
// operation: who is calling (Principal) and whether that caller may reach a
// given node (Policy.Authorize).
//
// It is deliberately dependency-free policy. The package takes the facts it
// needs as parameters rather than importing the node registry or the HTTP
// layer, so the same decision function can be consumed by every authorization
// seam (relay proxy, node registry reads, watch forwarding, gateway) without an
// import cycle and without each seam re-deriving the rules.
package identity

import "strings"

// Entitlement names. An entitlement is an orthogonal capability bit checked at
// exactly one place, instead of scattering special-case tenant comparisons
// through the auth path.
const (
	// EntitlementCenterExecution permits running an agent ON the center
	// instance itself (decision ⑨). Exactly one account holds it: the platform
	// superadmin. Tenants never do — their execution happens on their own
	// nodes, which is what keeps the center from becoming an execution host for
	// arbitrary users.
	EntitlementCenterExecution = "center_execution"

	// EntitlementRelayOnly is the tenant plan capability: forward/relay to the
	// tenant's own nodes. This is the only plan tier implemented so far
	// (decision ⑬).
	EntitlementRelayOnly = "relay_only"

	// EntitlementAgentRuntime is reserved for the future plan where the
	// platform provisions a container node for a tenant. Deliberately not
	// implemented (decision ⑨⑬); defined so the name is not invented ad hoc
	// later.
	EntitlementAgentRuntime = "agent_runtime"
)

// RolePlatformAdmin is the global platform role, distinct from a tenant role.
// A platform admin administers the platform; it does NOT imply read access to
// tenant data (that requires an explicit, time-bounded Delegation).
const RolePlatformAdmin = "admin"

// PrincipalKind records how a caller authenticated. It matters for audit and
// for deciding which capabilities are even meaningful, but authorization
// decisions should key off TenantID/UserID/Entitlements rather than the kind.
type PrincipalKind string

const (
	KindUser     PrincipalKind = "user"      // signed-in tenant member
	KindPlatform PrincipalKind = "platform"  // platform-owned subject (weixin bot, cron)
	KindWatch    PrincipalKind = "watch"     // paired watch device
	KindNode     PrincipalKind = "node"      // joined node acting through the center
	KindBizKey   PrincipalKind = "biz_key"   // business key (agent step platform)
	KindProxyKey PrincipalKind = "proxy_key" // gdx_ usage-gateway key
)

// Principal is the authenticated caller.
type Principal struct {
	// TenantID is the billing and isolation boundary. Empty for a platform
	// admin who holds no tenant membership.
	TenantID string

	// UserID identifies the subject within the tenant.
	UserID string

	// PlatformRole is global ("" or "admin"). Kept separate from any tenant
	// role: one is a platform employee, the other a tenant member, and merging
	// them is how "tenant A's admin can manage tenant B" bugs appear.
	PlatformRole string

	// Kind records the authentication path (for audit and for callers that need
	// to know, e.g. whether a watch device scope applies).
	Kind PrincipalKind

	// Entitlements are the capability bits granted to this principal.
	Entitlements []string

	// AuthMethod is a short label for the audit trail ("web_token",
	// "watch_device", "relay_trust", ...).
	AuthMethod string
}

// NewPrincipal returns a Principal with normalized (de-duplicated, sorted)
// entitlements. Empty entitlement names are dropped.
func NewPrincipal(tenantID, userID, platformRole string, kind PrincipalKind, entitlements ...string) Principal {
	return Principal{
		TenantID:     strings.TrimSpace(tenantID),
		UserID:       strings.TrimSpace(userID),
		PlatformRole: strings.TrimSpace(platformRole),
		Kind:         kind,
		Entitlements: NormalizeEntitlements(entitlements),
	}
}

// NormalizeEntitlements lowercases, trims, de-duplicates and sorts entitlement
// names. Sorting makes the result comparable with reflect.DeepEqual and stable
// in audit output.
func NormalizeEntitlements(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// Valid reports whether the principal identifies a subject.
//
// A tenant member must carry a TenantID; a platform admin legitimately may not
// (platform_role is global, not a tenant membership). Enforcement of the tenant
// boundary itself happens in Authorize, not here.
func (p Principal) Valid() bool {
	return strings.TrimSpace(p.UserID) != ""
}

// HasTenant reports whether the principal is scoped to a tenant.
func (p Principal) HasTenant() bool {
	return strings.TrimSpace(p.TenantID) != ""
}

// HasEntitlement reports whether the principal holds the named capability.
func (p Principal) HasEntitlement(name string) bool {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return false
	}
	for _, have := range p.Entitlements {
		if have == want {
			return true
		}
	}
	return false
}

// IsPlatformAdmin reports whether the principal is a platform superadmin.
func (p Principal) IsPlatformAdmin() bool {
	return strings.EqualFold(strings.TrimSpace(p.PlatformRole), RolePlatformAdmin)
}

// CanExecuteOnCenter reports whether this principal may run an agent on the
// center instance (decision ⑨).
//
// Only the platform superadmin holds EntitlementCenterExecution. Tenant
// principals are rejected at every center execution entry point, which is what
// makes "only one account can run an agent on the server" a property of the
// identity model rather than a per-handler check.
func (p Principal) CanExecuteOnCenter() bool {
	return p.HasEntitlement(EntitlementCenterExecution)
}

// sortStrings sorts in place. Kept local so the package stays dependency-free
// (no "sort" import churn for a three-line helper).
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
