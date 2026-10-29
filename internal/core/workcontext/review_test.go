package workcontext

import (
	"context"
	"errors"
	"testing"
)

// archiveContext archives a context and returns its post-archive version.
func archiveContext(t *testing.T, s *Store, id string, version int) Context {
	t.Helper()
	archived := statusArchived
	item, err := s.Patch(context.Background(), testScope(), id, ContextPatch{Status: &archived, IfVersion: version})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	return item
}

// TestSessionBindingTrustedLookup verifies the backend-only session lookup:
// deterministic most-recent context for one owner, not-found, and rejection
// when a session identity maps to two owners.
func TestSessionBindingTrustedLookup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "会话主线A")
	b := mustCreate(t, store, testScope(), "会话主线B")

	if _, err := store.LinkSession(ctx, testScope(), a.ID, SessionLink{SessionID: "sess-1", Topic: "主题A"}); err != nil {
		t.Fatalf("link a: %v", err)
	}
	if _, err := store.LinkSession(ctx, testScope(), b.ID, SessionLink{SessionID: "sess-1", Topic: "主题B"}); err != nil {
		t.Fatalf("link b: %v", err)
	}
	// Bump A so it becomes the most recently updated binding.
	newTitle := "会话主线A-v2"
	if _, err := store.Patch(ctx, testScope(), a.ID, ContextPatch{Title: &newTitle, IfVersion: a.Version}); err != nil {
		t.Fatalf("patch a: %v", err)
	}
	scope, contextID, err := store.SessionBinding(ctx, "sess-1")
	if err != nil {
		t.Fatalf("SessionBinding: %v", err)
	}
	if scope != testScope() {
		t.Fatalf("binding scope = %+v, want %+v", scope, testScope())
	}
	if contextID != a.ID {
		t.Fatalf("binding context = %s, want most recent %s", contextID, a.ID)
	}

	if _, _, err := store.SessionBinding(ctx, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty session err = %v, want ErrInvalid", err)
	}
	if _, _, err := store.SessionBinding(ctx, "missing-session"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session err = %v, want ErrNotFound", err)
	}

	// A session identity shared across owners is a conflict, not a guess. Seed
	// the cross-owner link directly to exercise the trusted lookup's guard.
	other := Scope{TenantID: "tenant-a", OwnerID: "owner-2"}
	otherCtx := mustCreate(t, store, other, "其他 owner 主线")
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO wc_context_sessions(tenant_id, id, context_id, session_id, topic, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		"tenant-a", "sl-cross", otherCtx.ID, "sess-1", "", nowRFC3339()); err != nil {
		t.Fatalf("seed cross-owner link: %v", err)
	}
	if _, _, err := store.SessionBinding(ctx, "sess-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-owner binding err = %v, want ErrConflict", err)
	}
}

// TestLinkSessionRejectsCrossOwner verifies a session cannot be bound to a
// context of a different owner, but may be shared within the same owner.
func TestLinkSessionRejectsCrossOwner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mine := mustCreate(t, store, testScope(), "我的主线")
	if _, err := store.LinkSession(ctx, testScope(), mine.ID, SessionLink{SessionID: "shared"}); err != nil {
		t.Fatalf("LinkSession: %v", err)
	}
	// Same owner, another context: allowed.
	alsoMine := mustCreate(t, store, testScope(), "我的另一主线")
	if _, err := store.LinkSession(ctx, testScope(), alsoMine.ID, SessionLink{SessionID: "shared"}); err != nil {
		t.Fatalf("same-owner relink: %v", err)
	}
	// Different owner, same tenant: rejected as a conflict.
	other := Scope{TenantID: "tenant-a", OwnerID: "owner-2"}
	their := mustCreate(t, store, other, "别人的主线")
	if _, err := store.LinkSession(ctx, other, their.ID, SessionLink{SessionID: "shared"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-owner link err = %v, want ErrConflict", err)
	}
}

// TestSupersedeDeferredAndValidated verifies supersession is deferred to
// confirmation and validated against the predecessor.
func TestSupersedeDeferredAndValidated(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "supersede")
	other := mustCreate(t, store, testScope(), "other")

	predecessor, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{
		Title: "生产版本", Content: "v2.3.1", Scope: `{"service":"api","env":"prod"}`,
		Subject: "service:api", Predicate: "version",
		Source: "https://ci.example.com/42", ObservedAt: "2026-10-29T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateFact predecessor: %v", err)
	}
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, predecessor.ID, FactReview{Status: "confirmed", IfVersion: predecessor.Version}); err != nil {
		t.Fatalf("confirm predecessor: %v", err)
	}

	// Mismatched predicate must be rejected at candidate creation.
	if _, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{
		Title: "生产版本", Content: "v2.4.0", Scope: `{"service":"api","env":"prod"}`,
		Subject: "service:api", Predicate: "image", SupersedesID: predecessor.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched predicate err = %v, want ErrInvalid", err)
	}
	// A predecessor in another context must not resolve.
	if _, err := store.CreateFact(ctx, testScope(), other.ID, FactInput{
		Title: "生产版本", Content: "v2.4.0", Scope: `{"service":"api","env":"prod"}`,
		Subject: "service:api", Predicate: "version", SupersedesID: predecessor.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-context predecessor err = %v, want ErrInvalid", err)
	}

	successor, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{
		Title: "生产版本", Content: "v2.4.0", Scope: `{"service":"api","env":"prod"}`,
		Subject: "service:api", Predicate: "version",
		Source: "https://ci.example.com/99", ObservedAt: "2026-10-30T00:00:00Z", SupersedesID: predecessor.ID,
	})
	if err != nil {
		t.Fatalf("CreateFact successor: %v", err)
	}
	if got := factStatus(t, store, a.ID, predecessor.ID); got != "confirmed" {
		t.Fatalf("predecessor after candidate = %q, want confirmed", got)
	}
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, successor.ID, FactReview{Status: "confirmed", IfVersion: successor.Version}); err != nil {
		t.Fatalf("confirm successor: %v", err)
	}
	if got := factStatus(t, store, a.ID, predecessor.ID); got != "superseded" {
		t.Fatalf("predecessor after confirm = %q, want superseded", got)
	}
}

// TestCreateFactValidation verifies input validation on fact creation.
func TestCreateFactValidation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "validation")
	valid := func() FactInput {
		return FactInput{
			Title: "部署", Content: "v1", Scope: `{"service":"api"}`,
			Source: "https://ci.example.com/1", ObservedAt: "2026-10-30T00:00:00Z",
		}
	}
	cases := []struct {
		name   string
		mutate func(*FactInput)
	}{
		{"empty title", func(in *FactInput) { in.Title = "  " }},
		{"empty content", func(in *FactInput) { in.Content = "" }},
		{"empty scope", func(in *FactInput) { in.Scope = "" }},
		{"unknown scope value", func(in *FactInput) { in.Scope = `{"env":"unknown"}` }},
		{"bad provenance", func(in *FactInput) { in.Provenance = "robot" }},
		{"bad source scheme", func(in *FactInput) { in.Source = "javascript:alert(1)" }},
		{"metadata endpoint", func(in *FactInput) { in.Source = "http://169.254.169.254/latest/meta-data" }},
		{"bad observed_at", func(in *FactInput) { in.ObservedAt = "yesterday" }},
	}
	for _, tc := range cases {
		in := valid()
		tc.mutate(&in)
		if _, err := store.CreateFact(ctx, testScope(), a.ID, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s err = %v, want ErrInvalid", tc.name, err)
		}
	}
	// A valid fact defaults provenance to human_report, never system verified.
	in := valid()
	in.Provenance = ""
	item, err := store.CreateFact(ctx, testScope(), a.ID, in)
	if err != nil {
		t.Fatalf("valid CreateFact: %v", err)
	}
	if item.Provenance != provenanceHumanReport {
		t.Fatalf("provenance = %q, want %q", item.Provenance, provenanceHumanReport)
	}
	// Confirmation still requires an observation time.
	noTime := valid()
	noTime.ObservedAt = ""
	cand, err := store.CreateFact(ctx, testScope(), a.ID, noTime)
	if err != nil {
		t.Fatalf("CreateFact no observed_at: %v", err)
	}
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, cand.ID, FactReview{Status: "confirmed", IfVersion: cand.Version}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("confirm without observed_at err = %v, want ErrInvalid", err)
	}
}

// TestArchivedGuardsEveryWrite verifies every mutating path rejects edits on an
// archived context while reads stay available.
func TestArchivedGuardsEveryWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "归档主线")
	active := mustCreate(t, store, testScope(), "活跃主线")
	fact, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{Title: "f", Content: "c", Scope: `{"service":"api"}`})
	if err != nil {
		t.Fatalf("CreateFact: %v", err)
	}
	a = archiveContext(t, store, a.ID, a.Version)

	// Reads on the archived context keep working.
	if _, err := store.Get(ctx, testScope(), a.ID); err != nil {
		t.Fatalf("Get archived: %v", err)
	}
	if _, err := store.List(ctx, testScope()); err != nil {
		t.Fatalf("List with archived: %v", err)
	}

	if _, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{Title: "f2", Content: "c", Scope: `{"service":"api"}`}); !errors.Is(err, ErrArchived) {
		t.Fatalf("CreateFact archived err = %v, want ErrArchived", err)
	}
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, fact.ID, FactReview{Status: "confirmed", IfVersion: fact.Version}); !errors.Is(err, ErrArchived) {
		t.Fatalf("ReviewFact archived err = %v, want ErrArchived", err)
	}
	if _, err := store.SetFactCandidate(ctx, testScope(), a.ID, fact.ID, "cand"); !errors.Is(err, ErrArchived) {
		t.Fatalf("SetFactCandidate archived err = %v, want ErrArchived", err)
	}
	if _, err := store.LinkSession(ctx, testScope(), a.ID, SessionLink{SessionID: "s"}); !errors.Is(err, ErrArchived) {
		t.Fatalf("LinkSession archived err = %v, want ErrArchived", err)
	}
	if _, err := store.AddResource(ctx, testScope(), a.ID, ResourceInput{Title: "r"}); !errors.Is(err, ErrArchived) {
		t.Fatalf("AddResource archived err = %v, want ErrArchived", err)
	}
	if _, err := store.AddRelation(ctx, testScope(), a.ID, RelationInput{ToID: active.ID}); !errors.Is(err, ErrArchived) {
		t.Fatalf("AddRelation archived err = %v, want ErrArchived", err)
	}
	if _, err := store.ReserveDelegation(ctx, testScope(), a.ID, Delegation{RequestID: "r1"}); !errors.Is(err, ErrArchived) {
		t.Fatalf("ReserveDelegation archived err = %v, want ErrArchived", err)
	}
	if err := store.RecordEvent(ctx, testScope(), a.ID, "k", "t", "s", "d"); !errors.Is(err, ErrArchived) {
		t.Fatalf("RecordEvent archived err = %v, want ErrArchived", err)
	}
}

// TestScopeRequiresOwner verifies an empty owner identity is rejected while an
// empty tenant is allowed for operator/self-host use.
func TestScopeRequiresOwner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, Scope{TenantID: "tenant-a"}, ContextInput{Title: "no owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty owner create err = %v, want ErrInvalid", err)
	}
	item, err := store.Create(ctx, Scope{OwnerID: "solo"}, ContextInput{Title: "operator"})
	if err != nil {
		t.Fatalf("empty tenant create: %v", err)
	}
	if _, err := store.Get(ctx, Scope{OwnerID: "solo"}, item.ID); err != nil {
		t.Fatalf("empty tenant get: %v", err)
	}
	// Isolation still holds by owner even with empty tenant.
	if _, err := store.Get(ctx, Scope{OwnerID: "someone-else"}, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get err = %v, want ErrNotFound", err)
	}
}
