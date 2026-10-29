package workcontext

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore opens a throwaway store in a temp directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "work-contexts.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testScope() Scope { return Scope{TenantID: "tenant-a", OwnerID: "owner-1"} }

// factStatus reads a single fact's current status for assertions.
func factStatus(t *testing.T, s *Store, contextID, factID string) string {
	t.Helper()
	detail, err := s.Get(context.Background(), testScope(), contextID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, f := range detail.Facts {
		if f.ID == factID {
			return f.Status
		}
	}
	t.Fatalf("fact %s not found in context %s", factID, contextID)
	return ""
}

func mustCreate(t *testing.T, s *Store, scope Scope, title string) Context {
	t.Helper()
	item, err := s.Create(context.Background(), scope, ContextInput{Title: title, Background: "bg", Goal: "goal"})
	if err != nil {
		t.Fatalf("Create(%q): %v", title, err)
	}
	return item
}

// TestMigrationReopenAndSchema verifies the ordered migration is recorded and a
// reopened database keeps its data.
func TestMigrationReopenAndSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "work-contexts.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var version int
	if err := store.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM wc_schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("migration version = %d, want %d", version, schemaVersion)
	}
	created := mustCreate(t, store, testScope(), "重开持久化")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	detail, err := reopened.Get(context.Background(), testScope(), created.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if detail.Context.Title != "重开持久化" {
		t.Fatalf("title = %q, want 重开持久化", detail.Context.Title)
	}
}

// TestDetailCollectionsAreNeverNull verifies every Detail collection is an
// empty slice, never a JSON null.
func TestDetailCollectionsAreNeverNull(t *testing.T) {
	store := newTestStore(t)
	created := mustCreate(t, store, testScope(), "empty detail")
	detail, err := store.Get(context.Background(), testScope(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Resources == nil || detail.Sessions == nil || detail.Delegations == nil ||
		detail.Facts == nil || detail.Relations == nil || detail.Events == nil {
		t.Fatalf("detail collections must be non-nil: %+v", detail)
	}
}

// TestPatchVersionConflict verifies the CAS guard: a stale if_version is a 409.
func TestPatchVersionConflict(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := mustCreate(t, store, testScope(), "cas")
	newTitle := "cas v2"
	updated, err := store.Patch(ctx, testScope(), created.ID, ContextPatch{Title: &newTitle, IfVersion: created.Version})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if updated.Version != created.Version+1 {
		t.Fatalf("version = %d, want %d", updated.Version, created.Version+1)
	}
	if _, err := store.Patch(ctx, testScope(), created.ID, ContextPatch{Title: &newTitle, IfVersion: created.Version}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale patch err = %v, want ErrConflict", err)
	}
}

// TestArchiveIsReadOnly verifies archived contexts reject edits until restored.
func TestArchiveIsReadOnly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := mustCreate(t, store, testScope(), "archive me")
	archived := "archived"
	item, err := store.Patch(ctx, testScope(), created.ID, ContextPatch{Status: &archived, IfVersion: created.Version})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if item.Status != "archived" {
		t.Fatalf("status = %q, want archived", item.Status)
	}
	newTitle := "should fail"
	if _, err := store.Patch(ctx, testScope(), created.ID, ContextPatch{Title: &newTitle, IfVersion: item.Version}); !errors.Is(err, ErrArchived) {
		t.Fatalf("edit archived err = %v, want ErrArchived", err)
	}
	cp := Checkpoint{Summary: "blocked"}
	if _, err := store.SaveCheckpoint(ctx, testScope(), created.ID, item.Version, cp); !errors.Is(err, ErrArchived) {
		t.Fatalf("checkpoint on archived err = %v, want ErrArchived", err)
	}
	active := "active"
	restored, err := store.Patch(ctx, testScope(), created.ID, ContextPatch{Status: &active, IfVersion: item.Version})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Status != "active" {
		t.Fatalf("restored status = %q, want active", restored.Status)
	}
}

// TestScopeIsolation verifies tenant+owner isolation on every access.
func TestScopeIsolation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := mustCreate(t, store, testScope(), "private")
	otherOwner := Scope{TenantID: "tenant-a", OwnerID: "owner-2"}
	otherTenant := Scope{TenantID: "tenant-b", OwnerID: "owner-1"}
	for _, scope := range []Scope{otherOwner, otherTenant} {
		if _, err := store.Get(ctx, scope, created.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-scope Get err = %v, want ErrNotFound", err)
		}
		if _, err := store.Patch(ctx, scope, created.ID, ContextPatch{IfVersion: 1}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-scope Patch err = %v, want ErrNotFound", err)
		}
		list, err := store.List(ctx, scope)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("cross-scope List returned %d rows, want 0", len(list))
		}
	}
}

// TestCheckpointHistoryAndCAS verifies immutable revisions and optimistic locks.
func TestCheckpointHistoryAndCAS(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	created := mustCreate(t, store, testScope(), "checkpoints")
	first, err := store.SaveCheckpoint(ctx, testScope(), created.ID, created.Version, Checkpoint{
		Summary:     "r1",
		Completed:   []string{"a"},
		NextActions: []string{"b"},
	})
	if err != nil {
		t.Fatalf("checkpoint 1: %v", err)
	}
	if first.Checkpoint.Revision != 1 {
		t.Fatalf("revision = %d, want 1", first.Checkpoint.Revision)
	}
	second, err := store.SaveCheckpoint(ctx, testScope(), created.ID, first.Version, Checkpoint{Summary: "r2"})
	if err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}
	if second.Checkpoint.Revision != 2 {
		t.Fatalf("revision = %d, want 2", second.Checkpoint.Revision)
	}
	if _, err := store.SaveCheckpoint(ctx, testScope(), created.ID, created.Version, Checkpoint{Summary: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale checkpoint err = %v, want ErrConflict", err)
	}
	history, err := store.Checkpoints(ctx, testScope(), created.ID)
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	if len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 {
		t.Fatalf("history order = %+v, want revisions 2,1", history)
	}
	// A checkpoint always produces at least one appended event.
	detail, err := store.Get(ctx, testScope(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	found := false
	for _, ev := range detail.Events {
		if ev.Kind == "checkpoint_saved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected checkpoint_saved event in %+v", detail.Events)
	}
}

// TestResourceSharedAndUnlink verifies shared resources across contexts and
// reference-counted deletion.
func TestResourceSharedAndUnlink(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "ctx a")
	b := mustCreate(t, store, testScope(), "ctx b")
	input := ResourceInput{ID: "res-shared", Title: "部署文档", Kind: "document", Locator: "https://example.com/doc"}
	res, err := store.AddResource(ctx, testScope(), a.ID, input)
	if err != nil {
		t.Fatalf("AddResource a: %v", err)
	}
	if len(res.ContextIDs) != 1 || res.ContextIDs[0] != a.ID {
		t.Fatalf("context_ids = %v, want [%s]", res.ContextIDs, a.ID)
	}
	// Same ID links the existing resource to a second context.
	if _, err := store.AddResource(ctx, testScope(), b.ID, input); err != nil {
		t.Fatalf("AddResource b: %v", err)
	}
	list, err := store.ListResources(ctx, testScope())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(list) != 1 || len(list[0].ContextIDs) != 2 {
		t.Fatalf("resources = %+v, want 1 resource with 2 contexts", list)
	}
	if err := store.RemoveResource(ctx, testScope(), a.ID, "res-shared"); err != nil {
		t.Fatalf("RemoveResource a: %v", err)
	}
	// Resource survives while one reference remains.
	if _, err := store.ListResources(ctx, testScope()); err != nil {
		t.Fatalf("ListResources after unlink: %v", err)
	}
	remaining, _ := store.ListResources(ctx, testScope())
	if len(remaining) != 1 {
		t.Fatalf("after unlink = %+v, want 1", remaining)
	}
	if err := store.RemoveResource(ctx, testScope(), b.ID, "res-shared"); err != nil {
		t.Fatalf("RemoveResource b: %v", err)
	}
	remaining, _ = store.ListResources(ctx, testScope())
	if len(remaining) != 0 {
		t.Fatalf("after both unlinks = %+v, want 0", remaining)
	}
}

// TestResourceValidation verifies locator and node binding rules.
func TestResourceValidation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "ctx")
	cases := []struct {
		name  string
		input ResourceInput
	}{
		{"javascript scheme", ResourceInput{Title: "x", Locator: "javascript:alert(1)"}},
		{"signed url", ResourceInput{Title: "x", Locator: "https://ex.com/a?token=secret"}},
		{"directory without node", ResourceInput{Title: "x", Kind: "directory", Locator: "/srv/app"}},
	}
	for _, tc := range cases {
		if _, err := store.AddResource(ctx, testScope(), a.ID, tc.input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s err = %v, want ErrInvalid", tc.name, err)
		}
	}
	// A node-bound directory is accepted.
	if _, err := store.AddResource(ctx, testScope(), a.ID, ResourceInput{Title: "dir", Kind: "directory", Locator: "/srv/app", NodeID: "node-1"}); err != nil {
		t.Fatalf("valid directory resource err = %v", err)
	}
}

// TestSessionLinks verifies idempotent session association and unlinking.
func TestSessionLinks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "ctx")
	link, err := store.LinkSession(ctx, testScope(), a.ID, SessionLink{SessionID: "sess-1", Topic: "主题一"})
	if err != nil {
		t.Fatalf("LinkSession: %v", err)
	}
	again, err := store.LinkSession(ctx, testScope(), a.ID, SessionLink{SessionID: "sess-1", Topic: "主题一"})
	if err != nil {
		t.Fatalf("LinkSession repeat: %v", err)
	}
	if again.ID != link.ID {
		t.Fatalf("relink id = %s, want %s", again.ID, link.ID)
	}
	detail, err := store.Get(ctx, testScope(), a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want 1", detail.Sessions)
	}
	if err := store.UnlinkSession(ctx, testScope(), a.ID, "sess-1"); err != nil {
		t.Fatalf("UnlinkSession: %v", err)
	}
	detail, _ = store.Get(ctx, testScope(), a.ID)
	if len(detail.Sessions) != 0 {
		t.Fatalf("sessions after unlink = %+v, want 0", detail.Sessions)
	}
}

// TestRelationsAndCycleDetection verifies relation creation, self-reference
// rejection, and dependency cycle rejection.
func TestRelationsAndCycleDetection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "A")
	b := mustCreate(t, store, testScope(), "B")
	c := mustCreate(t, store, testScope(), "C")

	if _, err := store.AddRelation(ctx, testScope(), a.ID, RelationInput{ToID: a.ID, Kind: "related_to"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self relation err = %v, want ErrInvalid", err)
	}
	if _, err := store.AddRelation(ctx, testScope(), a.ID, RelationInput{ToID: b.ID, Kind: "depends_on"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("condition-less depends_on err = %v, want ErrInvalid", err)
	}
	if _, err := store.AddRelation(ctx, testScope(), a.ID, RelationInput{ToID: b.ID, Kind: "depends_on", Condition: "B ships"}); err != nil {
		t.Fatalf("A depends_on B: %v", err)
	}
	if _, err := store.AddRelation(ctx, testScope(), b.ID, RelationInput{ToID: c.ID, Kind: "depends_on", Condition: "C ships"}); err != nil {
		t.Fatalf("B depends_on C: %v", err)
	}
	// C depends_on A would close A -> B -> C -> A.
	if _, err := store.AddRelation(ctx, testScope(), c.ID, RelationInput{ToID: a.ID, Kind: "depends_on", Condition: "A ships"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle err = %v, want ErrInvalid", err)
	}
	if _, err := store.AddRelation(ctx, testScope(), a.ID, RelationInput{ToID: c.ID, Kind: "related_to"}); err != nil {
		t.Fatalf("related_to: %v", err)
	}
	detail, _ := store.Get(ctx, testScope(), a.ID)
	if len(detail.Relations) != 2 {
		t.Fatalf("relations = %+v, want 2", detail.Relations)
	}
	if err := store.RemoveRelation(ctx, testScope(), a.ID, detail.Relations[0].ID); err != nil {
		t.Fatalf("RemoveRelation: %v", err)
	}
	detail, _ = store.Get(ctx, testScope(), a.ID)
	if len(detail.Relations) != 1 {
		t.Fatalf("relations after remove = %+v, want 1", detail.Relations)
	}
}

// TestFactsReviewSupersedeCandidate verifies fact governance.
func TestFactsReviewSupersedeCandidate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "facts")

	unbacked, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{Title: "AI 声称测试通过", Content: "no evidence", Scope: `{"service":"api"}`})
	if err != nil {
		t.Fatalf("CreateFact: %v", err)
	}
	if unbacked.Status != "candidate" {
		t.Fatalf("new fact status = %q, want candidate", unbacked.Status)
	}
	if unbacked.Provenance != provenanceHumanReport {
		t.Fatalf("default provenance = %q, want human_report", unbacked.Provenance)
	}
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, unbacked.ID, FactReview{Status: "confirmed", IfVersion: unbacked.Version}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("confirm without source err = %v, want ErrInvalid", err)
	}

	backed, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{
		Title:      "生产版本",
		Content:    "v2.3.1",
		Kind:       "deployment",
		Scope:      `{"service":"api","env":"prod"}`,
		Source:     "https://ci.example.com/build/42",
		ObservedAt: "2026-10-29T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateFact backed: %v", err)
	}
	confirmed, err := store.ReviewFact(ctx, testScope(), a.ID, backed.ID, FactReview{Status: "confirmed", IfVersion: backed.Version})
	if err != nil {
		t.Fatalf("ReviewFact confirmed: %v", err)
	}
	if confirmed.Status != "confirmed" || confirmed.VerifiedAt == "" {
		t.Fatalf("confirmed = %+v, want status confirmed and verified_at set", confirmed)
	}
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, backed.ID, FactReview{Status: "stale", IfVersion: backed.Version}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale review err = %v, want ErrConflict", err)
	}

	// A candidate referencing the confirmed fact must NOT supersede it yet.
	successor, err := store.CreateFact(ctx, testScope(), a.ID, FactInput{
		Title: "生产版本", Content: "v2.4.0", Scope: `{"service":"api","env":"prod"}`,
		Source:     "https://ci.example.com/build/99",
		ObservedAt: "2026-10-30T00:00:00Z", SupersedesID: confirmed.ID,
	})
	if err != nil {
		t.Fatalf("CreateFact successor: %v", err)
	}
	if got := factStatus(t, store, a.ID, confirmed.ID); got != "confirmed" {
		t.Fatalf("predecessor status after candidate create = %q, want confirmed (supersede deferred)", got)
	}
	// Confirming the successor is what retires the predecessor.
	if _, err := store.ReviewFact(ctx, testScope(), a.ID, successor.ID, FactReview{Status: "confirmed", IfVersion: successor.Version}); err != nil {
		t.Fatalf("ReviewFact successor: %v", err)
	}
	if got := factStatus(t, store, a.ID, confirmed.ID); got != "superseded" {
		t.Fatalf("predecessor status after confirm = %q, want superseded", got)
	}

	withCandidate, err := store.SetFactCandidate(ctx, testScope(), a.ID, successor.ID, "mem-cand-1")
	if err != nil {
		t.Fatalf("SetFactCandidate: %v", err)
	}
	if withCandidate.MemoryCandidateID != "mem-cand-1" {
		t.Fatalf("memory_candidate_id = %q", withCandidate.MemoryCandidateID)
	}
}

// TestRecallRangesWithChinese verifies current/related/all recall and Chinese
// text matching.
func TestRecallRangesWithChinese(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "主线A")
	b := mustCreate(t, store, testScope(), "主线B")
	unrelated := mustCreate(t, store, testScope(), "主线C")

	if _, err := store.AddRelation(ctx, testScope(), a.ID, RelationInput{ToID: b.ID, Kind: "related_to"}); err != nil {
		t.Fatalf("relation: %v", err)
	}
	mk := func(contextID, title, content string) {
		if _, err := store.CreateFact(ctx, testScope(), contextID, FactInput{Title: title, Content: content, Scope: `{"service":"api"}`}); err != nil {
			t.Fatalf("CreateFact %s: %v", title, err)
		}
	}
	mk(a.ID, "部署信息", "生产环境部署版本 v2.3.1")
	mk(b.ID, "部署信息", "预发环境部署版本 v2.2.0")
	mk(unrelated.ID, "部署信息", "其他主线部署版本 v1.0.0")

	current, err := store.Recall(ctx, testScope(), a.ID, "current", "部署版本")
	if err != nil {
		t.Fatalf("Recall current: %v", err)
	}
	if len(current) != 1 || current[0].ContextID != a.ID {
		t.Fatalf("current recall = %+v, want only context A", current)
	}
	related, err := store.Recall(ctx, testScope(), a.ID, "direct", "部署版本")
	if err != nil {
		t.Fatalf("Recall direct: %v", err)
	}
	if len(related) != 2 {
		t.Fatalf("direct recall = %d facts, want 2 (A and B)", len(related))
	}
	all, err := store.Recall(ctx, testScope(), a.ID, "all", "部署版本")
	if err != nil {
		t.Fatalf("Recall all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all recall = %d facts, want 3", len(all))
	}
	// Empty query returns everything within range.
	if got, err := store.Recall(ctx, testScope(), a.ID, "current", ""); err != nil || len(got) != 1 {
		t.Fatalf("empty-query recall = %d (%v), want 1", len(got), err)
	}
	if _, err := store.Recall(ctx, testScope(), a.ID, "bogus", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown scope err = %v, want ErrInvalid", err)
	}
}

// TestRecallCrossTenantMasked verifies recall never crosses scope.
func TestRecallCrossTenantMasked(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "A")
	if _, err := store.CreateFact(context.Background(), testScope(), a.ID, FactInput{Title: "密钥轮换", Content: "已完成", Scope: `{"service":"api"}`}); err != nil {
		t.Fatalf("CreateFact: %v", err)
	}
	other := Scope{TenantID: "tenant-b", OwnerID: "owner-1"}
	if _, err := store.Recall(ctx, other, a.ID, "all", "密钥"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope recall err = %v, want ErrNotFound", err)
	}
}

// TestDelegationIdempotency verifies reserve replay keeps the pack, complete is
// idempotent, and a conflicting card is rejected.
func TestDelegationIdempotency(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "delegate")
	first, err := store.ReserveDelegation(ctx, testScope(), a.ID, Delegation{
		RequestID: "req-1", Title: "实现导出", Pack: `{"v":1}`, ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("ReserveDelegation: %v", err)
	}
	if first.State != delegationStateReserved || first.Pack != `{"v":1}` {
		t.Fatalf("reservation = %+v", first)
	}
	replay, err := store.ReserveDelegation(ctx, testScope(), a.ID, Delegation{
		RequestID: "req-1", Title: "改写标题", Pack: `{"v":999}`,
	})
	if err != nil {
		t.Fatalf("ReserveDelegation replay: %v", err)
	}
	if replay.ID != first.ID || replay.Pack != `{"v":1}` || replay.Title != "实现导出" {
		t.Fatalf("replay mutated reservation: %+v", replay)
	}
	completed, err := store.CompleteDelegation(ctx, testScope(), a.ID, first.ID, "card-1", "proj-1")
	if err != nil {
		t.Fatalf("CompleteDelegation: %v", err)
	}
	if completed.State != delegationStateCompleted || completed.CardID != "card-1" {
		t.Fatalf("completed = %+v", completed)
	}
	if again, err := store.CompleteDelegation(ctx, testScope(), a.ID, first.ID, "card-1", "proj-1"); err != nil || again.CardID != "card-1" {
		t.Fatalf("idempotent complete = %+v err=%v", again, err)
	}
	if _, err := store.CompleteDelegation(ctx, testScope(), a.ID, first.ID, "card-2", "proj-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting card err = %v, want ErrConflict", err)
	}
}

// TestRecordEventDedup verifies dedup keys collapse duplicate events.
func TestRecordEventDedup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, store, testScope(), "events")
	for i := 0; i < 3; i++ {
		if err := store.RecordEvent(ctx, testScope(), a.ID, "task_status", "任务完成", "taskboard", "card-1:done"); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
	detail, _ := store.Get(ctx, testScope(), a.ID)
	count := 0
	for _, ev := range detail.Events {
		if ev.Kind == "task_status" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("deduped task_status events = %d, want 1", count)
	}
}

// TestOpenRejectsEmptyPath guards the constructor.
func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open("  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open empty err = %v, want ErrInvalid", err)
	}
	_ = strings.TrimSpace("")
}
