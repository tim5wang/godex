package todo

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status represents task status.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusDeleted    Status = "deleted"
)

// Item represents a todo item.
type Item struct {
	ID         int       `json:"id"`
	Content    string    `json:"content"`
	Status     Status    `json:"status"`
	ActiveForm string    `json:"active_form"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Repository persists todo snapshots without exposing a storage backend to
// the domain manager.
type Repository interface {
	Load() ([]Item, error)
	Save([]Item) error
}

// Manager handles todo items.
type Manager struct {
	mu         sync.RWMutex
	items      []Item
	nextID     int
	repository Repository
}

// NewManager creates a new todo manager backed by repository. A nil
// repository creates an in-memory manager.
func NewManager(repository Repository) *Manager {
	m := &Manager{items: []Item{}, nextID: 1, repository: repository}
	m.load()
	return m
}

func (m *Manager) load() {
	if m.repository == nil {
		return
	}
	items, err := m.repository.Load()
	if err != nil {
		return
	}
	m.items = cloneItems(items)
	for _, item := range items {
		if item.ID >= m.nextID {
			m.nextID = item.ID + 1
		}
	}
}

func (m *Manager) persist(items []Item) error {
	if m.repository == nil {
		return nil
	}
	return m.repository.Save(cloneItems(items))
}

// Add adds a new todo item.
func (m *Manager) Add(content, activeText string) (*Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	item := Item{
		ID:         m.nextID,
		Content:    content,
		Status:     StatusPending,
		ActiveForm: activeText,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	updated := append(cloneItems(m.items), item)
	if err := m.persist(updated); err != nil {
		return nil, err
	}

	m.items = updated
	m.nextID++
	return &item, nil
}

// Update updates a todo item.
func (m *Manager) Update(id int, status Status) (*Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	updated := cloneItems(m.items)
	for i, item := range updated {
		if item.ID == id {
			updated[i].Status = status
			updated[i].UpdatedAt = time.Now()
			if err := m.persist(updated); err != nil {
				return nil, err
			}
			m.items = updated
			return &updated[i], nil
		}
	}
	return nil, fmt.Errorf("todo not found: %d", id)
}

// Replace atomically replaces the entire todo list.
func (m *Manager) Replace(items []Item) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	replaced := make([]Item, 0, len(items))
	for i, item := range items {
		createdAt := item.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		updatedAt := item.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}
		replaced = append(replaced, Item{
			ID:         i + 1,
			Content:    item.Content,
			Status:     item.Status,
			ActiveForm: item.ActiveForm,
			CreatedAt:  createdAt,
			UpdatedAt:  updatedAt,
		})
	}

	if err := m.persist(replaced); err != nil {
		return nil, err
	}

	m.items = replaced
	m.nextID = len(replaced) + 1
	return cloneItems(replaced), nil
}

// Reset empties the in-memory todo list, rewinds the next-id counter, and
// persists the empty state to disk so subsequent Add() calls start from id 1.
// Returns any error encountered while writing the empty file so the caller
// can surface a real diagnostic instead of silently believing the list was
// cleared — a silent failure here is exactly what caused stale todos to
// haunt later sessions.
func (m *Manager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.persist([]Item{}); err != nil {
		return fmt.Errorf("reset todos: %w", err)
	}
	m.items = []Item{}
	m.nextID = 1
	return nil
}

// List returns all todo items.
func (m *Manager) List() []Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneItems(m.items)
}

// Get returns a todo item by ID.
func (m *Manager) Get(id int) (*Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, item := range m.items {
		if item.ID == id {
			copy := item
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("todo not found: %d", id)
}

// Delete marks a todo as deleted.
func (m *Manager) Delete(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	updated := cloneItems(m.items)
	for i, item := range updated {
		if item.ID == id {
			updated[i].Status = StatusDeleted
			updated[i].UpdatedAt = time.Now()
			if err := m.persist(updated); err != nil {
				return err
			}
			m.items = updated
			return nil
		}
	}
	return fmt.Errorf("todo not found: %d", id)
}

// Render returns a formatted string representation of todos.
func (m *Manager) Render() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return renderItems(m.items)
}

func renderItems(items []Item) string {
	if len(items) == 0 {
		return "No todos."
	}

	lines := make([]string, 0, len(items)+1)
	done := 0
	for _, item := range items {
		status := "[?]"
		switch item.Status {
		case StatusCompleted:
			status = "[x]"
			done++
		case StatusInProgress:
			status = "[>]"
		case StatusPending:
			status = "[ ]"
		}
		suffix := ""
		if item.Status == StatusInProgress {
			suffix = " <- " + item.ActiveForm
		}
		lines = append(lines, fmt.Sprintf("%s %s%s", status, item.Content, suffix))
	}
	lines = append(lines, fmt.Sprintf("\n(%d/%d completed)", done, len(items)))
	return strings.Join(lines, "\n")
}

func cloneItems(items []Item) []Item {
	result := make([]Item, len(items))
	copy(result, items)
	return result
}
