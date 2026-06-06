package conversation

import (
	"strings"
	"testing"
)

// fakeTodoProvider is a minimal todoStalenessProvider for loop guard tests.
// streak is the number of turns the current in_progress item has gone stale;
// itemID/content/activeForm are returned to the loop guard.
type fakeTodoProvider struct {
	itemID     int
	content    string
	activeForm string
	stale      bool
	streak     int
}

func (f fakeTodoProvider) StaleInProgress(maxTurns int) (int, string, string, bool) {
	if !f.stale || f.streak < maxTurns {
		return 0, "", "", false
	}
	return f.itemID, f.content, f.activeForm, true
}

func readFileExecuted(id string) ExecutedTool {
	return ExecutedTool{
		ID:   id,
		Name: "read_file",
		Input: map[string]interface{}{
			"path": "README.md",
		},
	}
}

func todoWriteExecuted(id string) ExecutedTool {
	return ExecutedTool{
		ID:   id,
		Name: "todo_write",
		Input: map[string]interface{}{
			"items": []interface{}{},
		},
	}
}

func TestLoopGuardStaleTodoTriggersRecover(t *testing.T) {
	guard := newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:    10, // disable the other detectors
		MaxRecoveries:       2,
		StaleTodoThreshold:  3,
	})
	provider := fakeTodoProvider{
		itemID:     42,
		content:    "Write the regression test",
		activeForm: "Writing the regression test",
		stale:      true,
		streak:     3,
	}

	executed := []ExecutedTool{readFileExecuted("t1")}
	decision := guard.Observe(executed, provider)

	if decision.Action != loopGuardRecover {
		t.Fatalf("expected loopGuardRecover, got %s (reason=%s)", decision.Action, decision.Reason)
	}
	if decision.Reason != "stale_todo_in_progress" {
		t.Fatalf("expected reason stale_todo_in_progress, got %q", decision.Reason)
	}
	if decision.Fingerprint != "stale_todo:42" {
		t.Fatalf("expected fingerprint stale_todo:42, got %q", decision.Fingerprint)
	}
	if !strings.Contains(decision.Feedback, "Write the regression test") {
		t.Fatalf("expected feedback to mention in_progress content, got %q", decision.Feedback)
	}
	if !strings.Contains(decision.Feedback, "todo_write") {
		t.Fatalf("expected feedback to tell the model to call todo_write, got %q", decision.Feedback)
	}
}

func TestLoopGuardStaleTodoResetsOnUpdate(t *testing.T) {
	guard := newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:   10,
		MaxRecoveries:      2,
		StaleTodoThreshold: 3,
	})

	// Three turns of read_file with no todo update: the provider reports stale=true.
	provider := fakeTodoProvider{
		itemID:     7,
		content:    "Stale item",
		activeForm: "Stalling",
		stale:      true,
		streak:     3,
	}
	executed := []ExecutedTool{
		readFileExecuted("t1"),
		readFileExecuted("t2"),
		readFileExecuted("t3"),
	}
	decision := guard.Observe(executed, provider)
	if decision.Action != loopGuardRecover {
		t.Fatalf("expected first stale call to recover, got %s", decision.Action)
	}

	// Next turn: model actually calls todo_write; provider reports no longer stale.
	cleared := fakeTodoProvider{
		itemID:  7,
		stale:   false,
		streak:  0,
		content: "Stale item",
	}
	decision = guard.Observe([]ExecutedTool{todoWriteExecuted("tw1")}, cleared)
	if decision.Action != loopGuardAllow {
		t.Fatalf("expected loopGuardAllow after todo_write clears stale state, got %s (reason=%s)", decision.Action, decision.Reason)
	}
}

func TestLoopGuardStaleTodoAbortsAfterBudgetExhausted(t *testing.T) {
	guard := newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:   10,
		MaxRecoveries:      1,
		StaleTodoThreshold: 3,
	})
	provider := fakeTodoProvider{
		itemID:     99,
		content:    "Will not be updated",
		activeForm: "Stuck",
		stale:      true,
		streak:     3,
	}

	// First stale observation: recover (budget 1/1).
	first := guard.Observe([]ExecutedTool{readFileExecuted("t1")}, provider)
	if first.Action != loopGuardRecover {
		t.Fatalf("expected first call to recover with MaxRecoveries=1, got %s", first.Action)
	}

	// Second stale observation on the same itemID: budget exhausted for that
	// itemID, so it should abort, not recover.
	second := guard.Observe([]ExecutedTool{readFileExecuted("t2")}, provider)
	if second.Action != loopGuardAbort {
		t.Fatalf("expected second stale call to abort, got %s", second.Action)
	}
	if second.Reason != "stale_todo_in_progress" {
		t.Fatalf("expected abort reason stale_todo_in_progress, got %q", second.Reason)
	}
	if second.AbortReason == "" {
		t.Fatal("expected non-empty AbortReason on stale-todo abort")
	}
}

func TestLoopGuardStaleTodoProviderNilIsAllow(t *testing.T) {
	guard := newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:   10,
		MaxRecoveries:      2,
		StaleTodoThreshold: 3,
	})
	decision := guard.Observe([]ExecutedTool{readFileExecuted("t1")}, nil)
	if decision.Action != loopGuardAllow {
		t.Fatalf("expected nil provider to fall through to allow, got %s (reason=%s)", decision.Action, decision.Reason)
	}
}

func TestLoopGuardStaleTodoSkipsWhenNoNonTodoExecution(t *testing.T) {
	guard := newLoopGuard(loopGuardConfig{
		MaxRepeatedTools:   10,
		MaxRecoveries:      2,
		StaleTodoThreshold: 3,
	})
	provider := fakeTodoProvider{
		itemID:  1,
		content: "Only todo writes",
		stale:   true,
		streak:  3,
	}
	// Only todo_write executed, no other tool: stale todo detection should skip
	// because the model is actively updating todos, not stalling on other work.
	decision := guard.Observe([]ExecutedTool{todoWriteExecuted("tw1")}, provider)
	if decision.Action != loopGuardAllow {
		t.Fatalf("expected allow when only todo_write executed, got %s (reason=%s)", decision.Action, decision.Reason)
	}
}
