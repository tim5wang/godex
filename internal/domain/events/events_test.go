package events

import (
	"context"
	"testing"
	"time"
)

func TestBroadcasterEmitsToAttachedSinks(t *testing.T) {
	b := NewBroadcaster()
	got := make([]EventType, 0, 2)

	unsubscribe := b.Attach(SinkFunc(func(event Event) {
		got = append(got, event.Type)
	}))
	b.Attach(SinkFunc(func(event Event) {
		got = append(got, event.Type)
	}))

	b.Emit(Event{Type: EventAssistantTextDelta})
	unsubscribe()
	b.Emit(Event{Type: EventTurnCompleted})

	want := []EventType{
		EventAssistantTextDelta,
		EventAssistantTextDelta,
		EventTurnCompleted,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected event fan-out count: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected event order: got %v want %v", got, want)
		}
	}
}

func TestBroadcasterSubscribeBlocksUntilContextCancel(t *testing.T) {
	b := NewBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- b.Subscribe(ctx, SinkFunc(func(Event) {}))
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	if err := <-done; err != context.Canceled {
		t.Fatalf("expected subscribe to return context canceled, got %v", err)
	}
}

func TestRecorderKeepsBoundedHistoryIncludingTextDeltas(t *testing.T) {
	recorder := NewRecorder(3)
	recorder.Emit(Event{Type: EventAssistantTextDelta})
	recorder.Emit(Event{Type: EventUserMessageAccepted})
	recorder.Emit(Event{Type: EventToolCallStarted})
	recorder.Emit(Event{Type: EventToolCallFinished})
	recorder.Emit(Event{Type: EventTurnCompleted})

	got := recorder.Entries(0)
	if len(got) != 3 {
		t.Fatalf("expected bounded history of 3 events, got %d", len(got))
	}

	want := []EventType{
		EventToolCallStarted,
		EventToolCallFinished,
		EventTurnCompleted,
	}
	for i, event := range got {
		if event.Type != want[i] {
			t.Fatalf("unexpected event at %d: got %s want %s", i, event.Type, want[i])
		}
	}
}

func TestRecorderSeedAppliesCapacityAndFiltering(t *testing.T) {
	recorder := NewRecorder(2)
	recorder.Seed([]Event{
		{Type: EventUserMessageAccepted},
		{Type: EventAssistantTextDelta},
		{Type: EventWarningRaised},
		{Type: EventTurnCompleted},
	})

	got := recorder.Entries(0)
	if len(got) != 2 {
		t.Fatalf("expected 2 seeded events, got %d", len(got))
	}
	if got[0].Type != EventWarningRaised || got[1].Type != EventTurnCompleted {
		t.Fatalf("unexpected seeded recorder contents: %+v", got)
	}
}

func TestRecorderPersistsThinkingAndTextDeltas(t *testing.T) {
	recorder := NewRecorder(10)
	recorder.Emit(Event{Type: EventAssistantTextDelta})
	recorder.Emit(Event{Type: EventAssistantThinkingDelta})
	recorder.Emit(Event{Type: EventToolCallStarted})

	got := recorder.Entries(0)
	if len(got) != 3 {
		t.Fatalf("expected text delta + thinking delta + tool event recorded, got %d: %+v", len(got), got)
	}
	if got[0].Type != EventAssistantTextDelta || got[1].Type != EventAssistantThinkingDelta || got[2].Type != EventToolCallStarted {
		t.Fatalf("unexpected recorder contents: %+v", got)
	}
	if !RecordableEvent(Event{Type: EventAssistantTextDelta}) {
		t.Fatal("assistant_text_delta must be recordable so re-entry can rebuild the process text between tool calls")
	}
	if !RecordableEvent(Event{Type: EventAssistantThinkingDelta}) {
		t.Fatal("assistant_thinking_delta must be recordable so re-entry can rebuild the thinking process")
	}
}

// TestRecorderMergesConsecutiveTextDeltas verifies the anti-flood guard:
// a chatty agent (codex-acp streams the reply in 1-10 char chunks) must not
// flood the bounded window and evict the tool calls / thinking deltas around
// it. Consecutive same-turn assistant_text_delta events collapse into one
// entry with the text appended; a delta after a tool event (or a different
// turn) starts a new entry.
func TestRecorderMergesConsecutiveTextDeltas(t *testing.T) {
	recorder := NewRecorder(10)
	emit := func(turnID, text string) {
		recorder.Emit(Event{
			SessionID: "s1",
			TurnID:    turnID,
			Type:      EventAssistantTextDelta,
			Payload:   TextPayload{Role: "assistant", Text: text},
		})
	}
	emit("t1", "收到两个")
	emit("t1", "问题。先")
	emit("t1", "诊断 i18n")
	// A tool call between deltas closes the open entry (mirrors the live
	// sameStream semantics: text before a tool must not merge with text after).
	recorder.Emit(Event{SessionID: "s1", TurnID: "t1", Type: EventToolCallStarted})
	emit("t1", "找到根因")
	// A different turn starts a new entry.
	emit("t2", "next turn")

	got := recorder.Entries(0)
	if len(got) != 4 {
		t.Fatalf("expected [merged text, tool, text, other-turn text], got %d: %+v", len(got), got)
	}
	if got[0].Type != EventAssistantTextDelta {
		t.Fatalf("expected first entry text delta, got %s", got[0].Type)
	}
	merged, ok := got[0].Payload.(TextPayload)
	if !ok || merged.Text != "收到两个问题。先诊断 i18n" {
		t.Fatalf("expected merged delta text, got %+v", got[0].Payload)
	}
	if got[1].Type != EventToolCallStarted {
		t.Fatalf("expected tool event at index 1, got %s", got[1].Type)
	}
	if got[2].Type != EventAssistantTextDelta {
		t.Fatalf("expected post-tool text delta at index 2, got %s", got[2].Type)
	}
	if post, ok := got[2].Payload.(TextPayload); !ok || post.Text != "找到根因" {
		t.Fatalf("expected post-tool delta text, got %+v", got[2].Payload)
	}
	if got[3].TurnID != "t2" {
		t.Fatalf("expected different-turn delta kept separate, got %s", got[3].TurnID)
	}
}
