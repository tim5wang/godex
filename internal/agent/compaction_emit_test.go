package agent

import (
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/events"
)

// TestCompactConversationEmitsSnapshotReadyEvent verifies that the manual
// compaction path (CompactConversationWithMode) emits the same
// snapshot_ready + compacted=true event the automatic path emits in the
// runner, so manual compactions are recorded in the session timeline and
// surfaced by the compaction history panel.
func TestCompactConversationEmitsSnapshotReadyEvent(t *testing.T) {
	a := newTestAgent(t, 100000)
	a.AddMessage(strings.Repeat("manual compact should emit snapshot ", 40))

	var got *events.Event
	a.emitSink = events.SinkFunc(func(event events.Event) {
		if event.Type == events.EventSnapshotReady {
			event := event
			got = &event
		}
	})

	output, err := a.CompactConversationWithMode("fast")
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if output == "" {
		t.Fatal("expected summary output")
	}
	if got == nil {
		t.Fatal("expected snapshot_ready event from manual compaction")
	}
	payload, ok := got.Payload.(events.SnapshotPayload)
	if !ok {
		t.Fatalf("expected SnapshotPayload, got %T", got.Payload)
	}
	if !payload.Compacted {
		t.Fatal("expected compacted=true on manual compaction event")
	}
	if len(payload.CompressionReasons) != 1 || payload.CompressionReasons[0] != "manual" {
		t.Fatalf("expected manual compression reason, got %v", payload.CompressionReasons)
	}
	if payload.TokenEstimateBefore == 0 {
		t.Fatal("expected non-zero token_estimate_before")
	}
	if payload.TokenEstimateAfter == 0 {
		t.Fatal("expected non-zero token_estimate_after")
	}
}
