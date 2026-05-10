package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// Status represents a task lifecycle state.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

// FileTask represents a persistent file-based task
type FileTask struct {
	ID          int       `json:"id"`
	Subject     string    `json:"subject"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	BlockedBy   []int     `json:"blockedBy,omitempty"`
}

// Manager handles file-based tasks
type Manager struct {
	mu       sync.RWMutex
	tasks    map[int]*FileTask
	tasksDir string
}

// NewManager creates a new task manager
func NewManager(tasksDir string) *Manager {
	m := &Manager{
		tasks:    make(map[int]*FileTask),
		tasksDir: tasksDir,
	}
	m.loadAll()
	return m
}

// loadAll loads all tasks from disk
func (m *Manager) loadAll() {
	entries, err := os.ReadDir(m.tasksDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))]); err != nil {
			continue
		}
		path := filepath.Join(m.tasksDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var task FileTask
		if err := json.Unmarshal(data, &task); err == nil {
			m.tasks[task.ID] = &task
		}
	}
}

// save saves a task to disk
func (m *Manager) save(task *FileTask) error {
	filename := fmt.Sprintf("%d.json", task.ID)
	path := filepath.Join(m.tasksDir, filename)

	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}

	return fsutil.WriteFileAtomic(path, data, 0644)
}

// Create creates a new task
func (m *Manager) Create(subject, description string) (*FileTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find next available ID
	id := 1
	for {
		if _, exists := m.tasks[id]; !exists {
			break
		}
		id++
	}

	task := &FileTask{
		ID:          id,
		Subject:     subject,
		Description: description,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		BlockedBy:   []int{},
	}

	if err := m.save(task); err != nil {
		return nil, err
	}
	m.tasks[id] = task

	return cloneTask(task), nil
}

// Get returns a task by ID
func (m *Manager) Get(id int) (*FileTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if task, ok := m.tasks[id]; ok {
		return cloneTask(task), nil
	}
	return nil, fmt.Errorf("task not found: %d", id)
}

// Update updates a task's status and blocked_by
func (m *Manager) Update(id int, status Status, addBlockedBy []int, removeBlockedBy []int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("task not found: %d", id)
	}

	updated := cloneTask(task)

	if status != "" {
		if !status.Valid() {
			return fmt.Errorf("invalid task status: %s", status)
		}
		updated.Status = status
	}
	updated.UpdatedAt = time.Now()

	normalizedBlockedBy, err := m.normalizeBlockedBy(id, updated.BlockedBy, addBlockedBy, removeBlockedBy)
	if err != nil {
		return err
	}
	updated.BlockedBy = normalizedBlockedBy

	if err := m.save(updated); err != nil {
		return err
	}
	m.tasks[id] = updated
	return nil
}

// List returns all tasks
func (m *Manager) List() []*FileTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasks := make([]*FileTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, cloneTask(task))
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks
}

// ClaimPending atomically claims a task that is still pending and unblocked.
func (m *Manager) ClaimPending(id int) (*FileTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task not found: %d", id)
	}
	if task.Status != StatusPending {
		return nil, fmt.Errorf("task is not available for claiming (status: %s)", task.Status)
	}
	if len(task.BlockedBy) > 0 {
		return nil, fmt.Errorf("task is blocked by dependencies")
	}

	updated := cloneTask(task)
	updated.Status = StatusInProgress
	updated.UpdatedAt = time.Now()
	if err := m.save(updated); err != nil {
		return nil, err
	}
	m.tasks[id] = updated
	return cloneTask(updated), nil
}

// Delete deletes a task
func (m *Manager) Delete(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tasks[id]; !ok {
		return fmt.Errorf("task not found: %d", id)
	}

	updatedTasks := make(map[int]*FileTask, len(m.tasks)-1)
	for taskID, taskItem := range m.tasks {
		if taskID == id {
			continue
		}
		updated := cloneTask(taskItem)
		if filtered, changed := removeBlockedByID(updated.BlockedBy, id); changed {
			updated.BlockedBy = filtered
			updated.UpdatedAt = time.Now()
			if err := m.save(updated); err != nil {
				return err
			}
		}
		updatedTasks[taskID] = updated
	}

	filename := fmt.Sprintf("%d.json", id)
	path := filepath.Join(m.tasksDir, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.tasks = updatedTasks
	return nil
}

// Valid reports whether the status is a supported task status.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusInProgress, StatusCompleted:
		return true
	default:
		return false
	}
}

// ParseStatus converts a tool/user supplied string into a task status.
func ParseStatus(raw string) (Status, error) {
	status := Status(raw)
	if status == "" {
		return "", nil
	}
	if !status.Valid() {
		return "", fmt.Errorf("invalid task status: %s", raw)
	}
	return status, nil
}

func cloneTask(task *FileTask) *FileTask {
	if task == nil {
		return nil
	}

	cloned := *task
	if len(task.BlockedBy) > 0 {
		cloned.BlockedBy = append([]int{}, task.BlockedBy...)
	}
	return &cloned
}

func (m *Manager) normalizeBlockedBy(taskID int, current []int, addBlockedBy []int, removeBlockedBy []int) ([]int, error) {
	removeSet := make(map[int]struct{}, len(removeBlockedBy))
	for _, blockedID := range removeBlockedBy {
		removeSet[blockedID] = struct{}{}
	}

	combined := make([]int, 0, len(current)+len(addBlockedBy))
	seen := make(map[int]struct{}, len(current)+len(addBlockedBy))
	appendBlockedID := func(blockedID int) {
		if _, removed := removeSet[blockedID]; removed {
			return
		}
		if _, ok := seen[blockedID]; ok {
			return
		}
		seen[blockedID] = struct{}{}
		combined = append(combined, blockedID)
	}

	for _, blockedID := range current {
		appendBlockedID(blockedID)
	}
	for _, blockedID := range addBlockedBy {
		appendBlockedID(blockedID)
	}

	for _, blockedID := range combined {
		switch {
		case blockedID == taskID:
			return nil, fmt.Errorf("task cannot be blocked by itself: %d", taskID)
		case blockedID <= 0:
			return nil, fmt.Errorf("invalid blocked_by task id: %d", blockedID)
		case m.tasks[blockedID] == nil:
			return nil, fmt.Errorf("blocked_by task not found: %d", blockedID)
		}
	}

	sort.Ints(combined)
	return combined, nil
}

func removeBlockedByID(blockedBy []int, target int) ([]int, bool) {
	if len(blockedBy) == 0 {
		return nil, false
	}

	filtered := make([]int, 0, len(blockedBy))
	changed := false
	for _, blockedID := range blockedBy {
		if blockedID == target {
			changed = true
			continue
		}
		filtered = append(filtered, blockedID)
	}
	if len(filtered) == 0 {
		return nil, changed
	}
	return filtered, changed
}
