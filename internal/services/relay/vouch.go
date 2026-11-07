package relay

import (
	"encoding/json"
	"strings"
)

// VouchIdentityHeader carries the identity the CENTER authenticated for a
// proxied request, so a target node can attribute it to a real user instead of
// the anonymous "relay-node" placeholder.
//
// Why it exists: some node surfaces are PER-OWNER — the work desktop stores its
// rows keyed by (tenant_id, owner_id) derived from the principal. Across the
// relay the node only ever learned "a node called", so every remote read was
// scoped to the wrong owner and came back empty: the desktop looked broken from
// a phone while the same rows were plainly there on the node itself.
//
// Trust model: this header is set by the CENTER from its own authenticated
// resolution of the caller (identity.Decision) — NEVER copied from the caller.
// It is deliberately absent from relayForwardedHeaders, and the proxy sets it
// after that allowlist runs, so an inbound value is discarded rather than
// relayed. A node honours it only on a request that arrived over its own
// authenticated relay channel (RelayTrustHeader), which a direct caller cannot
// forge. The value is therefore exactly as trustworthy as the relay hop itself —
// the same boundary WatchScopesHeader already relies on.
//
// Absent or malformed means "nobody in particular": the node keeps its
// placeholder identity, which owns nothing, so an un-attributable caller sees an
// empty per-owner surface rather than someone else's.
const VouchIdentityHeader = "X-Godex-Vouch-Identity"

// VouchIdentity is the caller identity the center asserts for a forwarded
// request. UserID is the owner a per-owner surface scopes to; TenantID is the
// isolation boundary and may be empty in a single-tenant deployment.
type VouchIdentity struct {
	TenantID string `json:"tenant,omitempty"`
	UserID   string `json:"user"`
}

// EncodeVouch renders an identity for the header, or "" when there is nothing
// worth asserting.
//
// An empty user yields no header on purpose: the placeholder identity a node
// falls back to owns nothing, which is the fail-safe reading. Sending a vouch
// with a blank owner would instead invite a surface to scope to "".
func EncodeVouch(tenantID, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	raw, err := json.Marshal(VouchIdentity{TenantID: strings.TrimSpace(tenantID), UserID: userID})
	if err != nil {
		// Unreachable for two strings; no header is the safe answer regardless.
		return ""
	}
	return string(raw)
}

// DecodeVouch parses a vouch header value.
//
// ok is false for an absent, malformed or owner-less value, so the caller keeps
// the placeholder identity instead of trusting a half-parsed assertion.
func DecodeVouch(value string) (VouchIdentity, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return VouchIdentity{}, false
	}
	var id VouchIdentity
	if err := json.Unmarshal([]byte(value), &id); err != nil {
		return VouchIdentity{}, false
	}
	id.TenantID = strings.TrimSpace(id.TenantID)
	id.UserID = strings.TrimSpace(id.UserID)
	if id.UserID == "" {
		return VouchIdentity{}, false
	}
	return id, true
}
