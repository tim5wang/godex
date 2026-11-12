package workcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDreamOrganizationReducesReviewWorkAndCanRevert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	makeFact := func(title, content, source, until string) Fact {
		t.Helper()
		f, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: title, Content: content, Kind: "method", Scope: `{"env":"test"}`, Source: source, ObservedAt: "2026-11-11T00:00:00Z", ValidUntil: until})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	a := makeFact("first wording", "Use Vision on this macOS node.", "report://one", "")
	b := makeFact("second wording", a.Content, "report://two", "")
	old := makeFact("old Dream suggestion", "derived suggestion", "dream://old-run", "")
	expired := makeFact("expired", "old observation", "report://old", "2026-11-11T01:00:00Z")
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	result := DreamResult{Summary: "organized", Duplicates: [][]string{{a.ID, b.ID}}, Candidates: []DreamCandidate{{Title: "reference only", Content: "Keep evidence scoped", SourceFactIDs: []string{a.ID}}}}
	if err = s.FinishDream(ctx, testScope(), *in, result, nil, now); err != nil {
		t.Fatal(err)
	}
	facts, _ := s.Facts(ctx, testScope(), c.ID)
	pending := 0
	for _, f := range facts {
		if f.Title == "reference only" {
			t.Fatal("organization created more review work")
		}
		if f.Status == "candidate" || f.Status == "observed" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("duplicate/expired/legacy suggestions were not reduced: pending=%d", pending)
	}
	for _, id := range []string{old.ID, expired.ID} {
		f, _ := s.Fact(ctx, testScope(), c.ID, id)
		if f.Status != "archived" {
			t.Fatalf("old observation not archived: %+v", f)
		}
	}
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	if state.SourceSeq != before.SourceSeq {
		t.Fatal("organization dirtied itself")
	}
	runs, _ := s.DreamRuns(ctx, testScope(), c.ID)
	raw, _ := json.Marshal(runs[0]["result"])
	if !strings.Contains(string(raw), `"references"`) || !strings.Contains(string(raw), `"mutations"`) {
		t.Fatalf("missing references/audit: %s", raw)
	}
	if _, err = s.RevertDream(ctx, testScope(), c.ID, in.RunID); err != nil {
		t.Fatal(err)
	}
	for _, original := range []Fact{a, b, old, expired} {
		f, _ := s.Fact(ctx, testScope(), c.ID, original.ID)
		if f.Status != original.Status || f.Content != original.Content || f.Source != original.Source {
			t.Fatalf("rollback lost original evidence: %+v", f)
		}
	}
}

func TestDreamConflictsBecomeOneVersionedDecision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	a := memoryFact(t, s, c.ID, "use Vision")
	b := memoryFact(t, s, c.ID, "use tesseract")
	d := memoryFact(t, s, c.ID, "OCR unavailable")
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	result := DreamResult{Conflicts: [][]string{{a.ID, b.ID}, {b.ID, d.ID}}, Duplicates: [][]string{{a.ID, b.ID}}}
	if err = s.FinishDream(ctx, testScope(), *in, result, nil, now); err != nil {
		t.Fatal(err)
	}
	issues, err := s.MemoryIssues(ctx, testScope(), c.ID)
	if err != nil || len(issues) != 1 || len(issues[0].Facts) != 3 {
		t.Fatalf("conflicts did not coalesce: %+v %v", issues, err)
	}
	versions := map[string]int{}
	for _, f := range issues[0].Facts {
		if f.Status != "stale" {
			t.Fatal("competing evidence still current")
		}
		versions[f.ID] = f.Version
	}
	other := Scope{TenantID: testScope().TenantID, OwnerID: "other"}
	if err = s.ResolveMemoryIssue(ctx, other, c.ID, issues[0].ID, ResolveMemoryIssue{KeepID: a.ID, Versions: versions}); err == nil {
		t.Fatal("foreign owner resolved issue")
	}
	wrong := map[string]int{}
	for id, v := range versions {
		wrong[id] = v
	}
	wrong[a.ID]--
	if err = s.ResolveMemoryIssue(ctx, testScope(), c.ID, issues[0].ID, ResolveMemoryIssue{KeepID: a.ID, Versions: wrong}); err == nil {
		t.Fatal("stale decision applied")
	}
	if err = s.ResolveMemoryIssue(ctx, testScope(), c.ID, issues[0].ID, ResolveMemoryIssue{KeepID: a.ID, Versions: versions}); err != nil {
		t.Fatal(err)
	}
	issues, err = s.MemoryIssues(ctx, testScope(), c.ID)
	if err != nil || len(issues) != 0 {
		t.Fatalf("resolved issue remains: %+v %v", issues, err)
	}
	kept, _ := s.Fact(ctx, testScope(), c.ID, a.ID)
	if kept.Status != "confirmed" {
		t.Fatal("original verified status lost after human resolution")
	}
	for _, id := range []string{b.ID, d.ID} {
		f, _ := s.Fact(ctx, testScope(), c.ID, id)
		if f.Status != "archived" {
			t.Fatal("other claims not archived")
		}
	}
	if _, err = s.RevertDream(ctx, testScope(), c.ID, in.RunID); err == nil {
		t.Fatal("rollback overwrote human resolution")
	}
}

func TestDreamBudgetAndTimeoutPreserveEverySource(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	for i := 0; i < 24; i++ {
		_, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: strings.Repeat("长标题", 40), Content: strings.Repeat("较长的完整证据", 100), Kind: "fact", Scope: `{"env":"test"}`})
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	settings, err := s.ConfigureDreamWithTimeout(ctx, testScope(), c.ID, false, 3600, 300, now)
	if err != nil || settings.TimeoutSeconds != 300 {
		t.Fatal(settings, err)
	}
	seen := map[string]bool{}
	force := true
	for i := 0; i < 30; i++ {
		in, state, e := s.BeginDream(ctx, testScope(), c.ID, force, false, now)
		force = false
		if e != nil {
			t.Fatal(e)
		}
		if in == nil {
			break
		}
		if len(in.ModelInput()) > dreamInputBytes {
			t.Fatalf("unbounded model input: %d", len(in.ModelInput()))
		}
		lease, _ := time.Parse(time.RFC3339Nano, state.LeaseUntil)
		if lease.Before(now.Add(330 * time.Second)) {
			t.Fatal("lease shorter than request window")
		}
		for _, f := range in.Facts {
			seen[f.ID] = true
		}
		if e = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); e != nil {
			t.Fatal(e)
		}
	}
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	if len(seen) != 24 || state.RescanCursor >= 0 || state.SourceSeq != state.ProcessedSeq {
		t.Fatalf("batch trimming lost sources: %d %+v", len(seen), state)
	}
}

func TestDreamDoesNotMergeAcrossScopeOrIncompleteEvidence(t *testing.T) {
	for _, mode := range []string{"scope", "time", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			c := mustCreate(t, s, testScope(), "desktop")
			a := memoryFact(t, s, c.ID, "one")
			b := memoryFact(t, s, c.ID, "two")
			switch mode {
			case "scope":
				_, _ = s.db.Exec(`UPDATE wc_facts SET scope='other environment' WHERE id=?`, b.ID)
			case "time":
				_, _ = s.db.Exec(`UPDATE wc_facts SET observed_at='2026-11-01T00:00:00Z' WHERE id=?`, b.ID)
			case "truncated":
				_, _ = s.db.Exec(`UPDATE wc_facts SET content=? WHERE id=?`, strings.Repeat("long source details ", 100), b.ID)
			}
			now := time.Now().UTC()
			in, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.FinishDream(ctx, testScope(), *in, DreamResult{Duplicates: [][]string{{a.ID, b.ID}}}, nil, now); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{a.ID, b.ID} {
				f, _ := s.Fact(ctx, testScope(), c.ID, id)
				if f.Status != "confirmed" {
					t.Fatalf("non-equivalent evidence was retired: %+v", f)
				}
			}
		})
	}
}

func TestDreamMutationRollbackRefusesConcurrentEditAndInvalidatesRecallRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	a := memoryFact(t, s, c.ID, "one")
	b := memoryFact(t, s, c.ID, "two")
	now := time.Now().UTC()
	revision := s.MemoryRevision(ctx, testScope())
	in, _, _ := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err := s.FinishDream(ctx, testScope(), *in, DreamResult{Duplicates: [][]string{{a.ID, b.ID}}}, nil, now); err != nil {
		t.Fatal(err)
	}
	if s.MemoryRevision(ctx, testScope()) == revision {
		t.Fatal("derived retirement did not invalidate cached recall")
	}
	facts, _ := s.Facts(ctx, testScope(), c.ID)
	var archived Fact
	for _, f := range facts {
		if f.Status == "archived" {
			archived = f
		}
	}
	if archived.ID == "" {
		t.Fatal("equivalent evidence was not merged")
	}
	if _, err := s.ReviewFact(ctx, testScope(), c.ID, archived.ID, FactReview{Status: "stale", IfVersion: archived.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevertDream(ctx, testScope(), c.ID, in.RunID); err == nil {
		t.Fatal("rollback overwrote human edit")
	}
}

func TestDreamFullScanLeavesNewChangesForNextIncrement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	for i := 0; i < 24; i++ {
		memoryFact(t, s, c.ID, strings.Repeat("source", i+1))
	}
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); err != nil {
		t.Fatal(err)
	}
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	through := state.RescanThrough
	memoryFact(t, s, c.ID, "arrived during full organization")
	for i := 0; i < 20; i++ {
		state, _ = s.MemoryState(ctx, testScope(), c.ID)
		if state.RescanCursor < 0 {
			break
		}
		in, _, err = s.BeginDream(ctx, testScope(), c.ID, false, false, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	state, _ = s.MemoryState(ctx, testScope(), c.ID)
	if state.ProcessedSeq != through || state.SourceSeq <= state.ProcessedSeq {
		t.Fatalf("full scan consumed unsnapshotted changes: %+v", state)
	}
}

func TestDreamExpiryAlsoDirtiesUnverifiedObservations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	now := time.Now().UTC()
	f, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: "temporary observation", Content: "temporary", Scope: `{"env":"test"}`, ValidUntil: now.Add(time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	in, _, _ := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); err != nil {
		t.Fatal(err)
	}
	before, _ := s.MemoryState(ctx, testScope(), c.ID)
	if err = s.ExpireFacts(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if after.SourceSeq <= before.SourceSeq {
		t.Fatal("observation expiry did not schedule maintenance")
	}
	in, _, _ = s.BeginDream(ctx, testScope(), c.ID, false, false, now.Add(2*time.Hour))
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired, _ := s.Fact(ctx, testScope(), c.ID, f.ID)
	if expired.Status != "archived" {
		t.Fatal("expired observation remains active")
	}
}

func TestDreamEquivalentChainMergesOnceAndReverts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	a := memoryFact(t, s, c.ID, "one wording")
	b := memoryFact(t, s, c.ID, "another wording")
	d := memoryFact(t, s, c.ID, "third wording")
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishDream(ctx, testScope(), *in, DreamResult{Duplicates: [][]string{{a.ID, b.ID}, {b.ID, d.ID}}}, nil, now); err != nil {
		t.Fatal(err)
	}
	facts, _ := s.Facts(ctx, testScope(), c.ID)
	active, archived := 0, 0
	for _, f := range facts {
		if f.Status == "confirmed" {
			active++
		}
		if f.Status == "archived" {
			archived++
		}
	}
	if active != 1 || archived != 2 {
		t.Fatalf("equivalent chain incomplete: active=%d archived=%d", active, archived)
	}
	if _, err = s.RevertDream(ctx, testScope(), c.ID, in.RunID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID, d.ID} {
		f, _ := s.Fact(ctx, testScope(), c.ID, id)
		if f.Status != "confirmed" {
			t.Fatal("merge rollback lost evidence")
		}
	}
}
