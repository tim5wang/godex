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

func TestCardTemplateIDCRUD(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)

	// Create carries TemplateID through.
	card, err := l.CreateCard(CreateCardInput{
		ProjectID:  project.ID,
		Title:      "template-pinned task",
		TemplateID: "geek",
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if card.TemplateID != "geek" {
		t.Fatalf("expected template_id geek, got %q", card.TemplateID)
	}

	// Update replaces TemplateID.
	tpl := "reviewer"
	if _, err := l.UpdateCard(card.ID, card.Version, "human", UpdateCardInput{TemplateID: &tpl}); err != nil {
		t.Fatalf("update template_id: %v", err)
	}
	got, err := l.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.TemplateID != "reviewer" {
		t.Fatalf("expected template_id reviewer after update, got %q", got.TemplateID)
	}

	// Clearing (empty pointer) is preserved, not treated as "no change".
	clear := ""
	if _, err := l.UpdateCard(got.ID, got.Version, "human", UpdateCardInput{TemplateID: &clear}); err != nil {
		t.Fatalf("clear template_id: %v", err)
	}
	got, err = l.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.TemplateID != "" {
		t.Fatalf("expected empty template_id after clear, got %q", got.TemplateID)
	}
}

func TestCardResearchCRUD(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)

	// Create carries the structured research asset through.
	card, err := l.CreateCard(CreateCardInput{
		ProjectID: project.ID,
		Title:     "planner-verified task",
		Research: &Research{
			Facts:         []string{"  Card 模型在 types.go  ", "Card 模型在 types.go"},
			Locations:     []string{"types.go:141"},
			ExcludedPaths: []string{"/internal/tools/"},
			OpenQuestions: []string{"确认 UpdateCard 写入 research"},
		},
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	if card.Research == nil {
		t.Fatal("expected research set on create")
	}
	// Facts are trimmed and de-duplicated; excluded paths normalized (no slashes).
	if len(card.Research.Facts) != 1 || card.Research.Facts[0] != "Card 模型在 types.go" {
		t.Fatalf("expected 1 deduped fact, got %v", card.Research.Facts)
	}
	if len(card.Research.ExcludedPaths) != 1 || card.Research.ExcludedPaths[0] != "internal/tools" {
		t.Fatalf("expected normalized excluded path, got %v", card.Research.ExcludedPaths)
	}

	// Update replaces research in full.
	replace := &Research{Facts: []string{"v2 事实"}, OpenQuestions: []string{"新开放点"}}
	if _, err := l.UpdateCard(card.ID, card.Version, "human", UpdateCardInput{Research: replace}); err != nil {
		t.Fatalf("update research: %v", err)
	}
	got, err := l.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.Research == nil || len(got.Research.Facts) != 1 || got.Research.Facts[0] != "v2 事实" {
		t.Fatalf("expected replaced research, got %+v", got.Research)
	}
	if len(got.Research.ExcludedPaths) != 0 {
		t.Fatalf("old excluded_paths should be gone after replace, got %v", got.Research.ExcludedPaths)
	}

	// Clearing research (explicit empty) is not treated as "no change".
	empty := &Research{}
	if _, err := l.UpdateCard(got.ID, got.Version, "human", UpdateCardInput{Research: empty}); err != nil {
		t.Fatalf("clear research: %v", err)
	}
	got, err = l.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.Research != nil {
		t.Fatalf("expected nil research after clear, got %+v", got.Research)
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

func TestUpdateCardReplacesChecklistPreservingDone(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{
		ProjectID: project.ID,
		Title:     "checklist edit",
		Checklist: []string{"first", "second"},
	})

	// Mark "first" done with evidence, then edit the checklist: keep "first",
	// rename "second" -> "second renamed", add "third", drop a blank line. The
	// done/evidence for the unchanged "first" must survive.
	if _, err := l.ChecklistCheck(card.ID, card.Version, "human", 0, "evidence-A"); err != nil {
		t.Fatalf("check: %v", err)
	}
	cur, _ := l.GetCard(card.ID)

	edited := []string{"first", "second renamed", " ", "third"}
	if _, err := l.UpdateCard(cur.ID, cur.Version, "human", UpdateCardInput{Checklist: &edited}); err != nil {
		t.Fatalf("update checklist: %v", err)
	}
	got, _ := l.GetCard(cur.ID)
	if len(got.Checklist) != 3 {
		t.Fatalf("expected 3 checklist items, got %d: %+v", len(got.Checklist), got.Checklist)
	}
	if got.Checklist[0].Text != "first" || !got.Checklist[0].Done || got.Checklist[0].Evidence != "evidence-A" {
		t.Fatalf("expected preserved done/evidence on unchanged item, got %+v", got.Checklist[0])
	}
	if got.Checklist[1].Text != "second renamed" || got.Checklist[1].Done {
		t.Fatalf("expected renamed item to reset done, got %+v", got.Checklist[1])
	}
	if got.Checklist[2].Text != "third" {
		t.Fatalf("expected new item third, got %+v", got.Checklist[2])
	}
}

func TestAddCommentAppendsInOrder(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "comment"})

	if _, err := l.AddComment(card.ID, card.Version, "human", "first word"); err != nil {
		t.Fatalf("add comment: %v", err)
	}
	cur, _ := l.GetCard(card.ID)
	if len(cur.Comments) != 1 || cur.Comments[0].Text != "first word" || cur.Comments[0].Author != "human" {
		t.Fatalf("expected one comment, got %+v", cur.Comments)
	}
	if _, err := l.AddComment(cur.ID, cur.Version, "agent", "second word"); err != nil {
		t.Fatalf("add second comment: %v", err)
	}
	got, _ := l.GetCard(cur.ID)
	if len(got.Comments) != 2 || got.Comments[1].Text != "second word" || got.Comments[1].Author != "agent" {
		t.Fatalf("expected appended comment, got %+v", got.Comments)
	}
	if !got.Comments[0].CreatedAt.Before(got.Comments[1].CreatedAt) {
		t.Fatalf("expected chronological order, got %v then %v", got.Comments[0].CreatedAt, got.Comments[1].CreatedAt)
	}
}

func TestSoftDeleteGuardsRunningExecution(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "busy"})

	if _, err := l.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil); err != nil {
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

// A card claimed by an execution holds the hosting/execution session id. The
// same execution session must be able to advance its own held card (this is the
// root cause of the stuck-card bug: agent tools presented a fixed "agent" actor
// that never matched the holder, so the card could never move out of
// in_progress).
func TestHolderCanAdvanceOwnCardBySessionID(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "held"})
	card, _ = l.MoveCard(card.ID, card.Version, StatusTodo, "agent-a")
	card, _ = l.MoveCard(card.ID, card.Version, StatusInProgress, "agent-a")

	// A different actor still cannot steal a held card.
	if _, err := l.MoveCard(card.ID, card.Version, StatusInReview, "agent-b"); !errors.Is(err, ErrCardHeld) {
		t.Fatalf("expected agent-b to be refused on held card, got %v", err)
	}
	// The holder (session id) can advance its own held card.
	moved, err := l.MoveCard(card.ID, card.Version, StatusInReview, "agent-a")
	if err != nil {
		t.Fatalf("holder session advance: %v", err)
	}
	if moved.Status != StatusInReview || moved.Holder != "" {
		t.Fatalf("expected holder cleared on in_review, got %+v", moved)
	}
}

// A human operator is a superuser: they may advance a card even when it is
// held by an execution session that is stuck/abandoned, clearing the holder.
// This lets manual curation recover a card that a dead runtime session can no
// longer release.
func TestHumanCanUnstickHeldCard(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "stuck", StartStatus: StatusTodo})
	card, _ = l.MoveCard(card.ID, card.Version, StatusInProgress, "session-1")
	if card.Holder != "session-1" {
		t.Fatalf("expected holder session-1, got %q", card.Holder)
	}

	// A non-holder agent is still blocked.
	if _, err := l.MoveCard(card.ID, card.Version, StatusInReview, "agent-a"); !errors.Is(err, ErrCardHeld) {
		t.Fatalf("expected non-holder agent blocked, got %v", err)
	}
	// Human advances and clears the holder.
	moved, err := l.MoveCard(card.ID, card.Version, StatusInReview, humanActor)
	if err != nil {
		t.Fatalf("human unstick hold: %v", err)
	}
	if moved.Status != StatusInReview || moved.Holder != "" {
		t.Fatalf("expected human to clear holder, got %+v", moved)
	}
}

// StartExecution now advances the card to in_progress so the ledger can never
// show a "backlog/todo card with a running execution" contradiction — the
// disk-vs-memory inconsistency that made stuck runs look like zombies.
func TestStartExecutionAdvancesCardToInProgress(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "exec"})
	if card.Status != StatusBacklog {
		t.Fatalf("expected default backlog, got %q", card.Status)
	}

	running, err := l.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil)
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if running.Status != StatusInProgress {
		t.Fatalf("expected StartExecution to advance card to in_progress, got %q", running.Status)
	}
	if running.Holder != "session-1" {
		t.Fatalf("expected holder session-1, got %q", running.Holder)
	}
}

// UpdateExecutionObservation writes a live stage/error snapshot onto a running
// execution and persists it (round-trip through a reopen), so the board reflects
// where a run is stalled without opening the conversation.
func TestUpdateExecutionObservationPersists(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "obs"})
	if _, err := l.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}

	updated, err := l.UpdateExecutionObservation(card.ID, "ex-1", ExecutionObservation{
		Stage:     StageWaitingApproval,
		ErrorType: ErrTypeProvider,
		LastError: "LLM provider returned empty responses",
		LastTool:  "edit_file",
	})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	ex := updated.Executions[0]
	if ex.Stage != StageWaitingApproval || ex.ErrorType != ErrTypeProvider || ex.LastTool != "edit_file" {
		t.Fatalf("observation not written: %+v", ex)
	}

	// Round-trip through a reopened ledger (fresh in-memory).
	reopened, err := OpenLedger(l.path, t.TempDir())
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	got, err := reopened.GetCard(card.ID)
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.Executions[0].Stage != StageWaitingApproval || got.Executions[0].LastError != "LLM provider returned empty responses" {
		t.Fatalf("observation not persisted: %+v", got.Executions[0])
	}
}

// FinishExecutionWithObs finalizes a running execution and carries the failure
// detail (error type / message) into the terminal record, so PJM sees "how it
// failed" without opening the session.
func TestFinishExecutionWithObservation(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "fin"})
	if _, err := l.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}

	finished, err := l.FinishExecutionWithObs(card.ID, "ex-1", ExecutionFailed, "provider empty", ExecutionObservation{
		Stage:     StageError,
		ErrorType: ErrTypeProvider,
		LastError: "LLM provider returned empty responses",
	})
	if err != nil {
		t.Fatalf("finish with obs: %v", err)
	}
	ex := finished.Executions[0]
	if ex.Status != ExecutionFailed || ex.ErrorType != ErrTypeProvider || ex.LastError != "LLM provider returned empty responses" {
		t.Fatalf("expected failed execution carrying error detail, got %+v", ex)
	}
	if finished.Holder != "" {
		t.Fatalf("expected holder cleared on finish, got %q", finished.Holder)
	}
}

// A partial observation update never clobbers a prior non-empty field with an
// empty string (e.g. a later idle stage must not wipe a recorded last error).
func TestUpdateExecutionObservationDoesNotClobber(t *testing.T) {
	l := openTestLedger(t)
	project := seedProject(t, l)
	card, _ := l.CreateCard(CreateCardInput{ProjectID: project.ID, Title: "clobber"})
	if _, err := l.StartExecution(card.ID, "ex-1", "session-1", "session-1", nil); err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if _, err := l.UpdateExecutionObservation(card.ID, "ex-1", ExecutionObservation{
		Stage:     StageError,
		ErrorType: ErrTypeProvider,
		LastError: "provider empty",
	}); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	// A later idle observation with an empty error must keep the recorded error.
	updated, err := l.UpdateExecutionObservation(card.ID, "ex-1", ExecutionObservation{Stage: StageIdle})
	if err != nil {
		t.Fatalf("update idle observation: %v", err)
	}
	ex := updated.Executions[0]
	if ex.Stage != StageIdle || ex.ErrorType != ErrTypeProvider || ex.LastError != "provider empty" {
		t.Fatalf("idle observation clobbered error fields: %+v", ex)
	}
}
