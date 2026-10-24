package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// pairWatchWithScopes walks start → approve(with scopes) → token and returns the
// device credential.
//
// A separate helper from pairWatch because the scope set is the whole point of
// the test using it: pairWatch always takes the server's DefaultScopes, which
// would make a "read-only credential is refused" assertion pass for the wrong
// reason (or fail because the route was never actually restricted).
func pairWatchWithScopes(t *testing.T, server *httptest.Server, scopes []string) string {
	t.Helper()
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{
		"name": "Scoped Watch", "kind": "watch",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/start status=%d body=%s", resp.StatusCode, body)
	}
	var pairing struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode pairing: %v (%s)", err, body)
	}

	resp, body = postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code": pairing.UserCode,
		"scopes":    scopes,
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("pair/approve status=%d body=%s", resp.StatusCode, body)
	}

	resp, body = postWatchJSON(t, server.URL+"/watch/pair/token", map[string]any{
		"device_code": pairing.DeviceCode,
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token status=%d body=%s", resp.StatusCode, body)
	}
	var approved struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("decode approved: %v (%s)", err, body)
	}
	if approved.Token == "" {
		t.Fatalf("expected a token, got %s", body)
	}
	return approved.Token
}

// TestWatchTemplatesListsCuratedPresets checks the wrist catalogue.
//
// The endpoint must be useful even though the node typically holds far more
// templates: it is a curated subset, and the watch preset has to be in it —
// otherwise "new chat" on a wrist would create a session with no valid default.
func TestWatchTemplatesListsCuratedPresets(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	resp, body := getWatch(t, server.URL+"/watch/templates", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch/templates status=%d body=%s", resp.StatusCode, body)
	}
	var items []watchTemplateItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(items) == 0 {
		t.Fatalf("expected at least one template, got none")
	}

	ids := map[string]bool{}
	for _, item := range items {
		if item.ID == "" {
			t.Errorf("template with empty id: %s", body)
		}
		ids[item.ID] = true
	}
	if !ids[watchDefaultTemplate] {
		t.Errorf("default template %q missing from %v", watchDefaultTemplate, ids)
	}
}

// TestWatchCreateSessionStartsNewSessionEachTime is the behaviour that makes
// "new chat" mean new chat.
//
// godex derives a session id from channel+key, so a fixed key would silently
// resume the previous conversation instead of starting another one. This test
// exists because that failure is invisible from the response shape alone — both
// answers look like success.
func TestWatchCreateSessionStartsNewSessionEachTime(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	first := createWatchSession(t, server.URL, token, map[string]any{})
	second := createWatchSession(t, server.URL, token, map[string]any{})
	if first.SessionID == "" || second.SessionID == "" {
		t.Fatalf("empty session id: %q %q", first.SessionID, second.SessionID)
	}
	if first.SessionID == second.SessionID {
		t.Fatalf("expected two distinct sessions, both were %q", first.SessionID)
	}
}

// TestWatchCreateSessionAcceptsExplicitTemplate checks the preset is honoured.
func TestWatchCreateSessionAcceptsExplicitTemplate(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	created := createWatchSession(t, server.URL, token, map[string]any{
		"template_id": "minimal",
	})
	if created.SessionID == "" {
		t.Fatalf("empty session id")
	}

	// The session must actually exist and be readable through the normal route.
	resp, body := getWatch(t, server.URL+"/watch/sessions/"+created.SessionID, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session detail status=%d body=%s", resp.StatusCode, body)
	}
}

// TestWatchCreateSessionRejectsUnknownTemplate: failing before creating is the
// point — the alternative is a session that quietly lacks the preset the user
// picked, or an error reported after it already exists.
func TestWatchCreateSessionRejectsUnknownTemplate(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	resp, body := postWatchJSON(t, server.URL+"/watch/sessions", map[string]any{
		"template_id": "does-not-exist",
	}, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.StatusCode, body)
	}
}

// TestWatchCreateSessionRequiresCommandScope: creating a session mutates node
// state, so a read-only credential must not be able to spawn work.
func TestWatchCreateSessionRequiresCommandScope(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatchWithScopes(t, server, []string{"watch:read"})

	resp, body := postWatchJSON(t, server.URL+"/watch/sessions", map[string]any{}, token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", resp.StatusCode, body)
	}
}

// TestWatchCreatedSessionIsListed pins the bug that made "新建对话" look broken.
//
// OpenSession deliberately keeps a conversation-less session in memory only, and
// the list is built from persisted manifests. So POST returned 201 with an id the
// very next GET could not find. The watch remembers that id as its current
// session and re-resolves it against the list, so the miss silently fell back to
// the previous conversation: "已新建对话" and then the old chat still on screen.
//
// Both halves are asserted because either alone can pass while the feature is
// broken: create must answer 201, and the returned id must be listable.
//
// ListSessions enumerates only persisted manifests (SessionsDir + the SQLite
// store), so this passing is itself the proof that the session reached disk — no
// separate "survives restart" assertion is needed or wanted.
func TestWatchCreatedSessionIsListed(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	created := createWatchSession(t, server.URL, token, map[string]any{})

	listed := listWatchSessionIDs(t, server.URL, token)
	found := false
	for _, id := range listed {
		if id == created.SessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created session %q missing from list %v", created.SessionID, listed)
	}
}

func listWatchSessionIDs(t *testing.T, baseURL, token string) []string {
	t.Helper()
	resp, body := getWatch(t, baseURL+"/watch/sessions", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch/sessions status=%d body=%s", resp.StatusCode, body)
	}
	var items []watchSessionItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.SessionID)
	}
	return ids
}

func createWatchSession(t *testing.T, baseURL, token string, payload map[string]any) watchSessionItem {
	t.Helper()
	resp, body := postWatchJSON(t, baseURL+"/watch/sessions", payload, token)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("watch/sessions status=%d body=%s", resp.StatusCode, body)
	}
	var created watchSessionItem
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return created
}
