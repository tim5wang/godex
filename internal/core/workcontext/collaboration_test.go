package workcontext

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestMainConversationRolesAndIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx, scope := context.Background(), testScope()
	c := mustCreate(t, s, scope, "roles")
	worker, err := s.LinkSession(ctx, scope, c.ID, SessionLink{SessionID: "taskboard-worker", Role: "worker"})
	if err != nil || worker.Role != "worker" {
		t.Fatal(worker, err)
	}
	first, err := s.LinkSession(ctx, scope, c.ID, SessionLink{SessionID: "first"})
	if err != nil || first.Role != "lead" {
		t.Fatal(first, err)
	}
	second, err := s.LinkSession(ctx, scope, c.ID, SessionLink{SessionID: "second"})
	if err != nil || second.Role != "member" {
		t.Fatal(second, err)
	}
	if _, err := s.SetMainSession(ctx, scope, c.ID, second.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMainSession(ctx, scope, c.ID, worker.SessionID); !errors.Is(err, ErrInvalid) {
		t.Fatal("worker became lead", err)
	}
	if _, err := s.SetMainSession(ctx, Scope{TenantID: scope.TenantID, OwnerID: "intruder"}, c.ID, first.SessionID); !errors.Is(err, ErrNotFound) {
		t.Fatal("owner leaked", err)
	}
	if _, err := s.SetMainSession(ctx, Scope{TenantID: "other-tenant", OwnerID: scope.OwnerID}, c.ID, first.SessionID); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant leaked", err)
	}
	if err := s.UnlinkSession(ctx, scope, c.ID, second.SessionID); err != nil {
		t.Fatal(err)
	}
	role, _ := s.SessionRole(ctx, scope, c.ID, first.SessionID)
	if role != "lead" {
		t.Fatal("oldest member was not promoted", role)
	}
	other := mustCreate(t, s, scope, "references")
	ref, err := s.LinkSession(ctx, scope, other.ID, SessionLink{SessionID: first.SessionID})
	if err != nil || ref.Role != "member" {
		t.Fatal("reference incorrectly became lead", ref, err)
	}
	if _, err := s.SetMainSession(ctx, scope, other.ID, first.SessionID); !errors.Is(err, ErrConflict) {
		t.Fatal("secondary link became an unbound leader", err)
	}
	archived := "archived"
	if _, err := s.Patch(ctx, scope, c.ID, ContextPatch{IfVersion: c.Version, Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMainSession(ctx, scope, c.ID, first.SessionID); !errors.Is(err, ErrArchived) {
		t.Fatal("archived desktop edited", err)
	}
}

func TestResourceMentionsAreIdempotentAndSafe(t *testing.T) {
	s := newTestStore(t)
	ctx, scope := context.Background(), testScope()
	c := mustCreate(t, s, scope, "resources")
	input := ResourceInput{Title: "curated", Kind: "directory", Locator: "/srv/project", NodeID: "node-a", Purpose: "source code", Source: "session://original"}
	one, err := s.AddResource(ctx, scope, c.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, scope, c.ID)
	input.Title, input.Purpose = "automatic", "mention"
	two, err := s.AddResource(ctx, scope, c.ID, input)
	if err != nil || one.ID != two.ID || two.Title != "curated" || two.Purpose != "source code" || two.Source != one.Source {
		t.Fatal("curated metadata overwritten", two, err)
	}
	after, _ := s.Get(ctx, scope, c.ID)
	if len(after.Resources) != 1 || len(after.Events) != len(before.Events) {
		t.Fatal("duplicate mention made duplicate resources/events")
	}
	for _, locator := range []string{"https://user:pass@example.com/", "https://example.com/?token=secret", "https://metadata.google.internal/"} {
		if _, err := s.AddResource(ctx, scope, c.ID, ResourceInput{Title: "unsafe", Kind: "web", Locator: locator}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe locator accepted: %v", err)
		}
	}
	if _, err := s.AddResource(ctx, scope, c.ID, ResourceInput{Title: "unbound", Kind: "directory", Locator: "/srv/project"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbound directory accepted", err)
	}
}

func TestEventDetailsUseImmutableCheckpointAndPersistTurnReport(t *testing.T) {
	s := newTestStore(t)
	ctx, scope := context.Background(), testScope()
	c := mustCreate(t, s, scope, "history")
	for _, summary := range []string{"original", "current"} {
		if _, err := s.SaveCheckpoint(ctx, scope, c.ID, 0, Checkpoint{Summary: summary, EvidenceRefs: []string{"report://1"}}); err != nil {
			t.Fatal(err)
		}
	}
	detail, _ := s.Get(ctx, scope, c.ID)
	found := false
	for _, event := range detail.Events {
		if event.Kind != "checkpoint_saved" {
			continue
		}
		out, err := s.EventDetail(ctx, scope, c.ID, event.ID)
		if err != nil {
			t.Fatal(err)
		}
		if out.Checkpoint.Revision == 1 {
			found = true
			if out.Checkpoint.Summary != "original" {
				t.Fatal("current text passed off as history")
			}
		}
		if _, err := s.EventDetail(ctx, Scope{OwnerID: "intruder"}, c.ID, event.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("event leaked", err)
		}
	}
	if !found {
		t.Fatal("old checkpoint missing")
	}
	for i := 0; i < 2; i++ {
		inserted, err := s.RecordEventIfNew(ctx, scope, c.ID, SessionTurnEventKind, "repair · completed", "session://s1", "session:s1:turn:t1", EventDetail{Request: "repair", Result: "tests passed (agent report)", Status: "completed"})
		if err != nil || inserted != (i == 0) {
			t.Fatal(inserted, err)
		}
	}
	detail, _ = s.Get(ctx, scope, c.ID)
	for _, event := range detail.Events {
		if event.Kind == SessionTurnEventKind {
			out, err := s.EventDetail(ctx, scope, c.ID, event.ID)
			if err != nil || out.SessionID != "s1" || out.TurnID != "t1" || out.Result == "" {
				t.Fatal(out, err)
			}
		}
	}
}

func TestMainConversationChoiceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, scope := context.Background(), testScope()
	c := mustCreate(t, s, scope, "persisted roles")
	for _, sid := range []string{"one", "two"} {
		if _, err := s.LinkSession(ctx, scope, c.ID, SessionLink{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetMainSession(ctx, scope, c.ID, "two"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	role, err := s.SessionRole(ctx, scope, c.ID, "two")
	if err != nil || role != "lead" {
		t.Fatal("main role was not persisted", role, err)
	}
	index, err := s.SessionIndex(ctx, scope)
	if err != nil || len(index[c.ID]) != 2 || index[c.ID][1].Role != "lead" {
		t.Fatal(index, err)
	}
}

func TestCollaborationMigrationFromV11(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE wc_schema_migrations(version INTEGER PRIMARY KEY,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db}
	for version := 1; version <= 11; version++ {
		if err := legacy.applyMigration(version, migrations[version]); err != nil {
			t.Fatal(err)
		}
	}
	stamp := nowRFC3339()
	if _, err = db.Exec(`INSERT INTO wc_contexts(tenant_id,id,owner_id,title,background,goal,status,version,checkpoint_json,created_at,updated_at) VALUES('t','c','u','legacy','','','active',1,'{}',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for i, sid := range []string{"web-first", "web-second"} {
		if _, err = db.Exec(`INSERT INTO wc_context_sessions(tenant_id,id,context_id,session_id,topic,created_at) VALUES('t',?,'c',?,'topic',?)`, string(rune('a'+i)), sid, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO wc_session_bindings(session_id,tenant_id,owner_id,context_id) VALUES('web-first','t','u','c'),('web-second','t','u','c')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	detail, err := s.Get(context.Background(), Scope{TenantID: "t", OwnerID: "u"}, "c")
	if err != nil || len(detail.Sessions) != 2 || detail.Sessions[0].Role != "lead" || detail.Sessions[1].Role != "member" {
		t.Fatal(detail.Sessions, err)
	}
}
