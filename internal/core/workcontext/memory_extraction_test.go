package workcontext

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func extractionFixture(t *testing.T, s *Store, id, turn string) ExtractionJob {
	t.Helper()
	job := ExtractionJob{ContextID: id, SessionID: "session", TurnID: turn, Sources: []ExtractionSource{{ID: "user", Kind: "user_statement", Text: "Keep the scoped node's validated OCR method", SHA256: strings.Repeat("a", 64)}}}
	if _, err := s.EnqueueMemoryExtraction(context.Background(), testScope(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimMemoryExtraction(context.Background(), testScope(), id, time.Now())
	if err != nil || claimed == nil {
		t.Fatal(claimed, err)
	}
	return *claimed
}
func TestMemoryObservationConcurrentIdempotencyAndNoRevival(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "idempotent")
	input := FactInput{Title: "Vision", Content: "Use Vision OCR on this node.", Kind: "method", Scope: `{"node":"fixture"}`, Source: "session://a"}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, _, err := s.CreateObservation(ctx, testScope(), c.ID, input)
			if err != nil {
				errs <- err
			} else {
				ids <- f.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatal("concurrent duplicate observations", unique)
	}
	facts, _ := s.Facts(ctx, testScope(), c.ID)
	f := facts[0]
	if _, err := s.ReviewFact(ctx, testScope(), c.ID, f.ID, FactReview{Status: "archived", IfVersion: f.Version}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	input.Title = "Equivalent heading"
	input.Source = "session://b"
	again, added, err := s.CreateObservation(ctx, testScope(), c.ID, input)
	if err != nil || added || again.ID != f.ID || again.Status != "archived" {
		t.Fatal("replay revived memory", again, added, err)
	}
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if after.SourceSeq != before.SourceSeq {
		t.Fatal("duplicate write dirtied Dream")
	}
}
func TestMemoryExtractionEmptyResultWatermarkSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "empty")
	job := extractionFixture(t, s, c.ID, "one")
	if err = s.FinishMemoryExtraction(ctx, testScope(), job, ExtractionResult{}, nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job.TurnID = "two"
	job.Sources[0].ID = "different-user-id"
	queued, err := s.EnqueueMemoryExtraction(ctx, testScope(), job)
	if err != nil || queued {
		t.Fatal("unchanged source requeued after empty result/restart", queued, err)
	}
	next, err := s.ClaimMemoryExtraction(ctx, testScope(), c.ID, time.Now())
	if err != nil || next != nil {
		t.Fatal(next, err)
	}
	history, _ := s.ExtractionHistory(ctx, testScope(), c.ID)
	if len(history) != 1 || history[0]["status"] != "completed" {
		t.Fatal(history)
	}
}
func TestMemoryExtractionSourcesAtomicityAndNoPromotion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "atomic")
	job := extractionFixture(t, s, c.ID, "one")
	result := ExtractionResult{Observations: []ExtractedObservation{{Title: "OCR", Content: "Use Vision on the fixture node", Kind: "method", SourceIDs: []string{"user"}}, {Title: "bad", Content: "invented source", Kind: "fact", SourceIDs: []string{"forged"}}}}
	if err := s.FinishMemoryExtraction(ctx, testScope(), job, result, nil); err == nil {
		t.Fatal("unknown source accepted")
	}
	facts, _ := s.Facts(ctx, testScope(), c.ID)
	if len(facts) != 0 {
		t.Fatal("partial batch escaped rollback")
	}
	result.Observations = result.Observations[:1]
	if err := s.FinishMemoryExtraction(ctx, testScope(), job, result, nil); err != nil {
		t.Fatal(err)
	}
	facts, _ = s.Facts(ctx, testScope(), c.ID)
	if len(facts) != 1 || facts[0].Status != "observed" || facts[0].Provenance != "derived" || facts[0].VerifiedAt != "" || facts[0].Current(time.Now()) {
		t.Fatal("extraction promoted an observation", facts)
	}
	if !strings.Contains(facts[0].SourceExcerpt, "user_statement") || !strings.Contains(facts[0].Scope, c.ID) {
		t.Fatal("lost source or applicability", facts)
	}
	other := Scope{TenantID: testScope().TenantID, OwnerID: "other"}
	if _, err := s.ExtractionHistory(ctx, other, c.ID); err == nil {
		t.Fatal("extraction history crossed owners")
	}
	if err := s.FinishMemoryExtraction(ctx, testScope(), job, result, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("completed extraction replayed", err)
	}
}
func TestMemoryExtractionLeaseAndRetryBackoff(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "lease")
	job := ExtractionJob{ContextID: c.ID, SessionID: "s", TurnID: "t", Sources: []ExtractionSource{{ID: "source", Kind: "user_statement", Text: "observation", SHA256: strings.Repeat("b", 64)}}}
	if _, err := s.EnqueueMemoryExtraction(ctx, testScope(), job); err != nil {
		t.Fatal(err)
	}
	old, err := s.ClaimMemoryExtraction(ctx, testScope(), c.ID, time.Now().Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := s.ClaimMemoryExtraction(ctx, testScope(), c.ID, time.Now())
	if err != nil || renewed == nil || renewed.Attempt != 2 {
		t.Fatal(renewed, err)
	}
	if err = s.FinishMemoryExtraction(ctx, testScope(), *old, ExtractionResult{}, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lease applied", err)
	}
	if err = s.FinishMemoryExtraction(ctx, testScope(), *renewed, ExtractionResult{}, fmt.Errorf("provider unavailable")); err != nil {
		t.Fatal(err)
	}
	if next, _ := s.ClaimMemoryExtraction(ctx, testScope(), c.ID, time.Now()); next != nil {
		t.Fatal("failure triggered a model retry loop")
	}
	next, err := s.ClaimMemoryExtraction(ctx, testScope(), c.ID, time.Now().Add(2*time.Minute))
	if err != nil || next == nil || next.Attempt != 3 {
		t.Fatal(next, err)
	}
	if err = s.FinishMemoryExtraction(ctx, testScope(), *next, ExtractionResult{}, fmt.Errorf("still unavailable")); err != nil {
		t.Fatal(err)
	}
	if fourth, _ := s.ClaimMemoryExtraction(ctx, testScope(), c.ID, time.Now().Add(time.Hour)); fourth != nil {
		t.Fatal("retry budget exceeded")
	}
}

func TestMemoryObservationDedupPreservesSignificantWhitespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "literal")
	input := FactInput{Title: "literal", Content: `The expected string is "a  b".`, Kind: "fact", Scope: `{"env":"test"}`}
	a, _, err := s.CreateObservation(ctx, testScope(), c.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Content = `The expected string is "a b".`
	b, added, err := s.CreateObservation(ctx, testScope(), c.ID, input)
	if err != nil || !added || a.ID == b.ID {
		t.Fatal("different code/string literals were merged", a, b, err)
	}
}
