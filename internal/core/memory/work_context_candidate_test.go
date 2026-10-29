package memory

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newProposalTestExtractor(t *testing.T) (*Manager, *Extractor) {
	t.Helper()
	manager := NewManager(filepath.Join(t.TempDir(), "memory"))
	return manager, NewExtractor(manager, t.TempDir())
}

func validProposal() Candidate {
	return Candidate{
		Title:   "Prefer table-driven tests",
		Summary: "The user prefers table-driven Go tests for new packages.",
		Content: "When adding a new Go package, prefer table-driven tests with subtests.",
		Type:    TypeWorkflow,
		Source:  "work-context",
	}
}

func TestProposeCandidateValidationRejectsBadInput(t *testing.T) {
	manager, extractor := newProposalTestExtractor(t)

	cases := []struct {
		name      string
		mutate    func(*Candidate)
		wantInErr string
	}{
		{"missing title", func(c *Candidate) { c.Title = "   " }, "title"},
		{"missing content", func(c *Candidate) { c.Content = "" }, "content"},
		{"missing source", func(c *Candidate) { c.Source = " " }, "source"},
		{"unsupported type", func(c *Candidate) { c.Type = Type("bogus") }, "type"},
		{"empty type", func(c *Candidate) { c.Type = "" }, "type"},
		{"title too long", func(c *Candidate) {
			c.Title = strings.Repeat("t", maxProposedCandidateTitleRunes+1)
		}, "title"},
		{"content too long", func(c *Candidate) {
			c.Content = strings.Repeat("c", maxProposedCandidateContentRunes+1)
		}, "content"},
		{"summary too long", func(c *Candidate) {
			c.Summary = strings.Repeat("s", maxProposedCandidateSummaryRunes+1)
		}, "summary"},
		{"source too long", func(c *Candidate) {
			c.Source = strings.Repeat("s", maxProposedCandidateSourceRunes+1)
		}, "source"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := validProposal()
			tc.mutate(&candidate)

			added, err := extractor.ProposeCandidate(candidate)
			if err == nil {
				t.Fatalf("expected validation error, got added=%+v", added)
			}
			if !errors.Is(err, ErrInvalidCandidate) {
				t.Fatalf("expected ErrInvalidCandidate, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("expected error to mention %q, got %v", tc.wantInErr, err)
			}
			if len(added) != 0 {
				t.Fatalf("expected no candidates added, got %+v", added)
			}
		})
	}

	// Nothing invalid may have reached the pending inbox.
	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("expected empty candidate inbox, got %+v", stored)
	}
	// And certainly nothing durable.
	entries, err := manager.List()
	if err != nil {
		t.Fatalf("list memory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no durable memory written, got %+v", entries)
	}
}

func TestProposeCandidateFillsTimestampsAndFingerprint(t *testing.T) {
	manager, extractor := newProposalTestExtractor(t)

	candidate := validProposal()
	candidate.Title = "  Prefer   table-driven tests  "
	candidate.Summary = "  The user prefers table-driven Go tests.  "
	candidate.Type = TypeWorkflow
	explicit := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("UTC+8", 8*3600))
	candidate.CreatedAt = explicit

	added, err := extractor.ProposeCandidate(candidate)
	if err != nil {
		t.Fatalf("propose candidate: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added candidate, got %+v", added)
	}

	got := added[0]
	if got.Fingerprint == "" {
		t.Fatalf("expected fingerprint to be filled, got %+v", got)
	}
	if len(got.Fingerprint) != 40 {
		t.Fatalf("expected sha1 fingerprint, got %q", got.Fingerprint)
	}
	if !got.CreatedAt.Equal(explicit) {
		t.Fatalf("expected preserved created_at %v, got %v", explicit, got.CreatedAt)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatalf("expected created_at normalized to UTC, got %v", got.CreatedAt.Location())
	}
	if got.Title != "Prefer table-driven tests" {
		t.Fatalf("expected normalized title, got %q", got.Title)
	}
	if got.Summary != "The user prefers table-driven Go tests." {
		t.Fatalf("expected normalized summary, got %q", got.Summary)
	}
	if got.Source != "work-context" || got.Type != TypeWorkflow {
		t.Fatalf("expected type/source preserved, got %+v", got)
	}

	// A proposal without a timestamp still gets one filled in.
	second := validProposal()
	second.Title = "Prefer short commits"
	addedSecond, err := extractor.ProposeCandidate(second)
	if err != nil {
		t.Fatalf("propose second candidate: %v", err)
	}
	if len(addedSecond) != 1 || addedSecond[0].CreatedAt.IsZero() {
		t.Fatalf("expected filled created_at, got %+v", addedSecond)
	}

	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 pending candidates, got %+v", stored)
	}
	entries, err := manager.List()
	if err != nil {
		t.Fatalf("list memory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("proposal must not write durable memory, got %+v", entries)
	}
}

func TestProposeCandidateRetryIsStableAndDeduped(t *testing.T) {
	manager, extractor := newProposalTestExtractor(t)
	candidate := validProposal()

	first, err := extractor.ProposeCandidate(candidate)
	if err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 added candidate, got %+v", first)
	}

	// Exact retry: same stable fingerprint, nothing added twice.
	retry, err := extractor.ProposeCandidate(candidate)
	if err != nil {
		t.Fatalf("retry proposal: %v", err)
	}
	if len(retry) != 0 {
		t.Fatalf("expected retry to be deduped, got %+v", retry)
	}

	// Retry with a fresh timestamp must still collapse onto the same entry.
	restamped := candidate
	restamped.CreatedAt = time.Now().UTC().Add(time.Hour)
	restampedAgain, err := extractor.ProposeCandidate(restamped)
	if err != nil {
		t.Fatalf("restamped proposal: %v", err)
	}
	if len(restampedAgain) != 0 {
		t.Fatalf("expected restamped retry to be deduped, got %+v", restampedAgain)
	}

	// Whitespace churn in title still maps to the same fingerprint.
	churned := candidate
	churned.Title = "  prefer   table-driven   tests "
	if churned.Title == candidate.Title {
		t.Fatal("expected churned title to differ before normalization")
	}
	churnedAdded, err := extractor.ProposeCandidate(churned)
	if err != nil {
		t.Fatalf("churned proposal: %v", err)
	}
	if len(churnedAdded) != 0 {
		t.Fatalf("expected whitespace churn to be deduped, got %+v", churnedAdded)
	}

	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected a single deduped candidate, got %+v", stored)
	}
	if stored[0].Fingerprint != first[0].Fingerprint {
		t.Fatalf("expected stable fingerprint %q, got %q", first[0].Fingerprint, stored[0].Fingerprint)
	}
}

func TestProposeCandidateSkipsSemanticDuplicateOfPendingCandidate(t *testing.T) {
	_, extractor := newProposalTestExtractor(t)

	original := validProposal()
	if _, err := extractor.ProposeCandidate(original); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	// Same title+summary (same semantic key) but different content ->
	// different fingerprint, therefore only the semantic-key dedupe can stop it.
	restated := original
	restated.Content = "Prefer table-driven tests with subtests in new Go packages."
	added, err := extractor.ProposeCandidate(restated)
	if err != nil {
		t.Fatalf("restated proposal: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("expected semantic duplicate to be suppressed, got %+v", added)
	}
}

func TestProposeCandidateSkipsAlreadyDurableMemory(t *testing.T) {
	manager, extractor := newProposalTestExtractor(t)

	candidate := validProposal()
	if _, err := manager.Remember(SaveInput{
		Title:   candidate.Title,
		Summary: candidate.Summary,
		Content: candidate.Content,
		Type:    candidate.Type,
		Source:  candidate.Source,
	}); err != nil {
		t.Fatalf("seed durable memory: %v", err)
	}

	added, err := extractor.ProposeCandidate(candidate)
	if err != nil {
		t.Fatalf("propose candidate: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("expected candidate already durable to be skipped, got %+v", added)
	}
}

func TestProposeCandidateRespectsSuppression(t *testing.T) {
	manager, extractor := newProposalTestExtractor(t)
	candidate := validProposal()

	first, err := extractor.ProposeCandidate(candidate)
	if err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 added candidate, got %+v", first)
	}

	if _, err := manager.DismissCandidate(first[0].Fingerprint); err != nil {
		t.Fatalf("dismiss candidate: %v", err)
	}

	// Suppression must survive a retry of the exact same proposal...
	again, err := extractor.ProposeCandidate(candidate)
	if err != nil {
		t.Fatalf("suppressed proposal: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected suppressed proposal to be ignored, got %+v", again)
	}

	// ...and of a restatement sharing the semantic key (same title+summary,
	// different content), since suppression is recorded both by fingerprint and
	// by semantic key.
	restated := candidate
	restated.Content = "Prefer table-driven tests with subtests in new Go packages."
	restatedAgain, err := extractor.ProposeCandidate(restated)
	if err != nil {
		t.Fatalf("suppressed restatement: %v", err)
	}
	if len(restatedAgain) != 0 {
		t.Fatalf("expected suppressed restatement to be ignored, got %+v", restatedAgain)
	}
}

func TestProposeCandidateAcceptPromotesThroughExistingFlow(t *testing.T) {
	manager, extractor := newProposalTestExtractor(t)

	added, err := extractor.ProposeCandidate(validProposal())
	if err != nil {
		t.Fatalf("propose candidate: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 added candidate, got %+v", added)
	}

	entry, err := manager.AcceptCandidate(added[0].Fingerprint)
	if err != nil {
		t.Fatalf("accept candidate: %v", err)
	}
	if entry.Title != added[0].Title || entry.Type != added[0].Type {
		t.Fatalf("expected accepted entry to match candidate, got %+v", entry)
	}

	entries, err := manager.List()
	if err != nil {
		t.Fatalf("list memory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 durable memory entry, got %+v", entries)
	}

	// Promotion drains the inbox, and re-proposing now hits the durable dedupe.
	stored, err := manager.ListCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("expected empty inbox after accept, got %+v", stored)
	}

	again, err := extractor.ProposeCandidate(validProposal())
	if err != nil {
		t.Fatalf("re-propose after accept: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected accepted proposal to stay deduped, got %+v", again)
	}
}
