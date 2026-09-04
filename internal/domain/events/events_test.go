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

func TestRecorderKeepsBoundedHistoryAndSkipsTextDeltas(t *testing.T) {
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

func TestRecorderPersistsThinkingDeltasButSkipsTextDeltas(t *testing.T) {
	recorder := NewRecorder(10)
	recorder.Emit(Event{Type: EventAssistantTextDelta})
	recorder.Emit(Event{Type: EventAssistantThinkingDelta})
	recorder.Emit(Event{Type: EventToolCallStarted})

	got := recorder.Entries(0)
	if len(got) != 2 {
		t.Fatalf("expected thinking delta + tool event recorded (text delta skipped), got %d: %+v", len(got), got)
	}
	if got[0].Type != EventAssistantThinkingDelta || got[1].Type != EventToolCallStarted {
		t.Fatalf("unexpected recorder contents: %+v", got)
	}
	if RecordableEvent(Event{Type: EventAssistantTextDelta}) {
		t.Fatal("assistant_text_delta must stay non-recordable (final text comes from assistant_message_completed)")
	}
	if !RecordableEvent(Event{Type: EventAssistantThinkingDelta}) {
		t.Fatal("assistant_thinking_delta must be recordable so re-entry can rebuild the thinking process")
	}
}
