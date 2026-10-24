package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
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
