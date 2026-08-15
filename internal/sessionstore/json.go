package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

const (
	manifestFile      = "manifest.json"
	stateFile         = "state.json"
	timelineFile      = "timeline.json"
	turnsFile         = "turns.json"
	turnQueueFile     = "turn_queue.json"
	eventJournalFile  = "events.jsonl"
	graphFile         = "graph.json"
	checkpointPointer = "checkpoint.json"
	checkpointsDir    = "checkpoints"
)

type JSONStore struct {
	root string
}

func NewJSONStore(root string) *JSONStore {
	return &JSONStore{root: root}
}

// LoadManifest reads only the small list metadata file. It intentionally
// avoids state/timeline/checkpoint I/O used by the full Load path.
func (s *JSONStore) LoadManifest(ctx context.Context, id string) (json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return readOptional(filepath.Join(s.root, id, manifestFile))
}

func (s *JSONStore) Load(ctx context.Context, id string) (SessionData, bool, error) {
	if err := ctx.Err(); err != nil {
		return SessionData{}, false, err
	}
	dir := filepath.Join(s.root, id)
	manifest, hasManifest, err := readOptional(filepath.Join(dir, manifestFile))
	if err != nil {
		return SessionData{}, false, err
	}
	state, hasState, err := readOptional(filepath.Join(dir, stateFile))
	if err != nil {
		return SessionData{}, false, err
	}
	if !hasManifest && !hasState {
		return SessionData{}, false, nil
	}
	if !hasManifest || !hasState {
		return SessionData{}, false, fmt.Errorf("session %q missing required manifest/state", id)
	}
	data := SessionData{SessionID: id, Manifest: manifest, State: state}
	if data.Timeline, _, err = readOptional(filepath.Join(dir, timelineFile)); err != nil {
		return SessionData{}, false, err
	}
	if data.Turns, _, err = readOptional(filepath.Join(dir, turnsFile)); err != nil {
		return SessionData{}, false, err
	}
	if data.Queue, _, err = readOptional(filepath.Join(dir, turnQueueFile)); err != nil {
		return SessionData{}, false, err
	}
	if data.EventJournal, _, err = readOptional(filepath.Join(dir, eventJournalFile)); err != nil {
		return SessionData{}, false, err
	}
	if data.Graph, _, err = readOptional(filepath.Join(dir, graphFile)); err != nil {
		return SessionData{}, false, err
	}
	checkpoint, err := s.loadCheckpoint(dir)
	if err != nil {
		return SessionData{}, false, err
	}
	data.Checkpoint = checkpoint
	return data, true, nil
}

func (s *JSONStore) Save(ctx context.Context, data SessionData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(data.SessionID) == "" {
		return fmt.Errorf("missing session id")
	}
	dir := filepath.Join(s.root, data.SessionID)
	if err := writeRequiredBlob(filepath.Join(dir, manifestFile), data.Manifest); err != nil {
		return err
	}
	if err := writeRequiredBlob(filepath.Join(dir, stateFile), data.State); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(dir, timelineFile), data.Timeline); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(dir, turnsFile), data.Turns); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(dir, turnQueueFile), data.Queue); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(dir, eventJournalFile), data.EventJournal); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(dir, graphFile), data.Graph); err != nil {
		return err
	}
	if data.Checkpoint == nil {
		if err := os.Remove(filepath.Join(dir, checkpointPointer)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return s.saveCheckpoint(dir, data.Checkpoint)
}

func (s *JSONStore) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		_, ok, err := s.Load(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *JSONStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.RemoveAll(filepath.Join(s.root, id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *JSONStore) Diagnostics(ctx context.Context) Diagnostics {
	diag := Diagnostics{Backend: string(BackendJSON), Path: s.root}
	if err := ctx.Err(); err != nil {
		diag.Error = err.Error()
		return diag
	}
	if err := os.MkdirAll(s.root, 0755); err != nil {
		diag.Error = err.Error()
		return diag
	}
	diag.Healthy = true
	return diag
}

func (s *JSONStore) loadCheckpoint(sessionDir string) (*CheckpointData, error) {
	pointer, ok, err := readOptional(filepath.Join(sessionDir, checkpointPointer))
	if err != nil || !ok {
		return nil, err
	}
	cp := &CheckpointData{Pointer: pointer, ID: checkpointID(pointer)}
	cpDir := filepath.Join(sessionDir, checkpointsDir, cp.ID)
	if cp.ID == "" {
		return cp, nil
	}
	if cp.Manifest, _, err = readOptional(filepath.Join(cpDir, manifestFile)); err != nil {
		return nil, err
	}
	if cp.State, _, err = readOptional(filepath.Join(cpDir, stateFile)); err != nil {
		return nil, err
	}
	if cp.Timeline, _, err = readOptional(filepath.Join(cpDir, timelineFile)); err != nil {
		return nil, err
	}
	if cp.Turns, _, err = readOptional(filepath.Join(cpDir, turnsFile)); err != nil {
		return nil, err
	}
	if cp.Queue, _, err = readOptional(filepath.Join(cpDir, turnQueueFile)); err != nil {
		return nil, err
	}
	return cp, nil
}

func (s *JSONStore) saveCheckpoint(sessionDir string, cp *CheckpointData) error {
	if err := writeRequiredBlob(filepath.Join(sessionDir, checkpointPointer), cp.Pointer); err != nil {
		return err
	}
	if strings.TrimSpace(cp.ID) == "" {
		return nil
	}
	cpDir := filepath.Join(sessionDir, checkpointsDir, cp.ID)
	if err := writeOptionalBlob(filepath.Join(cpDir, manifestFile), cp.Manifest); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(cpDir, stateFile), cp.State); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(cpDir, timelineFile), cp.Timeline); err != nil {
		return err
	}
	if err := writeOptionalBlob(filepath.Join(cpDir, turnsFile), cp.Turns); err != nil {
		return err
	}
	return writeOptionalBlob(filepath.Join(cpDir, turnQueueFile), cp.Queue)
}

func checkpointID(pointer json.RawMessage) string {
	var payload struct {
		Current string `json:"current"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal(pointer, &payload); err != nil {
		return ""
	}
	if payload.Current != "" {
		return payload.Current
	}
	return payload.ID
}

func readOptional(path string) (json.RawMessage, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return json.RawMessage(data), true, nil
}

func writeRequiredBlob(path string, data json.RawMessage) error {
	if len(data) == 0 {
		return fmt.Errorf("missing required blob %s", filepath.Base(path))
	}
	return fsutil.WriteFileAtomic(path, data, 0644)
}

func writeOptionalBlob(path string, data json.RawMessage) error {
	if len(data) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return fsutil.WriteFileAtomic(path, data, 0644)
}
