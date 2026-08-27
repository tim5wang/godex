package taskboard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskboard", "ledger.json")
	l, err := OpenLedger(path, t.TempDir())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	return l
}

func seedProject(t *testing.T, l *Ledger) Project {
	t.Helper()
	project, err := l.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func TestLedgerPersistsAcrossReopen(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "taskboard", "ledger.json")
	workspace := t.TempDir()
	l, err := OpenLedger(path, workspace)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	project, err := l.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	card, err := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "fix bug", Urgency: UrgencyUrgent, Checklist: []string{"repro", "fix", "test"}})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}

	reopened, err := OpenLedger(path, workspace)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if got.Title != "fix bug" || got.Status != StatusBacklog || got.Version != 1 || len(got.Checklist) != 3 {
		t.Fatalf("unexpected card after reopen: %+v", got)
	}
	if _, err := reopened.GetCard("missing"); !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("expected ErrCardNotFound, got %v", err)
	}
}

func TestCreateCardDefaultsToBuiltInProjectAndNormalizesUrgency(t *testing.T) {
	l := openTestLedger(t)
	projects := l.ListProjects()
	if len(projects) != 1 || !projects[0].BuiltIn {
		t.Fatalf("expected one built-in default project, got %+v", projects)
	}
	card, err := l.CreateCard(CreateCardInput{Title: "quick task", Urgency: "weird"})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if card.ProjectID != projects[0].ID {
		t.Fatalf("expected default project %q, got %q", projects[0].ID, card.ProjectID)
	}
	if card.Urgency != UrgencyNormal {
		t.Fatalf("expected urgency normalized to normal, got %q", card.Urgency)
	}
	if card.Status != StatusBacklog {
		t.Fatalf("expected backlog start, got %q", card.Status)
	}
}

func TestMoveCardLinearFlowAndDoneGate(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "flow"})

	// backlog -> in_progress is a skip: refused.
	if _, err := l.MoveCard(card.ID, card.Version, StatusInProgress, "agent-a"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected skip transition refused, got %v", err)
	}

	card, _ = l.MoveCard(card.ID, card.Version, StatusTodo, "agent-a") // backlog -> todo
	if card.Status != StatusTodo || card.Holder != "" {
		t.Fatalf("unexpected card after todo move: %+v", card)
	}

	// done is human-only, even from a legal predecessor state.
	card, _ = l.MoveCard(card.ID, card.Version, StatusInProgress, "agent-a")
	if _, err := l.MoveCard(card.ID, card.Version, StatusDone, "agent-a"); !errors.Is(err, ErrDoneIsHumanOnly) {
		t.Fatalf("expected done gate, got %v", err)
	}

	// Another actor cannot steal a held card.
	if _, err := l.MoveCard(card.ID, card.Version, StatusInReview, "agent-b"); !errors.Is(err, ErrCardHeld) {
		t.Fatalf("expected held card to refuse agent-b, got %v", err)
	}
	// The holder can advance.
	moved, err := l.MoveCard(card.ID, card.Version, StatusInReview, "agent-a")
	if err != nil {
		t.Fatalf("holder advance: %v", err)
	}
	if moved.Status != StatusInReview || moved.Holder != "" {
		t.Fatalf("expected holder cleared on in_review, got %+v", moved)
	}

	// Human acceptance from in_review.
	done, err := l.CompleteCard(card.ID, moved.Version, "human-1", false)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Status != StatusDone {
		t.Fatalf("expected done, got %q", done.Status)
	}

	// Agent gate holds even from done-adjacent states.
	if _, err := l.MoveCard(card.ID, done.Version, StatusDone, "agent-a"); !errors.Is(err, ErrDoneIsHumanOnly) {
		t.Fatalf("expected done gate on done card too, got %v", err)
	}
}

func TestCompleteCardRequiresChecklistOrForce(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{
		ProjectID:   project.ID,
		Title:       "with dod",
		Checklist:   []string{"criterion-a", "criterion-b"},
		StartStatus: StatusTodo,
	})
	card, _ = l.MoveCard(card.ID, card.Version, StatusInProgress, "agent-a")
	card, _ = l.MoveCard(card.ID, card.Version, StatusInReview, "agent-a")

	if _, err := l.CompleteCard(card.ID, card.Version, "human", false); err == nil {
		t.Fatalf("expected unmet DoD to block acceptance")
	}
	updated, err := l.ChecklistCheck(card.ID, card.Version, "agent-a", 0, "repro confirmed at commit abc")
	if err != nil {
		t.Fatalf("check item: %v", err)
	}
	if _, err := l.CompleteCard(updated.ID, updated.Version, "human", false); err == nil {
		t.Fatalf("expected remaining unchecked item to block acceptance")
	}
	updated, err = l.ChecklistCheck(updated.ID, updated.Version, "agent-a", 1, "unit test green")
	if err != nil {
		t.Fatalf("check item 2: %v", err)
	}
	done, err := l.CompleteCard(updated.ID, updated.Version, "human", false)
	if err != nil {
		t.Fatalf("complete after full checklist: %v", err)
	}
	if done.Status != StatusDone {
		t.Fatalf("expected done, got %q", done.Status)
	}
}

func TestRejectCardBouncesWithReason(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "reject me", StartStatus: StatusTodo})
	card, _ = l.MoveCard(card.ID, card.Version, StatusInProgress, "agent-a")
	card, _ = l.MoveCard(card.ID, card.Version, StatusInReview, "agent-a")

	rejected, err := l.RejectCard(card.ID, card.Version, "human", "wrong approach, see comment")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != StatusTodo {
		t.Fatalf("expected todo after rejection, got %q", rejected.Status)
	}
	if len(rejected.Comments) != 1 || rejected.Comments[0].Author != "human" {
		t.Fatalf("expected reason comment, got %+v", rejected.Comments)
	}
}

func TestVersionConflict(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "v1"})

	newTitle := "v2"
	if _, err := l.UpdateCard(card.ID, card.Version, "human", UpdateCardInput{Title: &newTitle}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := l.UpdateCard(card.ID, card.Version, "human", UpdateCardInput{Title: &newTitle}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected stale version to conflict, got %v", err)
	}
}

func TestSoftDeleteGuardsRunningExecution(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "busy"})

	if _, err := l.StartExecution(card.ID, "ex-1", "session-1", "session-1"); err != nil {
		t.Fatalf("start execution: %v", err)
	}
	running, _ := l.GetCard(card.ID)
	if !running.HasRunningExecution() || running.Holder != "session-1" {
		t.Fatalf("expected running execution + holder, got %+v", running)
	}
	if _, err := l.SoftDeleteCard(card.ID, running.Version, "human"); !errors.Is(err, ErrRunningExecution) {
		t.Fatalf("expected running execution to block delete, got %v", err)
	}

	finished, err := l.FinishExecution(card.ID, "ex-1", ExecutionCompleted, "all done")
	if err != nil {
		t.Fatalf("finish execution: %v", err)
	}
	if finished.Holder != "" || finished.Executions[0].Status != ExecutionCompleted {
		t.Fatalf("expected execution closed and holder cleared, got %+v", finished)
	}
	deleted, err := l.SoftDeleteCard(card.ID, finished.Version, "human")
	if err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("expected card soft-deleted, got %+v", deleted)
	}
	// Hidden from default listing, visible with IncludeDeleted.
	if got := l.ListCards(CardFilter{}); len(got) != 0 {
		t.Fatalf("expected empty default listing, got %+v", got)
	}
	if got := l.ListCards(CardFilter{IncludeDeleted: true}); len(got) != 1 {
		t.Fatalf("expected deleted card in include-deleted listing, got %+v", got)
	}
}

func TestProjectDeleteGuards(t *testing.T) {
	l := openTestLedger(t)
	builtIn := l.ListProjects()[0]
	if err := l.DeleteProject(builtIn.ID); !errors.Is(err, ErrBuiltInProject) {
		t.Fatalf("expected built-in guard, got %v", err)
	}

	project := seedProject(t, l)
	if _, err := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "pin"}); err != nil {
		t.Fatalf("create card: %v", err)
	}
	if err := l.DeleteProject(project.ID); !errors.Is(err, ErrProjectHasCards) {
		t.Fatalf("expected cards guard, got %v", err)
	}
}

func TestListCardsFilterAndUrgencyOrder(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	mk := func(title, urgency string) Card {
		t.Helper()
		card, err := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: title, Urgency: urgency})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return card
	}
	mk("low-1", UrgencyLow)
	mk("urgent-1", UrgencyUrgent)
	mk("normal-1", UrgencyNormal)

	cards := l.ListCards(CardFilter{ProjectID: project.ID})
	if len(cards) != 3 {
		t.Fatalf("expected 3 cards, got %d", len(cards))
	}
	if cards[0].Title != "urgent-1" || cards[2].Title != "low-1" {
		t.Fatalf("expected urgency ordering urgent>normal>low, got %s..%s", cards[0].Title, cards[2].Title)
	}
	if got := l.ListCards(CardFilter{ProjectID: project.ID, Status: StatusBacklog}); len(got) != 3 {
		t.Fatalf("expected 3 backlog cards, got %d", len(got))
	}
	if got := l.ListCards(CardFilter{ProjectID: "nope"}); len(got) != 0 {
		t.Fatalf("expected empty for foreign project, got %d", len(got))
	}
}

func TestLedgerTimestampsAdvanceMonotonicUnderFakeClock(t *testing.T) {
	l := openTestLedger(t)
	base := time.Now().Add(-time.Hour)
	now := base
	l.now = func() time.Time { now = now.Add(time.Minute); return now }
	project := seedProject(t, l)
	first, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "one"})
	second, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "two"})
	if !second.CreatedAt.After(first.CreatedAt) {
		t.Fatalf("expected monotonic timestamps, got %v then %v", first.CreatedAt, second.CreatedAt)
	}
}

func TestDeleteProjectOrphanAndRecreate(t *testing.T) {
	l := openTestLedger(t)
	project, err := l.CreateProject("temp", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := l.DeleteProject(project.ID); err != nil {
		t.Fatalf("delete empty project: %v", err)
	}
	if _, err := l.CreateProject("temp", t.TempDir()); err != nil {
		t.Fatalf("recreate project: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.path)); err != nil {
		t.Fatalf("ledger file missing: %v", err)
	}
}
