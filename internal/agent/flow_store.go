// Flow Spec v1 version store (docs/business-flow-runtime-design.md §6).
// Layout:
//
//	{StateDir}/flows/{flowID}/
//	  versions/{version}/flow.json        # immutable definition
//	  versions/{version}/compiled.json    # compile artifact (nodes/edges + digest)
//	  current.json                        # draft/gray/published -> version
//	  runs/{runID}.json                   # FlowRun record (runtime body lives in workflows/)
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

	"github.com/tim5wang/godex/internal/core/flow"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// Flow status values (design doc §3.1).
const (
	FlowStatusDraft      = "draft"
	FlowStatusGray       = "gray"
	FlowStatusPublished  = "published"
	FlowStatusDeprecated = "deprecated"
)

type flowStore struct {
	dir string
	mu  sync.Mutex
}

func newFlowStore(dir string) *flowStore {
	return &flowStore{dir: strings.TrimSpace(dir)}
}

// flowVersionRecord is one immutable version of a flow on disk.
type flowVersionRecord struct {
	Version   string           `json:"version"`
	Status    string           `json:"status"`
	Flow      *flow.Definition `json:"flow"`
	Compiled  *flow.Compiled   `json:"compiled,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// flowCurrent points each status lane at a version (design doc §6).
type flowCurrent struct {
	Draft     string `json:"draft,omitempty"`
	Gray      string `json:"gray,omitempty"`
	Published string `json:"published,omitempty"`
	// DesignerSessionID is the chat session that designed/updated this flow
	// (pinned by create_flow via flowSessionID(ctx)). The UI resumes this
	// session when opening the flow's natural-language tab, so the
	// conversation survives flow id changes (the session's locator key is
	// the flow id at creation time, which may differ from the current one).
	DesignerSessionID string `json:"designer_session_id,omitempty"`
}

// flowRunRecord is the per-run record; runtime state lives in workflows/.
type flowRunRecord struct {
	RunID                  string         `json:"run_id"`
	FlowID                 string         `json:"flow_id"`
	Version                string         `json:"version"`
	Digest                 string         `json:"digest"`
	SessionID              string         `json:"session_id,omitempty"`
	WorkflowID             string         `json:"workflow_id,omitempty"`
	Status                 string         `json:"status"`
	Inputs                 map[string]any `json:"inputs,omitempty"`
	Outputs                map[string]any `json:"outputs,omitempty"`
	OutputSpec             []flow.VarDef  `json:"output_spec,omitempty"`
	RunTimeoutAt           time.Time      `json:"run_timeout_at,omitempty"`
	IdempotencyKeyHash     string         `json:"idempotency_key_hash,omitempty"`
	IdempotencyRequestHash string         `json:"idempotency_request_hash,omitempty"`
	Error                  string         `json:"error,omitempty"`
	// WebhookSent guards the one-time on_complete delivery (P1.3).
	WebhookSent bool      `json:"webhook_sent,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
}

func (s *flowStore) flowDir(flowID string) (string, error) {
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return "", fmt.Errorf("missing flow_id")
	}
	if err := validateFlowID(flowID); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, flowID), nil
}

func validateFlowID(id string) error {
	for _, r := range id {
		if !(r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("invalid flow_id %q (allowed: [a-z0-9_-])", id)
		}
	}
	return nil
}

func (s *flowStore) versionsDir(flowID string) (string, error) {
	dir, err := s.flowDir(flowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "versions"), nil
}

func (s *flowStore) saveVersion(flowID string, rec flowVersionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.versionsDir(flowID)
	if err != nil {
		return err
	}
	ver := strings.TrimSpace(rec.Version)
	if ver == "" {
		return fmt.Errorf("missing version")
	}
	verDir := filepath.Join(dir, ver)
	if err := os.MkdirAll(verDir, 0755); err != nil {
		return err
	}
	if rec.Flow != nil {
		if err := fsutil.WriteJSONAtomic(filepath.Join(verDir, "flow.json"), rec.Flow, 0644); err != nil {
			return err
		}
	}
	if rec.Compiled != nil {
		if err := fsutil.WriteJSONAtomic(filepath.Join(verDir, "compiled.json"), rec.Compiled, 0644); err != nil {
			return err
		}
	}
	meta := struct {
		Version   string    `json:"version"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}{rec.Version, rec.Status, rec.CreatedAt, rec.UpdatedAt}
	return fsutil.WriteJSONAtomic(filepath.Join(verDir, "meta.json"), meta, 0644)
}

// readJSONFile is defined in subagent_events.go (same package).

func (s *flowStore) loadVersion(flowID, version string) (flowVersionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.versionsDir(flowID)
	if err != nil {
		return flowVersionRecord{}, err
	}
	verDir := filepath.Join(dir, strings.TrimSpace(version))
	var rec flowVersionRecord
	if err := readJSONFile(filepath.Join(verDir, "meta.json"), &rec); err != nil {
		return flowVersionRecord{}, err
	}
	rec.Version = strings.TrimSpace(version)
	var def flow.Definition
	if err := readJSONFile(filepath.Join(verDir, "flow.json"), &def); err != nil {
		return flowVersionRecord{}, err
	}
	rec.Flow = &def
	_ = readJSONFile(filepath.Join(verDir, "compiled.json"), &rec.Compiled)
	return rec, nil
}

func (s *flowStore) listVersions(flowID string) ([]flowVersionRecord, error) {
	// No outer lock: loadVersion takes the store lock per file read, and
	// holding it across the loop would self-deadlock.
	dir, err := s.versionsDir(flowID)
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
	out := make([]flowVersionRecord, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, err := s.loadVersion(flowID, e.Name())
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (s *flowStore) currentPath(flowID string) (string, error) {
	dir, err := s.flowDir(flowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "current.json"), nil
}

func (s *flowStore) loadCurrent(flowID string) (flowCurrent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.currentPath(flowID)
	if err != nil {
		return flowCurrent{}, err
	}
	var cur flowCurrent
	_ = readJSONFile(path, &cur)
	return cur, nil
}

func (s *flowStore) saveCurrent(flowID string, cur flowCurrent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.currentPath(flowID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(path, cur, 0644)
}

// setStatusLane points the given status lane at a version (design doc §6
// current.json). Publish also updates the gray lane when the source was gray.
func (s *flowStore) setStatusLane(flowID, status, version string) error {
	cur, err := s.loadCurrent(flowID)
	if err != nil {
		return err
	}
	switch status {
	case FlowStatusDraft:
		cur.Draft = version
	case FlowStatusGray:
		cur.Gray = version
	case FlowStatusPublished:
		cur.Published = version
	default:
		return nil // deprecated/archived do not occupy a lane
	}
	return s.saveCurrent(flowID, cur)
}

// setDesignerSessionID records the chat session that designed/updated the
// flow (create_flow passes flowSessionID(ctx)). It persists to current.json
// so the UI can resume the same conversation across flow renames.
func (s *flowStore) setDesignerSessionID(flowID, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	cur, err := s.loadCurrent(flowID)
	if err != nil {
		return err
	}
	cur.DesignerSessionID = sessionID
	return s.saveCurrent(flowID, cur)
}

// deleteVersion removes one immutable version directory (flow.json +
// compiled.json + meta.json). If the version currently occupies a status
// lane (draft/gray/published), the lane is cleared so the flow keeps a
// consistent view; no run record is touched (runs live under runs/).
func (s *flowStore) deleteVersion(flowID, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.versionsDir(flowID)
	if err != nil {
		return err
	}
	verDir := filepath.Join(dir, strings.TrimSpace(version))
	if err := os.RemoveAll(verDir); err != nil {
		return err
	}
	// Clear any status lane pointing at the removed version.
	cur, err := s.loadCurrent(flowID)
	if err != nil {
		return err
	}
	changed := false
	if cur.Draft == version {
		cur.Draft = ""
		changed = true
	}
	if cur.Gray == version {
		cur.Gray = ""
		changed = true
	}
	if cur.Published == version {
		cur.Published = ""
		changed = true
	}
	if changed {
		return s.saveCurrent(flowID, cur)
	}
	return nil
}

// deleteFlow removes the whole flow directory (versions + runs + current).
// Callers must guard against deleting a flow with active runs.
func (s *flowStore) deleteFlow(flowID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.flowDir(flowID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *flowStore) runsDir(flowID string) (string, error) {
	dir, err := s.flowDir(flowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runs"), nil
}

func (s *flowStore) saveRun(flowID string, rec flowRunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.runsDir(flowID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return fsutil.WriteJSONAtomic(filepath.Join(dir, rec.RunID+".json"), rec, 0644)
}

func (s *flowStore) loadRun(flowID, runID string) (flowRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.runsDir(flowID)
	if err != nil {
		return flowRunRecord{}, err
	}
	var rec flowRunRecord
	if err := readJSONFile(filepath.Join(dir, runID+".json"), &rec); err != nil {
		return flowRunRecord{}, err
	}
	return rec, nil
}

func (s *flowStore) listRuns(flowID string) ([]flowRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.runsDir(flowID)
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
	out := make([]flowRunRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var rec flowRunRecord
		if err := readJSONFile(filepath.Join(dir, e.Name()), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

// findIdempotentRun scans durable run records for a gateway idempotency key.
// The key metadata lives in the run record itself, so the run and its replay
// identity become durable together in one atomic JSON replacement.
func (s *flowStore) findIdempotentRun(flowID, keyHash string) (flowRunRecord, bool, error) {
	if strings.TrimSpace(keyHash) == "" {
		return flowRunRecord{}, false, nil
	}
	runs, err := s.listRuns(flowID)
	if err != nil {
		return flowRunRecord{}, false, err
	}
	for _, rec := range runs {
		if rec.IdempotencyKeyHash == keyHash {
			return rec, true, nil
		}
	}
	return flowRunRecord{}, false, nil
}

// listFlows scans the store for flow ids (sorted).
func (s *flowStore) listFlows() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

var _ = json.Marshal
