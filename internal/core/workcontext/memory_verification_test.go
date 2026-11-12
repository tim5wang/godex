package workcontext

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func verificationProof() []VerificationEvidence {
	return []VerificationEvidence{{Successful: true, ToolCallID: "read-source", ToolName: "read_file", Input: map[string]any{"path": "source.go"}, OutputSHA256: strings.Repeat("a", 64), Excerpt: "const available = true", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
}

func TestMemoryVerificationConfirmAuditAndAtomicRollback(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "verification")
	a := memoryFact(t, s, c.ID, "first")
	b := memoryFact(t, s, c.ID, "second")
	state, _ := s.MemoryState(ctx, testScope(), c.ID)
	op := VerificationOperation{Action: "confirm", FactID: a.ID, IfVersion: a.Version, Reason: "source.go verifies the scoped implementation", Evidence: verificationProof()}
	for _, change := range []string{"no evidence", "failed tool", "stale version", "foreign fact", "no owner"} {
		t.Run(change, func(t *testing.T) {
			bad := op
			scope := testScope()
			switch change {
			case "no evidence":
				bad.Evidence = nil
			case "failed tool":
				bad.Evidence = verificationProof()
				bad.Evidence[0].Successful = false
			case "stale version":
				bad.IfVersion--
			case "foreign fact":
				bad.FactID = "other-desktop"
			case "no owner":
				scope.OwnerID = ""
			}
			if _, err := s.ApplyMemoryVerification(ctx, scope, c.ID, "session", "turn", []VerificationOperation{bad}); err == nil {
				t.Fatal("invalid verification accepted")
			}
		})
	}
	bad := VerificationOperation{Action: "archive", FactID: b.ID, IfVersion: 999, Reason: "must rollback"}
	if _, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op, bad}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected CAS failure: %v", err)
	}
	history, _ := s.MemoryVerificationHistory(ctx, testScope(), c.ID, "session", "turn")
	got, _ := s.Fact(ctx, testScope(), c.ID, a.ID)
	if len(history) != 0 || got.Version != a.Version || got.Source != a.Source {
		t.Fatal("failed batch left partial writes")
	}
	record, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.Fact(ctx, testScope(), c.ID, a.ID)
	if got.Status != "confirmed" || got.Version != a.Version+1 || got.Provenance != "system_query" || got.VerifiedAt == "" || got.Source != "verification://"+record.ID+"/"+a.ID || len(got.SourceRevision) != 64 {
		t.Fatalf("missing host verification: %+v", got)
	}
	if record.Entries[0].Changes[0].Before.Source != a.Source || record.Entries[0].Evidence[0].ToolCallID != "read-source" {
		t.Fatal("audit lost original evidence")
	}
	after, _ := s.MemoryState(ctx, testScope(), c.ID)
	if after.SourceSeq <= state.SourceSeq {
		t.Fatal("verified source did not dirty derived context")
	}
	if _, err = s.MemoryVerificationHistory(ctx, Scope{TenantID: testScope().TenantID, OwnerID: "other"}, c.ID, "", ""); err == nil {
		t.Fatal("audit crossed owners")
	}
}

func TestMemoryVerificationReplacementAndMerge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "replacement")
	a := memoryFact(t, s, c.ID, "old conclusion")
	op := VerificationOperation{Action: "replace", FactID: a.ID, IfVersion: a.Version, Reason: "checked source replaces outdated conclusion", Evidence: verificationProof(), Replacement: &FactInput{Title: "correct conclusion", Content: "current implementation", Scope: `{"env":"production"}`}}
	if _, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op}); err == nil {
		t.Fatal("scope widened")
	}
	op.Replacement.Scope = ""
	record, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := s.Fact(ctx, testScope(), c.ID, a.ID)
	replacement := record.Entries[0].Changes[1].After
	if old.Status != "superseded" || replacement.Status != "confirmed" || replacement.Scope != a.Scope || replacement.Kind != a.Kind || replacement.SupersedesID != a.ID {
		t.Fatal("replacement lost applicability")
	}
	b := memoryFact(t, s, c.ID, "duplicate")
	group := VerificationOperation{Action: "merge", FactIDs: []string{replacement.ID, b.ID}, KeepID: replacement.ID, Versions: map[string]int{replacement.ID: replacement.Version, b.ID: b.Version}, Reason: "same scoped claim verified in source", Evidence: verificationProof()}
	if _, err = s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{group}); err != nil {
		t.Fatal(err)
	}
	if factStatus(t, s, c.ID, b.ID) != "archived" {
		t.Fatal("duplicate retained")
	}
	if _, err = s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{group}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale group replay accepted", err)
	}
}

func TestMemoryVerificationResolveConflictAndBlockers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "conflict")
	a := memoryFact(t, s, c.ID, "one")
	b := memoryFact(t, s, c.ID, "two")
	in, _, _ := s.BeginDream(ctx, testScope(), c.ID, true, false, time.Now())
	if err := s.FinishDream(ctx, testScope(), *in, DreamResult{Conflicts: [][]string{{a.ID, b.ID}}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	issues, _ := s.MemoryIssues(ctx, testScope(), c.ID)
	versions := map[string]int{}
	for _, f := range issues[0].Facts {
		versions[f.ID] = f.Version
	}
	op := VerificationOperation{Action: "resolve_conflict", IssueID: issues[0].ID, KeepID: a.ID, Versions: versions, Reason: "original code supports first claim", Evidence: verificationProof()}
	if _, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op}); err != nil {
		t.Fatal(err)
	}
	issues, _ = s.MemoryIssues(ctx, testScope(), c.ID)
	if len(issues) != 0 || factStatus(t, s, c.ID, b.ID) != "archived" {
		t.Fatal("conflict did not close")
	}
	f, _ := s.Fact(ctx, testScope(), c.ID, a.ID)
	block := VerificationOperation{Action: "block", FactID: f.ID, IfVersion: f.Version, BlockKind: "access", Reason: "source endpoint denied access", Evidence: verificationProof()}
	if _, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{block}); err == nil {
		t.Fatal("access blocker without failed attempt accepted")
	}
	block.Evidence[0].Successful = false
	block.Evidence[0].Error = "permission denied"
	if _, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{block}); err != nil {
		t.Fatal(err)
	}
	f, _ = s.Fact(ctx, testScope(), c.ID, a.ID)
	if f.Status != "verification_blocked" || f.Current(time.Now()) || !strings.Contains(f.SourceExcerpt, "permission") && !strings.Contains(f.SourceExcerpt, "denied") {
		t.Fatalf("block masquerades as confirmed: %+v", f)
	}
	block.IfVersion = f.Version
	block.BlockKind = "missing_evidence"
	block.Evidence = nil
	if _, err := s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{block}); err == nil {
		t.Fatal("unattempted verification accepted")
	}
}

func TestMemoryVerificationExpiredFactsAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "expired")
	f, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: "expired", Content: "old", Kind: "fact", Scope: `{"env":"test"}`, ValidUntil: "2020-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	op := VerificationOperation{Action: "confirm", FactID: f.ID, IfVersion: f.Version, Reason: "cannot revive stale deadline", Evidence: verificationProof()}
	if _, err = s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op}); err == nil {
		t.Fatal("expired fact confirmed")
	}
	op.Action = "archive"
	op.Evidence = nil
	if _, err = s.ApplyMemoryVerification(ctx, testScope(), c.ID, "session", "turn", []VerificationOperation{op}); err != nil {
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
	history, err := s.MemoryVerificationHistory(ctx, testScope(), c.ID, "session", "turn")
	if err != nil || len(history) != 1 || history[0].Entries[0].Changes[0].Before.Content != "old" {
		t.Fatal("audit not durable", history, err)
	}
}

func TestMemoryVerificationPendingSupersessionNeedsPredecessorVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, testScope(), "proposal")
	old := memoryFact(t, s, c.ID, "old")
	proposal, err := s.CreateFact(ctx, testScope(), c.ID, FactInput{Title: "new", Content: "corrected conclusion", Scope: old.Scope, Kind: old.Kind, SupersedesID: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	op := VerificationOperation{Action: "confirm", FactID: proposal.ID, IfVersion: proposal.Version, Reason: "corrected conclusion checked", Evidence: verificationProof()}
	if _, err = s.ApplyMemoryVerification(ctx, testScope(), c.ID, "s", "t", []VerificationOperation{op}); err == nil {
		t.Fatal("confirmation ignored live predecessor")
	}
	op.Action = "merge"
	op.FactIDs = []string{old.ID, proposal.ID}
	op.KeepID = proposal.ID
	op.Versions = map[string]int{old.ID: old.Version, proposal.ID: proposal.Version}
	if _, err = s.ApplyMemoryVerification(ctx, testScope(), c.ID, "s", "t", []VerificationOperation{op}); err != nil {
		t.Fatal(err)
	}
	if factStatus(t, s, c.ID, old.ID) != "archived" || factStatus(t, s, c.ID, proposal.ID) != "confirmed" {
		t.Fatal("explicit supersession did not close")
	}
}
