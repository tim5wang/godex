package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
)

const (
	flowSessionStreamReadBuffer    = 16
	flowSessionStreamReadLimit     = agent.MaxFlowSessionEventPayloadBytes + 8192
	flowSessionStreamEventPageSize = 500
)

type flowSessionStreamMessage struct {
	Type           string          `json:"type"`
	Source         string          `json:"source,omitempty"`
	SourceSequence uint64          `json:"source_sequence,omitempty"`
	EventType      string          `json:"event_type,omitempty"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

type flowSessionStreamInbound struct {
	message flowSessionStreamMessage
	err     error
}

func registerFlowSessionStreamRoute(
	mux *http.ServeMux,
	service *backend.Service,
	protected func(http.Handler) http.Handler,
	tokenProvider func() string,
) {
	if service == nil {
		return
	}
	auth := voiceQueryTokenAuth(protected, tokenProvider)
	mux.Handle("GET /v1/flows/{id}/sessions/{sessionID}/ws", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleFlowSessionStream(w, r, service)
	})))
}

func handleFlowSessionStream(w http.ResponseWriter, r *http.Request, service *backend.Service) {
	flowID := r.PathValue("id")
	sessionID := r.PathValue("sessionID")
	after, err := parseFlowSessionSequence(r.URL.Query().Get("after_sequence"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	view, err := service.GetFlowSession(flowID, sessionID)
	if err != nil {
		writeFlowSessionError(w, err)
		return
	}
	if after > view.LastSequence {
		writeError(w, http.StatusBadRequest, fmt.Errorf("after_sequence %d exceeds the latest session sequence %d", after, view.LastSequence))
		return
	}
	if view.Status == "completed" || view.Status == "canceled" {
		writeError(w, http.StatusConflict, fmt.Errorf("cannot connect to a %s flow session", view.Status))
		return
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	changed, unsubscribe := service.SubscribeFlowSessionEvents(flowID, sessionID)
	defer unsubscribe()
	// Refresh after subscribing so a latest-wins update committed between the
	// initial status check and subscription is reflected in the resume snapshot.
	view, err = service.GetFlowSession(flowID, sessionID)
	if err != nil {
		return
	}
	conn.SetReadLimit(flowSessionStreamReadLimit)
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(map[string]any{
		"type": "resumed", "after_sequence": after,
		"status": view.Status, "state": view.State, "state_version": view.StateVersion,
		"latest_signal_outputs": view.LatestSignalOutputs,
	}); err != nil {
		return
	}

	incoming := make(chan flowSessionStreamInbound, flowSessionStreamReadBuffer)
	readerDone := make(chan struct{})
	go readFlowSessionStream(conn, incoming, readerDone)
	defer close(readerDone)

	eventOffset := int64(0)
	writeEvents := func() error {
		return writeFlowSessionEventPages(
			&after,
			&eventOffset,
			flowSessionStreamEventPageSize,
			func(cursor uint64, offset int64, limit int) ([]agent.FlowSessionEvent, int64, error) {
				return service.FlowSessionEventPage(flowID, sessionID, cursor, offset, limit)
			},
			func(event agent.FlowSessionEvent) error {
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				return conn.WriteJSON(map[string]any{"type": "session_event", "event": event})
			},
		)
	}
	writeSnapshot := func() error {
		view, err := service.GetFlowSession(flowID, sessionID)
		if err != nil {
			return err
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(map[string]any{
			"type": "session_snapshot", "status": view.Status, "state": view.State,
			"state_version": view.StateVersion, "latest_signal_outputs": view.LatestSignalOutputs,
		})
	}
	if err := writeEvents(); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case item := <-incoming:
			if item.err != nil {
				if !websocket.IsCloseError(item.err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					writeVoiceError(conn, "bad_message", item.err.Error())
				}
				return
			}
			if err := handleFlowSessionStreamInput(service, flowID, sessionID, item.message, conn); err != nil {
				return
			}
		case <-changed:
			if err := writeEvents(); err != nil {
				return
			}
			if err := writeSnapshot(); err != nil {
				return
			}
		}
	}
}

func writeFlowSessionEventPages(
	cursor *uint64,
	offset *int64,
	pageSize int,
	readPage func(after uint64, offset int64, limit int) ([]agent.FlowSessionEvent, int64, error),
	writeEvent func(agent.FlowSessionEvent) error,
) error {
	if cursor == nil || offset == nil || *offset < 0 || pageSize <= 0 || readPage == nil || writeEvent == nil {
		return fmt.Errorf("flow session event page reader is not configured")
	}
	for {
		events, nextOffset, err := readPage(*cursor, *offset, pageSize)
		if err != nil {
			return err
		}
		if nextOffset < *offset {
			return fmt.Errorf("flow session event offset did not advance")
		}
		if len(events) >= pageSize && nextOffset == *offset {
			return fmt.Errorf("full flow session event page did not advance byte offset")
		}
		for _, event := range events {
			if event.Sequence <= *cursor {
				return fmt.Errorf("flow session event sequence %d did not advance cursor %d", event.Sequence, *cursor)
			}
			if err := writeEvent(event); err != nil {
				return err
			}
			*cursor = event.Sequence
		}
		*offset = nextOffset
		if len(events) < pageSize {
			return nil
		}
	}
}

func readFlowSessionStream(conn *websocket.Conn, incoming chan<- flowSessionStreamInbound, done <-chan struct{}) {
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case incoming <- flowSessionStreamInbound{err: err}:
			case <-done:
			}
			return
		}
		if messageType != websocket.TextMessage {
			select {
			case incoming <- flowSessionStreamInbound{err: errors.New("flow session websocket accepts JSON text messages only")}:
			case <-done:
			}
			return
		}
		var message flowSessionStreamMessage
		if err := json.Unmarshal(data, &message); err != nil {
			select {
			case incoming <- flowSessionStreamInbound{err: fmt.Errorf("invalid flow session websocket JSON: %w", err)}:
			case <-done:
			}
			return
		}
		select {
		case incoming <- flowSessionStreamInbound{message: message}:
		case <-done:
			return
		}
	}
}

func handleFlowSessionStreamInput(
	service *backend.Service,
	flowID, sessionID string,
	message flowSessionStreamMessage,
	conn *websocket.Conn,
) error {
	if message.Type == "ping" {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(map[string]any{"type": "pong"})
	}
	input := agent.FlowSessionEventInput{
		Source:         message.Source,
		SourceSequence: message.SourceSequence,
		Type:           message.EventType,
		CorrelationID:  message.CorrelationID,
		OccurredAt:     message.OccurredAt,
		Payload:        message.Payload,
	}
	if input.Source == "" {
		input.Source = "websocket-client"
	}
	switch message.Type {
	case "event":
		receipt, err := service.AppendFlowSessionEvent(flowID, sessionID, input)
		if err != nil {
			writeFlowSessionWebSocketError(conn, err)
			return nil
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(map[string]any{"type": "accepted", "delivery": "durable", "receipt": receipt})
	case "signal":
		receipt, err := service.PublishFlowSessionSignal(flowID, sessionID, input)
		if err != nil {
			if errors.Is(err, backend.ErrFlowSessionRuntimeUnavailable) {
				writeVoiceError(conn, "runtime_unavailable", err.Error())
			} else if errors.Is(err, backend.ErrFlowSessionMailboxFull) {
				writeVoiceError(conn, "mailbox_full", err.Error())
			} else {
				writeVoiceError(conn, "invalid_signal", err.Error())
			}
			return nil
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(map[string]any{"type": "accepted", "delivery": "latest_wins", "receipt": receipt})
	default:
		writeVoiceError(conn, "bad_message", `type must be "event", "signal" or "ping"`)
		return nil
	}
}

func writeFlowSessionWebSocketError(conn *websocket.Conn, err error) {
	code := "invalid_event"
	switch {
	case errors.Is(err, agent.ErrFlowSessionNotFound):
		code = "session_not_found"
	case errors.Is(err, agent.ErrFlowSessionConflict):
		code = "session_conflict"
	case errors.Is(err, agent.ErrFlowSessionEventTooLarge):
		code = "event_too_large"
	case errors.Is(err, agent.ErrFlowSessionPendingLimit):
		code = "pending_limit"
	}
	writeVoiceError(conn, code, err.Error())
}

func parseFlowSessionSequence(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid after_sequence: %w", err)
	}
	return sequence, nil
}
