package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const longTaskRunsDir = "runs"

// longTaskRunRecord is one durable run record for a longtask. It is
// written under ~/.godex/workflows/<workflowID>/runs/<runID>.json and is
// the authoritative state for run resumption, status reads, and async
// coordination. The in-memory sync.Map (longTaskAsyncRuns) is only an
// in-process accelerator and is not consulted after a godex restart.
type longTaskRunRecord struct {
	RunID         string                 `json:"run_id"`
	WorkflowID    string                 `json:"workflow_id"`
	SessionID     string                 `json:"session_id,omitempty"`
	StartedAt     time.Time              `json:"started_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	Status        string                 `json:"status"`
	Iterations    int                    `json:"iterations"`
	MaxIterations int                    `json:"max_iterations,omitempty"`
	Started       []string               `json:"started,omitempty"`
	Finalized     []string               `json:"finalized,omitempty"`
	Repaired      []longTaskRepairSummary `json:"repaired,omitempty"`
	BlockedBy     string                 `json:"blocked_by,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Async         bool                   `json:"async,omitempty"`
	// LastRefluxKey is the dedupe key for T11 assistant reflux messages.
	// Empty means no reflux has been emitted yet for this run.
	LastRefluxKey string `json:"last_reflux_key,omitempty"`
}

func (s *workflowStore) runsDir(workflowID string) (string, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return "", fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, workflowID, longTaskRunsDir), nil
}

func (s *workflowStore) writeLongTaskRun(record longTaskRunRecord) error {
	if record.RunID == "" {
		return fmt.Errorf("longtask run record missing run_id")
	}
	dir, err := s.runsDir(record.WorkflowID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, record.RunID+".json")
	return fsutil.WriteJSONAtomic(path, record, 0644)
}

func (s *workflowStore) loadLongTaskRun(workflowID, runID string) (longTaskRunRecord, error) {
	var rec longTaskRunRecord
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return rec, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	runID = strings.TrimSpace(runID)
	if workflowID == "" || runID == "" {
		return rec, fmt.Errorf("missing workflow_id or run_id")
	}
	if err := validateWorkflowID(workflowID); err != nil {
		return rec, err
	}
	dir, err := s.runsDir(workflowID)
	if err != nil {
		return rec, err
	}
	if err := readJSONFile(filepath.Join(dir, runID+".json"), &rec); err != nil {
		return rec, fmt.Errorf("read longtask run record: %w", err)
	}
	return rec, nil
}

func (s *workflowStore) listLongTaskRuns(workflowID string) ([]longTaskRunRecord, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, fmt.Errorf("workflow store is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if err := validateWorkflowID(workflowID); err != nil {
		return nil, err
	}
	dir, err := s.runsDir(workflowID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]longTaskRunRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		runID := strings.TrimSuffix(entry.Name(), ".json")
		if runID == "" {
			continue
		}
		rec, err := s.loadLongTaskRun(workflowID, runID)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out, nil
}

// sweepStaleLongTaskRuns marks any runs whose status is "running" as
// "interrupted" so callers can detect that the godex process died mid-run.
// Returns the list of run ids that were updated. Safe to call at godex
// startup. The list is returned in workflowID order to keep startup
// deterministic; the actual load path does not depend on it.
func (s *workflowStore) sweepStaleLongTaskRuns() ([]string, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, nil
	}
	topEntries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	updated := []string{}
	for _, top := range topEntries {
		if !top.IsDir() {
			continue
		}
		workflowID := top.Name()
		records, err := s.listLongTaskRuns(workflowID)
		if err != nil {
			continue
		}
		for _, rec := range records {
			if rec.Status != "running" {
				continue
			}
			now := time.Now().UTC()
			rec.Status = "interrupted"
			rec.UpdatedAt = now
			rec.Message = "marked interrupted by godex startup sweep"
			if err := s.writeLongTaskRun(rec); err != nil {
				continue
			}
			_ = s.appendEvent(workflowID, map[string]interface{}{
				"event":   "longtask_run_interrupted",
				"run_id":  rec.RunID,
				"at":      now,
			})
			updated = append(updated, workflowID+"/"+rec.RunID)
		}
	}
	return updated, nil
}
