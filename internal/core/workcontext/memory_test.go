package workcontext

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func memoryFact(t *testing.T, s *Store, id, title string) Fact {
	t.Helper()
	ctx := context.Background()
	f, err := s.CreateFact(ctx, testScope(), id, FactInput{Title: title, Content: title + " evidence", Kind: "fact", Scope: `{"env":"test"}`, Source: "report://fixture/verified", ObservedAt: "2026-11-12T00:00:00Z", Provenance: "system_query"})
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.ReviewFact(ctx, testScope(), id, f.ID, FactReview{Status: "confirmed", IfVersion: f.Version})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPrimaryDesktopDoesNotFollowUpdates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, s, testScope(), "A")
	b := mustCreate(t, s, testScope(), "B")
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.LinkSession(ctx, testScope(), id, SessionLink{SessionID: "conversation", Topic: "topic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SaveCheckpoint(ctx, testScope(), b.ID, 0, Checkpoint{Summary: "newest B"}); err != nil {
		t.Fatal(err)
	}
	_, id, err := s.SessionBinding(ctx, "conversation")
	if err != nil || id != a.ID {
		t.Fatalf("binding drifted: %s %v", id, err)
	}
	other := Scope{TenantID: "other-tenant", OwnerID: testScope().OwnerID}
	c := mustCreate(t, s, other, "other")
	if _, err = s.LinkSession(ctx, other, c.ID, SessionLink{SessionID: "conversation", Topic: "bad"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-tenant link accepted: %v", err)
	}
	if err = s.UnlinkSession(ctx, testScope(), a.ID, "conversation"); err != nil {
		t.Fatal(err)
	}
	_, id, err = s.SessionBinding(ctx, "conversation")
	if err != nil || id != b.ID {
		t.Fatalf("explicit unlink did not move primary: %s %v", id, err)
	}
}

func TestDreamWatermarkIgnoresReadsAndDerivedWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	fact := memoryFact(t, s, c.ID, "verified fact")
	now := time.Now().UTC()
	before, err := s.MemoryState(ctx, testScope(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Focus(ctx, testScope(), c.ID)
	_, _ = s.Recall(ctx, testScope(), c.ID, "current", "fact")
	_, _ = s.MarkSeen(ctx, testScope(), c.ID)
	_, _ = s.RecordEventIfNew(ctx, testScope(), c.ID, SessionTurnEventKind, "done", "session://a", "turn:1")
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if after.SourceSeq != before.SourceSeq {
		t.Fatal("reading or completing a conversation dirtied memory")
	}
	input, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil || input == nil {
		t.Fatalf("begin: %v", err)
	}
	second, state, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil || second != nil || state.RunID != input.RunID {
		t.Fatal("concurrent Dream claimed")
	}
	result := DreamResult{Summary: "derived, scoped digest", Candidates: []DreamCandidate{{Title: "reusable proposal", Content: "only in test env", SourceFactIDs: []string{fact.ID}}}}
	if err = s.FinishDream(ctx, testScope(), *input, result, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	done, _ := s.MemoryState(ctx, testScope(), c.ID)
	if done.SourceSeq != before.SourceSeq || done.ProcessedSeq != before.SourceSeq {
		t.Fatalf("Dream self-triggered: %+v", done)
	}
	again, _, err := s.BeginDream(ctx, testScope(), c.ID, false, true, now.Add(24*time.Hour))
	if err != nil || again != nil {
		t.Fatalf("unchanged input reran: %v", err)
	}
	detail, _ := s.Get(ctx, testScope(), c.ID)
	var candidates int
	for _, f := range detail.Facts {
		if f.Title == "reusable proposal" {
			candidates++
			if f.Status != "candidate" || f.Scope != fact.Scope {
				t.Fatal("Dream strengthened or widened a proposal")
			}
		}
	}
	if candidates != 1 {
		t.Fatalf("derived candidate missing: %d", candidates)
	}
}

func TestDreamPreservesConcurrentSourceChanges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	memoryFact(t, s, c.ID, "first")
	now := time.Now().UTC()
	input, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil {
		t.Fatal(err)
	}
	memoryFact(t, s, c.ID, "arrived during Dream")
	if err = s.FinishDream(ctx, testScope(), *input, DreamResult{Summary: "snapshot digest"}, nil, now); err != nil {
		t.Fatal(err)
	}
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	if state.ProcessedSeq != input.ThroughSeq || state.SourceSeq <= state.ProcessedSeq {
		t.Fatalf("new input lost: %+v", state)
	}
	next, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil || next == nil {
		t.Fatal("pending input was cleared")
	}
	_ = s.FinishDream(ctx, testScope(), *next, DreamResult{}, nil, now)
}

func TestDreamRejectsUnknownEvidenceAndRetriesAfterFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	memoryFact(t, s, c.ID, "local")
	now := time.Now().UTC()
	input, _, _ := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	err := s.FinishDream(ctx, testScope(), *input, DreamResult{Candidates: []DreamCandidate{{Title: "forged", Content: "bad", SourceFactIDs: []string{"other-desktop-id"}}}}, nil, now)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged evidence accepted: %v", err)
	}
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	if state.ProcessedSeq != 0 || state.Status != "failed" {
		t.Fatalf("failed run consumed input: %+v", state)
	}
	targets, err := s.DueDreams(ctx, now.Add(time.Minute))
	if err != nil || len(targets) != 0 {
		t.Fatal("retry ignored backoff")
	}
	input, _, err = s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil || input == nil {
		t.Fatal("manual retry blocked")
	}
	_ = s.FinishDream(ctx, testScope(), *input, DreamResult{}, nil, now)
}

func TestDreamFullRescanIsBoundedAndComplete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	for i := 0; i < 20; i++ {
		memoryFact(t, s, c.ID, fmt.Sprintf("fact %d", i))
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	runs := 0
	force := true
	for {
		input, _, err := s.BeginDream(ctx, testScope(), c.ID, force, false, now)
		force = false
		if err != nil {
			t.Fatal(err)
		}
		if input == nil {
			break
		}
		runs++
		if len(input.Changes) > dreamBatchSize {
			t.Fatal("unbounded source snapshot")
		}
		for _, f := range input.Facts {
			seen[f.ID] = true
		}
		if err = s.FinishDream(ctx, testScope(), *input, DreamResult{}, nil, now); err != nil {
			t.Fatal(err)
		}
		if runs > 10 {
			t.Fatal("rescan loop")
		}
	}
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	if len(seen) != 20 || runs < 2 || state.ProcessedSeq != state.SourceSeq || state.RescanCursor >= 0 {
		t.Fatalf("incomplete full scan: seen=%d runs=%d state=%+v", len(seen), runs, state)
	}
	forced, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil || forced == nil {
		t.Fatal("manual full scan of unchanged memory was skipped")
	}
	_ = s.FinishDream(ctx, testScope(), *forced, DreamResult{}, nil, now)
}

func TestDreamRollbackPreservesEvidenceAndRefusesConcurrentEdits(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			c := mustCreate(t, s, testScope(), "desktop")
			f := memoryFact(t, s, c.ID, "source")
			now := time.Now().UTC()
			in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.FinishDream(ctx, testScope(), *in, DreamResult{Summary: "derived digest", Candidates: []DreamCandidate{{Title: "suggestion", Content: "scoped method", SourceFactIDs: []string{f.ID}}}}, nil, now); err != nil {
				t.Fatal(err)
			}
			if changed {
				memoryFact(t, s, c.ID, "new human evidence")
			}
			state, err := s.RevertDream(ctx, testScope(), c.ID, in.RunID)
			if changed {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("rollback undid concurrent evidence: %v", err)
				}
				return
			}
			if err != nil || state.Summary != "" || state.SourceSeq != state.ProcessedSeq {
				t.Fatalf("rollback: %+v %v", state, err)
			}
			detail, _ := s.Get(ctx, testScope(), c.ID)
			for _, fact := range detail.Facts {
				if fact.ID == f.ID && fact.Status != "confirmed" {
					t.Fatal("source evidence changed")
				}
				if fact.Title == "suggestion" && fact.Status != "rejected" {
					t.Fatal("generated candidate survived rollback")
				}
			}
			runs, _ := s.DreamRuns(ctx, testScope(), c.ID)
			if len(runs) != 1 || runs[0]["status"] != "reverted" {
				t.Fatalf("history: %+v", runs)
			}
		})
	}
}

func TestDreamExpiredLeaseRecoveredAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dream.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	memoryFact(t, s, c.ID, "fact")
	now := time.Now().UTC()
	old, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replacement, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now.Add(3*time.Minute))
	if err != nil || replacement == nil || replacement.RunID == old.RunID {
		t.Fatalf("lease unrecoverable: %+v %v", replacement, err)
	}
	if err = s.FinishDream(ctx, testScope(), *old, DreamResult{Summary: "late stale result"}, nil, now.Add(3*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("late result applied: %v", err)
	}
	if err = s.FinishDream(ctx, testScope(), *replacement, DreamResult{Summary: "recovered"}, nil, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.DreamRuns(ctx, testScope(), c.ID)
	if len(runs) != 2 || runs[1]["status"] != "failed" {
		t.Fatalf("expired history stuck running: %+v", runs)
	}
}

func TestDreamSchedulingAndValidityRespectOwnerAndEarliestDeadline(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	now := time.Now().UTC()
	f1 := memoryFact(t, s, c.ID, "source one")
	f2 := memoryFact(t, s, c.ID, "source two")
	later, earlier := now.Add(2*time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano)
	var err error
	f1, err = s.ReviewFact(ctx, testScope(), c.ID, f1.ID, FactReview{Status: "confirmed", IfVersion: f1.Version, ValidUntil: &later})
	if err != nil {
		t.Fatal(err)
	}
	f2, err = s.ReviewFact(ctx, testScope(), c.ID, f2.ID, FactReview{Status: "confirmed", IfVersion: f2.Version, ValidUntil: &earlier})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfigureDream(ctx, testScope(), c.ID, false, 3600, now); err != nil {
		t.Fatal(err)
	}
	targets, err := s.DueDreams(ctx, now.Add(24*time.Hour))
	if err != nil || len(targets) != 0 {
		t.Fatal("disabled schedule ran", err)
	}
	other := Scope{TenantID: testScope().TenantID, OwnerID: "other"}
	if _, _, err = s.BeginDream(ctx, other, c.ID, true, false, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner organized: %v", err)
	}
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil || in == nil {
		t.Fatal("manual disabled by schedule", err)
	}
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{Candidates: []DreamCandidate{{Title: "derived", Content: "do not extend source lifetime", SourceFactIDs: []string{f1.ID, f2.ID}}}}, nil, now); err != nil {
		t.Fatal(err)
	}
	detail, _ := s.Get(ctx, testScope(), c.ID)
	for _, f := range detail.Facts {
		if f.Title == "derived" && f.ValidUntil != earlier {
			t.Fatalf("candidate outlives earliest evidence: %+v", f)
		}
	}
	if err = s.ExpireFacts(ctx, now.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	expired, _ := s.Fact(ctx, testScope(), c.ID, f2.ID)
	if expired.Status != "stale" || expired.Current(now.Add(90*time.Minute)) {
		t.Fatal("expired evidence stayed current")
	}
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	_ = s.ExpireFacts(ctx, now.Add(90*time.Minute))
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if before.SourceSeq != after.SourceSeq {
		t.Fatal("expiry re-dirtied unchanged evidence")
	}
}

func TestDreamSemanticSourcesIncludePlanResourcesAndLinks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	resource, err := s.AddResource(ctx, testScope(), c.ID, ResourceInput{Title: "runbook", Kind: "document", Locator: "https://example.test/runbook.md"})
	if err != nil {
		t.Fatal(err)
	}
	todo, err := s.CreateTodo(ctx, testScope(), c.ID, TodoInput{Text: "validate source", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.LinkSession(ctx, testScope(), c.ID, SessionLink{SessionID: "chat", Topic: "work"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, r := range in.Records {
		if r.Value != "" {
			kinds[r.Kind] = true
		}
	}
	if !kinds["todo"] || !kinds["resource"] || !kinds["session"] {
		t.Fatalf("missing semantic input: %+v", in.Records)
	}
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); err != nil {
		t.Fatal(err)
	}
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	_, err = s.AddResource(ctx, testScope(), c.ID, ResourceInput{ID: resource.ID, Title: "updated runbook", Kind: "document", Locator: "https://example.test/runbook.md"})
	if err != nil {
		t.Fatal(err)
	}
	done := "done"
	_, err = s.PatchTodo(ctx, testScope(), c.ID, todo.ID, TodoPatch{Status: &done, IfVersion: todo.Version})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if after.SourceSeq <= before.SourceSeq {
		t.Fatal("resource/plan edit did not dirty memory")
	}
}

func TestDreamIdenticalCheckpointDoesNotDirtySource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	cp := Checkpoint{Summary: "same progress", NextActions: []string{"validate"}}
	saved, err := s.SaveCheckpoint(ctx, testScope(), c.ID, c.Version, cp)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	if _, err = s.SaveCheckpoint(ctx, testScope(), c.ID, saved.Version, cp); err != nil {
		t.Fatal(err)
	}
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if before.SourceSeq != after.SourceSeq {
		t.Fatalf("checkpoint metadata dirtied unchanged content: %+v -> %+v", before, after)
	}
}

func TestDreamIncrementIncludesRelevantExistingEvidenceAndRequiresOriginalReview(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	old := memoryFact(t, s, c.ID, "Vision OCR old")
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); err != nil {
		t.Fatal(err)
	}
	fresh := memoryFact(t, s, c.ID, "Vision OCR current")
	in, _, err = s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range in.Facts {
		if f.ID == old.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("increment could not compare prior relevant evidence")
	}
	result := DreamResult{Candidates: []DreamCandidate{{Title: "OCR proposal", Content: "only a proposal", SourceFactIDs: []string{old.ID, fresh.ID}}}}
	if err = s.FinishDream(ctx, testScope(), *in, result, nil, now); err != nil {
		t.Fatal(err)
	}
	detail, _ := s.Get(ctx, testScope(), c.ID)
	var candidate Fact
	for _, f := range detail.Facts {
		if f.Title == "OCR proposal" {
			candidate = f
		}
	}
	if candidate.ID == "" {
		t.Fatal("candidate missing")
	}
	if _, err = s.ReviewFact(ctx, testScope(), c.ID, candidate.ID, FactReview{Status: "confirmed", IfVersion: candidate.Version}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Dream summary used as verification source: %v", err)
	}
	source := "report://human/rechecked"
	observed := now.Format(time.RFC3339)
	confirmed, err := s.ReviewFact(ctx, testScope(), c.ID, candidate.ID, FactReview{Status: "confirmed", IfVersion: candidate.Version, Source: &source, ObservedAt: &observed})
	if err != nil || confirmed.Source != source {
		t.Fatalf("reviewed original evidence rejected: %+v %v", confirmed, err)
	}
}
