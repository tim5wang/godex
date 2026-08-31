package localstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/domain/message"
)

func TestTaskRepositoryIgnoresNonTaskJSONFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"id":1,"subject":"first","status":"pending"}`), 0644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "todos.json"), []byte(`[]`), 0644); err != nil {
		t.Fatalf("write todos file: %v", err)
	}

	tasks := NewTaskManager(dir).List()
	if len(tasks) != 1 || tasks[0].ID != 1 {
		t.Fatalf("expected only numeric task file to be loaded, got %#v", tasks)
	}
}

func TestTodoManagerForSessionScopesStorage(t *testing.T) {
	sessions := t.TempDir()
	a := NewTodoManagerForSession(sessions, "session-A")
	b := NewTodoManagerForSession(sessions, "session-B")

	if _, err := a.Add("from A", ""); err != nil {
		t.Fatalf("A.Add: %v", err)
	}
	if got := len(b.List()); got != 0 {
		t.Fatalf("session B should not see session A todos, got %d", got)
	}
	if _, err := b.Add("from B", ""); err != nil {
		t.Fatalf("B.Add: %v", err)
	}
	if got := len(NewTodoManagerForSession(sessions, "session-A").List()); got != 1 {
		t.Fatalf("expected reopened session A to contain 1 todo, got %d", got)
	}
	if got := len(NewTodoManagerForSession(sessions, "session-B").List()); got != 1 {
		t.Fatalf("expected reopened session B to contain 1 todo, got %d", got)
	}
}

func TestTodoManagerForSessionEmptyIDUsesBaseDirectory(t *testing.T) {
	base := t.TempDir()
	manager := NewTodoManagerForSession(base, "")
	if _, err := manager.Add("global", ""); err != nil {
		t.Fatalf("add global todo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "todos.json")); err != nil {
		t.Fatalf("expected todos.json directly under base directory: %v", err)
	}
}

func TestMessageRepositoryPersistsAndDeletesByID(t *testing.T) {
	dir := t.TempDir()
	bus := NewMessageBus(dir)
	if err := bus.Send(message.Message{ID: "message-1", From: "lead", To: "worker", Content: "hello"}); err != nil {
		t.Fatalf("send message: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "message-1.json")); err != nil {
		t.Fatalf("expected persisted message: %v", err)
	}

	reloaded := NewMessageBus(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if got := len(reloaded.ReadInbox("worker")); got != 1 {
		t.Fatalf("expected one loaded message, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "message-1.json")); !os.IsNotExist(err) {
		t.Fatalf("expected persisted message to be deleted, stat err=%v", err)
	}
}
