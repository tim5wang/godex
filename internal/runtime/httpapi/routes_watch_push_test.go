package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// newWatchPushTestServer mirrors the approval-detail setup: the queue must be fed
// by the real permission gate, not by hand-built items, or the test would pass
// even if derivation read the wrong source.
//
// webToken is applied to the config. Leaving it empty makes the bearer middleware a
// pass-through (see newTestConfig), which is fine for the derivation tests but
// useless for asserting anything about admission — so the auth tests pass a real
// token instead of expecting a rejection the server was never asked to make.
func newWatchPushTestServer(t *testing.T, webToken string) (*httptest.Server, *backend.Service, string) {
	t.Helper()
	const command = "rm -rf ./build && npm run build"

	cfg := newTestConfig(t)
	cfg.WebToken = webToken
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": command})}},
		{Content: []protocol.Block{protocol.TextBlock("done")}},
	}}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))

	auth, err := watchauth.New(filepath.Join(cfg.StateDir, "watch", "devices.json"))
	if err != nil {
		t.Fatalf("watchauth.New: %v", err)
	}
	server := httptest.NewServer(NewHandlerWithDependencies(Dependencies{
		Config:    manager,
		Backend:   service,
		WatchAuth: auth,
	}))
	t.Cleanup(server.Close)
	return server, service, pairWatch(t, server, webToken)
}

// raisePendingApproval drives a real high-risk tool call until the permission gate
// holds it, and returns the session id.
//
// token must be the configured web token (empty when the test leaves WebToken unset)
// because the session/message routes are behind the same gate this feature is.
func raisePendingApproval(t *testing.T, server *httptest.Server, service *backend.Service, key, token string) string {
	t.Helper()
	resp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions",
		map[string]any{"locator": map[string]string{"channel": "web", "key": key}}, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open session status=%d", resp.StatusCode)
	}
	var opened backend.OpenedSession
	if err := json.NewDecoder(resp.Body).Decode(&opened); err != nil {
		t.Fatalf("decode opened session: %v", err)
	}
	resp.Body.Close()

	msgResp := doJSONWithToken(t, http.MethodPost, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source": "web", "sender": "tester",
			"text": "build the project", "content": "build the project",
		},
	}, token)
	if msgResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(msgResp.Body)
		t.Fatalf("message status=%d body=%s", msgResp.StatusCode, string(body))
	}
	msgResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool {
		return len(s.PendingPermissions) > 0
	})

	// Drain the turn before the test returns. The stub caller's second response and
	// the session store's checkpoints keep writing into StateDir; without this wait
	// those writes race t.TempDir() cleanup ("RemoveAll cleanup: directory not
	// empty"), which is the documented flake in this package.
	t.Cleanup(func() {
		waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool {
			return !s.Running
		})
	})
	return opened.SessionID
}

func getPendingPush(t *testing.T, url, token string, since int64) (int64, []watchPushEvent) {
	t.Helper()
	target := url + "/watch/push/pending"
	if since > 0 {
		target += "?since=" + strconv.FormatInt(since, 10)
	}
	resp, body := getWatch(t, target, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push/pending status=%d body=%s", resp.StatusCode, body)
	}
	var out watchPushPendingResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode pending: %v (%s)", err, body)
	}
	return out.Cursor, out.Events
}

// TestWatchPushPendingReportsRealApproval is the acceptance criterion from §12.2
// B1: create a genuine pending approval, and the queue must surface it.
func TestWatchPushPendingReportsRealApproval(t *testing.T) {
	server, service, token := newWatchPushTestServer(t, "")
	sessionID := raisePendingApproval(t, server, service, "push-approval", "")

	cursor, events := getPendingPush(t, server.URL, token, 0)
	if cursor == 0 {
		t.Fatal("cursor must advance once an event exists")
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly one event, got %d: %+v", len(events), events)
	}
	got := events[0]
	if got.Kind != "approval_requested" {
		t.Fatalf("kind = %q, want approval_requested", got.Kind)
	}
	if got.Title != "需要批准" {
		t.Fatalf("title = %q, want the same wording the wrist uses", got.Title)
	}
	if got.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", got.SessionID, sessionID)
	}
	// The notification must carry something decidable, not just "something needs
	// you": the body is what the shade shows.
	if got.Body == "" {
		t.Fatal("event body must describe the request")
	}
	if got.ID == "" || got.Seq == 0 || got.CreatedAt.IsZero() {
		t.Fatalf("event must be fully identified: %+v", got)
	}
}

// TestWatchPushPendingDoesNotRepeatOnEveryPoll pins the property that makes
// derivation usable: the client polls every 30s, and re-notifying the same
// approval each time would make the feature worse than useless.
func TestWatchPushPendingDoesNotRepeatOnEveryPoll(t *testing.T) {
	server, service, token := newWatchPushTestServer(t, "")
	raisePendingApproval(t, server, service, "push-idempotent", "")

	cursor, first := getPendingPush(t, server.URL, token, 0)
	if len(first) != 1 {
		t.Fatalf("first poll should see one event, got %d", len(first))
	}
	// Poll again with the cursor: nothing new.
	next, again := getPendingPush(t, server.URL, token, cursor)
	if len(again) != 0 {
		t.Fatalf("repeat poll must not re-deliver, got %d: %+v", len(again), again)
	}
	if next != cursor {
		t.Fatalf("cursor moved without new events: %d -> %d", cursor, next)
	}
	// A client that ignores the cursor still sees the event exactly once.
	_, repeat := getPendingPush(t, server.URL, token, 0)
	if len(repeat) != 1 {
		t.Fatalf("without a cursor the event is still one entry, got %d", len(repeat))
	}
}

// TestWatchPushAckDropsDeliveredEvents covers the optional ack path.
func TestWatchPushAckDropsDeliveredEvents(t *testing.T) {
	server, service, token := newWatchPushTestServer(t, "")
	raisePendingApproval(t, server, service, "push-ack", "")

	_, events := getPendingPush(t, server.URL, token, 0)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	resp, body := postWatchJSON(t, server.URL+"/watch/push/ack",
		map[string]any{"ids": []string{events[0].ID}}, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", resp.StatusCode, body)
	}
	var acked map[string]int
	if err := json.Unmarshal(body, &acked); err != nil {
		t.Fatalf("decode ack: %v (%s)", err, body)
	}
	if acked["removed"] != 1 {
		t.Fatalf("ack should remove one event, got %s", body)
	}
	if _, left := getPendingPush(t, server.URL, token, 0); len(left) != 0 {
		t.Fatalf("acked event must not be delivered again, got %+v", left)
	}
}

// TestWatchPushRequiresReadScope is the "越权" half of the acceptance criterion.
func TestWatchPushRequiresReadScope(t *testing.T) {
	const webToken = "secret-token"
	server, _, _ := newWatchPushTestServer(t, webToken)

	// Pair a watch with command only: the push queue must be closed to it.
	resp, body := postWatchJSON(t, server.URL+"/watch/pair/start", map[string]any{"name": "CmdOnly"}, "")
	var pairing struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if err := json.Unmarshal(body, &pairing); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	postWatchJSON(t, server.URL+"/watch/pair/approve", map[string]any{
		"user_code": pairing.UserCode, "scopes": []string{"watch:command"},
	}, webToken)
	if resp, body = postWatchJSON(t, server.URL+"/watch/pair/token",
		map[string]any{"device_code": pairing.DeviceCode}, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("pair/token: %d %s", resp.StatusCode, body)
	}
	var approved struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &approved); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	resp, body = getWatch(t, server.URL+"/watch/push/pending", approved.Token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("command-only watch must not read the push queue: status=%d body=%s", resp.StatusCode, body)
	}
	// Same rule for ack.
	resp, body = postWatchJSON(t, server.URL+"/watch/push/ack", map[string]any{"ids": []string{"pe_x"}}, approved.Token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("command-only watch must not ack: status=%d body=%s", resp.StatusCode, body)
	}
	_ = resp
}

// TestWatchPushRejectsUnknownCredential confirms the gate is not a bypass: an
// unrecognised bearer token must be refused, not treated as "no watch here".
func TestWatchPushRejectsUnknownCredential(t *testing.T) {
	// A real web token must be configured, otherwise the bearer middleware is a
	// pass-through and the fallthrough would admit everything.
	server, _, _ := newWatchPushTestServer(t, "secret-token")
	resp, _ := getWatch(t, server.URL+"/watch/push/pending", "wch_not-a-real-credential")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown credential must be 401, got %d", resp.StatusCode)
	}
}

// TestWatchPushAdmitsPhoneWebToken is the D1 dependency: the phone bridge (mobile/)
// authenticates with the node's web token and has no wrist credential at all. If
// the queue only accepted watch credentials, the phone could never drain it and
// W5 would have no consumer — which is the entire purpose of the queue.
func TestWatchPushAdmitsPhoneWebToken(t *testing.T) {
	const webToken = "secret-token"
	server, service, watchToken := newWatchPushTestServer(t, webToken)
	raisePendingApproval(t, server, service, "push-phone", webToken)

	// The phone, using only the web token, sees the same event the wrist does.
	phoneCursor, phoneEvents := getPendingPush(t, server.URL, webToken, 0)
	if len(phoneEvents) != 1 {
		t.Fatalf("phone (web token) must drain the queue, got %d events", len(phoneEvents))
	}
	// The wrist sees the same cursor: one queue, not one per client.
	watchCursor, watchEvents := getPendingPush(t, server.URL, watchToken, 0)
	if phoneCursor != watchCursor {
		t.Fatalf("cursor diverged between clients: phone=%d watch=%d", phoneCursor, watchCursor)
	}
	if len(watchEvents) != 1 || watchEvents[0].ID != phoneEvents[0].ID {
		t.Fatalf("both clients must see the same event: phone=%+v watch=%+v", phoneEvents, watchEvents)
	}
	// A bad web token is still refused: admission accepts two credential types, it
	// does not accept anything.
	resp, _ := getWatch(t, server.URL+"/watch/push/pending", "wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad web token must be 401, got %d", resp.StatusCode)
	}
}

// ---- queue unit coverage (time and bounds) ----

func TestWatchPushQueuePrunesByTTL(t *testing.T) {
	q := newWatchPushQueue()
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	current := base
	q.now = func() time.Time { return current }

	q.snapshot([]watchApprovalItem{{Kind: "permission", SessionID: "s1", RequestID: "r1"}}, 0, "")
	if _, events := q.snapshot(nil, 0, ""); len(events) != 1 {
		t.Fatalf("event should be present initially, got %d", len(events))
	}

	// Past the TTL the event must be gone, and its key forgotten so a later
	// reappearance notifies again instead of being silently swallowed.
	current = base.Add(watchPushTTL + time.Minute)
	if _, events := q.snapshot(nil, 0, ""); len(events) != 0 {
		t.Fatalf("expired event must be pruned, got %+v", events)
	}
}

func TestWatchPushQueueIsBounded(t *testing.T) {
	q := newWatchPushQueue()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }

	for i := 0; i < maxWatchPushEvents+50; i++ {
		q.snapshot([]watchApprovalItem{{
			Kind: "permission", SessionID: "s", RequestID: "req-" + strconv.Itoa(i),
		}}, 0, "")
	}
	_, events := q.snapshot(nil, 0, "")
	if len(events) > maxWatchPushEvents {
		t.Fatalf("queue exceeds its bound: %d events", len(events))
	}
	if len(events) == 0 {
		t.Fatal("newest events must survive trimming")
	}
}
