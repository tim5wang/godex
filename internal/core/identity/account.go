// godex-feature: identity
// 多租户实体：Tenant / User / Membership 与「角色→能力位」映射（设计 §3.1）。
// 入口：internal/services/accounts（存储）、/auth/* 路由
// 文档：docs/multi-tenant-commercialization-design.md §3.1/§3.5/§7

package identity

import "strings"

// Tenant roles, scoped to one tenant. Deliberately separate from PlatformRole:
// merging them is how "tenant A's admin can manage tenant B" bugs appear.
const (
	RoleTenantOwner  = "owner"
	RoleTenantAdmin  = "admin"
	RoleTenantMember = "member"
)

// Tenant is the billing and isolation boundary (design §3.1).
type Tenant struct {
	ID        string
	Name      string
	Plan      string
	CreatedAt string
}

// User is a login subject. PasswordHash is a bcrypt hash; it is never serialized
// to JSON by the API layer.
type User struct {
	ID    string
	Email string
	// PasswordHash is the bcrypt hash. json:"-" so an API handler that returns a
	// User cannot leak it by forgetting a projection: the field simply is not
	// serializable.
	PasswordHash string `json:"-"`
	// PlatformRole is global ("" or RolePlatformAdmin).
	PlatformRole string
	CreatedAt    string
}

// Membership binds a user into a tenant with one tenant-scoped role.
type Membership struct {
	TenantID string
	UserID   string
	Role     string
	JoinedAt string
}

// NormalizeEmail lowercases and trims an email so lookups are case-insensitive
// and "User@x.com" cannot register alongside "user@x.com".
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizeTenantRole maps a role string onto a known tenant role.
func NormalizeTenantRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleTenantOwner:
		return RoleTenantOwner
	case RoleTenantAdmin:
		return RoleTenantAdmin
	case RoleTenantMember:
		return RoleTenantMember
	default:
		return ""
	}
}

// TenantRoleGrantsAdmin reports whether a tenant role may administer that tenant.
func TenantRoleGrantsAdmin(role string) bool {
	switch NormalizeTenantRole(role) {
	case RoleTenantOwner, RoleTenantAdmin:
		return true
	default:
		return false
	}
}

// EntitlementsFor returns the capability bits for a principal built from these
// facts (design §7, decision ⑬).
//
// The tenant plan tier implemented so far is relay-only: a tenant forwards to its
// OWN nodes and never executes on the center. center_execution is granted only to
// a platform admin, which is what makes "only one account can run an agent on the
// server" (decision ⑨) a property of the identity model rather than a per-handler
// check.
//
// Note the asymmetry: being a tenant owner/admin grants NO platform capability.
// A tenant admin administers their tenant; the platform is not theirs.
func EntitlementsFor(platformRole, tenantRole string, hasTenant bool) []string {
	out := []string{}
	if hasTenant {
		out = append(out, EntitlementRelayOnly)
	}
	if strings.EqualFold(strings.TrimSpace(platformRole), RolePlatformAdmin) {
		out = append(out, EntitlementCenterExecution, EntitlementRelayOnly)
	}
	return NormalizeEntitlements(out)
}

// PrincipalForUser assembles the Principal for a signed-in user (design §3.2).
//
// tenantRole is the user's role in tenantID; pass an empty tenantID for a
// platform-only user who holds no membership. AuthMethod is set by the caller so
// the audit trail names the credential (session cookie vs. bearer token).
func PrincipalForUser(tenantID, userID, platformRole, tenantRole, authMethod string) Principal {
	tenantID = strings.TrimSpace(tenantID)
	tenantRole = NormalizeTenantRole(tenantRole)
	principal := NewPrincipal(tenantID, userID, platformRole, KindUser,
		EntitlementsFor(platformRole, tenantRole, tenantID != "")...)
	principal.AuthMethod = strings.TrimSpace(authMethod)
	return principal
}
