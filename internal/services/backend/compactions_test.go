package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
)

// TestCompactionsMergesTimelineAndSummarySources verifies that the Compactions
// endpoint merges durable snapshot_ready+compacted=true events from the session
// timeline with historical summary messages persisted in session state, so
// early compactions that fell out of the recorder's rolling window are still
// reported.
func TestCompactionsMergesTimelineAndSummarySources(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "compactions-merge"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}

	// 1) Seed the session timeline with one compacted snapshot_ready event and
	// one non-compacted snapshot that must be ignored.
	now := time.Now()
	session.timeline.Seed([]events.Event{
		{
			SessionID: opened.SessionID,
			Type:      events.EventSnapshotReady,
			Timestamp: now.Add(-2 * time.Hour),
			Payload: events.SnapshotPayload{
				Compacted:           true,
				TokenEstimateBefore: 40000,
				TokenEstimateAfter:  12000,
				CompressionReasons:  []string{"auto"},
			},
		},
		{
			SessionID: opened.SessionID,
			Type:      events.EventSnapshotReady,
			Timestamp: now.Add(-time.Hour),
			Payload:   events.SnapshotPayload{Compacted: false},
		},
	})
	if err := service.writeSessionTimeline(session); err != nil {
		t.Fatalf("write timeline: %v", err)
	}

	// 2) Seed a historical summary message in session state (older than the
	// timeline window above).
	session.agent.RestoreStateForSession(opened.SessionID, agent.SessionState{
		Messages: []protocol.Message{
			protocol.NewSummaryMessage(
				"## Session Compaction Summary\nCompressed at: "+now.Add(-3*time.Hour).Format("2006-01-02 15:04"),
				"transcript_compactions_merge.json",
			),
		},
	})

	records, err := service.Compactions(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("compactions: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 compaction records, got %d: %+v", len(records), records)
	}
	// Newest first: the timeline event (-2h) must precede the summary (-3h).
	if records[0].Source != "snapshot_ready" {
		t.Fatalf("expected newest record from snapshot_ready, got %+v", records[0])
	}
	if records[1].Source != "summary" {
		t.Fatalf("expected oldest record from summary, got %+v", records[1])
	}
	if records[0].BeforeTokens != 40000 || records[0].AfterTokens != 12000 {
		t.Fatalf("unexpected token estimates: %+v", records[0])
	}
	if len(records[0].Reasons) != 1 || records[0].Reasons[0] != "auto" {
		t.Fatalf("unexpected reasons: %+v", records[0].Reasons)
	}
	if records[1].TranscriptRef != "transcript_compactions_merge.json" {
		t.Fatalf("unexpected transcript ref: %+v", records[1])
	}
}

// TestCompactionsDedupesSameMinuteRecords verifies that records sharing the
// same source and the same rounded minute are collapsed into one entry, which
// prevents double counting when both the timeline event and the summary
// message describe the same compaction.
func TestCompactionsDedupesSameMinuteRecords(t *testing.T) {
	cfg := newTestConfig(t)
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("ok")}}}})

	opened, err := service.OpenSession(context.Background(), SessionLocator{Channel: "web", Key: "compactions-dedupe"})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}

	now := time.Now().Truncate(time.Minute) // same minute for both events
	session.timeline.Seed([]events.Event{
		{
			SessionID: opened.SessionID,
			Type:      events.EventSnapshotReady,
			Timestamp: now.Add(10 * time.Second),
			Payload:   events.SnapshotPayload{Compacted: true},
		},
		{
			SessionID: opened.SessionID,
			Type:      events.EventSnapshotReady,
			Timestamp: now.Add(40 * time.Second),
			Payload:   events.SnapshotPayload{Compacted: true},
		},
	})
	if err := service.writeSessionTimeline(session); err != nil {
		t.Fatalf("write timeline: %v", err)
	}

	records, err := service.Compactions(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("compactions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 deduplicated record, got %d: %+v", len(records), records)
	}
}
