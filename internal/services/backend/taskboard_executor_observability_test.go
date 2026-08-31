package backend

import (
	"context"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/plugins/taskboard"
)

// fixedNow returns a fixed reference time so stall-threshold comparisons are
// deterministic (service.now is a func field set by the test).
func fixedNow(t0 time.Time) func() time.Time {
	return func() time.Time { return t0 }
}

// TestIsStalled_LiveOrRetryable verifies a run that is genuinely active (in a
// turn) or waiting for a recover/retry nudge is NEVER marked stalled, even if
// its last turn is old — a slow-but-alive or paused-by-design run is not a bug.
func TestIsStalled_LiveOrRetryable(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	svc := &Service{now: fixedNow(now)}
	e := &TaskboardExecutor{service: svc}
	ex := taskboard.Execution{StartedAt: old}

	cases := []struct {
		name     string
		snapshot Snapshot
		want     bool
	}{
		{"running turn in-process", Snapshot{Running: true, Turns: []TurnRecord{{UpdatedAt: old}}}, false},
		{"active turn id", Snapshot{ActiveTurnID: "turn-x", Turns: []TurnRecord{{UpdatedAt: old}}}, false},
		{"retryable turn waiting nudge", Snapshot{Turns: []TurnRecord{{ID: "turn-r", UpdatedAt: old, CanRetry: true}}}, false},
		{"idle with recent turn", Snapshot{Turns: []TurnRecord{{UpdatedAt: now.Add(-5 * time.Minute)}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.isStalled(&ex, c.snapshot); got != c.want {
				t.Errorf("isStalled = %v, want %v", got, c.want)
			}
		})
	}
}

// TestIsStalled_IdlePastThreshold verifies a run that is idle (no live turn, no
// retryable turn) with no progress past the threshold is flagged stalled.
func TestIsStalled_IdlePastThreshold(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	svc := &Service{now: fixedNow(now)}
	e := &TaskboardExecutor{service: svc}

	cases := []struct {
		name     string
		ex       taskboard.Execution
		snapshot Snapshot
		want     bool
	}{
		{
			name:     "idle with old last turn",
			ex:       taskboard.Execution{StartedAt: now.Add(-1 * time.Hour)},
			snapshot: Snapshot{Turns: []TurnRecord{{UpdatedAt: now.Add(-40 * time.Minute)}}},
			want:     true,
		},
		{
			name: "no turns, started past threshold",
			ex:   taskboard.Execution{StartedAt: now.Add(-40 * time.Minute)},
			want: true,
		},
		{
			name: "no turns, started recently",
			ex:   taskboard.Execution{StartedAt: now.Add(-5 * time.Minute)},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.isStalled(&c.ex, c.snapshot); got != c.want {
				t.Errorf("isStalled = %v, want %v", got, c.want)
			}
		})
	}
}

// TestReconcileReportsCardConsistencySignal verifies the G4 card-level consistency
// checks: an in_progress card with holder residue but no running execution, and
// an in_review card whose checklist is not fully done, both surface as signals.
// No running executions means no session snapshots are needed.
func TestReconcileReportsCardConsistencySignal(t *testing.T) {
	_, executor, _ := newTestTaskboardExecutor(t)
	ledger := executor.ledger

	// in_progress + holder, no running execution.
	stuck, err := ledger.CreateCard(taskboard.CreateCardInput{Title: "stuck-holder"})
	if err != nil {
		t.Fatalf("create stuck card: %v", err)
	}
	if stuck, err = ledger.MoveCard(stuck.ID, stuck.Version, taskboard.StatusTodo, "actor"); err != nil {
		t.Fatalf("move to todo: %v", err)
	}
	if stuck, err = ledger.MoveCard(stuck.ID, stuck.Version, taskboard.StatusInProgress, "actor"); err != nil {
		t.Fatalf("move to in_progress: %v", err)
	}
	if stuck.Status != taskboard.StatusInProgress || stuck.Holder == "" {
		t.Fatalf("expected in_progress+holder, got %s/%q", stuck.Status, stuck.Holder)
	}

	// in_review with incomplete checklist.
	incomplete, err := ledger.CreateCard(taskboard.CreateCardInput{
		Title:     "incomplete-dod",
		Checklist: []string{"item A"},
	})
	if err != nil {
		t.Fatalf("create incomplete card: %v", err)
	}
	if incomplete, err = ledger.MoveCard(incomplete.ID, incomplete.Version, taskboard.StatusTodo, "actor"); err != nil {
		t.Fatalf("move to todo: %v", err)
	}
	if incomplete, err = ledger.MoveCard(incomplete.ID, incomplete.Version, taskboard.StatusInProgress, "actor"); err != nil {
		t.Fatalf("move to in_progress: %v", err)
	}
	if incomplete, err = ledger.MoveCard(incomplete.ID, incomplete.Version, taskboard.StatusInReview, "actor"); err != nil {
		t.Fatalf("move to in_review: %v", err)
	}
	if incomplete.Status != taskboard.StatusInReview {
		t.Fatalf("expected in_review, got %s", incomplete.Status)
	}
	done, total := incomplete.ChecklistProgress()
	if done != 0 || total != 1 {
		t.Fatalf("expected 0/1 checklist done, got %d/%d", done, total)
	}

	report, err := executor.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 (no running executions)", report.Scanned)
	}
	if len(report.Results) != 0 {
		t.Errorf("Results = %d, want 0", len(report.Results))
	}
	if len(report.Signals) < 2 {
		t.Fatalf("Expected >=2 card-consistency signals, got %d: %+v", len(report.Signals), report.Signals)
	}
	for _, sig := range report.Signals {
		switch sig.CardTitle {
		case "stuck-holder":
			if sig.Field != "holder/execution" {
				t.Errorf("stuck-holder signal field = %q, want holder/execution", sig.Field)
			}
		case "incomplete-dod":
			if sig.Field != "dod" {
				t.Errorf("incomplete-dod signal field = %q, want dod", sig.Field)
			}
		default:
			t.Errorf("unexpected signal card %q", sig.CardTitle)
		}
	}
}
