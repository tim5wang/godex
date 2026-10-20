package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONQueueLoaderIgnoresHistoryAndHonorsLatestQueue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "session")
	cp := filepath.Join(dir, checkpointsDir, "cp")
	if err := os.MkdirAll(cp, 0755); err != nil {
		t.Fatal(err)
	}
	// Full checkpoint loading would fail; lightweight queue probing must not
	// even open these state paths.
	if err := os.Mkdir(filepath.Join(cp, stateFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, stateFile), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, checkpointPointer), `{"current":"cp"}`)
	write(filepath.Join(cp, turnQueueFile), `[{"id":"checkpoint"}]`)
	write(filepath.Join(dir, turnQueueFile), `[]`)
	now := time.Now()
	setTime := func(path string, at time.Time) {
		t.Helper()
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	setTime(filepath.Join(dir, checkpointPointer), now.Add(-time.Second))
	setTime(filepath.Join(dir, turnQueueFile), now)
	s := NewJSONStore(root)
	data, ok, err := s.LoadQueue(context.Background(), "session")
	if err != nil || !ok || string(data) != `[]` {
		t.Fatalf("newer cancellation lost: %s %v %v", data, ok, err)
	}
	setTime(filepath.Join(dir, checkpointPointer), now.Add(time.Second))
	data, ok, err = s.LoadQueue(context.Background(), "session")
	if err != nil || !ok || string(data) != `[{"id":"checkpoint"}]` {
		t.Fatalf("committed checkpoint ignored: %s %v %v", data, ok, err)
	}
}

func TestSQLiteQueueLoaderPrefersEmptyRootToOldCheckpoint(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := SessionData{SessionID: "session", Manifest: []byte(`{}`), State: []byte(`{}`), Queue: []byte(`[]`), Checkpoint: &CheckpointData{ID: "cp", Queue: []byte(`[{"id":"old"}]`)}}
	if err := s.Save(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	queue, ok, err := s.LoadQueue(context.Background(), "session")
	if err != nil || !ok || string(queue) != `[]` {
		t.Fatalf("queue: %s %v %v", queue, ok, err)
	}
}
