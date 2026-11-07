package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// TestWatchConversationKeepsReadingOrderAndDropsToolTraffic covers the shaping
// rules that decide what a wrist actually shows.
//
// A session's Messages carry tool results, compaction summaries and ephemeral
// notices alongside the conversation. Shipping those would fill a 227dp screen
// with content the user cannot act on, so the filter is the feature — asserted
// here rather than left to the endpoint's integration test, which cannot easily
// construct such a history through the real service.
func TestWatchConversationKeepsReadingOrderAndDropsToolTraffic(t *testing.T) {
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first question"),
		protocol.NewTextMessage("tool", "tool output that must not appear"),
		protocol.NewTextMessage(protocol.RoleAssistant, "first answer"),
		{ // ephemeral notice: must be filtered
			Role:     protocol.RoleUser,
			Content:  []protocol.Block{protocol.TextBlock("transient status")},
			Metadata: &protocol.Metadata{Ephemeral: true},
		},
		{ // assistant turn that only called a tool: empty bubble, must be dropped
			Role:    protocol.RoleAssistant,
			Content: []protocol.Block{protocol.TextBlock("   ")},
		},
		protocol.NewTextMessage(protocol.RoleUser, "second question"),
		protocol.NewTextMessage(protocol.RoleAssistant, "second answer"),
	}

	got := watchConversation(messages, 10)
	want := []string{
		"user:first question",
		"assistant:first answer",
		"user:second question",
		"assistant:second answer",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d messages, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i].Role+":"+got[i].Text != w {
			t.Errorf("position %d: want %q, got %q", i, w, got[i].Role+":"+got[i].Text)
		}
	}
}

// TestWatchConversationKeepsNewestTail checks that a limit takes the most recent
// messages, still in reading order.
//
// The tail, not the head: "scroll back through the conversation" means the turns
// nearest to now. Returning the beginning of a long session would show the user
// what they already know and hide what they just said.
func TestWatchConversationKeepsNewestTail(t *testing.T) {
	var messages []protocol.Message
	for _, text := range []string{"m1", "m2", "m3", "m4", "m5"} {
		messages = append(messages, protocol.NewTextMessage(protocol.RoleUser, text))
	}

	got := watchConversation(messages, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Text != "m4" || got[1].Text != "m5" {
		t.Fatalf("expected newest tail [m4 m5] in reading order, got [%s %s]", got[0].Text, got[1].Text)
	}
}

// TestWatchConversationMarksTruncation: a cut message must say so, otherwise the
// watch renders a partial reply as if it were complete.
func TestWatchConversationMarksTruncation(t *testing.T) {
	long := strings.Repeat("x", watchHistoryMessageRunes+50)
	short := "short"

	got := watchConversation([]protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, short),
		protocol.NewTextMessage(protocol.RoleAssistant, long),
	}, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Truncated {
		t.Error("short message must not be marked truncated")
	}
	if !got[1].Truncated {
		t.Error("long message must be marked truncated")
	}
	if len([]rune(got[1].Text)) > watchHistoryMessageRunes+1 {
		t.Errorf("truncated text not bounded: %d runes", len([]rune(got[1].Text)))
	}
}

// TestWatchHistoryEndpointRequiresCredential: history is session content, so it
// must not be readable without a watch credential.
func TestWatchHistoryEndpointRequiresCredential(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	resp, body := getWatch(t, server.URL+"/watch/sessions/anything/messages", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", resp.StatusCode, body)
	}
}

// TestWatchHistoryEndpointReturnsMessages drives the route end to end and checks
// the response shape the client parses.
func TestWatchHistoryEndpointReturnsMessages(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")

	created := createWatchSession(t, server.URL, token, map[string]any{})

	resp, body := getWatch(t, server.URL+"/watch/sessions/"+created.SessionID+"/messages", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got watchHistoryResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if got.SessionID != created.SessionID {
		t.Errorf("session_id: want %q, got %q", created.SessionID, got.SessionID)
	}
	// A brand-new session has no conversation; the field must still be a list
	// (absent field would make the client's non-null decoding fail).
	if got.Messages == nil {
		t.Errorf("messages must be an empty list, not null: %s", body)
	}
	if len(got.Messages) != 0 {
		t.Errorf("new session should have no conversation, got %+v", got.Messages)
	}
}

// TestWatchConversationIndexPointsAtTheOriginalMessage pins the handle the client
// uses to fetch the uncut text.
//
// Index must be the position in the ORIGINAL message slice, not in the filtered
// output: the full-text endpoint reads snapshot.Messages[index]. An off-by-filter
// index would open a different message than the one tapped, which is worse than
// showing a cut reply — the user would read someone else's turn as the answer.
func TestWatchConversationIndexPointsAtTheOriginalMessage(t *testing.T) {
	messages := []protocol.Message{
		protocol.NewTextMessage(protocol.RoleUser, "first question"),
		protocol.NewTextMessage("tool", "tool output that must not appear"),
		protocol.NewTextMessage(protocol.RoleAssistant, "first answer"),
		protocol.NewTextMessage(protocol.RoleUser, "second question"),
	}

	got := watchConversation(messages, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 conversation messages, got %d: %+v", len(got), got)
	}
	wantIndex := []int{0, 2, 3}
	for i, want := range wantIndex {
		if got[i].Index != want {
			t.Errorf("position %d: index=%d, want %d", i, got[i].Index, want)
		}
		// The index must actually resolve to the text we reported.
		if resolved := protocol.MessageText(messages[got[i].Index]); resolved != got[i].Text {
			t.Errorf("position %d: index %d resolves to %q, but text was %q",
				i, got[i].Index, resolved, got[i].Text)
		}
	}
}

// newWatchHistoryTestServer exposes the backend service so a test can drive a real
// turn (and therefore a real long reply) before reading it back through the wrist
// routes.
func newWatchHistoryTestServer(t *testing.T, reply string) (*httptest.Server, *backend.Service, string) {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{responses: []protocol.Response{
		{Content: []protocol.Block{protocol.TextBlock(reply)}},
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
	return server, service, pairWatch(t, server, "")
}

// TestWatchHistoryFullTextReturnsTheUncutReply is the regression for "opening a
// message shows it cut".
//
// The list is budgeted to watchHistoryMessageRunes so a wrist fetch stays cheap;
// that budget is not the bug. The bug was that "open full text" re-displayed the
// same cut string, because the client had no way to ask for the rest. This asserts
// the follow-up fetch actually returns the whole reply.
func TestWatchHistoryFullTextReturnsTheUncutReply(t *testing.T) {
	long := strings.Repeat("y", watchHistoryMessageRunes+300)
	server, service, token := newWatchHistoryTestServer(t, long)

	opened := createSessionWithLocator(t, server.URL, map[string]string{"channel": "web", "key": "watch-fulltext"})
	msgResp := postJSON(t, server.URL+"/sessions/"+opened.SessionID+"/messages", map[string]interface{}{
		"envelope": map[string]interface{}{
			"source": "web", "sender": "tester",
			"text": "long reply please", "content": "long reply please",
		},
	})
	if msgResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(msgResp.Body)
		t.Fatalf("message status=%d body=%s", msgResp.StatusCode, string(body))
	}
	msgResp.Body.Close()
	waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool {
		return !s.Running && len(s.Messages) >= 2
	})
	// Drain before TempDir cleanup, same as the other session-driving tests.
	t.Cleanup(func() {
		waitForHTTPSnapshot(t, service, opened.SessionID, func(s backend.Snapshot) bool { return !s.Running })
	})

	resp, body := getWatch(t, server.URL+"/watch/sessions/"+opened.SessionID+"/messages", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("history status=%d body=%s", resp.StatusCode, body)
	}
	var list watchHistoryResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}

	// Find the long assistant reply and confirm the list DID cut it.
	target := -1
	for _, m := range list.Messages {
		if m.Truncated {
			target = m.Index
			break
		}
	}
	if target < 0 {
		t.Fatalf("no truncated message in the list; the budget fixture did not apply: %+v", list.Messages)
	}

	resp, body = getWatch(t, server.URL+"/watch/sessions/"+opened.SessionID+"/messages/"+itoa(target), token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("full text status=%d body=%s", resp.StatusCode, body)
	}
	var full watchHistoryMessageDetail
	if err := json.Unmarshal(body, &full); err != nil {
		t.Fatalf("decode full text: %v (%s)", err, body)
	}
	if full.Index != target {
		t.Errorf("index=%d, want %d", full.Index, target)
	}
	if full.Truncated {
		t.Errorf("a %d-rune reply must fit the full-text cap", len([]rune(long)))
	}
	if got := len([]rune(full.Text)); got != len([]rune(long)) {
		t.Fatalf("full text is %d runes, want the whole %d-rune reply", got, len([]rune(long)))
	}
	if !strings.HasPrefix(full.Text, "yyyy") {
		t.Errorf("full text does not look like the reply: %q", full.Text[:min(20, len(full.Text))])
	}
}

// TestWatchHistoryFullTextRejectsBadIndex keeps the lookup honest: a missing index
// is a client bug (400) and an out-of-range one is a 404, not an empty message that
// would render as "the agent said nothing".
func TestWatchHistoryFullTextRejectsBadIndex(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	token := pairWatch(t, server, "")
	opened := createWatchSession(t, server.URL, token, map[string]any{})
	base := server.URL + "/watch/sessions/" + opened.SessionID + "/messages/"

	resp, body := getWatch(t, base+"not-a-number", token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-numeric index: status=%d body=%s (want 400)", resp.StatusCode, body)
	}
	resp, body = getWatch(t, base+"7", token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("out-of-range index: status=%d body=%s (want 404)", resp.StatusCode, body)
	}
}

// TestWatchHistoryFullTextRequiresCredential: the uncut text is session content,
// so it must not be readable without a watch credential. The list endpoint is
// already covered; this is the new surface, and a new surface is exactly where the
// gate gets forgotten.
func TestWatchHistoryFullTextRequiresCredential(t *testing.T) {
	server, _, _ := newWatchTestServer(t)
	resp, body := getWatch(t, server.URL+"/watch/sessions/anything/messages/0", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", resp.StatusCode, body)
	}
}

// itoa avoids importing strconv for one call in a test file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
