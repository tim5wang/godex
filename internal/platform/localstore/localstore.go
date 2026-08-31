// Package localstore provides JSON-file repository adapters for domain
// managers. Domain packages own the repository contracts; this package owns
// filesystem paths, serialization, and atomic writes.
package localstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// TaskRepository persists one task per numeric JSON filename.
type TaskRepository struct {
	dir string
}

// NewTaskRepository creates a JSON-file task repository rooted at dir.
func NewTaskRepository(dir string) *TaskRepository {
	return &TaskRepository{dir: dir}
}

// NewTaskManager wires the domain task manager to local JSON storage.
func NewTaskManager(dir string) *task.Manager {
	return task.NewManager(NewTaskRepository(dir))
}

func (r *TaskRepository) LoadAll() ([]task.FileTask, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	tasks := make([]task.FileTask, 0, len(entries))
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if ext != ".json" {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ext)); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			continue
		}
		var item task.FileTask
		if err := json.Unmarshal(data, &item); err == nil {
			tasks = append(tasks, item)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func (r *TaskRepository) Save(item task.FileTask) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(r.dir, fmt.Sprintf("%d.json", item.ID)), data, 0644)
}

func (r *TaskRepository) Delete(id int) error {
	err := os.Remove(filepath.Join(r.dir, fmt.Sprintf("%d.json", id)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// TodoRepository persists a complete todo snapshot in todos.json.
type TodoRepository struct {
	dir string
}

// NewTodoRepository creates a todos.json repository rooted at dir.
func NewTodoRepository(dir string) *TodoRepository {
	return &TodoRepository{dir: dir}
}

// NewTodoManager wires the domain todo manager to local JSON storage.
func NewTodoManager(dir string) *todo.Manager {
	return todo.NewManager(NewTodoRepository(dir))
}

// NewTodoManagerForSession scopes todos to <sessionsDir>/<sessionID>. An
// empty session ID retains the legacy <sessionsDir>/todos.json layout; an
// empty base directory returns an in-memory manager.
func NewTodoManagerForSession(sessionsDir, sessionID string) *todo.Manager {
	dir := strings.TrimSpace(sessionsDir)
	if dir == "" {
		return todo.NewManager(nil)
	}
	if id := strings.TrimSpace(sessionID); id != "" {
		dir = filepath.Join(dir, id)
	}
	return NewTodoManager(dir)
}

func (r *TodoRepository) Load() ([]todo.Item, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, "todos.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []todo.Item
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *TodoRepository) Save(items []todo.Item) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.dir, 0755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(r.dir, "todos.json"), data, 0644)
}

// MessageRepository persists one message per ID-named JSON file.
type MessageRepository struct {
	dir string
}

// NewMessageRepository creates an ID-named JSON message repository at dir.
func NewMessageRepository(dir string) *MessageRepository {
	return &MessageRepository{dir: dir}
}

// NewMessageBus wires the domain message bus to local JSON storage.
func NewMessageBus(dir string) *message.Bus {
	return message.NewBus(NewMessageRepository(dir))
}

func (r *MessageRepository) LoadAll() ([]message.Message, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	messages := make([]message.Message, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			continue
		}
		var item message.Message
		if err := json.Unmarshal(data, &item); err == nil {
			if item.ID == "" {
				item.ID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			}
			messages = append(messages, item)
		}
	}
	return messages, nil
}

func (r *MessageRepository) Save(item message.Message) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(r.dir, item.ID+".json"), data, 0644)
}

func (r *MessageRepository) Delete(id string) error {
	err := os.Remove(filepath.Join(r.dir, id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
