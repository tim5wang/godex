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
	"github.com/tim5wang/godex/internal/plugins/taskboard"
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

	q.snapshot([]watchApprovalItem{{Kind: "permission", SessionID: "s1", RequestID: "r1"}}, nil, 0, "")
	if _, events := q.snapshot(nil, nil, 0, ""); len(events) != 1 {
		t.Fatalf("event should be present initially, got %d", len(events))
	}

	// Past the TTL the event must be gone, and its key forgotten so a later
	// reappearance notifies again instead of being silently swallowed.
	current = base.Add(watchPushTTL + time.Minute)
	if _, events := q.snapshot(nil, nil, 0, ""); len(events) != 0 {
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
		}}, nil, 0, "")
	}
	_, events := q.snapshot(nil, nil, 0, "")
	if len(events) > maxWatchPushEvents {
		t.Fatalf("queue exceeds its bound: %d events", len(events))
	}
	if len(events) == 0 {
		t.Fatal("newest events must survive trimming")
	}
}

// ---- card_finished derivation (B2) ----

// mustTaskboardLedger returns the service's real taskboard ledger.
//
// The B2 tests drive this ledger rather than hand-building watchFinishedCard
// values: the event is derived from ledger state, so a fixture would agree with
// the derivation by construction and could not catch a wrong source query.
func mustTaskboardLedger(t *testing.T, service *backend.Service) *taskboard.Ledger {
	t.Helper()
	ledger := service.TaskboardLedger()
	if ledger == nil {
		t.Fatal("test setup: taskboard ledger unavailable")
	}
	return ledger
}

// cardInReview walks a card through the real transitions: create → todo →
// in_progress (with a running execution) → in_review.
func cardInReview(t *testing.T, ledger *taskboard.Ledger, title, execID string) taskboard.Card {
	t.Helper()
	card, err := ledger.CreateCard(taskboard.CreateCardInput{Title: title})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if card, err = ledger.MoveCard(card.ID, card.Version, taskboard.StatusTodo, "human"); err != nil {
		t.Fatalf("move to todo: %v", err)
	}
	// Starting an execution claims the card: it goes to in_progress with a holder.
	if card, err = ledger.StartExecution(card.ID, execID, "sess-"+execID, "agent", nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if card.Status != taskboard.StatusInProgress {
		t.Fatalf("expected execution start to claim the card, got %q", card.Status)
	}
	// The execution session releases its own card.
	if card, err = ledger.MoveCard(card.ID, card.Version, taskboard.StatusInReview, "sess-"+execID); err != nil {
		t.Fatalf("move to in_review: %v", err)
	}
	return card
}

// cardEventTitles returns the (kind,title) pairs the queue would deliver.
func cardEventTitles(events []watchPushEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind+":"+e.Title)
	}
	return out
}

// TestWatchPushReportsFinishedCard is the acceptance criterion from §12.2 B2: a
// card leaving in_progress must surface in the queue. It also pins the two
// follow-on behaviours that make the event usable rather than merely present:
// review activity must not re-notify, and acceptance (in_review → done) is a
// second, differently-worded notification.
func TestWatchPushReportsFinishedCard(t *testing.T) {
	server, service, token := newWatchPushTestServer(t, "")
	ledger := mustTaskboardLedger(t, service)

	if _, events := getPendingPush(t, server.URL, token, 0); len(events) != 0 {
		t.Fatalf("queue must start empty, got %+v", events)
	}

	card := cardInReview(t, ledger, "ship the wrist client", "exec-1")
	cursor, events := getPendingPush(t, server.URL, token, 0)
	if len(events) != 1 {
		t.Fatalf("expected exactly one event, got %+v", cardEventTitles(events))
	}
	got := events[0]
	if got.Kind != watchPushKindCardFinished {
		t.Fatalf("kind = %q, want %q", got.Kind, watchPushKindCardFinished)
	}
	if got.Title != "任务待验收" {
		t.Fatalf("title = %q, want 任务待验收 (in_review awaits a human)", got.Title)
	}
	if got.Body != card.Title {
		t.Fatalf("body = %q, want the card title %q", got.Body, card.Title)
	}
	// RequestID routes a tap to the approval screen; there is no approval behind
	// this event, so leaving it set would send the wrist to the wrong screen.
	if got.RequestID != "" {
		t.Fatalf("request_id = %q, want empty for an informational event", got.RequestID)
	}

	// Review activity bumps the card version. That must not re-notify: a reviewer
	// commenting or ticking a checklist item is routine, and the wrist would spam.
	commented, err := ledger.AddComment(card.ID, card.Version, "reviewer", "looks close")
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if _, more := getPendingPush(t, server.URL, token, cursor); len(more) != 0 {
		t.Fatalf("review activity must not re-notify, got %+v", cardEventTitles(more))
	}

	// Acceptance is a second finish with its own wording. The version comes from
	// the comment above: editing is an optimistic-concurrency write, and reusing
	// the stale version would be rejected rather than silently applied.
	accepted, err := ledger.CompleteCard(commented.ID, commented.Version, "human", true)
	if err != nil {
		t.Fatalf("complete card: %v", err)
	}
	if accepted.Status != taskboard.StatusDone {
		t.Fatalf("expected done, got %q", accepted.Status)
	}
	cursor, more := getPendingPush(t, server.URL, token, cursor)
	if len(more) != 1 {
		t.Fatalf("acceptance must notify separately, got %+v", cardEventTitles(more))
	}
	if more[0].Title != "任务已完成" {
		t.Fatalf("title = %q, want 任务已完成", more[0].Title)
	}

	// And it settles: repeated polls must not emit anything further.
	if _, settled := getPendingPush(t, server.URL, token, cursor); len(settled) != 0 {
		t.Fatalf("done card must not re-notify, got %+v", cardEventTitles(settled))
	}
}

// TestFinishedCardsExcludeUnfinishedWork guards the boundary: "left in_progress"
// must not be approximated by "has an execution" or "is active". A stopped run
// leaves the card in_progress — the work stopped, it did not finish — and a failed
// run likewise must not be announced as ready for acceptance.
func TestFinishedCardsExcludeUnfinishedWork(t *testing.T) {
	_, service, _ := newWatchPushTestServer(t, "")
	ledger := mustTaskboardLedger(t, service)

	// in_progress with a running execution: still working.
	inProgress, err := ledger.CreateCard(taskboard.CreateCardInput{Title: "still running"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inProgress, err = ledger.MoveCard(inProgress.ID, inProgress.Version, taskboard.StatusTodo, "human"); err != nil {
		t.Fatalf("todo: %v", err)
	}
	if inProgress, err = ledger.StartExecution(inProgress.ID, "exec-run", "sess-run", "agent", nil); err != nil {
		t.Fatalf("start: %v", err)
	}

	// in_progress whose execution failed: the card did not advance, so the human
	// has nothing to accept. FinishExecution closes the run but deliberately leaves
	// the card's status alone.
	failed, err := ledger.CreateCard(taskboard.CreateCardInput{Title: "failed run"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if failed, err = ledger.MoveCard(failed.ID, failed.Version, taskboard.StatusTodo, "human"); err != nil {
		t.Fatalf("todo: %v", err)
	}
	if failed, err = ledger.StartExecution(failed.ID, "exec-fail", "sess-fail", "agent", nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	if failed, err = ledger.FinishExecution(failed.ID, "exec-fail", taskboard.ExecutionFailed, "boom"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if failed.Status != taskboard.StatusInProgress {
		t.Fatalf("a failed run must leave the card in_progress, got %q", failed.Status)
	}

	finished := collectFinishedCards(service)
	if len(finished) != 0 {
		t.Fatalf("unfinished work must not qualify, got %+v", finished)
	}
}

// TestFinishedCardIdentityDistinguishesReDispatch covers the anchor: the same card
// finished twice must notify twice, and a re-dispatched card (new execution) is a
// new finish rather than a suppressed repeat.
func TestFinishedCardIdentityDistinguishesReDispatch(t *testing.T) {
	_, service, _ := newWatchPushTestServer(t, "")
	ledger := mustTaskboardLedger(t, service)

	card := cardInReview(t, ledger, "rework me", "exec-a")
	first := collectFinishedCards(service)
	if len(first) != 1 {
		t.Fatalf("expected the card to qualify once, got %+v", first)
	}

	// Send it back and finish it again through a NEW execution.
	var err error
	if card, err = ledger.MoveCard(card.ID, card.Version, taskboard.StatusTodo, "human"); err != nil {
		t.Fatalf("back to todo: %v", err)
	}
	if card, err = ledger.StartExecution(card.ID, "exec-b", "sess-b", "agent", nil); err != nil {
		t.Fatalf("start again: %v", err)
	}
	if card, err = ledger.MoveCard(card.ID, card.Version, taskboard.StatusInReview, "sess-b"); err != nil {
		t.Fatalf("in_review again: %v", err)
	}

	second := collectFinishedCards(service)
	if len(second) != 1 {
		t.Fatalf("expected one finished card, got %+v", second)
	}
	if watchPushKeyForCard(first[0]) == watchPushKeyForCard(second[0]) {
		t.Fatalf("a re-dispatch must produce a distinct identity, both were %q",
			watchPushKeyForCard(first[0]))
	}
}

// TestWatchPushQueueDeduplicatesFinishedCards pins once-per-finish at the queue
// level, where the seen-key bookkeeping actually lives.
func TestWatchPushQueueDeduplicatesFinishedCards(t *testing.T) {
	q := newWatchPushQueue()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }

	card := watchFinishedCard{CardID: "t1", Title: "ship it", Status: taskboard.StatusInReview, Anchor: "exec-1:in_review"}
	if _, events := q.snapshot(nil, []watchFinishedCard{card}, 0, ""); len(events) != 1 {
		t.Fatalf("first observation must queue an event, got %d", len(events))
	}
	if _, events := q.snapshot(nil, []watchFinishedCard{card}, 0, ""); len(events) != 1 {
		t.Fatalf("re-observing the same finish must not queue a second event, got %d", len(events))
	}

	// The same card accepted is a different finish and must notify.
	accepted := card
	accepted.Status = taskboard.StatusDone
	accepted.Anchor = "exec-1:done"
	if _, events := q.snapshot(nil, []watchFinishedCard{accepted}, 0, ""); len(events) != 2 {
		t.Fatalf("acceptance must add an event, got %d", len(events))
	}
}
