package background

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/tooling"
)

// Status represents the lifecycle of a background task.
type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusError       Status = "error"
	StatusCanceled    Status = "canceled"
	StatusInterrupted Status = "interrupted"
)

// Task represents a background task
type Task struct {
	ID              string
	SessionID       string
	TurnID          string
	Command         string
	Argv            []string
	Cmd             *exec.Cmd
	Ctx             context.Context
	Cancel          context.CancelFunc
	StartTime       time.Time
	EndTime         time.Time
	Done            chan struct{}
	Error           error
	Output          string
	OutputPath      string
	OutputTruncated bool
	OutputBytes     int64
	OutputStored    int64
	OutputLogPath   string
	SummaryPath     string
	ExitCode        int
	Status          Status
	Notified        bool
	cancelRequested bool
}

// Manager handles background tasks
type Manager struct {
	mu       sync.RWMutex
	tasks    map[string]*Task
	storeDir string
}

// OutputOptions controls retained output for a background task.
type OutputOptions struct {
	SpillDir  string
	StoreDir  string
	SessionID string
	TurnID    string
	Command   string
	Argv      []string
}

type OutputReadOptions struct {
	Offset     int64
	LimitBytes int64
	TailLines  int
	Query      string
}

type OutputReadResult struct {
	TaskID     string `json:"task_id"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	OutputPath string `json:"output_path,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"total_bytes"`
	Truncated  bool   `json:"truncated,omitempty"`
	Query      string `json:"query,omitempty"`
	MatchCount int    `json:"match_count,omitempty"`
}

type taskSummary struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	Command         string    `json:"command,omitempty"`
	Argv            []string  `json:"argv,omitempty"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time,omitempty"`
	Error           string    `json:"error,omitempty"`
	Output          string    `json:"output,omitempty"`
	OutputPath      string    `json:"output_path,omitempty"`
	OutputTruncated bool      `json:"output_truncated,omitempty"`
	OutputBytes     int64     `json:"output_bytes,omitempty"`
	OutputStored    int64     `json:"output_stored,omitempty"`
	OutputLogPath   string    `json:"output_log_path,omitempty"`
	ExitCode        int       `json:"exit_code,omitempty"`
	Status          Status    `json:"status"`
	Notified        bool      `json:"notified,omitempty"`
}

var (
	configureProcessGroup = func(cmd *exec.Cmd) error {
		_ = cmd
		return nil
	}
	killProcessTree = func(cmd *exec.Cmd) error {
		if cmd == nil || cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
)

// NewManager creates a new background manager
func NewManager() *Manager {
	return NewManagerWithStore("")
}

func NewManagerWithStore(storeDir string) *Manager {
	mgr := &Manager{
		tasks:    make(map[string]*Task),
		storeDir: strings.TrimSpace(storeDir),
	}
	mgr.loadStoredTasks()
	return mgr
}

func (m *Manager) loadStoredTasks() {
	if m.storeDir == "" {
		return
	}
	entries, err := os.ReadDir(m.storeDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		summary, err := readTaskSummary(filepath.Join(m.storeDir, entry.Name(), "summary.json"))
		if err != nil {
			continue
		}
		task := taskFromSummary(summary)
		task.SummaryPath = filepath.Join(m.storeDir, entry.Name(), "summary.json")
		if task.OutputLogPath == "" {
			task.OutputLogPath = filepath.Join(m.storeDir, entry.Name(), "output.log")
		}
		if task.Status == StatusRunning {
			task.Status = StatusInterrupted
			task.Error = fmt.Errorf("previous process stopped before background task completed")
			task.EndTime = time.Now()
		}
		if task.Done == nil {
			task.Done = make(chan struct{})
			close(task.Done)
		}
		m.tasks[task.ID] = task
		if task.Status == StatusInterrupted {
			_ = m.saveTaskSummary(task)
		}
	}
}

func readTaskSummary(path string) (taskSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return taskSummary{}, err
	}
	var summary taskSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return taskSummary{}, err
	}
	return summary, nil
}

func taskFromSummary(summary taskSummary) *Task {
	task := &Task{
		ID:              summary.ID,
		SessionID:       summary.SessionID,
		TurnID:          summary.TurnID,
		Command:         summary.Command,
		Argv:            append([]string{}, summary.Argv...),
		StartTime:       summary.StartTime,
		EndTime:         summary.EndTime,
		Output:          summary.Output,
		OutputPath:      summary.OutputPath,
		OutputTruncated: summary.OutputTruncated,
		OutputBytes:     summary.OutputBytes,
		OutputStored:    summary.OutputStored,
		OutputLogPath:   summary.OutputLogPath,
		ExitCode:        summary.ExitCode,
		Status:          summary.Status,
		Notified:        summary.Notified,
	}
	if summary.Error != "" {
		task.Error = fmt.Errorf("%s", summary.Error)
	}
	return task
}

// Start starts a command in background.
func (m *Manager) Start(id string, cmd *exec.Cmd, timeout time.Duration) (*Task, error) {
	return m.StartWithOptions(id, cmd, timeout, OutputOptions{})
}

// StartWithOptions starts a command in background with bounded output capture.
func (m *Manager) StartWithOptions(id string, cmd *exec.Cmd, timeout time.Duration, opts OutputOptions) (*Task, error) {
	if cmd == nil {
		return nil, fmt.Errorf("missing command")
	}
	storeDir := strings.TrimSpace(opts.StoreDir)
	if storeDir == "" {
		storeDir = m.storeDir
	}
	taskDir := ""
	outputPath := ""
	summaryPath := ""
	if storeDir != "" {
		taskDir = filepath.Join(storeDir, id)
		outputPath = filepath.Join(taskDir, "output.log")
		summaryPath = filepath.Join(taskDir, "summary.json")
		if err := os.MkdirAll(taskDir, 0755); err != nil {
			return nil, err
		}
	}

	baseCtx := context.Background()
	ctx, cancel := context.WithCancel(baseCtx)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(baseCtx, timeout)
	}

	output := tooling.NewOutputCapture(tooling.CommandOutputOptions{
		SpillDir:    opts.SpillDir,
		OutputPath:  outputPath,
		SpillBytes:  64 << 20,
		SpillPrefix: "background-",
	})
	cmd.Stdout = output
	cmd.Stderr = output

	task := &Task{
		ID:            id,
		SessionID:     strings.TrimSpace(opts.SessionID),
		TurnID:        strings.TrimSpace(opts.TurnID),
		Command:       strings.TrimSpace(opts.Command),
		Argv:          append([]string{}, opts.Argv...),
		Cmd:           cmd,
		Ctx:           ctx,
		Cancel:        cancel,
		StartTime:     time.Now(),
		Done:          make(chan struct{}),
		OutputLogPath: outputPath,
		OutputPath:    outputPath,
		SummaryPath:   summaryPath,
		Status:        StatusRunning,
	}
	if task.Command == "" && len(task.Argv) > 0 {
		task.Command = strings.Join(task.Argv, " ")
	}

	if err := configureProcessGroup(cmd); err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	m.mu.Lock()
	m.tasks[id] = task
	saveErr := m.saveTaskSummaryLocked(task)
	if saveErr != nil {
		delete(m.tasks, id)
	}
	m.mu.Unlock()
	if saveErr != nil {
		task.Cancel()
		return nil, saveErr
	}

	go m.waitForTask(task, output)

	return task, nil
}

func (m *Manager) saveTaskSummary(task *Task) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.saveTaskSummaryLocked(task)
}

func (m *Manager) saveTaskSummaryLocked(task *Task) error {
	if task == nil || strings.TrimSpace(task.SummaryPath) == "" {
		return nil
	}
	summary := taskSummary{
		ID:              task.ID,
		SessionID:       task.SessionID,
		TurnID:          task.TurnID,
		Command:         task.Command,
		Argv:            append([]string{}, task.Argv...),
		StartTime:       task.StartTime,
		EndTime:         task.EndTime,
		Error:           errorString(task.Error),
		Output:          task.Output,
		OutputPath:      task.OutputPath,
		OutputTruncated: task.OutputTruncated,
		OutputBytes:     task.OutputBytes,
		OutputStored:    task.OutputStored,
		OutputLogPath:   task.OutputLogPath,
		ExitCode:        task.ExitCode,
		Status:          task.Status,
		Notified:        task.Notified,
	}
	return fsutil.WriteJSONAtomic(task.SummaryPath, summary, 0644)
}

// Get returns a task by ID
func (m *Manager) Get(id string) (*Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if task, ok := m.tasks[id]; ok {
		return task, nil
	}
	return nil, fmt.Errorf("task not found: %s", id)
}

// List returns all running tasks
func (m *Manager) List() []*Task {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasks := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		if task.Status == StatusRunning {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

// Cancel cancels a running task
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}
	if task.Status != StatusRunning {
		return fmt.Errorf("task not running: %s", id)
	}

	task.cancelRequested = true
	task.Cancel()
	return nil
}

// Wait waits for a task to complete
func (m *Manager) Wait(id string) (*Task, error) {
	task, err := m.Get(id)
	if err != nil {
		return nil, err
	}

	<-task.Done
	return task, nil
}

// IsRunning checks if a task is still running
func (m *Manager) IsRunning(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, ok := m.tasks[id]
	return ok && task.Status == StatusRunning
}

func (m *Manager) ReadOutput(id string, opts OutputReadOptions) (OutputReadResult, error) {
	task, err := m.Get(id)
	if err != nil {
		return OutputReadResult{}, err
	}
	path := strings.TrimSpace(task.OutputLogPath)
	if path == "" {
		path = strings.TrimSpace(task.OutputPath)
	}
	if path == "" {
		return OutputReadResult{
			TaskID: id,
			Status: string(task.Status),
			Output: task.Output,
			Bytes:  int64(len([]byte(task.Output))),
		}, nil
	}
	data, total, offset, truncated, matches, err := readTaskOutput(path, opts)
	if err != nil {
		return OutputReadResult{}, err
	}
	return OutputReadResult{
		TaskID:     id,
		Status:     string(task.Status),
		Output:     data,
		OutputPath: path,
		Offset:     offset,
		Bytes:      int64(len([]byte(data))),
		TotalBytes: total,
		Truncated:  truncated,
		Query:      strings.TrimSpace(opts.Query),
		MatchCount: matches,
	}, nil
}

func readTaskOutput(path string, opts OutputReadOptions) (string, int64, int64, bool, int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, 0, false, 0, err
	}
	total := info.Size()
	limit := opts.LimitBytes
	if limit <= 0 {
		limit = 32 << 10
	}
	if limit > 512<<10 {
		limit = 512 << 10
	}
	query := strings.TrimSpace(opts.Query)
	if query != "" {
		return searchTaskOutput(path, query, limit, total)
	}
	if opts.TailLines > 0 {
		return tailTaskOutput(path, opts.TailLines, limit, total)
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	file, err := os.Open(path)
	if err != nil {
		return "", total, offset, false, 0, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", total, offset, false, 0, err
	}
	buf := make([]byte, limit)
	n, err := file.Read(buf)
	if err != nil && !errorsIsEOF(err) {
		return "", total, offset, false, 0, err
	}
	return string(buf[:n]), total, offset, offset+int64(n) < total, 0, nil
}

func searchTaskOutput(path, query string, limit, total int64) (string, int64, int64, bool, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", total, 0, false, 0, err
	}
	defer file.Close()
	var builder strings.Builder
	scanner := bufio.NewScanner(file)
	matches := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
			continue
		}
		matches++
		if int64(builder.Len()+len(line)+1) <= limit {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return "", total, 0, false, matches, err
	}
	return strings.TrimRight(builder.String(), "\n"), total, 0, matches > 0 && int64(builder.Len()) >= limit, matches, nil
}

func tailTaskOutput(path string, lines int, limit, total int64) (string, int64, int64, bool, int, error) {
	if lines <= 0 {
		lines = 20
	}
	if lines > 500 {
		lines = 500
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", total, 0, false, 0, err
	}
	text := strings.TrimRight(string(data), "\n")
	parts := strings.Split(text, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	out := strings.Join(parts, "\n")
	truncated := false
	if int64(len([]byte(out))) > limit {
		runes := []rune(out)
		for len([]byte(string(runes))) > int(limit) && len(runes) > 0 {
			runes = runes[1:]
			truncated = true
		}
		out = string(runes)
	}
	offset := total - int64(len([]byte(out)))
	if offset < 0 {
		offset = 0
	}
	return out, total, offset, truncated, 0, nil
}

func errorsIsEOF(err error) bool {
	return err == nil || err == io.EOF
}

// Notification represents a background task notification
type Notification struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// PeekNotifications previews completed task notifications without acknowledging them.
func (m *Manager) PeekNotifications() []Notification {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var notifs []Notification
	for id, task := range m.tasks {
		if task.Status == StatusRunning || task.Notified {
			continue
		}

		result := task.Output
		if len(result) > 500 {
			result = result[:500]
		}
		if task.OutputTruncated && task.OutputPath != "" {
			result = fmt.Sprintf("%s\n[full output: %s]", result, task.OutputPath)
		}
		notifs = append(notifs, Notification{
			TaskID: id,
			Status: string(task.Status),
			Result: result,
			Error:  errorString(task.Error),
		})
	}
	return notifs
}

// AckNotifications marks the provided notifications as delivered.
func (m *Manager) AckNotifications(notifs []Notification) {
	if len(notifs) == 0 {
		return
	}

	ackIDs := make(map[string]struct{}, len(notifs))
	for _, notif := range notifs {
		if notif.TaskID != "" {
			ackIDs[notif.TaskID] = struct{}{}
		}
	}
	if len(ackIDs) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, task := range m.tasks {
		if _, ok := ackIDs[id]; ok {
			task.Notified = true
			_ = m.saveTaskSummaryLocked(task)
		}
	}
}

// Drain drains completed task notifications.
func (m *Manager) Drain() []Notification {
	notifs := m.PeekNotifications()
	m.AckNotifications(notifs)
	return notifs
}

func (m *Manager) waitForTask(task *Task, output *tooling.OutputCapture) {
	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-task.Ctx.Done():
			m.mu.Lock()
			running := task.Status == StatusRunning
			if running {
				task.cancelRequested = true
			}
			m.mu.Unlock()

			if running && task.Cmd != nil && task.Cmd.Process != nil {
				if err := killProcessTree(task.Cmd); err != nil {
					_ = task.Cmd.Process.Kill()
				}
			}
		case <-ctxDone:
		}
	}()

	err := task.Cmd.Wait()
	close(ctxDone)
	_ = output.Close()
	outputResult := output.Result()

	m.mu.Lock()
	task.Output = outputResult.ModelText()
	task.OutputPath = outputResult.FilePath
	task.OutputTruncated = outputResult.Truncated
	task.OutputBytes = outputResult.Bytes
	task.OutputStored = outputResult.StoredBytes
	task.Error = err
	task.EndTime = time.Now()
	task.ExitCode = 0
	if err != nil && task.Cmd != nil && task.Cmd.ProcessState != nil {
		task.ExitCode = task.Cmd.ProcessState.ExitCode()
	}

	switch {
	case task.cancelRequested || (task.Ctx != nil && task.Ctx.Err() != nil):
		task.Status = StatusCanceled
	case err != nil:
		task.Status = StatusError
	default:
		task.Status = StatusCompleted
	}
	_ = m.saveTaskSummaryLocked(task)
	m.mu.Unlock()

	close(task.Done)
	task.Cancel()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
