package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/relay"
)

// relayVouchRequest builds a request that arrives over this node's own
// authenticated relay channel, optionally carrying a center-asserted identity.
func relayVouchRequest(vouch string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/work-contexts", nil)
	if vouch != "" {
		r.Header.Set(relay.VouchIdentityHeader, vouch)
	}
	return r
}

// TestRelayTrustAdoptsVouchedOwner pins the fix for the empty remote desktop: a
// request the center authenticated as a user must resolve to THAT user, not to
// the anonymous placeholder — otherwise every per-owner surface (work desktop
// rows are keyed by owner) comes back empty across the relay.
func TestRelayTrustAdoptsVouchedOwner(t *testing.T) {
	p := classifyPrincipal(relayVouchRequest(relay.EncodeVouch("", "local-operator")), nodeProxyMatch{}, true, nil)
	if p.UserID != "local-operator" {
		t.Fatalf("UserID = %q, want the vouched owner", p.UserID)
	}
	if p.TenantID != "" {
		t.Fatalf("TenantID = %q, want empty as vouched", p.TenantID)
	}
}

// TestRelayTrustVouchKeepsNodeKindAndCapabilities is the privilege half: the
// vouch conveys OWNERSHIP, not authority. Naming a user must not turn a
// relay-originated call into the operator or hand it center execution.
func TestRelayTrustVouchKeepsNodeKindAndCapabilities(t *testing.T) {
	p := classifyPrincipal(relayVouchRequest(relay.EncodeVouch("tenant-a", "alice")), nodeProxyMatch{}, true, nil)
	if p.Kind != identity.KindNode {
		t.Fatalf("Kind = %q, want node", p.Kind)
	}
	if p.AuthMethod != AuthMethodRelayTrust {
		t.Fatalf("AuthMethod = %q, want %q", p.AuthMethod, AuthMethodRelayTrust)
	}
	if p.IsPlatformAdmin() || p.CanExecuteOnCenter() {
		t.Fatalf("vouched relay principal gained authority: %#v", p)
	}
	if p.TenantID != "tenant-a" {
		t.Fatalf("TenantID = %q, want the vouched tenant", p.TenantID)
	}
}

// TestRelayTrustWithoutVouchKeepsPlaceholder pins "illegal access returns empty":
// an un-attributable relay caller owns nothing, so a per-owner surface is empty
// rather than showing some other user's rows.
func TestRelayTrustWithoutVouchKeepsPlaceholder(t *testing.T) {
	for name, raw := range map[string]string{
		"no header": "",
		"malformed": "alice",
		"ownerless": `{"tenant":"t1"}`,
	} {
		p := classifyPrincipal(relayVouchRequest(raw), nodeProxyMatch{}, true, nil)
		if p.UserID != "relay-node" {
			t.Errorf("%s: UserID = %q, want the relay-node placeholder", name, p.UserID)
		}
	}
}

// TestVouchIgnoredWithoutRelayTrust is the spoofing half: a DIRECT caller (not on
// the relay channel) that sends the vouch header must not have it honoured —
// otherwise anyone who can reach the node's API could name themselves the owner
// of any desktop. Here the caller presents a valid web token, so it resolves to
// the operator; the forged vouch is ignored.
func TestVouchIgnoredWithoutRelayTrust(t *testing.T) {
	r := relayVouchRequest(relay.EncodeVouch("tenant-a", "mallory"))
	r.Header.Set("Authorization", "Bearer whatever")
	p := classifyPrincipal(r, nodeProxyMatch{}, false, nil)
	if p.UserID == "mallory" {
		t.Fatal("a non-relay caller must not choose its own owner via the vouch header")
	}
}
