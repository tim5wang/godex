package httpapi

import (
	"net/http"
	"testing"
)

// The center's own workspace is operator-only, and the gate must be UNIFORM across
// the session surface.
//
// This is a regression test for a real hole: the list and single-session reads were
// merely `protected` (any authenticated caller) while only create was gated. A
// freshly registered tenant user could therefore list the operator's conversations,
// read their content, rename them, and DELETE them — while POST /sessions refused
// it with 403. The reported symptom was "I can see the old list but nothing opens";
// the delete was the part that actually destroyed data.
//
// Node actors stay exempt (relay-originated remote control), which is pinned by
// TestRelayTrustHeaderAcceptsSessions in relay_trust_test.go — that test still
// expects 200 with a valid trust header, so the exemption is covered rather than
// assumed.

// sessionSurfaceCalls are the session-scoped requests a tenant user must be refused.
// Kept as a table so a future route added to the surface is a one-line addition
// rather than a new test that might not be written.
func sessionSurfaceCalls(sessionID string) []struct {
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
		{"list sessions", http.MethodGet, "/sessions", nil},
		{"open a session", http.MethodPost, "/sessions", map[string]any{"locator": map[string]any{"channel": "web", "key": "tenant"}}},
		{"read one session", http.MethodGet, "/sessions/" + sessionID, nil},
		{"read timeline", http.MethodGet, "/sessions/" + sessionID + "/timeline", nil},
		{"read context usage", http.MethodGet, "/sessions/" + sessionID + "/context-usage", nil},
		{"read ledger", http.MethodGet, "/sessions/" + sessionID + "/ledger", nil},
		{"rename a session", http.MethodPatch, "/sessions/" + sessionID + "/title", map[string]any{"title": "owned"}},
		{"delete a session", http.MethodDelete, "/sessions/" + sessionID, nil},
	}
}

// TestTenantUserIsRefusedTheCenterSessionSurface is the hole itself: every
// session-scoped call a tenant user makes must be refused, uniformly.
func TestTenantUserIsRefusedTheCenterSessionSurface(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	// The operator (web token holder) owns the center's workspace and opens a
	// session there, which is what the tenant user would be reading.
	resp, body := doJSON(t, http.MethodPost, server.URL+"/sessions", "web-secret",
		map[string]any{"locator": map[string]any{"channel": "web", "key": "operator-private"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator open session: status = %d, want 200: %v", resp.StatusCode, body)
	}
	sessionID, _ := body["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("operator open session returned no session_id: %v", body)
	}

	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")

	for _, tc := range sessionSurfaceCalls(sessionID) {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, tenantToken, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s as tenant: status = %d, want 403: %v", tc.name, resp.StatusCode, body)
			}
		})
	}
}

// TestTenantDeleteCannotDestroyTheOperatorsSession is the destructive half, asserted
// on the outcome rather than the status code: after the tenant's delete attempt the
// session must still be readable by the operator.
func TestTenantDeleteCannotDestroyTheOperatorsSession(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/sessions", "web-secret",
		map[string]any{"locator": map[string]any{"channel": "web", "key": "operator-private"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator open session: status = %d, want 200: %v", resp.StatusCode, body)
	}
	sessionID, _ := body["session_id"].(string)

	tenantToken, _, _ := registerTenantOwner(t, server, "tenant@example.com")
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/sessions/"+sessionID, tenantToken, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tenant delete: status = %d, want 403", resp.StatusCode)
	}

	// The session must survive: the operator still reads it.
	resp, body = doJSON(t, http.MethodGet, server.URL+"/sessions/"+sessionID, "web-secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session did not survive the tenant's delete: status = %d, want 200: %v", resp.StatusCode, body)
	}
}

// TestOperatorStillOwnsTheCenterSessionSurface is the other half of the change: the
// gate must not break the operator, who is the surface's intended user.
//
// It asserts "not 403" rather than a specific success code on purpose: 409/404 here
// would be backend lifecycle semantics (an opened session with no state.json yet
// reports itself as not deletable), which is unrelated to what this test claims —
// that the GATE does not refuse the operator.
func TestOperatorStillOwnsTheCenterSessionSurface(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	resp, body := doJSON(t, http.MethodPost, server.URL+"/sessions", "web-secret",
		map[string]any{"locator": map[string]any{"channel": "web", "key": "operator-private"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator open session: status = %d, want 200: %v", resp.StatusCode, body)
	}
	sessionID, _ := body["session_id"].(string)

	for _, tc := range sessionSurfaceCalls(sessionID) {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, tc.method, server.URL+tc.path, "web-secret", tc.body)
			if resp.StatusCode == http.StatusForbidden {
				t.Fatalf("operator refused on %s: %v", tc.name, body)
			}
		})
	}
}

// TestUnauthenticatedCallerIsRefusedTheSessionSurface pins that the gate keeps the
// authentication requirement rather than replacing it.
func TestUnauthenticatedCallerIsRefusedTheSessionSurface(t *testing.T) {
	server, _ := newAuthServer(t, "web-secret")

	resp, _ := doJSON(t, http.MethodGet, server.URL+"/sessions", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: status = %d, want 401", resp.StatusCode)
	}
}
