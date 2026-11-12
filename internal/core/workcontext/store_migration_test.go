package workcontext

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestOpenUpgradesAnExistingV4Database pins the upgrade path for databases created before
// migration 5.
//
// The attention-yield numerator reads wc_todos.completed_at, which was added by ALTER in
// migration 5. Two ways that could break existing installs, both asserted here:
//
//  1. The column never appears, so every yield read fails. (Migration 5 is written as an
//     ALTER rather than by editing migration 1, so fresh databases run CREATE-then-ALTER
//     and existing ones run ALTER alone. Editing migration 1 instead would make the ALTER
//     fail on fresh databases with "duplicate column name".)
//  2. The column appears pre-filled, which would silently count historical completions as
//     "finished today" and inflate the user's own yield on the very first read.
//
// The fixture is built by hand from migrations 1..4 (the real pre-5 shape), not by
// opening a store and dropping the column: this exercises the actual upgrade path.
func TestOpenUpgradesAnExistingV4Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-v4.sqlite")

	// Build a v4 database: apply migrations 1..4 verbatim, exactly as an older binary did.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// The migration ledger is created by Open() itself, not by any migration, so the raw
	// fixture has to create it too (matching what an older binary left behind).
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS wc_schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	for _, version := range []int{1, 2, 3, 4} {
		statements, ok := migrations[version]
		if !ok {
			t.Fatalf("missing migration %d in fixture build", version)
		}
		for _, stmt := range statements {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("migration %d: %v", version, err)
			}
		}
		if _, err := db.Exec(`INSERT INTO wc_schema_migrations(version, applied_at) VALUES(?, ?)`, version, nowRFC3339()); err != nil {
			t.Fatalf("record migration %d: %v", version, err)
		}
	}
	// A legacy install with one ALREADY-finished plan item, written before the column existed.
	if _, err := db.Exec(`INSERT INTO wc_contexts(tenant_id, id, owner_id, title, background, goal, status, version, checkpoint_json, created_at, updated_at, archived_at)
		VALUES('tenant-a', 'ctx-1', 'owner-1', 'legacy', '', 'goal', 'active', 1, '{}', ?, ?, '')`, nowRFC3339(), nowRFC3339()); err != nil {
		t.Fatalf("seed context: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO wc_todos(tenant_id, id, context_id, owner_id, text, status, assignee, due_at, source, position, version, created_at, updated_at)
		VALUES('tenant-a', 'todo-legacy', 'ctx-1', 'owner-1', '早已完成的一项', 'done', '', '', 'human', 1, 1, ?, ?)`, nowRFC3339(), nowRFC3339()); err != nil {
		t.Fatalf("seed legacy todo: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Now open it the way the current binary does.
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy database: %v", err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM wc_schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("expected the legacy database to be upgraded to %d, got %d", schemaVersion, version)
	}

	// The legacy item must survive and must NOT be counted as finished today: its
	// completion time is unknown, and guessing "now" would fake the numerator. The window
	// starts far enough back to include anything, so the only reason this reads 0 is the
	// missing stamp.
	count, err := store.CountTodosCompletedSince(context.Background(), testScope(), time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CountTodosCompletedSince on upgraded database: %v", err)
	}
	if count != 0 {
		t.Fatalf("a pre-migration done item has an unknown completion time and must not count, got %d", count)
	}

	// The item itself is intact, and reads back with an empty completion stamp.
	items, err := store.ListTodos(context.Background(), testScope(), "ctx-1")
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("legacy plan item should survive the upgrade, got %d items", len(items))
	}
	if items[0].Status != "done" {
		t.Fatalf("legacy status changed during upgrade: %q", items[0].Status)
	}
	if items[0].CompletedAt != "" {
		t.Fatalf("legacy item must not be backfilled with a completion time, got %q", items[0].CompletedAt)
	}
	input, _, err := store.BeginDream(context.Background(), testScope(), "ctx-1", false, false, time.Now())
	if err != nil || input == nil {
		t.Fatal("legacy Dream inventory missing", err)
	}
	found := false
	for _, record := range input.Records {
		if record.Kind == "todo" && record.EntityID == "todo-legacy" {
			found = true
		}
	}
	if !found {
		t.Fatal("pre-migration plan was omitted from initial Dream input")
	}
}
