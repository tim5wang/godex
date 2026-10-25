package identity

import (
	"strings"
	"time"
)

// Delegation is a tenant's explicit, time-bounded authorization for a platform
// operator to read that tenant's data (design §3.6, decision ⑪).
//
// It exists because the two obvious alternatives are both wrong:
//
//   - A platform ROLE must not imply cross-tenant read. Role is "who this person
//     is", not "which tenant agreed to this", so deriving access from it would
//     make "allowed to look" mean "allowed to look at anyone, always".
//   - `center_execution` must not stand in for it either: that is the capability
//     to run an agent on the center, a different question from reading a tenant's
//     nodes.
//
// The tenant grants it, the clock ends it, and every use is audited. That is what
// keeps "we can help debug" from degrading into "we can look whenever we want".
type Delegation struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	// OperatorUserID is the platform operator this grant is FOR. Naming the
	// operator (rather than allowing "any operator") means a grant to one person
	// cannot be used by another, and the audit entry can name who read what.
	OperatorUserID string `json:"operator_user_id"`
	// GrantedBy is the tenant user who made the grant, so the tenant can see who
	// authorized it on their side.
	GrantedBy string `json:"granted_by,omitempty"`
	// Scope narrows what may be read. Empty means the default read-only scope;
	// a grant never widens beyond read (design §3.6 "默认只读").
	Scope     string `json:"scope,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	// RevokedAt ends a grant early. The tenant can withdraw at any time; there is
	// deliberately no "renew silently" path, so an expired grant needs a new,
	// separately visible act by the tenant.
	RevokedAt string `json:"revoked_at,omitempty"`
}

// Delegation scopes. Read-only by design: a grant is for diagnosis, and the
// design lists "默认只读" as a hard property, not a default.
const (
	// ScopeReadNodes lets the operator read the tenant's node inventory and
	// overviews — the minimum needed to explain why a node is unreachable.
	ScopeReadNodes = "read_nodes"
)

// DefaultDelegationTTL bounds a grant when the tenant does not choose a duration.
// Short on purpose: a debugging window, not a standing arrangement.
const DefaultDelegationTTL = 2 * time.Hour

// MaxDelegationTTL caps what a tenant may grant in one act.
//
// A cap rather than free choice because "grant for a year" is functionally the
// silent standing bypass this design rejects; forcing periodic re-granting keeps
// each window visible to the tenant.
const MaxDelegationTTL = 24 * time.Hour

// NormalizeScope validates a requested scope, defaulting to read-only nodes.
//
// Unknown scopes collapse to the default rather than being stored verbatim: a
// stored scope the authorization code does not understand would be a grant whose
// limits nobody can reason about.
func NormalizeScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", ScopeReadNodes:
		return ScopeReadNodes
	default:
		return ScopeReadNodes
	}
}

// Active reports whether the delegation permits access at the given time.
//
// Expiry and revocation are both checked here so every seam shares one answer;
// a caller that checked only one of them would honour a withdrawn grant.
func (d Delegation) Active(now time.Time) bool {
	if strings.TrimSpace(d.RevokedAt) != "" {
		return false
	}
	expires := parseDelegationTime(d.ExpiresAt)
	if expires.IsZero() {
		// An unparseable or missing expiry is treated as expired, not as
		// "never expires": the failure direction must be "no access".
		return false
	}
	return now.Before(expires)
}

// AllowsRead reports whether the delegation covers read access to nodes.
// Only read scopes exist today, so this is where a future write scope would be
// consulted — deliberately a separate question from Active().
func (d Delegation) AllowsRead() bool {
	return NormalizeScope(d.Scope) == ScopeReadNodes
}

func parseDelegationTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// CurrentTermsVersion identifies the terms revision this build discloses to
// tenants. Recorded alongside each acceptance so "which text did they agree to"
// is answerable later; without a version in the record, an agreement cannot be
// tied to the document it was made against.
//
// Bump it when the disclosed text changes: existing records stay attached to the
// revision they were made under, which is the only way a stored acceptance remains
// meaningful after the terms are edited.
const CurrentTermsVersion = "2026-10"

// ConsentRecord is the user-agreement trail (design §3.6 "用户协议").
//
// Recorded at signup so the deployment can show what a tenant agreed to and when,
// separately from any later grant. Kept minimal on purpose: id, terms version,
// time, and the source address — no more than is needed to answer "did they agree
// to this version".
type ConsentRecord struct {
	TenantID     string `json:"tenant_id"`
	UserID       string `json:"user_id,omitempty"`
	TermsVersion string `json:"terms_version"`
	AcceptedAt   string `json:"accepted_at"`
	// SourceIP is the request's origin. Recorded as supplied by the caller's
	// transport; behind a proxy that means the proxy, which is why the delegation
	// audit also records the operator identity rather than relying on address.
	SourceIP string `json:"source_ip,omitempty"`
}
