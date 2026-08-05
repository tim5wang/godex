package relay

import (
	"testing"
)

func TestStoreEventsBridgeRecordsSnapshots(t *testing.T) {
	store := NewEventStore()
	sink := StoreEvents(store)

	sink("node-a", Frame{
		Type:    FrameEvent,
		Kind:    EventKindSnapshot,
		Payload: []byte(`{"version":"v1.2.0","jobs":[{"id":"j1","phase":"executing","turn":3,"total":5}]}`),
	})

	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected overview after snapshot event")
	}
	if overview.Version != "v1.2.0" {
		t.Errorf("version = %q", overview.Version)
	}
	if len(overview.Jobs) != 1 || overview.Jobs[0].ID != "j1" || overview.Jobs[0].Turn != 3 {
		t.Errorf("jobs = %+v", overview.Jobs)
	}
}

func TestStoreEventsBridgeIgnoresMalformedPayload(t *testing.T) {
	store := NewEventStore()
	sink := StoreEvents(store)

	sink("node-a", Frame{Type: FrameEvent, Kind: EventKindSnapshot, Payload: []byte("{not-json")})

	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected store entry even for malformed payload")
	}
	if overview.Version != "" {
		t.Errorf("expected empty snapshot for malformed payload, got version %q", overview.Version)
	}
}

func TestStoreEventsBridgeRecordsExplicitEvents(t *testing.T) {
	store := NewEventStore()
	sink := StoreEvents(store)

	sink("node-a", Frame{Type: FrameEvent, Kind: "approval", Payload: []byte(`{"id":"ap1"}`)})

	overview, ok := store.Overview("node-a")
	if !ok {
		t.Fatal("expected overview after explicit event")
	}
	if len(overview.RecentEvents) != 1 || overview.RecentEvents[0].Kind != "approval" {
		t.Fatalf("recent events = %+v", overview.RecentEvents)
	}
}

func TestStoreEventsBridgeIgnoresNonEventFrames(t *testing.T) {
	store := NewEventStore()
	sink := StoreEvents(store)

	sink("node-a", Frame{Type: FramePong, Seq: 1})

	if _, ok := store.Overview("node-a"); ok {
		t.Fatal("expected no store entry for non-event frame")
	}
}
