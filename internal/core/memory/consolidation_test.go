package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConsolidationActions(t *testing.T) {
	out := "UPDATE 1: Use Go for runtime code\n" +
		"DELETE 3\n" +
		"ADD: Always run go test ./... before commit\n" +
		"NONE\n" +
		"garbage line\n" +
		"UPDATE 0: bad index\n" +
		"DELETE abc\n"
	actions := parseConsolidationActions(out)
	if len(actions) != 3 {
		t.Fatalf("expected 3 valid actions, got %+v", actions)
	}
	if actions[0].Kind != "update" || actions[0].Index != 1 || actions[0].Text != "Use Go for runtime code" {
		t.Errorf("unexpected update action: %+v", actions[0])
	}
	if actions[1].Kind != "delete" || actions[1].Index != 3 {
		t.Errorf("unexpected delete action: %+v", actions[1])
	}
	if actions[2].Kind != "add" || actions[2].Text != "Always run go test ./... before commit" {
		t.Errorf("unexpected add action: %+v", actions[2])
	}
}

func TestParseConsolidationActionsEmptyAndNone(t *testing.T) {
	if actions := parseConsolidationActions(""); len(actions) != 0 {
		t.Fatalf("empty output should yield no actions, got %+v", actions)
	}
	if actions := parseConsolidationActions("NONE"); len(actions) != 0 {
		t.Fatalf("NONE should yield no actions, got %+v", actions)
	}
	if actions := parseConsolidationActions("  \nNONE\n"); len(actions) != 0 {
		t.Fatalf("whitespace+NONE should yield no actions, got %+v", actions)
	}
}

func TestApplyConsolidationActions(t *testing.T) {
	candidates := []Candidate{
		newCandidate("Use Go", "Use Go for runtime code", "Use Go for runtime code", TypeProject, "extractor"),
		newCandidate("Prefer concise prose", "Prefer concise prose in docs", "Prefer concise prose in docs", TypeWorkflow, "extractor"),
		newCandidate("Add tests", "Add integration tests", "Add integration tests", TypeProject, "extractor"),
	}
	actions := []ConsolidationAction{
		{Kind: "update", Index: 1, Text: "Use Go for all runtime code"},
		{Kind: "delete", Index: 3},
		{Kind: "add", Text: "Run go test ./... before every commit"},
	}
	next, mutations := applyConsolidationActions(candidates, actions)
	if mutations != 3 {
		t.Fatalf("expected 3 mutations, got %d", mutations)
	}
	if len(next) != 3 {
		t.Fatalf("expected 3 candidates after apply (1 update, 1 delete, 1 add), got %d: %+v", len(next), next)
	}
	if next[0].Content != "Use Go for all runtime code" {
		t.Errorf("expected updated content, got %q", next[0].Content)
	}
	if next[0].Fingerprint == candidates[0].Fingerprint {
		t.Errorf("expected updated fingerprint to differ from original")
	}
	if next[1].Content != "Prefer concise prose in docs" {
		t.Errorf("expected second candidate untouched, got %q", next[1].Content)
	}
	if next[2].Content != "Run go test ./... before every commit" {
		t.Errorf("expected added candidate last, got %q", next[2].Content)
	}
}

func TestApplyConsolidationActionsIgnoresInvalidIndexes(t *testing.T) {
	candidates := []Candidate{
		newCandidate("Only", "Only candidate", "Only candidate", TypeProject, "extractor"),
	}
	actions := []ConsolidationAction{
		{Kind: "update", Index: 9, Text: "nope"},
		{Kind: "delete", Index: 0},
		{Kind: "add", Text: "  "}, // blank add ignored
	}
	next, mutations := applyConsolidationActions(candidates, actions)
	if mutations != 0 {
		t.Fatalf("expected 0 mutations, got %d", mutations)
	}
	if len(next) != 1 {
		t.Fatalf("expected original candidate preserved, got %+v", next)
	}
}

func TestConsolidatorMaybeMaintainThreshold(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	calls := 0
	consolidator := NewConsolidator(ConsolidatorOptions{
		Manager: manager,
		OneShot: func(ctx context.Context, prompt, input string) (string, error) {
			calls++
			return "NONE", nil
		},
		AfterN: 3,
		Log:    func(string) {},
	})

	// Below threshold: no model call.
	if err := consolidator.MaybeMaintain(context.Background()); err != nil {
		t.Fatalf("maybe maintain: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no model call below threshold, got %d", calls)
	}

	// Seed 2 candidates (still below 3).
	if err := manager.writeCandidates([]Candidate{
		newCandidate("A", "A fact", "A fact", TypeProject, "extractor"),
		newCandidate("B", "B fact", "B fact", TypeProject, "extractor"),
	}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	if err := consolidator.MaybeMaintain(context.Background()); err != nil {
		t.Fatalf("maybe maintain: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no model call with 2 < 3 candidates, got %d", calls)
	}

	// Seed 3 candidates: triggers.
	if err := manager.writeCandidates([]Candidate{
		newCandidate("A", "A fact", "A fact", TypeProject, "extractor"),
		newCandidate("B", "B fact", "B fact", TypeProject, "extractor"),
		newCandidate("C", "C fact", "C fact", TypeProject, "extractor"),
	}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	if err := consolidator.MaybeMaintain(context.Background()); err != nil {
		t.Fatalf("maybe maintain: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 model call at threshold, got %d", calls)
	}
}

func TestConsolidatorMaintainAppliesActionsToStore(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	if err := manager.writeCandidates([]Candidate{
		newCandidate("Use Go", "Use Go for runtime code", "Use Go for runtime code", TypeProject, "extractor"),
		newCandidate("Prefer concise", "Prefer concise prose", "Prefer concise prose", TypeWorkflow, "extractor"),
		newCandidate("Stale note", "Stale note about removed tool", "Stale note about removed tool", TypeProject, "extractor"),
	}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	consolidator := NewConsolidator(ConsolidatorOptions{
		Manager: manager,
		OneShot: func(ctx context.Context, prompt, input string) (string, error) {
			return "UPDATE 1: Use Go for all runtime code\nDELETE 3\n", nil
		},
		AfterN: 1,
		Log:    func(string) {},
	})
	if err := consolidator.Maintain(context.Background()); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 candidates after update+delete, got %+v", stored)
	}
	if stored[0].Content != "Use Go for all runtime code" {
		t.Errorf("expected updated first candidate, got %q", stored[0].Content)
	}
	for _, c := range stored {
		if strings.Contains(c.Content, "Stale note") {
			t.Errorf("expected stale candidate deleted, got %+v", stored)
		}
	}
}

func TestConsolidatorDegradesOnModelFailure(t *testing.T) {
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	if err := manager.writeCandidates([]Candidate{
		newCandidate("A", "A fact", "A fact", TypeProject, "extractor"),
	}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	calls := 0
	consolidator := NewConsolidator(ConsolidatorOptions{
		Manager: manager,
		OneShot: func(ctx context.Context, prompt, input string) (string, error) {
			calls++
			return "", context.DeadlineExceeded
		},
		AfterN: 1,
		Log:    func(string) {},
	})
	if err := consolidator.Maintain(context.Background()); err != nil {
		t.Fatalf("maintain should swallow model errors: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 model call, got %d", calls)
	}
	// Degraded: subsequent passes are no-ops.
	if err := consolidator.Maintain(context.Background()); err != nil {
		t.Fatalf("degraded maintain: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected no further model calls after degrade, got %d", calls)
	}
	// Candidates untouched.
	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected candidates untouched, got %+v", stored)
	}
}

func TestConsolidatorNoOpWhenUnavailable(t *testing.T) {
	// Nil consolidator / missing oneShot must not panic and must be a no-op.
	var nilConsolidator *Consolidator
	if err := nilConsolidator.MaybeMaintain(context.Background()); err != nil {
		t.Fatalf("nil maybe maintain: %v", err)
	}
	if err := nilConsolidator.Maintain(context.Background()); err != nil {
		t.Fatalf("nil maintain: %v", err)
	}
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	consolidator := NewConsolidator(ConsolidatorOptions{Manager: manager, AfterN: 1, Now: time.Now})
	if err := consolidator.Maintain(context.Background()); err != nil {
		t.Fatalf("missing oneShot maintain should be a no-op: %v", err)
	}
}
