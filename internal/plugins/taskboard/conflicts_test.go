package taskboard

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// moveToInProgress walks a card from its start status to in_progress following
// the linear kanban transition (backlog→todo→in_progress), so tests can set up
// an active card without tripping the invalid-transition gate. It returns the
// final card so callers use the correct post-transition version.
func moveToInProgress(t *testing.T, l *Ledger, card Card) Card {
	t.Helper()
	cur := card
	switch cur.Status {
	case StatusBacklog:
		tmp, err := l.MoveCard(cur.ID, cur.Version, StatusTodo, agentActor)
		if err != nil {
			t.Fatalf("move to todo: %v", err)
		}
		cur = tmp
		fallthrough
	case StatusTodo:
		tmp, err := l.MoveCard(cur.ID, cur.Version, StatusInProgress, agentActor)
		if err != nil {
			t.Fatalf("move to in_progress: %v", err)
		}
		cur = tmp
	case StatusInProgress:
		// already there
	default:
		t.Fatalf("cannot setup card in status %q", cur.Status)
	}
	return cur
}

// ---- Gate 1: touched_paths static declaration ----

func TestCardTouchedPathsCRUD(t *testing.T) {
	l := openTestLedger(t)
	card, err := l.CreateCard(CreateCardInput{
		Title:        "src card",
		TouchedPaths: []string{"internal/platform/tooling", "/internal/platform/tooling/", "internal/tools", "internal/platform/tooling"},
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	// normalization: trim slashes + dedupe, order preserved
	if got := card.TouchedPaths; len(got) != 2 || got[0] != "internal/platform/tooling" || got[1] != "internal/tools" {
		t.Fatalf("expected normalized [internal/platform/tooling internal/tools], got %+v", got)
	}

	// update replaces via pointer slice
	tp := []string{"internal/plugins/taskboard"}
	card, err = l.UpdateCard(card.ID, card.Version, "agent", UpdateCardInput{TouchedPaths: &tp})
	if err != nil {
		t.Fatalf("update card: %v", err)
	}
	if got := card.TouchedPaths; len(got) != 1 || got[0] != "internal/plugins/taskboard" {
		t.Fatalf("expected replaced paths, got %+v", got)
	}
}

// ---- Gate 2: dispatch intercept ----

func TestPrecheckDispatchBlocksOverlap(t *testing.T) {
	l := openTestLedger(t)
	active, err := l.CreateCard(CreateCardInput{Title: "in-flight", TouchedPaths: []string{"internal/platform/tooling"}})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	moveToInProgress(t, l, active)

	// same package overlap -> blocked
	overlap, _ := l.CreateCard(CreateCardInput{Title: "overlap", TouchedPaths: []string{"internal/platform/tooling"}})
	err = l.PrecheckDispatchConflicts(overlap)
	if !errors.Is(err, ErrPathConflict) {
		t.Fatalf("expected ErrPathConflict for exact overlap, got %v", err)
	}
	pce, ok := err.(*PathConflictError)
	if !ok || len(pce.Report.Conflicts) == 0 {
		t.Fatalf("expected a PathConflictError with report, got %#v", err)
	}
	if pce.Report.Conflicts[0].OtherCard != active.ID {
		t.Fatalf("expected conflict against %q, got %q", active.ID, pce.Report.Conflicts[0].OtherCard)
	}

	// parent package overlap -> blocked ("internal" prefixes "internal/platform/tooling")
	parent, _ := l.CreateCard(CreateCardInput{Title: "parent", TouchedPaths: []string{"internal"}})
	if err := l.PrecheckDispatchConflicts(parent); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("expected ErrPathConflict for parent overlap, got %v", err)
	}

	// sibling package -> no conflict
	sibling, _ := l.CreateCard(CreateCardInput{Title: "sibling", TouchedPaths: []string{"internal/tools"}})
	if err := l.PrecheckDispatchConflicts(sibling); err != nil {
		t.Fatalf("expected no conflict for sibling package, got %v", err)
	}
}

// ---- Gate 2 regression: in_review must NOT block dispatch ----

// An in_review card has stopped working (its execution was closed on leaving
// in_progress) and only awaits human acceptance, so it no longer occupies
// workspace impact and must NOT block a path-overlapping dispatch.
func TestPrecheckDispatchIgnoresInReviewCard(t *testing.T) {
	l := openTestLedger(t)
	// A card that finished its work and is awaiting human acceptance.
	done, _ := l.CreateCard(CreateCardInput{Title: "awaiting review", TouchedPaths: []string{"internal/platform/tooling"}})
	done = moveToInProgress(t, l, done)
	done, err := l.MoveCard(done.ID, done.Version, StatusInReview, agentActor)
	if err != nil {
		t.Fatalf("move to in_review: %v", err)
	}
	if done.Status != StatusInReview {
		t.Fatalf("expected card in in_review, got %q", done.Status)
	}

	// Dispatching a card over the same path must now succeed.
	overlap, _ := l.CreateCard(CreateCardInput{Title: "overlap", TouchedPaths: []string{"internal/platform/tooling"}})
	if err := l.PrecheckDispatchConflicts(overlap); err != nil {
		t.Fatalf("expected no dispatch conflict against in_review card, got %v", err)
	}
}

// An in_progress card still holds a write risk and must keep blocking a
// path-overlapping dispatch (original protection preserved — no regression).
func TestPrecheckDispatchStillBlocksInProgressCard(t *testing.T) {
	l := openTestLedger(t)
	active, _ := l.CreateCard(CreateCardInput{Title: "in-flight", TouchedPaths: []string{"internal/platform/tooling"}})
	active = moveToInProgress(t, l, active)

	overlap, _ := l.CreateCard(CreateCardInput{Title: "overlap", TouchedPaths: []string{"internal/platform/tooling"}})
	if err := l.PrecheckDispatchConflicts(overlap); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("expected ErrPathConflict against in_progress card, got %v", err)
	}
}

// A card with a running execution is also a live writer, even if a stale/temporary
// status would otherwise read as inactive: it must block dispatch too.
func TestPrecheckDispatchBlocksCardWithRunningExecution(t *testing.T) {
	l := openTestLedger(t)
	card, _ := l.CreateCard(CreateCardInput{Title: "executing", TouchedPaths: []string{"internal/platform/tooling"}})
	card = moveToInProgress(t, l, card)
	// Simulate a running execution on this card (StartExecution forces in_progress
	// and registers the run, so the running-execution branch is exercised).
	if _, err := l.StartExecution(card.ID, "exec-1", "sess-1", agentActor, nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}
	overlap, _ := l.CreateCard(CreateCardInput{Title: "overlap", TouchedPaths: []string{"internal/platform/tooling"}})
	if err := l.PrecheckDispatchConflicts(overlap); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("expected ErrPathConflict against card with running execution, got %v", err)
	}
}

func TestPrecheckDispatchSkipsCardsWithoutPaths(t *testing.T) {
	l := openTestLedger(t)
	active, _ := l.CreateCard(CreateCardInput{Title: "active", TouchedPaths: []string{"internal/platform/tooling"}})
	moveToInProgress(t, l, active)
	// a card that did not declare its surface still dispatches (declared only;
	// runtime report is the gate-3 fallback).
	noDecl, _ := l.CreateCard(CreateCardInput{Title: "no declaration"})
	if err := l.PrecheckDispatchConflicts(noDecl); err != nil {
		t.Fatalf("expected no dispatch conflict for undeclared card, got %v", err)
	}
}

// ---- Gate 3: dynamic observation ----

func TestReportObservedPathsUnionsWithDeclared(t *testing.T) {
	l := openTestLedger(t)
	card, _ := l.CreateCard(CreateCardInput{Title: "observed", TouchedPaths: []string{"internal/platform/tooling"}})
	card, err := l.ReportObservedPaths(card.ID, card.Version, agentActor, []string{"internal/plugins/taskboard", "internal/platform/tooling"})
	if err != nil {
		t.Fatalf("report observed: %v", err)
	}
	if len(card.ObservedPaths) != 2 {
		t.Fatalf("expected 2 observed paths, got %+v", card.ObservedPaths)
	}
	// impact surface = declared ∪ observed
	impact := cardImpactPaths(card)
	if len(impact) != 2 || impact[0] != "internal/platform/tooling" || impact[1] != "internal/plugins/taskboard" {
		t.Fatalf("unexpected impact surface: %+v", impact)
	}
}

func TestReportObservedPathsTriggersDynamicConflict(t *testing.T) {
	l := openTestLedger(t)
	active, _ := l.CreateCard(CreateCardInput{Title: "active", TouchedPaths: []string{"internal/platform/tooling"}})
	moveToInProgress(t, l, active)
	// this card declares nothing, then reports a real edit overlapping the active card
	card, _ := l.CreateCard(CreateCardInput{Title: "late reporter"})
	card, err := l.ReportObservedPaths(card.ID, card.Version, agentActor, []string{"internal/platform/tooling"})
	if err != nil {
		t.Fatalf("report observed: %v", err)
	}
	report := l.CheckCardPathConflicts(card)
	if !report.HasConflicts() || report.Conflicts[0].OtherCard != active.ID {
		t.Fatalf("expected dynamic conflict against active card, got %+v", report)
	}
}

// ---- Gate 4: merge precheck on entering in_review ----

func TestMoveToInReviewAttachesMergeReport(t *testing.T) {
	l := openTestLedger(t)
	active, _ := l.CreateCard(CreateCardInput{Title: "active", TouchedPaths: []string{"internal/platform/tooling"}})
	moveToInProgress(t, l, active)

	card, _ := l.CreateCard(CreateCardInput{Title: "merging", TouchedPaths: []string{"internal/platform/tooling"}})
	card = moveToInProgress(t, l, card)
	got, err := l.MoveCard(card.ID, card.Version, StatusInReview, agentActor)
	if err != nil {
		t.Fatalf("move to in_review: %v", err)
	}
	if got.MergeReport == nil || !got.MergeReport.HasConflicts() {
		t.Fatalf("expected a conflict merge report on in_review, got %+v", got.MergeReport)
	}
	if got.MergeReport.Conflicts[0].OtherCard != active.ID {
		t.Fatalf("expected conflict against active card, got %+v", got.MergeReport.Conflicts)
	}
}

func TestMoveToInReviewRecomputesAndClears(t *testing.T) {
	l := openTestLedger(t)
	// active card that will conflict
	active, _ := l.CreateCard(CreateCardInput{Title: "active", TouchedPaths: []string{"internal/platform/tooling"}})
	active = moveToInProgress(t, l, active)
	card, _ := l.CreateCard(CreateCardInput{Title: "merging", TouchedPaths: []string{"internal/platform/tooling"}})
	card = moveToInProgress(t, l, card)

	// moves to in_review with a conflict → report attached
	got, _ := l.MoveCard(card.ID, card.Version, StatusInReview, agentActor)
	if got.MergeReport == nil || !got.MergeReport.HasConflicts() {
		t.Fatalf("expected conflict report on in_review, got %+v", got.MergeReport)
	}
	// resolve the conflict: soft-delete the active card so it drops out of the
	// active set and no longer occupies its impact surface.
	if _, err := l.SoftDeleteCard(active.ID, active.Version, agentActor); err != nil {
		t.Fatalf("soft-delete active: %v", err)
	}
	// bounce to todo then re-review: recomputed report must be empty and clear stale
	got, _ = l.MoveCard(card.ID, got.Version, StatusTodo, agentActor)
	got, _ = l.MoveCard(card.ID, got.Version, StatusInProgress, agentActor)
	got, _ = l.MoveCard(card.ID, got.Version, StatusInReview, agentActor)
	if got.MergeReport != nil && got.MergeReport.HasConflicts() {
		t.Fatalf("expected cleared merge report after resolving conflict, got %+v", got.MergeReport)
	}
}

// ---- tool-level gates 2/3/4 round-trip through the taskboard tool ----

func TestTaskboardDispatchGateBlocksOverlap(t *testing.T) {
	ledger := openTestLedger(t)
	exec := &fakeExecutor{ledger: ledger}
	runner, ok := NewTaskboardToolWithExecutor(ledger, exec).(toolRunner)
	if !ok {
		t.Fatalf("tool does not implement Execute")
	}
	active, _ := ledger.CreateCard(CreateCardInput{Title: "in-flight", TouchedPaths: []string{"internal/platform/tooling"}})
	moveToInProgress(t, ledger, active)
	overlap, _ := ledger.CreateCard(CreateCardInput{Title: "overlap", TouchedPaths: []string{"internal/platform/tooling"}})
	_, err := runner.Execute(context.Background(), map[string]any{"action": "dispatch", "card_id": overlap.ID})
	if err == nil || !strings.Contains(err.Error(), ErrPathConflict.Error()) {
		t.Fatalf("expected dispatch blocked with ErrPathConflict, got %v", err)
	}
	if exec.lastID != "" {
		t.Fatalf("expected executor not to run the blocked card, got %q", exec.lastID)
	}
}

func TestTaskboardReportTouchedToolAction(t *testing.T) {
	tool := newTool(t)
	res := mustExec(t, tool, map[string]interface{}{
		"action": "create", "title": "reporter", "touched_paths": []interface{}{"internal/platform/tooling"},
	})
	card := res["card"].(map[string]any)
	cardID := card["id"].(string)
	version := resultVersion(res)

	// report observed paths
	res = mustExec(t, tool, map[string]interface{}{
		"action": "report_touched", "card_id": cardID, "version": version,
		"observed_paths": []interface{}{"internal/plugins/taskboard"},
	})
	if got := res["observed_paths"].([]interface{}); len(got) != 1 {
		t.Fatalf("expected observed_paths, got %+v", res["observed_paths"])
	}
	if res["conflicts"].(bool) {
		t.Fatalf("expected no conflicts on isolated card, got %+v", res["conflict_report"])
	}
}

func TestTaskboardMergePrecheckToolAction(t *testing.T) {
	tool := newTool(t)
	// create a conflicting active card
	active := mustExec(t, tool, map[string]interface{}{
		"action": "create", "title": "active", "touched_paths": []interface{}{"internal/platform/tooling"},
	})
	activeID := active["card"].(map[string]any)["id"].(string)
	activeVer := resultVersion(active)
	mustExec(t, tool, map[string]interface{}{"action": "move", "card_id": activeID, "version": activeVer, "to": StatusTodo})
	mustExec(t, tool, map[string]interface{}{"action": "move", "card_id": activeID, "version": activeVer, "to": StatusInProgress})

	// target card in_progress with same path
	target := mustExec(t, tool, map[string]interface{}{
		"action": "create", "title": "target", "touched_paths": []interface{}{"internal/platform/tooling"},
	})
	targetID := target["card"].(map[string]any)["id"].(string)
	targetVer := resultVersion(target)
	mustExec(t, tool, map[string]interface{}{"action": "move", "card_id": targetID, "version": targetVer, "to": StatusTodo})
	mustExec(t, tool, map[string]interface{}{"action": "move", "card_id": targetID, "version": targetVer, "to": StatusInProgress})

	// merge_precheck should surface the conflict
	res := mustExec(t, tool, map[string]interface{}{
		"action": "merge_precheck", "card_id": targetID, "version": targetVer,
	})
	if res["conflicts"].(bool) != true {
		t.Fatalf("expected conflicts in merge precheck, got %+v", res["merge_report"])
	}
	// move to in_review attaches the merge report automatically (gate 4 in MoveCard)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "move", "card_id": targetID, "version": targetVer, "to": StatusInReview,
	}); err != nil {
		t.Fatalf("move to in_review: %v", err)
	}
	// compact card does not carry the report; verify via a full get
	full := mustExec(t, tool, map[string]interface{}{"action": "get", "card_id": targetID})
	cardFull := full["card"].(map[string]any)
	if cardFull["merge_report"] == nil {
		t.Fatalf("expected merge_report attached on in_review via get, got %+v", cardFull)
	}
}
