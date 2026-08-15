package sessionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestJSONStoreLoadsExistingSessionLayoutAndOmitsMissingOptionalFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionID := "web-existing"
	sessionDir := filepath.Join(root, sessionID)
	mustMkdir(t, filepath.Join(sessionDir, "checkpoints", "cp-1"))
	writeFile(t, filepath.Join(sessionDir, "manifest.json"), `{"session_id":"web-existing"}`)
	writeFile(t, filepath.Join(sessionDir, "state.json"), `{"messages":[]}`)
	writeFile(t, filepath.Join(sessionDir, "turns.json"), `[{"id":"turn-1"}]`)
	writeFile(t, filepath.Join(sessionDir, "events.jsonl"), "{\"type\":\"accepted\"}\n")
	writeFile(t, filepath.Join(sessionDir, "graph.json"), `{"branches":[]}`)
	writeFile(t, filepath.Join(sessionDir, "checkpoint.json"), `{"current":"cp-1"}`)
	writeFile(t, filepath.Join(sessionDir, "checkpoints", "cp-1", "manifest.json"), `{"session_id":"web-existing","checkpoint":true}`)
	writeFile(t, filepath.Join(sessionDir, "checkpoints", "cp-1", "state.json"), `{"messages":["checkpoint"]}`)

	store := NewJSONStore(root)
	data, ok, err := store.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ok {
		t.Fatal("expected session found")
	}
	if data.SessionID != sessionID {
		t.Fatalf("session id = %q", data.SessionID)
	}
	assertRawEqual(t, data.Manifest, `{"session_id":"web-existing"}`)
	assertRawEqual(t, data.State, `{"messages":[]}`)
	assertRawEqual(t, data.Turns, `[{"id":"turn-1"}]`)
	assertRawEqual(t, data.EventJournal, "{\"type\":\"accepted\"}\n")
	assertRawEqual(t, data.Graph, `{"branches":[]}`)
	if len(data.Timeline) != 0 || len(data.Queue) != 0 {
		t.Fatalf("expected missing optional files omitted, got timeline=%q queue=%q", data.Timeline, data.Queue)
	}
	if data.Checkpoint == nil {
		t.Fatal("expected checkpoint")
	}
	if data.Checkpoint.ID != "cp-1" {
		t.Fatalf("checkpoint id = %q", data.Checkpoint.ID)
	}
	assertRawEqual(t, data.Checkpoint.Pointer, `{"current":"cp-1"}`)
	assertRawEqual(t, data.Checkpoint.Manifest, `{"session_id":"web-existing","checkpoint":true}`)
	assertRawEqual(t, data.Checkpoint.State, `{"messages":["checkpoint"]}`)

	_, ok, err = store.Load(ctx, "missing")
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if ok {
		t.Fatal("expected missing session not found")
	}
}

func TestJSONStoreLoadManifestDoesNotRequireState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionID := "manifest-only"
	dir := filepath.Join(root, sessionID)
	mustMkdir(t, dir)
	writeFile(t, filepath.Join(dir, "manifest.json"), `{"session_id":"manifest-only","title":"Fast list"}`)
	// A corrupt state proves the metadata path did not read or decode it.
	writeFile(t, filepath.Join(dir, "state.json"), `{not-json`)

	manifest, ok, err := NewJSONStore(root).LoadManifest(ctx, sessionID)
	if err != nil || !ok {
		t.Fatalf("load manifest ok=%v err=%v", ok, err)
	}
	assertRawEqual(t, manifest, `{"session_id":"manifest-only","title":"Fast list"}`)
}

func TestJSONStoreSaveListDeleteAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewJSONStore(root)
	data := sampleSessionData("json-session")
	if err := store.Save(ctx, data); err != nil {
		t.Fatalf("save: %v", err)
	}

	ids, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"json-session"}) {
		t.Fatalf("ids = %v", ids)
	}
	loaded, ok, err := store.Load(ctx, "json-session")
	if err != nil || !ok {
		t.Fatalf("load saved ok=%v err=%v", ok, err)
	}
	assertSessionDataEqual(t, loaded, data)

	diag := store.Diagnostics(ctx)
	if diag.Backend != string(BackendJSON) || diag.Path != root || !diag.Healthy || diag.Error != "" {
		t.Fatalf("unexpected diagnostics: %+v", diag)
	}

	if err := store.Delete(ctx, "json-session"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, ok, err = store.Load(ctx, "json-session")
	if err != nil {
		t.Fatalf("load deleted: %v", err)
	}
	if ok {
		t.Fatal("expected deleted session missing")
	}
}

func TestSQLiteStoreRestoresAfterRestartAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	data := sampleSessionData("sqlite-session")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	if err := store.Save(ctx, data); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen sqlite store: %v", err)
	}
	defer reopened.Close()
	loaded, ok, err := reopened.Load(ctx, "sqlite-session")
	if err != nil || !ok {
		t.Fatalf("load after restart ok=%v err=%v", ok, err)
	}
	assertSessionDataEqual(t, loaded, data)
	manifest, ok, err := reopened.LoadManifest(ctx, "sqlite-session")
	if err != nil || !ok {
		t.Fatalf("load sqlite manifest ok=%v err=%v", ok, err)
	}
	assertRawEqual(t, manifest, string(data.Manifest))

	diag := reopened.Diagnostics(ctx)
	if diag.Backend != string(BackendSQLite) || diag.SQLitePath != path || diag.SchemaVersion != 1 || !diag.Healthy || diag.Error != "" {
		t.Fatalf("unexpected diagnostics: %+v", diag)
	}
}

func TestCopySessionBothWays(t *testing.T) {
	ctx := context.Background()
	jsonStore := NewJSONStore(t.TempDir())
	sqliteStore, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	defer sqliteStore.Close()

	fromJSON := sampleSessionData("copy-json")
	if err := jsonStore.Save(ctx, fromJSON); err != nil {
		t.Fatalf("save json: %v", err)
	}
	if err := CopySession(ctx, sqliteStore, jsonStore, "copy-json"); err != nil {
		t.Fatalf("copy json to sqlite: %v", err)
	}
	loadedSQLite, ok, err := sqliteStore.Load(ctx, "copy-json")
	if err != nil || !ok {
		t.Fatalf("load sqlite copy ok=%v err=%v", ok, err)
	}
	assertSessionDataEqual(t, loadedSQLite, fromJSON)

	fromSQLite := sampleSessionData("copy-sqlite")
	if err := sqliteStore.Save(ctx, fromSQLite); err != nil {
		t.Fatalf("save sqlite: %v", err)
	}
	if err := CopySession(ctx, jsonStore, sqliteStore, "copy-sqlite"); err != nil {
		t.Fatalf("copy sqlite to json: %v", err)
	}
	loadedJSON, ok, err := jsonStore.Load(ctx, "copy-sqlite")
	if err != nil || !ok {
		t.Fatalf("load json copy ok=%v err=%v", ok, err)
	}
	assertSessionDataEqual(t, loadedJSON, fromSQLite)
}

func TestSQLiteStoreListDelete(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	defer store.Close()
	for _, id := range []string{"b", "a"} {
		if err := store.Save(ctx, sampleSessionData(id)); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	ids, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("ids = %v", ids)
	}
	if err := store.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ids, err = store.List(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if !reflect.DeepEqual(ids, []string{"b"}) {
		t.Fatalf("ids after delete = %v", ids)
	}
}

func sampleSessionData(id string) SessionData {
	return SessionData{
		SessionID:    id,
		Manifest:     json.RawMessage(`{"session_id":"` + id + `"}`),
		State:        json.RawMessage(`{"messages":["root"]}`),
		Timeline:     json.RawMessage(`[{"event":"root"}]`),
		Turns:        json.RawMessage(`[{"id":"turn-1"}]`),
		Queue:        json.RawMessage(`[{"id":"queued"}]`),
		EventJournal: json.RawMessage("{\"type\":\"root\"}\n{\"type\":\"done\"}\n"),
		Graph:        json.RawMessage(`{"nodes":["root"]}`),
		Checkpoint: &CheckpointData{
			ID:       "cp-1",
			Pointer:  json.RawMessage(`{"current":"cp-1"}`),
			Manifest: json.RawMessage(`{"session_id":"` + id + `","checkpoint":true}`),
			State:    json.RawMessage(`{"messages":["checkpoint"]}`),
			Timeline: json.RawMessage(`[{"event":"checkpoint"}]`),
			Turns:    json.RawMessage(`[{"id":"turn-cp"}]`),
			Queue:    json.RawMessage(`[{"id":"queue-cp"}]`),
		},
	}
}

func assertSessionDataEqual(t *testing.T, got, want SessionData) {
	t.Helper()
	if got.SessionID != want.SessionID {
		t.Fatalf("session id got %q want %q", got.SessionID, want.SessionID)
	}
	assertRawEqual(t, got.Manifest, string(want.Manifest))
	assertRawEqual(t, got.State, string(want.State))
	assertRawEqual(t, got.Timeline, string(want.Timeline))
	assertRawEqual(t, got.Turns, string(want.Turns))
	assertRawEqual(t, got.Queue, string(want.Queue))
	assertRawEqual(t, got.EventJournal, string(want.EventJournal))
	assertRawEqual(t, got.Graph, string(want.Graph))
	if (got.Checkpoint == nil) != (want.Checkpoint == nil) {
		t.Fatalf("checkpoint got nil=%v want nil=%v", got.Checkpoint == nil, want.Checkpoint == nil)
	}
	if got.Checkpoint == nil {
		return
	}
	if got.Checkpoint.ID != want.Checkpoint.ID {
		t.Fatalf("checkpoint id got %q want %q", got.Checkpoint.ID, want.Checkpoint.ID)
	}
	assertRawEqual(t, got.Checkpoint.Pointer, string(want.Checkpoint.Pointer))
	assertRawEqual(t, got.Checkpoint.Manifest, string(want.Checkpoint.Manifest))
	assertRawEqual(t, got.Checkpoint.State, string(want.Checkpoint.State))
	assertRawEqual(t, got.Checkpoint.Timeline, string(want.Checkpoint.Timeline))
	assertRawEqual(t, got.Checkpoint.Turns, string(want.Checkpoint.Turns))
	assertRawEqual(t, got.Checkpoint.Queue, string(want.Checkpoint.Queue))
}

func assertRawEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if !bytes.Equal(got, json.RawMessage(want)) {
		t.Fatalf("raw got %q want %q", got, want)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func sorted(ids []string) []string {
	sort.Strings(ids)
	return ids
}
