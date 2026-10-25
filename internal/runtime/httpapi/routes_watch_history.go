// godex-feature: watch-session-history
// 手表端查看多轮对话历史：GET /watch/sessions/{id}/messages
// 文档：docs/wearos-watch-implementation-plan.md
package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// Defaults for the wrist history read.
//
// A separate endpoint from the session detail rather than an extra field on it:
// the detail is polled (the background loop re-reads it every few seconds while
// a turn runs), so attaching a conversation to it would re-ship the whole history
// on every poll. History only changes when a turn does, and is only wanted when
// the user asks to scroll back, so it is fetched on demand.
const (
	watchHistoryDefaultLimit = 12
	watchHistoryMaxLimit     = 40

	// Per-message cap. A wrist cannot usefully render more, and one runaway reply
	// (a pasted log, a generated file) would otherwise consume the whole budget.
	watchHistoryMessageRunes = 600
)

// watchHistoryMessage is one turn of the conversation as the wrist shows it.
type watchHistoryMessage struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp,omitempty"`
	// Truncated tells the client the text was cut, so it can offer to open the
	// full reply rather than silently showing an incomplete answer as complete.
	Truncated bool `json:"truncated,omitempty"`
}

type watchHistoryResponse struct {
	SessionID string                `json:"session_id"`
	Messages  []watchHistoryMessage `json:"messages"`
}

// registerWatchHistoryRoute serves the recent conversation, oldest first.
//
// Read scope: this only reads what the session already holds.
func registerWatchHistoryRoute(mux *http.ServeMux, api watchAPI, read func(http.Handler) http.Handler) {
	service := api.service

	mux.Handle("GET /watch/sessions/{id}/messages", read(api.proxy.withForwardToNode(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("id")
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session service unavailable"))
			return
		}
		limit := watchHistoryDefaultLimit
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if n := parseIntQuery(raw, watchHistoryDefaultLimit); n > 0 && n <= watchHistoryMaxLimit {
				limit = n
			}
		}

		snapshot, err := loadWatchSession(r.Context(), service, sessionID)
		if err != nil {
			writeError(w, statusForSessionError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, watchHistoryResponse{
			SessionID: snapshot.SessionID,
			Messages:  watchConversation(snapshot.Messages, limit),
		})
	}))))
}

// watchConversation keeps the last `limit` user/assistant messages.
//
// Filtering rather than shipping every message: a session's Messages also carry
// tool results, compaction summaries and ephemeral notices, none of which belong
// in a conversation view. Assistant messages with no text (a turn that only
// called a tool) are dropped for the same reason: an empty bubble on a watch is
// just a gap the user has to scroll past.
func watchConversation(messages []protocol.Message, limit int) []watchHistoryMessage {
	if limit <= 0 {
		return nil
	}
	out := make([]watchHistoryMessage, 0, limit)
	// Walk backwards so a long conversation costs one pass and a bounded slice,
	// then reverse into reading order.
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		msg := messages[i]
		if msg.Role != protocol.RoleUser && msg.Role != protocol.RoleAssistant {
			continue
		}
		if msg.Metadata != nil && msg.Metadata.Ephemeral {
			continue
		}
		text := strings.TrimSpace(protocol.MessageText(msg))
		if text == "" {
			continue
		}
		cut := truncateRunes(text, watchHistoryMessageRunes)
		out = append(out, watchHistoryMessage{
			Role:      msg.Role,
			Text:      cut,
			Timestamp: messageTimestamp(msg),
			Truncated: len([]rune(text)) > watchHistoryMessageRunes,
		})
	}
	// Reverse in place: the backwards walk produced newest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// messageTimestamp reads the envelope time the message already carries.
//
// Empty when absent, which the client renders as "no time" rather than inventing
// one from the position in the list.
func messageTimestamp(msg protocol.Message) string {
	if msg.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(msg.Metadata.Timestamp)
}
