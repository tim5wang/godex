package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
)

func TestSessionEventBatcherHoldsDeltasUntilStreamEnd(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "batch-deltas"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	turnID := "turn-batch"

	emit := func(eventType events.EventType, payload any) {
		session.events.Emit(events.Event{
			SessionID: opened.SessionID,
			TurnID:    turnID,
			Type:      eventType,
			Timestamp: time.Now(),
			Payload:   payload,
		})
	}

	emit(events.EventAssistantTextDelta, events.TextPayload{Role: protocol.RoleAssistant, Text: "hel"})
	emit(events.EventAssistantTextDelta, events.TextPayload{Role: protocol.RoleAssistant, Text: "lo"})
	if got := len(readJournalOrEmpty(t, service, opened.SessionID)); got != 0 {
		t.Fatalf("expected no journal writes while streaming, got %d events", got)
	}

	// The stream-end event drains the accumulated deltas in one flush.
	now := time.Now()
	emit(events.EventModelRequestCompleted, events.ModelRequestPayload{
		Model:        "deepseek-v4-flash",
		DurationMS:   1000,
		StopReason:   "tool_calls",
		StartedAt:    now.Add(-time.Second),
		CompletedAt:  now,
		InputTokens:  100,
		OutputTokens: 10,
	})
	journal := readPersistedEventJournal(t, service, opened.SessionID)
	if len(journal) != 3 {
		t.Fatalf("expected deltas + stream-end event in journal after flush, got %d events", len(journal))
	}
	var deltaText string
	for _, item := range journal {
		switch item.Type {
		case events.EventAssistantTextDelta:
			if payload, ok := item.Payload.(map[string]interface{}); ok {
				if text, ok := payload["text"].(string); ok {
					deltaText += text
				}
			}
		case events.EventModelRequestCompleted:
		default:
			t.Fatalf("unexpected journaled event %q", item.Type)
		}
	}
	if deltaText != "hello" {
		t.Fatalf("expected streamed text in journal, got %q", deltaText)
	}
}

func TestSessionEventBatcherWritesThroughTurnBoundary(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})
	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "batch-boundary"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}
	session.events.Emit(events.Event{
		SessionID: opened.SessionID,
		TurnID:    "turn-boundary",
		Type:      events.EventUserMessageAccepted,
		Timestamp: time.Now(),
		Payload: events.MessagePayload{
			Source: string(message.SourceWeb),
			Sender: cfg.LeadName,
			Text:   "boundary event",
		},
	})
	journal := readPersistedEventJournal(t, service, opened.SessionID)
	if len(journal) != 1 || journal[0].Type != events.EventUserMessageAccepted {
		t.Fatalf("expected turn boundary event written through immediately, got %+v", journal)
	}
}

func readJournalOrEmpty(t *testing.T, service *Service, sessionID string) []events.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(service.sessionDir(sessionID), eventJournalFileName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read event journal: %v", err)
	}
	var decoded []events.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event journal line: %v", err)
		}
		decoded = append(decoded, event)
	}
	return decoded
}
