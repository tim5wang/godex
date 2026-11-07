package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/identity"
)

// forwardedVouch drives one proxy request through a real relay hop and reports
// the vouch header the TARGET NODE received ("" when none).
//
// End to end on purpose: the property under test is "what reaches the node",
// and a unit test on the encoder alone would pass even if the proxy never set
// the header, or let the caller's value through.
func forwardedVouch(t *testing.T, decision identity.Decision, inboundVouch string) string {
	t.Helper()
	hub, wsURL := serveRelayHub(t, func(nodeID, credential string) bool {
		return credential == "ck_secret"
	})
	var got string
	agent := NewAgent(AgentConfig{
		CenterURL: wsURL, NodeID: "node-b", Credential: "ck_secret", Version: "v1",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get(VouchIdentityHeader)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	})
	if err := agent.Start(context.Background()); err != nil {
		t.Fatalf("agent start: %v", err)
	}
	defer agent.Stop(context.Background())
	waitOnline(t, hub, "node-b")

	proxy := NewProxyHandler(hub, func(r *http.Request) bool { return true })
	proxy.AuthorizeNode = func(r *http.Request, nodeID string) identity.Decision { return decision }
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/control/nodes/node-b/proxy/v1/work-contexts", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if inboundVouch != "" {
		req.Header.Set(VouchIdentityHeader, inboundVouch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return got
}

// TestProxyAssertsCallerIdentityToNode pins the center half of the fix: the node
// learns WHOSE request this is from the decision the center authenticated, so a
// per-owner surface on the node resolves the real user instead of the
// anonymous placeholder.
func TestProxyAssertsCallerIdentityToNode(t *testing.T) {
	decision := identity.Decision{
		Allowed: true, Reason: identity.ReasonSingleTenant,
		UserID: "local-operator", Method: "web_token",
	}
	got := forwardedVouch(t, decision, "")
	id, ok := DecodeVouch(got)
	if !ok {
		t.Fatalf("node received no usable vouch (%q)", got)
	}
	if id.UserID != "local-operator" {
		t.Fatalf("vouched user = %q, want local-operator", id.UserID)
	}
}

// TestProxyVouchOverridesCallerSuppliedValue is the spoofing guard: a caller that
// sends its own vouch header must not have it relayed — the center asserts the
// identity IT resolved, or none at all. Otherwise anyone able to reach the proxy
// could name themselves the owner of any desktop.
func TestProxyVouchOverridesCallerSuppliedValue(t *testing.T) {
	decision := identity.Decision{
		Allowed: true, Reason: identity.ReasonSingleTenant,
		UserID: "local-operator", Method: "web_token",
	}
	forged := EncodeVouch("", "mallory")
	got := forwardedVouch(t, decision, forged)
	if got == forged {
		t.Fatal("the caller's forged vouch was relayed verbatim")
	}
	id, ok := DecodeVouch(got)
	if !ok {
		t.Fatalf("node received no usable vouch (%q)", got)
	}
	if id.UserID != "local-operator" {
		t.Fatalf("vouched user = %q, want the authenticated local-operator", id.UserID)
	}
}

// TestProxyOmitsVouchWhenDecisionNamesNobody pins the fail-safe end to end: with
// no user in the decision, no header travels, and the node keeps its placeholder
// identity — which owns nothing, so a per-owner surface stays empty rather than
// showing another user's rows.
func TestProxyOmitsVouchWhenDecisionNamesNobody(t *testing.T) {
	for name, decision := range map[string]identity.Decision{
		"empty user": {Allowed: true, Reason: identity.ReasonSingleTenant},
		"blank user": {Allowed: true, Reason: identity.ReasonSingleTenant, UserID: "   "},
	} {
		if got := forwardedVouch(t, decision, ""); got != "" {
			t.Errorf("%s: node received vouch %q, want none", name, got)
		}
	}
}
