package workcontext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func scopedDreamFact(t *testing.T, s *Store, id, title, scope string) Fact {
	t.Helper()
	ctx := context.Background()
	f, err := s.CreateFact(ctx, testScope(), id, FactInput{Title: title, Content: title + " evidence", Kind: "method", Scope: scope, Source: "report://fixture/evidence", ObservedAt: time.Now().UTC().Format(time.RFC3339), Provenance: "system_query"})
	if err != nil {
		t.Fatal(err)
	}
	f, err = s.ReviewFact(ctx, testScope(), id, f.ID, FactReview{Status: "confirmed", IfVersion: f.Version})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDreamMixedApplicabilityDoesNotAbortFullOrganization(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	a := scopedDreamFact(t, s, c.ID, "UI behavior", "godex / work desktop UI")
	b := scopedDreamFact(t, s, c.ID, "runtime behavior", "godex session runtime")
	for i := 0; i < 18; i++ {
		memoryFact(t, s, c.ID, strings.Repeat("x", i+1))
	}
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	result := DreamResult{Summary: "safe reference digest", Candidates: []DreamCandidate{
		{Title: "cross-scope generalization", Content: "must not become evidence", SourceFactIDs: []string{a.ID, b.ID}},
		{Title: "valid UI proposal", Content: "applies only to its original UI scope", SourceFactIDs: []string{a.ID}},
	}}
	if err = s.FinishDream(ctx, testScope(), *in, result, nil, now); err != nil {
		t.Fatalf("mixed-scope suggestion aborted useful output: %v", err)
	}
	firstRun := in.RunID
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	if state.Status != "completed" || state.RescanCursor < 0 {
		t.Fatalf("full organization did not continue: %+v", state)
	}
	for state.RescanCursor >= 0 {
		in, _, err = s.BeginDream(ctx, testScope(), c.ID, false, false, now)
		if err != nil || in == nil {
			t.Fatalf("continuation blocked: %v", err)
		}
		if err = s.FinishDream(ctx, testScope(), *in, DreamResult{}, nil, now); err != nil {
			t.Fatal(err)
		}
		state, _ = s.MemoryState(ctx, testScope(), c.ID)
	}
	if state.SourceSeq != state.ProcessedSeq || state.Summary != result.Summary {
		t.Fatalf("organization incomplete: %+v", state)
	}
	detail, _ := s.Get(ctx, testScope(), c.ID)
	var valid int
	for _, f := range detail.Facts {
		if f.Title == "cross-scope generalization" {
			t.Fatal("cross-scope proposal was saved")
		}
		if f.Title == "valid UI proposal" {
			valid++
			if f.Scope != a.Scope || f.Status != "candidate" {
				t.Fatalf("applicability or verification widened: %+v", f)
			}
		}
		if (f.ID == a.ID || f.ID == b.ID) && (f.Status != "confirmed" || f.Version != 2) {
			t.Fatal("source evidence changed")
		}
	}
	if valid != 0 {
		t.Fatal("reference became an approval task")
	}
	runs, _ := s.DreamRuns(ctx, testScope(), c.ID)
	found := false
	for _, run := range runs {
		if run["id"] == firstRun {
			raw, _ := json.Marshal(run["result"])
			var saved struct {
				Skipped []struct{ Title, Reason string } `json:"skipped_candidates"`
			}
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			found = len(saved.Skipped) == 1 && saved.Skipped[0].Title == "cross-scope generalization" && saved.Skipped[0].Reason == "mixed_applicability"
		}
	}
	if !found {
		t.Fatal("skipped proposal not preserved in organization history")
	}
}

func TestDreamEquivalentJSONScopesCanMerge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	a := scopedDreamFact(t, s, c.ID, "one", `{"node":"mac-test","env":"test"}`)
	b := scopedDreamFact(t, s, c.ID, "two", `{ "env": "test", "node": "mac-test" }`)
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil {
		t.Fatal(err)
	}
	err = s.FinishDream(ctx, testScope(), *in, DreamResult{Candidates: []DreamCandidate{{Title: "merged", Content: "same applicability", SourceFactIDs: []string{a.ID, b.ID}}}}, nil, now)
	if err != nil {
		t.Fatalf("JSON formatting treated as widened scope: %v", err)
	}
	runs, _ := s.DreamRuns(ctx, testScope(), c.ID)
	raw, _ := json.Marshal(runs[0]["result"])
	var saved struct {
		References []DreamCandidate `json:"references"`
	}
	if err = json.Unmarshal(raw, &saved); err != nil || len(saved.References) != 1 {
		t.Fatalf("equivalent-scope reference discarded: %s %v", raw, err)
	}
}

func TestDreamMissingApplicabilityIsSkipped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "desktop")
	f := memoryFact(t, s, c.ID, "unscoped")
	// Older rows can predate the API's mandatory applicability validation.
	if _, err := s.db.ExecContext(ctx, `UPDATE wc_facts SET scope='' WHERE id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
	if err != nil || in == nil {
		t.Fatalf("begin: %v", err)
	}
	result := DreamResult{Summary: "retained digest", Candidates: []DreamCandidate{{Title: "unscoped proposal", Content: "must not become knowledge", SourceFactIDs: []string{f.ID}}}}
	if err = s.FinishDream(ctx, testScope(), *in, result, nil, now); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Get(ctx, testScope(), c.ID)
	if err != nil || len(detail.Facts) != 1 || detail.Facts[0].ID != f.ID {
		t.Fatalf("unscoped proposal was saved: %+v %v", detail, err)
	}
	runs, err := s.DreamRuns(ctx, testScope(), c.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("history: %+v %v", runs, err)
	}
	raw, _ := json.Marshal(runs[0]["result"])
	var saved struct {
		Skipped []DreamSkippedCandidate `json:"skipped_candidates"`
	}
	if err = json.Unmarshal(raw, &saved); err != nil || len(saved.Skipped) != 1 || saved.Skipped[0].Reason != "missing_applicability" {
		t.Fatalf("missing scope reason not audited: %s %v", raw, err)
	}
}

func TestDreamScopeSkipDoesNotHideUnknownOrChangedEvidence(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "changed"}[changed], func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			c := mustCreate(t, s, testScope(), "desktop")
			a := memoryFact(t, s, c.ID, "source")
			b := scopedDreamFact(t, s, c.ID, "other", "other scope")
			now := time.Now().UTC()
			in, _, err := s.BeginDream(ctx, testScope(), c.ID, false, false, now)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{a.ID, b.ID}
			want := ErrInvalid
			if changed {
				_, err = s.ReviewFact(ctx, testScope(), c.ID, b.ID, FactReview{Status: "stale", IfVersion: b.Version})
				if err != nil {
					t.Fatal(err)
				}
				want = ErrConflict
			} else {
				ids = append(ids, "unknown-source")
			}
			err = s.FinishDream(ctx, testScope(), *in, DreamResult{Candidates: []DreamCandidate{{Title: "invalid", Content: "must fail", SourceFactIDs: ids}}}, nil, now)
			if !errors.Is(err, want) {
				t.Fatalf("evidence error hidden by scope skip: %v", err)
			}
			state, _ := s.MemoryState(ctx, testScope(), c.ID)
			if state.ProcessedSeq != 0 || state.Status != "failed" {
				t.Fatal("invalid evidence consumed watermark")
			}
		})
	}
}

func TestDreamScopeGroupsPreserveFullScopeIdentity(t *testing.T) {
	prefix := strings.Repeat("same-prefix", 45)
	input := DreamInput{Facts: []Fact{{ID: "a", Scope: prefix + "-test"}, {ID: "b", Scope: prefix + "-prod"}, {ID: "c", Scope: `{"account":9007199254740992}`}, {ID: "d", Scope: `{"account":9007199254740993}`}}}
	raw := input.ModelInput()
	var model struct {
		Groups []struct {
			IDs []string `json:"source_fact_ids"`
		} `json:"scope_groups"`
	}
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		t.Fatal(err)
	}
	if len(model.Groups) != 4 {
		t.Fatalf("full/precise scope identity was collapsed: %s", raw)
	}
	if input.Facts[0].Scope != prefix+"-test" {
		t.Fatal("model truncation mutated evidence")
	}
}
