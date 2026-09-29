package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func TestFlowSessionWebSocketReconnectUsesSequenceCursorAndDeduplicatesIngress(t *testing.T) {
	cfg := newTestConfig(t)
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, nil), commands.NewService(cfg))
	def := &flow.Definition{
		FlowID:        "fl_session_ws_reconnect",
		Version:       "1",
		Status:        "draft",
		ExecutionMode: flow.ExecutionModeSession,
		SessionWorkflow: &flow.SessionWorkflowSpec{Triggers: []flow.SessionTrigger{
			{EventType: "turn.started", EntryNode: "mark"},
		}},
		Nodes: []flow.Node{{
			ID: "mark", Kind: flow.KindFunction,
			Function: &flow.FunctionSpec{
				Runtime: flow.FunctionRuntimeJS,
				Source:  `function handle(ctx, event) { return {session_state: {turn: event.payload.turn}}; }`,
			},
		}},
	}
	if _, err := service.CreateFlow(agent.FlowCreateArgs{Def: def}); err != nil {
		t.Fatalf("create session flow: %v", err)
	}
	session, err := service.CreateFlowSession(def.FlowID, def.Version, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	mux := http.NewServeMux()
	registerFlowSessionStreamRoute(mux, service, func(next http.Handler) http.Handler { return next }, func() string {
		return "test-web-token"
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/v1/flows/" + def.FlowID + "/sessions/" + session.SessionID +
		"/ws?token=test-web-token&after_sequence=0"

	dial := func(url string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatalf("dial flow session websocket: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		return conn
	}
	readMessage := func(conn *websocket.Conn) map[string]any {
		t.Helper()
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket message: %v", err)
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode websocket message %s: %v", data, err)
		}
		return message
	}

	input := map[string]any{
		"type": "event", "source": "web-client", "source_sequence": 1,
		"event_type": "turn.started", "payload": map[string]any{"turn": "t-1"},
	}
	first := dial(wsURL)
	defer first.Close()
	if message := readMessage(first); message["type"] != "resumed" || message["after_sequence"] != float64(0) {
		t.Fatalf("unexpected initial resume message: %+v", message)
	}
	if err := first.WriteJSON(input); err != nil {
		t.Fatalf("send event: %v", err)
	}
	if message := readMessage(first); message["type"] != "accepted" {
		t.Fatalf("expected ingress receipt, got %+v", message)
	}
	if message := readMessage(first); message["type"] != "session_event" {
		t.Fatalf("expected journal event delivery, got %+v", message)
	}
	_ = first.Close()

	reconnected := dial(wsURL)
	defer reconnected.Close()
	if message := readMessage(reconnected); message["type"] != "resumed" {
		t.Fatalf("expected reconnect resume marker, got %+v", message)
	}
	replayed := readMessage(reconnected)
	if replayed["type"] != "session_event" {
		t.Fatalf("expected event replay after reconnect, got %+v", replayed)
	}
	replayedEvent, ok := replayed["event"].(map[string]any)
	if !ok || replayedEvent["sequence"] != float64(1) || replayedEvent["type"] != "turn.started" {
		t.Fatalf("unexpected replayed event: %+v", replayed)
	}
	if err := reconnected.WriteJSON(input); err != nil {
		t.Fatalf("retry event after reconnect: %v", err)
	}
	receipt := readMessage(reconnected)
	if receipt["type"] != "accepted" {
		t.Fatalf("expected duplicate ingress receipt, got %+v", receipt)
	}
	receiptBody, ok := receipt["receipt"].(map[string]any)
	if !ok || receiptBody["duplicate"] != true || receiptBody["sequence"] != float64(1) {
		t.Fatalf("source sequence retry was not idempotent: %+v", receipt)
	}
	page, offset, err := service.FlowSessionEventPage(def.FlowID, session.SessionID, 0, 0, 1)
	if err != nil || len(page) != 1 || page[0].Sequence != 1 || offset <= 0 {
		t.Fatalf("read first offset-backed event page: page=%+v offset=%d err=%v", page, offset, err)
	}
	page, nextOffset, err := service.FlowSessionEventPage(def.FlowID, session.SessionID, 1, offset, 1)
	if err != nil || len(page) != 0 || nextOffset != offset {
		t.Fatalf("read event page from saved byte offset: page=%+v offset=%d err=%v", page, nextOffset, err)
	}

	afterCursorURL := strings.Replace(wsURL, "after_sequence=0", "after_sequence=1", 1)
	afterCursor := dial(afterCursorURL)
	defer afterCursor.Close()
	if message := readMessage(afterCursor); message["type"] != "resumed" {
		t.Fatalf("expected cursor resume marker, got %+v", message)
	}
	if err := afterCursor.WriteJSON(map[string]any{"type": "ping"}); err != nil {
		t.Fatalf("send ping after cursor: %v", err)
	}
	if message := readMessage(afterCursor); message["type"] != "pong" {
		t.Fatalf("cursor replayed an already-consumed event: %+v", message)
	}
}

func TestWriteFlowSessionEventPagesDrainsReconnectBacklog(t *testing.T) {
	const (
		pageSize = 500
		total    = 1205
	)
	cursor := uint64(0)
	offset := int64(0)
	readCalls := 0
	written := make([]uint64, 0, total)
	err := writeFlowSessionEventPages(
		&cursor,
		&offset,
		pageSize,
		func(after uint64, byteOffset int64, limit int) ([]agent.FlowSessionEvent, int64, error) {
			readCalls++
			events := make([]agent.FlowSessionEvent, 0, limit)
			for sequence := after + 1; sequence <= total && len(events) < limit; sequence++ {
				events = append(events, agent.FlowSessionEvent{Sequence: sequence})
			}
			return events, byteOffset + int64(len(events)), nil
		},
		func(event agent.FlowSessionEvent) error {
			written = append(written, event.Sequence)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("drain event pages: %v", err)
	}
	if cursor != total || offset != total || len(written) != total || readCalls != 3 {
		t.Fatalf("pagination stopped early: cursor=%d offset=%d written=%d read_calls=%d", cursor, offset, len(written), readCalls)
	}
	if written[0] != 1 || written[len(written)-1] != total {
		t.Fatalf("unexpected replay sequence range: first=%d last=%d", written[0], written[len(written)-1])
	}
}
