package sessionrepair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const (
	manifestFile      = "manifest.json"
	stateFile         = "state.json"
	checkpointPointer = "checkpoint.json"
	checkpointsDir    = "checkpoints"
	turnsFile         = "turns.json"
	turnQueueFile     = "turn_queue.json"
	timelineFile      = "timeline.json"
	eventJournalFile  = "events.jsonl"
)

type manifest struct {
	SessionID      string          `json:"session_id"`
	StateDigest    string          `json:"state_digest"`
	UpdatedAt      time.Time       `json:"updated_at"`
	LastActivityAt time.Time       `json:"last_activity_at"`
	Raw            json.RawMessage `json:"-"`
}

type checkpointPointerPayload struct {
	Current   string    `json:"current"`
	CreatedAt time.Time `json:"created_at"`
}

type turnRecord struct {
	ID              string           `json:"id"`
	Status          string           `json:"status"`
	UpdatedAt       time.Time        `json:"updated_at"`
	CompletedAt     *time.Time       `json:"completed_at,omitempty"`
	Error           string           `json:"error,omitempty"`
	CanResume       bool             `json:"can_resume,omitempty"`
	ResumeAvailable bool             `json:"resume_available,omitempty"`
	RecoveryHint    string           `json:"recovery_hint,omitempty"`
	Injections      []map[string]any `json:"injections,omitempty"`
}

type queuedTurn struct {
	ID        string         `json:"id"`
	Mode      string         `json:"mode"`
	Status    string         `json:"status"`
	UpdatedAt time.Time      `json:"updated_at"`
	Envelope  map[string]any `json:"envelope,omitempty"`
}

type sessionRepair struct {
	req       Request
	sessionID string
	dir       string
	report    SessionReport
	backupDir string
	backedUp  map[string]bool
}

func Diagnose(req Request) (Report, error) {
	req.DryRun = true
	return run(req)
}

func Repair(req Request) (Report, error) {
	return run(req)
}

func run(req Request) (Report, error) {
	req.SessionsDir = strings.TrimSpace(req.SessionsDir)
	if req.SessionsDir == "" {
		return Report{}, fmt.Errorf("missing sessions dir")
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	ids, err := sessionIDs(req.SessionsDir, req.SessionID)
	if err != nil {
		return Report{}, err
	}
	var report Report
	for _, id := range ids {
		item := newSessionRepair(req, id)
		sessionReport := item.run()
		report.addSession(sessionReport)
	}
	return report, nil
}

func sessionIDs(root, requested string) ([]string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		return []string{requested}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func newSessionRepair(req Request, sessionID string) *sessionRepair {
	return &sessionRepair{
		req:       req,
		sessionID: strings.TrimSpace(sessionID),
		dir:       filepath.Join(req.SessionsDir, strings.TrimSpace(sessionID)),
		report:    SessionReport{SessionID: strings.TrimSpace(sessionID)},
		backedUp:  map[string]bool{},
	}
}

func (r *sessionRepair) run() SessionReport {
	if r.sessionID == "" || r.sessionID != filepath.Base(r.sessionID) {
		r.report.Error = "invalid session id"
		return r.report
	}
	if info, err := os.Stat(r.dir); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		r.report.Error = err.Error()
		return r.report
	}
	r.emitRepairEvent(events.EventSessionRepairStarted, "started")
	if err := r.repairCheckpointPointer(); err != nil {
		r.fail(err)
		return r.report
	}
	if err := r.repairRootState(); err != nil {
		r.fail(err)
		return r.report
	}
	if err := r.repairTurns(); err != nil {
		r.fail(err)
		return r.report
	}
	if err := r.repairQueue(); err != nil {
		r.fail(err)
		return r.report
	}
	if r.report.Changed {
		r.report.BackupDir = r.backupDir
		r.emitRepairEvent(events.EventSessionRepairCompleted, "completed")
	}
	return r.report
}

func (r *sessionRepair) fail(err error) {
	r.report.Error = err.Error()
	r.emitRepairEvent(events.EventSessionRepairFailed, "failed")
}

func (r *sessionRepair) repairCheckpointPointer() error {
	pointerPath := filepath.Join(r.dir, checkpointPointer)
	data, exists, err := readOptional(pointerPath)
	if err != nil || !exists {
		return err
	}
	var pointer checkpointPointerPayload
	if err := json.Unmarshal(data, &pointer); err != nil || !validCheckpointID(pointer.Current) || !r.validCheckpoint(pointer.Current) {
		r.find(CodeCheckpointPointerInvalid, "medium", checkpointPointer, "checkpoint pointer does not point to a valid checkpoint")
		replacement := r.latestValidCheckpoint()
		switch {
		case replacement != "":
			return r.writeJSON(checkpointPointer, checkpointPointerPayload{Current: replacement, CreatedAt: r.req.Now}, CodeCheckpointPointerInvalid, "pointed checkpoint.json at latest valid checkpoint "+replacement)
		case r.rootStateValid():
			return r.removeFile(checkpointPointer, CodeCheckpointPointerInvalid, "removed invalid checkpoint pointer because root session files are valid")
		default:
			r.action(CodeCheckpointPointerInvalid, "skipped", checkpointPointer, "no valid checkpoint or root state available")
		}
	}
	return nil
}

func (r *sessionRepair) repairRootState() error {
	manifestData, manifestExists, manifestErr := readOptional(filepath.Join(r.dir, manifestFile))
	stateData, stateExists, stateErr := readOptional(filepath.Join(r.dir, stateFile))
	if manifestErr != nil {
		return manifestErr
	}
	if stateErr != nil {
		return stateErr
	}
	var decoded manifest
	manifestOK := manifestExists && json.Unmarshal(manifestData, &decoded) == nil
	stateOK := stateExists && json.Valid(stateData)
	if manifestOK && stateOK {
		actual := digest(stateData)
		if strings.TrimSpace(decoded.StateDigest) != actual {
			r.find(CodeManifestDigestRecomputed, "medium", manifestFile, "manifest state_digest does not match state.json")
			var raw map[string]any
			if err := json.Unmarshal(manifestData, &raw); err != nil {
				return err
			}
			raw["state_digest"] = actual
			raw["updated_at"] = r.req.Now
			raw["last_activity_at"] = r.req.Now
			return r.writeJSON(manifestFile, raw, CodeManifestDigestRecomputed, "recomputed manifest state_digest")
		}
		return nil
	}
	cp := r.latestValidCheckpoint()
	if cp == "" {
		if !manifestOK || !stateOK {
			r.find(CodeRootStateRestored, "high", stateFile, "root session files are missing or invalid and no valid checkpoint is available")
		}
		return nil
	}
	r.find(CodeRootStateRestored, "medium", stateFile, "root session files are missing or invalid; valid checkpoint is available")
	for _, name := range []string{manifestFile, stateFile, turnsFile, turnQueueFile} {
		src := filepath.Join(r.dir, checkpointsDir, cp, name)
		if _, err := os.Stat(src); err == nil {
			if err := r.copyFile(name, src, CodeRootStateRestored, "restored "+name+" from checkpoint "+cp); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *sessionRepair) repairTurns() error {
	path := filepath.Join(r.dir, turnsFile)
	data, exists, err := readOptional(path)
	if err != nil || !exists {
		return err
	}
	var turns []turnRecord
	if err := json.Unmarshal(data, &turns); err != nil {
		r.find(CodeStaleTurnInterrupted, "high", turnsFile, "turns.json is not valid JSON")
		return nil
	}
	changed := false
	for i := range turns {
		switch strings.TrimSpace(turns[i].Status) {
		case "running", "canceling":
			r.find(CodeStaleTurnInterrupted, "medium", turnsFile, "turn "+turns[i].ID+" was left "+turns[i].Status+" without an active runner")
			turns[i].Status = "interrupted"
			turns[i].Error = "Previous process stopped before this turn completed."
			turns[i].CanResume = true
			turns[i].ResumeAvailable = true
			turns[i].RecoveryHint = "Previous process stopped before this turn completed. Use resume to continue from the persisted checkpoint."
			turns[i].UpdatedAt = r.req.Now
			completed := r.req.Now
			turns[i].CompletedAt = &completed
			changed = true
		}
	}
	if changed {
		return r.writeJSON(turnsFile, turns, CodeStaleTurnInterrupted, "marked stale running turns interrupted")
	}
	return nil
}

func (r *sessionRepair) repairQueue() error {
	path := filepath.Join(r.dir, turnQueueFile)
	data, exists, err := readOptional(path)
	if err != nil || !exists {
		return err
	}
	var queue []queuedTurn
	if err := json.Unmarshal(data, &queue); err != nil {
		return nil
	}
	changed := false
	for i := range queue {
		if strings.TrimSpace(queue[i].Status) == "injected" {
			r.find(CodeOrphanInjectedQueued, "low", turnQueueFile, "injected queued turn "+queue[i].ID+" has no active runner")
			queue[i].Status = "queued"
			queue[i].Mode = "follow_up"
			queue[i].UpdatedAt = r.req.Now
			if queue[i].Envelope != nil {
				meta, _ := queue[i].Envelope["metadata"].(map[string]any)
				if meta == nil {
					meta = map[string]any{}
					queue[i].Envelope["metadata"] = meta
				}
				meta["queue_mode"] = "follow_up"
			}
			changed = true
		}
	}
	if changed {
		return r.writeJSON(turnQueueFile, queue, CodeOrphanInjectedQueued, "converted orphan injected queue items to follow_up queued turns")
	}
	return nil
}

func (r *sessionRepair) validCheckpoint(id string) bool {
	if !validCheckpointID(id) {
		return false
	}
	manifestData, exists, err := readOptional(filepath.Join(r.dir, checkpointsDir, id, manifestFile))
	if err != nil || !exists {
		return false
	}
	stateData, exists, err := readOptional(filepath.Join(r.dir, checkpointsDir, id, stateFile))
	if err != nil || !exists || !json.Valid(stateData) {
		return false
	}
	var decoded manifest
	if err := json.Unmarshal(manifestData, &decoded); err != nil {
		return false
	}
	return strings.TrimSpace(decoded.StateDigest) == digest(stateData)
}

func (r *sessionRepair) latestValidCheckpoint() string {
	entries, err := os.ReadDir(filepath.Join(r.dir, checkpointsDir))
	if err != nil {
		return ""
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && r.validCheckpoint(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (r *sessionRepair) rootStateValid() bool {
	manifestData, manifestExists, manifestErr := readOptional(filepath.Join(r.dir, manifestFile))
	stateData, stateExists, stateErr := readOptional(filepath.Join(r.dir, stateFile))
	if manifestErr != nil || stateErr != nil || !manifestExists || !stateExists || !json.Valid(stateData) {
		return false
	}
	var decoded manifest
	if err := json.Unmarshal(manifestData, &decoded); err != nil {
		return false
	}
	return strings.TrimSpace(decoded.StateDigest) == digest(stateData)
}

func (r *sessionRepair) writeJSON(rel string, value any, code, message string) error {
	r.action(code, actionStatus(r.req.DryRun), rel, message)
	if r.req.DryRun {
		return nil
	}
	if err := r.backup(rel); err != nil {
		return err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(r.dir, rel), value, 0644); err != nil {
		return err
	}
	r.report.Changed = true
	return nil
}

func (r *sessionRepair) copyFile(rel, src, code, message string) error {
	r.action(code, actionStatus(r.req.DryRun), rel, message)
	if r.req.DryRun {
		return nil
	}
	if err := r.backup(rel); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(r.dir, rel), data, 0644); err != nil {
		return err
	}
	r.report.Changed = true
	return nil
}

func (r *sessionRepair) removeFile(rel, code, message string) error {
	r.action(code, actionStatus(r.req.DryRun), rel, message)
	if r.req.DryRun {
		return nil
	}
	if err := r.backup(rel); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(r.dir, rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	r.report.Changed = true
	return nil
}

func (r *sessionRepair) backup(rel string) error {
	if r.backedUp[rel] {
		return nil
	}
	source := filepath.Join(r.dir, rel)
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		r.backedUp[rel] = true
		return nil
	}
	if err != nil {
		return err
	}
	if r.backupDir == "" {
		r.backupDir = filepath.Join(r.dir, ".repair-backups", r.req.Now.UTC().Format("20060102T150405.000000000Z"))
	}
	target := filepath.Join(r.backupDir, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(target, data, 0644); err != nil {
		return err
	}
	r.backedUp[rel] = true
	return nil
}

func (r *sessionRepair) emitRepairEvent(eventType events.EventType, status string) {
	if r.req.DryRun {
		return
	}
	event := events.Event{
		SessionID: r.sessionID,
		Type:      eventType,
		Timestamp: r.req.Now,
		Payload: events.SessionRepairPayload{
			Status:    status,
			Findings:  len(r.report.Findings),
			Actions:   len(r.report.Actions),
			BackupDir: filepath.ToSlash(r.backupDir),
		},
	}
	_ = appendEvent(filepath.Join(r.dir, eventJournalFile), event)
	_ = appendTimeline(filepath.Join(r.dir, timelineFile), event)
}

func (r *sessionRepair) find(code, severity, rel, message string) {
	r.report.Findings = append(r.report.Findings, Finding{Code: code, Severity: severity, Path: filepath.ToSlash(rel), Message: message})
}

func (r *sessionRepair) action(code, status, rel, message string) {
	r.report.Actions = append(r.report.Actions, Action{Code: code, Status: status, Path: filepath.ToSlash(rel), Message: message})
}

func actionStatus(dryRun bool) string {
	if dryRun {
		return "planned"
	}
	return "applied"
}

func validCheckpointID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && id != "." && id != ".." && id == filepath.Base(id)
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func appendEvent(path string, event events.Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func appendTimeline(path string, event events.Event) error {
	var items []events.Event
	if data, exists, err := readOptional(path); err == nil && exists {
		_ = json.Unmarshal(data, &items)
	}
	items = append(items, event)
	return fsutil.WriteJSONAtomic(path, items, 0644)
}
