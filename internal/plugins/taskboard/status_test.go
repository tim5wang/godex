package taskboard

import (
	"testing"
	"time"
)

func TestStatusCountsGroupsByCardAndExecutionStatus(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)

	mkCard := func(title, status string) Card {
		card, err := l.CreateCard(CreateCardInput{
			ProjectID: project.ID, Title: title, StartStatus: status, CreatedBy: "test",
		})
		if err != nil {
			t.Fatalf("create card: %v", err)
		}
		return card
	}

	// One done card (todo -> in_progress -> in_review -> done via human
	// acceptance), one in_progress card with a running execution, one
	// in_progress card with a failed execution.
	step := func(id string, version int, to string) int {
		moved, err := l.MoveCard(id, version, to, humanActor)
		if err != nil {
			t.Fatalf("move %s to %s: %v", id, to, err)
		}
		return moved.Version
	}
	doneCard := mkCard("done", StatusTodo)
	v := step(doneCard.ID, doneCard.Version, StatusInProgress)
	v = step(doneCard.ID, v, StatusInReview)
	if _, err := l.CompleteCard(doneCard.ID, v, humanActor, true); err != nil {
		t.Fatalf("complete done card: %v", err)
	}
	runningCard := mkCard("running", StatusTodo)
	if _, err := l.StartExecution(runningCard.ID, "exec-run", "sess-run", "test", nil); err != nil {
		t.Fatalf("start running execution: %v", err)
	}
	failedCard := mkCard("failed", StatusTodo)
	if _, err := l.StartExecution(failedCard.ID, "exec-fail", "sess-fail", "test", nil); err != nil {
		t.Fatalf("start failed execution: %v", err)
	}
	if _, err := l.FinishExecution(failedCard.ID, "exec-fail", ExecutionFailed, "boom"); err != nil {
		t.Fatalf("finish failed execution: %v", err)
	}

	sc := l.StatusCounts("")
	if sc.Total != 3 {
		t.Fatalf("expected total 3, got %d", sc.Total)
	}
	if sc.Cards[StatusDone] != 1 {
		t.Fatalf("expected 1 done card, got %d", sc.Cards[StatusDone])
	}
	if sc.Executions[ExecutionRunning] != 1 {
		t.Fatalf("expected 1 running execution, got %d", sc.Executions[ExecutionRunning])
	}
	if sc.Executions[ExecutionFailed] != 1 {
		t.Fatalf("expected 1 failed execution, got %d", sc.Executions[ExecutionFailed])
	}
	// failed terminal counts toward Error.
	if sc.Error != 1 {
		t.Fatalf("expected error 1, got %d", sc.Error)
	}

	// Running execution with a recorded error observation counts toward Error too.
	errCard := mkCard("err", StatusTodo)
	if _, err := l.StartExecution(errCard.ID, "exec-err", "sess-err", "test", nil); err != nil {
		t.Fatalf("start err execution: %v", err)
	}
	if _, err := l.UpdateExecutionObservation(errCard.ID, "exec-err", ExecutionObservation{ErrorType: "provider_network", LastError: "boom"}); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	sc = l.StatusCounts("")
	if sc.Error != 2 {
		t.Fatalf("expected error 2 after observation, got %d", sc.Error)
	}
}

func TestStatusCountsCountMap(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, err := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "a", StartStatus: StatusTodo, CreatedBy: "test"})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	v := card.Version
	if moved, err := l.MoveCard(card.ID, v, StatusInProgress, humanActor); err != nil {
		t.Fatalf("move to in_progress: %v", err)
	} else {
		v = moved.Version
	}
	if moved, err := l.MoveCard(card.ID, v, StatusInReview, humanActor); err != nil {
		t.Fatalf("move to in_review: %v", err)
	} else {
		v = moved.Version
	}
	if _, err := l.CompleteCard(card.ID, v, humanActor, true); err != nil {
		t.Fatalf("complete card: %v", err)
	}
	sc := l.StatusCounts("")
	m := sc.CountMap()
	if m["total_count"] != 1 || m["done_count"] != 1 {
		t.Fatalf("unexpected count map: %#v", m)
	}
	if _, ok := m["error_count"]; !ok {
		t.Fatalf("missing error_count in map")
	}
	// UpdatedAt is set by the snapshot.
	if sc.UpdatedAt.IsZero() || time.Since(sc.UpdatedAt) > time.Minute {
		t.Fatalf("expected recent UpdatedAt, got %v", sc.UpdatedAt)
	}
}

func TestStatusCountsProjectScoped(t *testing.T) {
	l := openTestLedger(t)
	p1 := seedProject(t, l)
	p2, err := l.CreateProject("other", t.TempDir())
	if err != nil {
		t.Fatalf("create p2: %v", err)
	}
	if _, err := l.CreateCard(CreateCardInput{ProjectID: p1.ID, Title: "p1", StartStatus: StatusTodo, CreatedBy: "test"}); err != nil {
		t.Fatalf("create p1 card: %v", err)
	}
	if _, err := l.CreateCard(CreateCardInput{ProjectID: p2.ID, Title: "p2", StartStatus: StatusTodo, CreatedBy: "test"}); err != nil {
		t.Fatalf("create p2 card: %v", err)
	}
	sc := l.StatusCounts(p1.ID)
	if sc.Total != 1 || sc.Cards[StatusTodo] != 1 {
		t.Fatalf("unexpected p1 counts: total=%d cards=%#v", sc.Total, sc.Cards)
	}
	if sc.Cards[StatusDone] != 0 {
		t.Fatalf("p2 card leaked into p1 counts")
	}
}
