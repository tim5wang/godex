// P1.1 human task store: the queue/record layer for human fallback nodes in
// Flow Spec v1. Layout (design doc §6):
//
//	{StateDir}/human-tasks/{queue}/{runID}/{nodeID}.json
//
// A task is registered when a user_input node carrying a workflowHumanSpec
// becomes ready; it stays pending until the orchestrator replies via
// complete_node (HTTP: POST /v1/flow-runs/{runID}/human/{nodeID}/reply), is
// canceled, or times out (on_timeout escalation is applied by the caller).
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// Human task status values (P1.1).
const (
	humanTaskStatusPending   = "pending"
	humanTaskStatusReplied   = "replied"
	humanTaskStatusCanceled  = "canceled"
	humanTaskStatusError     = "error"
	humanTaskStatusEscalated = "escalated"
)

// defaultHumanQueue is used when a human spec omits queue.
const defaultHumanQueue = "default"

// humanTaskRecord is one durable human task (per run node).
type humanTaskRecord struct {
	TaskID         string    `json:"task_id"` // "{runID}:{nodeID}"
	FlowID         string    `json:"flow_id"`
	RunID          string    `json:"run_id"`
	WorkflowID     string    `json:"workflow_id,omitempty"`
	NodeID         string    `json:"node_id"`
	Queue          string    `json:"queue"`
	AssigneePolicy string    `json:"assignee_policy,omitempty"`
	Form           any       `json:"form,omitempty"`
	Prompt         string    `json:"prompt"`
	Status         string    `json:"status"`
	Result         any       `json:"result,omitempty"`
	ResultVar      string    `json:"result_var,omitempty"`
	TimeoutMS      int       `json:"timeout_ms,omitempty"`
	OnTimeout      string    `json:"on_timeout,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	DueAt          time.Time `json:"due_at,omitempty"`
}

// humanTaskStore persists human tasks on disk (queue-scoped directories).
type humanTaskStore struct {
	dir string
	mu  sync.Mutex
}

func newHumanTaskStore(dir string) *humanTaskStore {
	return &humanTaskStore{dir: strings.TrimSpace(dir)}
}

// taskPath resolves the file for one task under its queue.
func (s *humanTaskStore) taskPath(queue, runID, nodeID string) (string, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return "", fmt.Errorf("human task store unavailable")
	}
	queue = strings.TrimSpace(queue)
	if queue == "" {
		queue = defaultHumanQueue
	}
	runID = strings.TrimSpace(runID)
	nodeID = strings.TrimSpace(nodeID)
	if runID == "" || nodeID == "" {
		return "", fmt.Errorf("missing run_id/node_id for human task")
	}
	return filepath.Join(s.dir, queue, runID, nodeID+".json"), nil
}

// saveTask writes one task (upsert).
func (s *humanTaskStore) saveTask(rec humanTaskRecord) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("human task store unavailable")
	}
	path, err := s.taskPath(rec.Queue, rec.RunID, rec.NodeID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fsutil.WriteJSONAtomic(path, rec, 0644)
}

// loadTask reads one task by queue+runID+nodeID.
func (s *humanTaskStore) loadTask(queue, runID, nodeID string) (humanTaskRecord, error) {
	var rec humanTaskRecord
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return rec, fmt.Errorf("human task store unavailable")
	}
	path, err := s.taskPath(queue, runID, nodeID)
	if err != nil {
		return rec, err
	}
	if err := readJSONFile(path, &rec); err != nil {
		return rec, fmt.Errorf("read human task %s: %w", path, err)
	}
	return rec, nil
}

// listTasks returns tasks across queues (newest first). Empty queue/status
// filters match all.
func (s *humanTaskStore) listTasks(queue, status string) ([]humanTaskRecord, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queue = strings.TrimSpace(queue)
	status = strings.TrimSpace(status)
	var out []humanTaskRecord
	queues := []string{""}
	if entries, err := os.ReadDir(s.dir); err == nil {
		queues = nil
		for _, e := range entries {
			if e.IsDir() {
				queues = append(queues, e.Name())
			}
		}
	}
	for _, q := range queues {
		if queue != "" && q != queue {
			continue
		}
		base := s.dir
		if q != "" {
			base = filepath.Join(s.dir, q)
		}
		runDirs, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, rd := range runDirs {
			if !rd.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(base, rd.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				path := filepath.Join(base, rd.Name(), f.Name())
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				var rec humanTaskRecord
				if err := json.Unmarshal(data, &rec); err != nil {
					continue
				}
				if status != "" && rec.Status != status {
					continue
				}
				out = append(out, rec)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// listRunTasks returns all tasks of one run (any queue).
func (s *humanTaskStore) listRunTasks(runID string) ([]humanTaskRecord, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	var out []humanTaskRecord
	queues := []string{""}
	if entries, err := os.ReadDir(s.dir); err == nil {
		queues = nil
		for _, e := range entries {
			if e.IsDir() {
				queues = append(queues, e.Name())
			}
		}
	}
	for _, q := range queues {
		base := s.dir
		if q != "" {
			base = filepath.Join(s.dir, q)
		}
		runDir := filepath.Join(base, runID)
		files, err := os.ReadDir(runDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(runDir, f.Name()))
			if err != nil {
				continue
			}
			var rec humanTaskRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				continue
			}
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
