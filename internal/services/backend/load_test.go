package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/message"
)

// TestRecoverInterruptedTurnAutoResumesPlainRunningTurn guards the positive
// path of runtime.recovery.auto_resume_interrupted_turns: a genuinely
// interrupted user turn gets exactly one auto-queued recovery.
func TestRecoverInterruptedTurnAutoResumesPlainRunningTurn(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Runtime.Recovery.AutoResumeInterruptedTurns = true
	locator := SessionLocator{Channel: "web", Key: "auto-resume-once"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}

	now := time.Now().Add(-time.Minute)
	turnID := "turn-original-1"
	envelope := message.NewTextEnvelope(message.SourceWeb, opened.SessionID, "user", "do the work", now)
	session.agent.AddEnvelope(envelope)
	session.recordTurnStarted(turnID, envelope, 0, now)
	if err := service.persistSession(session, now); err != nil {
		t.Fatalf("persist running turn: %v", err)
	}

	// Reopen in a fresh service: the running turn must be marked interrupted
	// and exactly one auto-recovery queued.
	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	reopened, err := restored.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	loaded, err := restored.requireSession(reopened.SessionID)
	if err != nil {
		t.Fatalf("require reopened session: %v", err)
	}
	if got := turnRecordStatus(loaded.turnRecords(0), turnID); got != "interrupted" {
		t.Fatalf("expected original turn to be marked interrupted, got %q", got)
	}
	// The auto-recovery must exist: either still queued or already picked up
	// by startQueuedTurns (OpenSession starts queued turns immediately). In
	// the latter case it shows up as a fresh turn record carrying the
	// interrupted_turn_recovery marker.
	waitForRecoveryTurn(t, loaded, turnID)
	// Wait for the async recovery turn to finish so the test leaves no
	// goroutine behind writing session files during TempDir cleanup.
	waitForBackendSnapshot(t, restored, reopened.SessionID, func(snapshot Snapshot) bool {
		return !snapshot.Running
	})
}

// waitForRecoveryTurn polls until an auto-recovery turn for the given source
// turn is visible (still queued or already started as a turn record).
func waitForRecoveryTurn(t *testing.T, session *sessionState, sourceTurnID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, queued := range session.queuedTurns(0) {
			if queued.Envelope.Metadata["recovery_of_turn_id"] == sourceTurnID {
				return
			}
		}
		for _, rec := range session.turnRecords(0) {
			if rec.ID == sourceTurnID {
				continue
			}
			if isRecoveryTurnRecord(rec) && rec.Envelope != nil && rec.Envelope.Metadata["recovery_of_turn_id"] == sourceTurnID {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for auto-recovery of %s; queue=%d records=%+v", sourceTurnID, len(session.queuedTurns(0)), session.turnRecords(0))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRecoverInterruptedTurnStopsResumeChain guards the resume-of-resume loop:
// when the interrupted turn is itself an auto-generated recovery turn, loading
// the session must NOT queue another "Resume interrupted turn …" recovery.
// Without this guard, every process restart appends one more recovery turn
// (R1 -> resume R1 -> R2 -> resume R2 -> …) and the session is never usable.
func TestRecoverInterruptedTurnStopsResumeChain(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Runtime.Recovery.AutoResumeInterruptedTurns = true
	locator := SessionLocator{Channel: "web", Key: "resume-loop-guard"}
	service := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})

	opened, err := service.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	session, err := service.requireSession(opened.SessionID)
	if err != nil {
		t.Fatalf("require session: %v", err)
	}

	now := time.Now().Add(-time.Minute)
	// The previous auto-recovery of the original turn was itself interrupted
	// mid-run (status "running" at process death) — exactly the state found in
	// the field: turn chain where every record has kind=interrupted_turn_recovery.
	recoveryID := "turn-recovery-1"
	envelope := message.NewRuntimeEnvelope(
		message.SourceCommand,
		opened.SessionID,
		"runtime",
		"Resume interrupted turn turn-original-1 from the persisted checkpoint and continue the previous task.",
		now,
		map[string]string{"recovery_of_turn_id": "turn-original-1", "kind": "interrupted_turn_recovery"},
	)
	session.agent.AddEnvelope(envelope)
	session.recordTurnStarted(recoveryID, envelope, 0, now)
	if err := service.persistSession(session, now); err != nil {
		t.Fatalf("persist running recovery turn: %v", err)
	}

	restored := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	reopened, err := restored.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	loaded, err := restored.requireSession(reopened.SessionID)
	if err != nil {
		t.Fatalf("require reopened session: %v", err)
	}
	// The running recovery turn must be marked interrupted (UI transparency)…
	if got := turnRecordStatus(loaded.turnRecords(0), recoveryID); got != "interrupted" {
		t.Fatalf("expected recovery turn to be marked interrupted, got %q", got)
	}
	// …but NO new recovery may be queued, breaking the resume chain.
	if queued := loaded.queuedTurns(0); len(queued) != 0 {
		t.Fatalf("expected no auto-recovery for a recovery turn, got %d: %+v", len(queued), queued)
	}
	// A second reopen (another restart) must stay quiet too.
	restoredAgain := newTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("unused")}}}})
	reopenedAgain, err := restoredAgain.OpenSession(context.Background(), locator)
	if err != nil {
		t.Fatalf("reopen session second time: %v", err)
	}
	loadedAgain, err := restoredAgain.requireSession(reopenedAgain.SessionID)
	if err != nil {
		t.Fatalf("require reopened session: %v", err)
	}
	if queued := loadedAgain.queuedTurns(0); len(queued) != 0 {
		t.Fatalf("expected no auto-recovery across repeated restarts, got %d: %+v", len(queued), queued)
	}
}

// TestIsRecoveryTurnRecord covers the marker detection used by the resume
// chain guard, including the summary-prefix fallback when the envelope was
// not persisted.
func TestIsRecoveryTurnRecord(t *testing.T) {
	now := time.Now()
	mk := func(summary string, metadata map[string]string) TurnRecord {
		envelope := message.NewTextEnvelope(message.SourceWeb, "s", "u", summary, now)
		envelope.Metadata = metadata
		return TurnRecord{Summary: summary, Envelope: &envelope}
	}
	cases := []struct {
		name   string
		record TurnRecord
		want   bool
	}{
		{
			name:   "recovery kind",
			record: mk("whatever", map[string]string{"kind": "interrupted_turn_recovery", "recovery_of_turn_id": "turn-x"}),
			want:   true,
		},
		{
			name:   "recovery_of_turn_id without kind",
			record: mk("whatever", map[string]string{"recovery_of_turn_id": "turn-x"}),
			want:   true,
		},
		{
			name:   "summary prefix fallback",
			record: TurnRecord{Summary: "Resume interrupted turn turn-x from the persisted checkpoint and continue the previous task."},
			want:   true,
		},
		{
			name:   "plain user turn",
			record: mk("do the work", nil),
			want:   false,
		},
		{
			name:   "summary mentioning resume without prefix",
			record: mk("should I resume the work?", nil),
			want:   false,
		},
		{
			name:   "empty record",
			record: TurnRecord{},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRecoveryTurnRecord(tc.record); got != tc.want {
				t.Fatalf("isRecoveryTurnRecord(%q) = %v, want %v", tc.record.Summary, got, tc.want)
			}
		})
	}
}
