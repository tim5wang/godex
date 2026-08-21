package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tim5wang/godex/internal/agent"
	coresec "github.com/tim5wang/godex/internal/core/security"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/sessiongraph"
	"github.com/tim5wang/godex/internal/sessionstore"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) loadSession(sessionID string, locator SessionLocator) (*sessionState, error) {
	now := s.now()
	session := &sessionState{
		id:         sessionID,
		locator:    locator,
		events:     events.NewBroadcaster(),
		gate:       make(chan struct{}, 1),
		createdAt:  now,
		updatedAt:  now,
		lastActive: now,
		timeline:   events.NewRecorder(200),
	}
	session.gate <- struct{}{}

	manifest, state, err := s.readSessionFiles(sessionID)
	if err != nil {
		return nil, err
	}
	graph, err := s.loadSessionGraph(sessionID)
	if err != nil {
		return nil, err
	}
	session.graph = graph
	session.timeline.Seed(s.readSessionTimeline(sessionID))
	session.seedTurns(s.readSessionTurns(sessionID))
	session.seedQueue(s.readSessionQueue(sessionID))
	session.events.Attach(persistentTimelineSink{service: s, session: session})
	if manifest != nil {
		session.locator = normalizeLocator(manifest.Locator)
		session.identity = manifest.Identity
		session.title = strings.TrimSpace(manifest.Title)
		session.modelProfileID = strings.TrimSpace(manifest.ModelProfileID)
		session.reasoningEffort = normalizeSessionReasoningEffort(manifest.ReasoningEffort)
		session.parentSessionID = strings.TrimSpace(manifest.ParentSessionID)
		session.forkedFromTurnID = strings.TrimSpace(manifest.ForkedFromTurnID)
		session.forkedFromMessageIndex = cloneIntPtr(manifest.ForkedFromMessageIndex)
		session.branchTitle = strings.TrimSpace(manifest.BranchTitle)
		session.createdAt = manifest.CreatedAt
		session.updatedAt = manifest.UpdatedAt
		session.lastActive = manifest.LastActivityAt
	}
	if session.modelProfileID == "" && s.cfg != nil {
		session.modelProfileID = s.cfg.DefaultProfileID
	}
	session.identity = agent.NormalizeAgentIdentity(session.identity, now, sessionID, "main", "GoDex", s.mainCapabilitySummary())

	// Sessions opened against an explicit per-session working directory
	// (locator metadata project_dir different from the service workspace)
	// get a cloned config pinned to that directory so their tools execute
	// there instead of in the service directory.  The default path keeps
	// using the shared global config untouched.
	sessionCfg := s.cfg
	if s.cfg != nil {
		baseDir := strings.TrimSpace(s.cfg.ProjectDir)
		if baseDir == "" {
			baseDir = strings.TrimSpace(s.cfg.WorkspaceDir)
		}
		if dir := cleanProjectDir(session.locator.Metadata[sessionProjectDirMetadataKey]); dir != "" && baseDir != "" && dir != cleanProjectDir(baseDir) {
			sessionCfg = agent.CloneConfigForWorkspace(s.cfg, dir)
		}
	}

	a := agent.NewForSession(sessionCfg, s.shared, sessionID)
	a.RegisterTools()
	// New-chat mode (locator metadata "mode", e.g. "minimal") pins the
	// initial active tool set and prompt complexity at creation time. It is
	// applied before RestoreStateForSession so a resumed session's persisted
	// bundle state wins over the creation preset.
	if mode := strings.TrimSpace(session.locator.Metadata["mode"]); mode != "" {
		a.ApplySessionMode(mode)
	}
	if session.modelProfileID != "" {
		if profile, ok := s.cfg.ModelProfileByID(session.modelProfileID); ok {
			if effort := normalizeSessionReasoningEffort(session.reasoningEffort); effort != "" {
				profile.ReasoningEffort = effort
			}
			a.ApplyModelProfile(profile)
		} else {
			// The persisted profile no longer exists in config.
			// Fall back to the default so the agent and
			// status bar agree on which model is active.
			session.modelProfileID = s.cfg.DefaultProfileID
		}
	}
	isNewSession := manifest == nil && state == nil
	if !isNewSession {
		a.RestoreStateForSession(sessionID, *state)
		_ = s.writeSessionGraph(session)
	} else {
		// Per-session skill preset (requested at creation) wins over the
		// global team.default_skills. Skills are a creation-time concern:
		// resumed sessions keep their persisted active-skill state.
		if requested := requestedSessionSkills(session.locator); len(requested) > 0 {
			a.LoadNamedSkills(requested)
		} else {
			a.LoadDefaultSkills()
		}
	}
	session.agent = a

	// Roadmap 6.1: route screener verdicts into the security audit trail.
	s.wireScreenAudit(a, sessionID)

	persistSession := false
	if (strings.TrimSpace(session.title) == "" || strings.TrimSpace(session.title) == "New chat") && state != nil {
		if derived := deriveSessionTitle(*state); derived != "" {
			session.title = derived
			persistSession = true
		}
	}
	if manifest != nil && strings.TrimSpace(manifest.Title) != strings.TrimSpace(session.title) {
		persistSession = true
	}
	if persistSession {
		if err := s.persistSession(session, now); err != nil {
			return nil, err
		}
	}
	if err := s.recoverInterruptedTurn(session, now); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *Service) readSessionFiles(sessionID string) (*SessionManifest, *agent.SessionState, error) {
	if data, ok, err := s.readSessionStoreData(context.Background(), sessionID); err != nil {
		return nil, nil, err
	} else if ok {
		return s.decodeStoredSessionFiles(sessionID, data)
	}
	var checkpointErr error
	var checkpoint *sessionCheckpointSnapshot
	if snapshot, ok, err := s.readSessionCheckpoint(sessionID); ok {
		if err == nil {
			checkpoint = snapshot
		} else {
			checkpointErr = err
		}
	}
	manifest, state, err := s.readLegacySessionFiles(sessionID)
	if err == nil {
		if checkpoint != nil && manifest != nil && checkpoint.Manifest != nil &&
			strings.TrimSpace(manifest.StateDigest) != strings.TrimSpace(checkpoint.Manifest.StateDigest) &&
			!s.legacySessionFilesNewerThanCheckpoint(sessionID) {
			return checkpoint.Manifest, checkpoint.State, nil
		}
		return manifest, state, nil
	}
	if checkpoint != nil {
		return checkpoint.Manifest, checkpoint.State, nil
	}
	if checkpointErr != nil {
		return nil, nil, checkpointErr
	}
	return nil, nil, err
}

func (s *Service) readSessionStoreData(ctx context.Context, sessionID string) (sessionstore.SessionData, bool, error) {
	if err := s.sqliteSessionStoreError(); err != nil {
		return sessionstore.SessionData{}, false, err
	}
	store := s.sqliteSessionStore()
	if store == nil {
		return sessionstore.SessionData{}, false, nil
	}
	return store.Load(ctx, sessionID)
}

func (s *Service) syncSessionStoreFromJSON(ctx context.Context, sessionID string) error {
	if err := s.sqliteSessionStoreError(); err != nil {
		return err
	}
	store := s.sqliteSessionStore()
	if store == nil || s == nil || s.cfg == nil {
		return nil
	}
	source := sessionstore.NewJSONStore(s.cfg.SessionsDir)
	data, ok, err := source.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return store.Save(ctx, data)
}

func (s *Service) decodeStoredSessionFiles(sessionID string, data sessionstore.SessionData) (*SessionManifest, *agent.SessionState, error) {
	if len(data.Manifest) == 0 || len(data.State) == 0 {
		return nil, nil, nil
	}
	var manifest SessionManifest
	if err := json.Unmarshal(data.Manifest, &manifest); err != nil {
		return nil, nil, newSessionCorruptError(sessionID, "decode %s: %v", manifestFileName, err)
	}
	var state agent.SessionState
	if err := json.Unmarshal(data.State, &state); err != nil {
		return nil, nil, newSessionCorruptError(sessionID, "decode %s: %v", stateFileName, err)
	}
	expected := strings.TrimSpace(manifest.StateDigest)
	if expected != "" && stateDigest(data.State) != expected {
		return nil, nil, newSessionCorruptError(sessionID, "state digest mismatch")
	}
	return &manifest, &state, nil
}

func (s *Service) sessionGraphPath(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), sessionGraphFileName)
}

func (s *Service) loadSessionGraph(sessionID string) (*sessiongraph.SessionGraph, error) {
	if data, ok, err := s.readSessionStoreData(context.Background(), sessionID); err != nil {
		return nil, err
	} else if ok && len(data.Graph) > 0 {
		var graph sessiongraph.SessionGraph
		if err := json.Unmarshal(data.Graph, &graph); err != nil {
			return nil, err
		}
		graph.EnsureMainBranch()
		return &graph, nil
	}
	store := sessiongraph.NewStore(s.sessionGraphPath(sessionID))
	graph, err := store.Load()
	if err != nil {
		graph = &sessiongraph.SessionGraph{}
	}
	if graph == nil {
		graph = &sessiongraph.SessionGraph{}
	}
	if _, ok := graph.Head(sessiongraph.MainBranchID); !ok {
		graph.EnsureMainBranch()
	}
	return graph, nil
}

func (s *Service) writeSessionGraph(session *sessionState) error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.graph == nil {
		session.graph = &sessiongraph.SessionGraph{}
	}
	session.graph.EnsureMainBranch()
	graph := session.graph.Clone()
	sessionID := session.id
	session.mu.Unlock()
	if err := sessiongraph.NewStore(s.sessionGraphPath(sessionID)).Save(graph); err != nil {
		return err
	}
	return s.syncSessionStoreFromJSON(context.Background(), sessionID)
}

// wireScreenAudit routes screener verdicts into the security audit trail
// (roadmap 6.1). Shadow-mode verdicts arrive fire-and-forget; the callback
// only records, it never gates the pipeline.
func (s *Service) wireScreenAudit(a *agent.Agent, sessionID string) {
	if a == nil {
		return
	}
	a.SetScreenAudit(func(hook coresec.ScreenHook, verdict coresec.ScreenVerdict) {
		severity := "info"
		summary := fmt.Sprintf("security screen %s: %s", hook, firstNonEmpty(verdict.Outcome, string(verdict.Decision)))
		if verdict.Unscreened {
			summary = fmt.Sprintf("security screen %s unavailable; content treated as untrusted data", hook)
		} else if verdict.Malicious() {
			severity = "warning"
			summary = fmt.Sprintf("security screen %s flagged %s (score %.2f >= threshold %.2f)", hook, verdict.Outcome, verdict.Score, verdict.Threshold)
		}
		s.appendSecurityEvent(security.SecurityEvent{
			Category:  "security",
			Action:    "screen_" + string(hook),
			Severity:  severity,
			SessionID: sessionID,
			Summary:   summary,
			Metadata: map[string]string{
				"hook":       string(hook),
				"decision":   string(verdict.Decision),
				"score":      fmt.Sprintf("%.3f", verdict.Score),
				"threshold":  fmt.Sprintf("%.3f", verdict.Threshold),
				"outcome":    verdict.Outcome,
				"unscreened": fmt.Sprintf("%t", verdict.Unscreened),
			},
		})
	})
}

func sessionGraphNodeID(prefix, id string) sessiongraph.NodeID {
	id = strings.TrimSpace(id)
	if id == "" {
		id = randomSuffix(8)
	}
	return sessiongraph.NodeID("node:" + strings.TrimSpace(prefix) + ":" + id)
}

func (s *Service) appendSessionGraphCheckpoint(session *sessionState, checkpointID, summary string) error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.graph == nil {
		session.graph = &sessiongraph.SessionGraph{}
	}
	session.graph.EnsureMainBranch()
	nodeID := sessionGraphNodeID("checkpoint", checkpointID)
	if _, err := session.graph.AppendNode(sessiongraph.MainBranchID, nodeID, sessiongraph.CheckpointRecord{
		CheckpointID: strings.TrimSpace(checkpointID),
		Summary:      strings.TrimSpace(summary),
	}); err != nil && !errors.Is(err, sessiongraph.ErrDuplicateID) {
		session.mu.Unlock()
		return err
	}
	session.mu.Unlock()
	return s.writeSessionGraph(session)
}

func (s *Service) cloneSessionGraphBranch(session *sessionState, fromBranch, branchID sessiongraph.BranchID, sourceNode sessiongraph.NodeID) error {
	if session == nil || branchID == "" {
		return nil
	}
	if fromBranch == "" {
		fromBranch = sessiongraph.MainBranchID
	}
	session.mu.Lock()
	if session.graph == nil {
		session.graph = &sessiongraph.SessionGraph{}
	}
	session.graph.EnsureMainBranch()
	if _, ok := session.graph.Head(branchID); !ok {
		if _, err := session.graph.CloneBranch(fromBranch, branchID); err != nil && !errors.Is(err, sessiongraph.ErrBranchExists) {
			session.mu.Unlock()
			return err
		}
	}
	if sourceNode != "" {
		if _, err := session.graph.RollbackBranch(branchID, sourceNode); err != nil && !errors.Is(err, sessiongraph.ErrNotFound) {
			session.mu.Unlock()
			return err
		}
	}
	session.mu.Unlock()
	return s.writeSessionGraph(session)
}

func (s *Service) appendSessionGraphMerge(session *sessionState, job agent.DurableSubagentJobView, summary string) error {
	workerBranch := sessiongraph.BranchID(firstNonEmpty(strings.TrimSpace(job.WorkerBranchID), strings.TrimSpace(job.SourceBranchID)))
	if session == nil || workerBranch == "" {
		return nil
	}
	sourceBranch := sessiongraph.BranchID(firstNonEmpty(strings.TrimSpace(job.SourceBranchID), string(sessiongraph.MainBranchID)))
	sourceNode := sessiongraph.NodeID(strings.TrimSpace(job.SourceNodeID))
	if err := s.cloneSessionGraphBranch(session, sourceBranch, workerBranch, sourceNode); err != nil {
		return err
	}
	session.mu.Lock()
	if session.graph == nil {
		session.graph = &sessiongraph.SessionGraph{}
	}
	session.graph.EnsureMainBranch()
	nodeID := sessionGraphNodeID("merge", firstNonEmpty(job.JobID, randomSuffix(8)))
	if _, err := session.graph.MergeBranch(sessiongraph.MainBranchID, workerBranch, nodeID, sessiongraph.MergeRecord{
		MergeID: strings.TrimSpace(job.JobID),
		Summary: strings.TrimSpace(summary),
	}); err != nil && !errors.Is(err, sessiongraph.ErrDuplicateID) {
		session.mu.Unlock()
		return err
	}
	session.mu.Unlock()
	return s.writeSessionGraph(session)
}

func (s *Service) legacySessionFilesNewerThanCheckpoint(sessionID string) bool {
	dir := s.sessionDir(sessionID)
	pointerInfo, err := os.Stat(filepath.Join(dir, checkpointPointerName))
	if err != nil {
		return false
	}
	for _, name := range []string{manifestFileName, stateFileName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.ModTime().After(pointerInfo.ModTime()) {
			return true
		}
	}
	return false
}

func (s *Service) readLegacySessionFiles(sessionID string) (*SessionManifest, *agent.SessionState, error) {
	dir := s.sessionDir(sessionID)
	manifestPath := filepath.Join(dir, manifestFileName)
	statePath := filepath.Join(dir, stateFileName)

	manifestData, manifestExists, err := readOptionalFile(manifestPath)
	if err != nil {
		return nil, nil, err
	}
	stateData, stateExists, err := readOptionalFile(statePath)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case !manifestExists && !stateExists:
		return nil, nil, nil
	case manifestExists != stateExists:
		missing := stateFileName
		if !manifestExists {
			missing = manifestFileName
		}
		return nil, nil, newSessionCorruptError(sessionID, "missing %s", missing)
	}

	var manifest *SessionManifest
	if manifestExists {
		var decoded SessionManifest
		if err := json.Unmarshal(manifestData, &decoded); err != nil {
			return nil, nil, newSessionCorruptError(sessionID, "decode %s: %v", manifestFileName, err)
		}
		manifest = &decoded
	}

	var state *agent.SessionState
	if stateExists {
		var decoded agent.SessionState
		if err := json.Unmarshal(stateData, &decoded); err != nil {
			return nil, nil, newSessionCorruptError(sessionID, "decode %s: %v", stateFileName, err)
		}
		if manifest != nil {
			expected := strings.TrimSpace(manifest.StateDigest)
			actual := stateDigest(stateData)
			if expected == "" {
				return nil, nil, newSessionCorruptError(sessionID, "missing state_digest in %s", manifestFileName)
			}
			if actual != expected {
				return nil, nil, newSessionCorruptError(sessionID, "state digest mismatch")
			}
		}
		state = &decoded
	}

	return manifest, state, nil
}

func (s *Service) readSessionCheckpoint(sessionID string) (*sessionCheckpointSnapshot, bool, error) {
	if data, ok, err := s.readSessionStoreData(context.Background(), sessionID); err != nil {
		return nil, false, err
	} else if ok && data.Checkpoint != nil && len(data.Checkpoint.Manifest) > 0 && len(data.Checkpoint.State) > 0 {
		return decodeStoredSessionCheckpoint(sessionID, data.Checkpoint)
	}
	dir := s.sessionDir(sessionID)
	pointerData, exists, err := readOptionalFile(filepath.Join(dir, checkpointPointerName))
	if err != nil || !exists {
		return nil, exists, err
	}
	var pointer sessionCheckpointPointer
	if err := json.Unmarshal(pointerData, &pointer); err != nil {
		return nil, true, newSessionCorruptError(sessionID, "decode %s: %v", checkpointPointerName, err)
	}
	current := strings.TrimSpace(pointer.Current)
	if current == "" || current == "." || current == ".." || current != filepath.Base(current) {
		return nil, true, newSessionCorruptError(sessionID, "invalid checkpoint pointer")
	}
	checkpointDir := filepath.Join(dir, checkpointsDirName, current)
	manifestData, exists, err := readOptionalFile(filepath.Join(checkpointDir, manifestFileName))
	if err != nil {
		return nil, true, err
	}
	if !exists {
		return nil, true, newSessionCorruptError(sessionID, "checkpoint missing %s", manifestFileName)
	}
	stateData, exists, err := readOptionalFile(filepath.Join(checkpointDir, stateFileName))
	if err != nil {
		return nil, true, err
	}
	if !exists {
		return nil, true, newSessionCorruptError(sessionID, "checkpoint missing %s", stateFileName)
	}

	var manifest SessionManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", manifestFileName, err)
	}
	expected := strings.TrimSpace(manifest.StateDigest)
	actual := stateDigest(stateData)
	if expected == "" {
		return nil, true, newSessionCorruptError(sessionID, "missing state_digest in checkpoint %s", manifestFileName)
	}
	if actual != expected {
		return nil, true, newSessionCorruptError(sessionID, "checkpoint state digest mismatch")
	}

	var state agent.SessionState
	if err := json.Unmarshal(stateData, &state); err != nil {
		return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", stateFileName, err)
	}

	snapshot := &sessionCheckpointSnapshot{Manifest: &manifest, State: &state}
	if data, exists, err := readOptionalFile(filepath.Join(checkpointDir, timelineFileName)); err != nil {
		return nil, true, err
	} else if exists {
		var decoded []events.Event
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", timelineFileName, err)
		}
		snapshot.Timeline = decoded
	}
	if data, exists, err := readOptionalFile(filepath.Join(checkpointDir, turnsFileName)); err != nil {
		return nil, true, err
	} else if exists {
		var decoded []TurnRecord
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", turnsFileName, err)
		}
		snapshot.Turns = normalizeTurnRecords(decoded)
	}
	if data, exists, err := readOptionalFile(filepath.Join(checkpointDir, turnQueueFileName)); err != nil {
		return nil, true, err
	} else if exists {
		var decoded []QueuedTurn
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", turnQueueFileName, err)
		}
		snapshot.Queue = normalizeQueuedTurns(decoded)
	}
	return snapshot, true, nil
}

func decodeStoredSessionCheckpoint(sessionID string, cp *sessionstore.CheckpointData) (*sessionCheckpointSnapshot, bool, error) {
	var manifest SessionManifest
	if err := json.Unmarshal(cp.Manifest, &manifest); err != nil {
		return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", manifestFileName, err)
	}
	expected := strings.TrimSpace(manifest.StateDigest)
	actual := stateDigest(cp.State)
	if expected != "" && actual != expected {
		return nil, true, newSessionCorruptError(sessionID, "checkpoint state digest mismatch")
	}
	var state agent.SessionState
	if err := json.Unmarshal(cp.State, &state); err != nil {
		return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", stateFileName, err)
	}
	snapshot := &sessionCheckpointSnapshot{Manifest: &manifest, State: &state}
	if len(cp.Timeline) > 0 {
		var timeline []events.Event
		if err := json.Unmarshal(cp.Timeline, &timeline); err != nil {
			return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", timelineFileName, err)
		}
		snapshot.Timeline = timeline
	}
	if len(cp.Turns) > 0 {
		var turns []TurnRecord
		if err := json.Unmarshal(cp.Turns, &turns); err != nil {
			return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", turnsFileName, err)
		}
		snapshot.Turns = normalizeTurnRecords(turns)
	}
	if len(cp.Queue) > 0 {
		var queue []QueuedTurn
		if err := json.Unmarshal(cp.Queue, &queue); err != nil {
			return nil, true, newSessionCorruptError(sessionID, "decode checkpoint %s: %v", turnQueueFileName, err)
		}
		snapshot.Queue = normalizeQueuedTurns(queue)
	}
	return snapshot, true, nil
}

func (s *Service) readSessionState(sessionID string) (*agent.SessionState, error) {
	if _, state, err := s.readSessionFiles(sessionID); err != nil || state != nil {
		return state, err
	}
	statePath := filepath.Join(s.sessionDir(sessionID), stateFileName)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}

	var decoded agent.SessionState
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, newSessionCorruptError(sessionID, "decode %s: %v", stateFileName, err)
	}
	return &decoded, nil
}

func (s *Service) readSessionTimeline(sessionID string) []events.Event {
	if data, ok, err := s.readSessionStoreData(context.Background(), sessionID); err == nil && ok && len(data.Timeline) > 0 {
		var decoded []events.Event
		if json.Unmarshal(data.Timeline, &decoded) == nil {
			return decoded
		}
	}
	if journal := s.readSessionEventJournal(sessionID); len(journal) > 0 {
		return journal
	}
	if checkpoint, ok, err := s.readSessionCheckpoint(sessionID); ok && err == nil && checkpoint != nil {
		if s.legacyFileNewerThanCheckpoint(sessionID, timelineFileName) {
			return s.readRootSessionTimeline(sessionID)
		}
		return checkpoint.Timeline
	}
	return s.readRootSessionTimeline(sessionID)
}

func (s *Service) readRootSessionTimeline(sessionID string) []events.Event {
	data, exists, err := readOptionalFile(filepath.Join(s.sessionDir(sessionID), timelineFileName))
	if err != nil || !exists {
		return nil
	}
	var decoded []events.Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil
	}
	return decoded
}

func (s *Service) legacyFileNewerThanCheckpoint(sessionID, name string) bool {
	pointerInfo, err := os.Stat(filepath.Join(s.sessionDir(sessionID), checkpointPointerName))
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(s.sessionDir(sessionID), name))
	return err == nil && info.ModTime().After(pointerInfo.ModTime())
}

func (s *Service) readSessionEventJournal(sessionID string) []events.Event {
	if data, ok, err := s.readSessionStoreData(context.Background(), sessionID); err == nil && ok && len(data.EventJournal) > 0 {
		return decodeSessionEventJournal(data.EventJournal)
	}
	file, err := os.Open(filepath.Join(s.sessionDir(sessionID), eventJournalFileName))
	if err != nil {
		return nil
	}
	defer file.Close()

	return decodeSessionEventJournalReader(bufio.NewReader(file))
}

func decodeSessionEventJournal(data []byte) []events.Event {
	return decodeSessionEventJournalReader(bufio.NewReader(bytes.NewReader(data)))
}

func decodeSessionEventJournalReader(reader *bufio.Reader) []events.Event {
	var out []events.Event
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var event events.Event
			if err := json.Unmarshal(line, &event); err == nil && events.RecordableEvent(event) {
				out = append(out, event)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return out
		}
	}
	return out
}

func (s *Service) readSessionTurns(sessionID string) []TurnRecord {
	if checkpoint, ok, err := s.readSessionCheckpoint(sessionID); ok && err == nil && checkpoint != nil {
		return checkpoint.Turns
	}
	data, exists, err := readOptionalFile(filepath.Join(s.sessionDir(sessionID), turnsFileName))
	if err != nil || !exists {
		return nil
	}
	var decoded []TurnRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil
	}
	return normalizeTurnRecords(decoded)
}

func (s *Service) readSessionQueue(sessionID string) []QueuedTurn {
	if checkpoint, ok, err := s.readSessionCheckpoint(sessionID); ok && err == nil && checkpoint != nil {
		return checkpoint.Queue
	}
	data, exists, err := readOptionalFile(filepath.Join(s.sessionDir(sessionID), turnQueueFileName))
	if err != nil || !exists {
		return nil
	}
	var decoded []QueuedTurn
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil
	}
	return normalizeQueuedTurns(decoded)
}

func (s *Service) appendSessionEventJournal(session *sessionState, event events.Event) error {
	if session == nil || !events.RecordableEvent(event) {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	session.timelineMu.Lock()
	defer session.timelineMu.Unlock()
	dir := s.sessionDir(session.id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, eventJournalFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return s.syncSessionStoreFromJSON(context.Background(), session.id)
}

// rotateSessionEventJournal truncates the append-only event journal after a
// turn has reached a terminal state and all its events have been captured by a
// durable checkpoint. The journal therefore only ever carries the crash-
// recovery delta since the last completed turn, instead of growing unboundedly
// for the life of the session. Rotation (truncate file + refresh the SQLite
// EventJournal copy) is best-effort: a failure never fails the caller, and a
// stale journal is harmless because events are deduplicated on replay.
//
// The truncation runs under timelineMu so it never races an in-flight append,
// and it is only safe to call once the session has no active turn.
func (s *Service) rotateSessionEventJournal(session *sessionState) error {
	if session == nil {
		return nil
	}
	session.timelineMu.Lock()
	defer session.timelineMu.Unlock()
	if err := os.Truncate(filepath.Join(s.sessionDir(session.id), eventJournalFileName), 0); err != nil {
		// A missing journal is a valid pre-rotate state (nothing to rotate yet);
		// treat it as a no-op rather than an error.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return s.syncSessionStoreFromJSON(context.Background(), session.id)
}

func (s *Service) writeSessionTimeline(session *sessionState) error {
	if session == nil || session.timeline == nil {
		return nil
	}
	session.timelineMu.Lock()
	defer session.timelineMu.Unlock()
	if err := fsutil.WriteJSONAtomic(filepath.Join(s.sessionDir(session.id), timelineFileName), session.timeline.Entries(0), 0644); err != nil {
		return err
	}
	return s.syncSessionStoreFromJSON(context.Background(), session.id)
}

func (s *Service) writeSessionTurns(session *sessionState) error {
	if session == nil {
		return nil
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(s.sessionDir(session.id), turnsFileName), session.turnRecords(0), 0644); err != nil {
		return err
	}
	return s.syncSessionStoreFromJSON(context.Background(), session.id)
}

func (s *Service) writeSessionQueue(session *sessionState) error {
	if session == nil {
		return nil
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(s.sessionDir(session.id), turnQueueFileName), session.queuedTurns(0), 0644); err != nil {
		return err
	}
	return s.syncSessionStoreFromJSON(context.Background(), session.id)
}

func (s *Service) recoverInterruptedTurn(session *sessionState, now time.Time) error {
	turnID := session.interruptedTurnIDFromRecords()
	if turnID == "" {
		turnID = interruptedTurnID(session.timeline.Entries(0))
	}
	if turnID == "" {
		return nil
	}
	session.updateTurnStatus(turnID, "interrupted", "", "Previous process stopped before this turn completed.", now)
	session.markTurnResumeAvailable(turnID, "Previous process stopped before this turn completed. Use resume to continue from the persisted checkpoint.", now)
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    turnID,
		Type:      events.EventWarningRaised,
		Timestamp: now,
		Payload: events.NoticePayload{
			Message: "Previous process stopped before this turn completed.",
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   false,
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    turnID,
		Type:      events.EventTurnCompleted,
		Timestamp: now,
		Payload:   events.TurnPayload{Status: "interrupted"},
	})
	if err := s.writeSessionTurns(session); err != nil {
		return err
	}
	if s.cfg != nil && s.cfg.Runtime.Recovery.AutoResumeInterruptedTurns && !session.hasQueuedRecoveryFor(turnID) {
		// Never auto-recover a turn that is itself an auto-generated recovery
		// turn. Once a resume has run and was interrupted again (for example
		// the task is stuck on a bash call that cannot finish), queuing yet
		// another "Resume interrupted turn …" produces an unbounded chain:
		// R1 -> resume R1 -> R2 -> resume R2 -> … on every process restart.
		// Each original turn therefore gets at most one automatic resume
		// attempt; afterwards the user decides whether to resume manually.
		if record, ok := session.turnRecordByID(turnID); ok && isRecoveryTurnRecord(record) {
			session.events.Emit(events.Event{
				SessionID: session.id,
				TurnID:    turnID,
				Type:      events.EventWarningRaised,
				Timestamp: now,
				Payload: events.NoticePayload{
					Message: "A previous automatic resume of this turn was interrupted as well; automatic recovery is disabled to avoid an endless resume loop. Resume manually if you still want to continue this task.",
				},
			})
		} else {
			recoveryID := session.nextTurnID(now)
			envelope := message.NewRuntimeEnvelope(
				message.SourceCommand,
				session.id,
				"runtime",
				fmt.Sprintf("Resume interrupted turn %s from the persisted checkpoint and continue the previous task.", turnID),
				now,
				map[string]string{"recovery_of_turn_id": turnID, "kind": "interrupted_turn_recovery"},
			)
			session.enqueue(QueuedTurn{
				ID:        recoveryID,
				Mode:      QueueModeFollowUp,
				Status:    "queued",
				Source:    string(envelope.Source),
				Sender:    strings.TrimSpace(envelope.Sender),
				Summary:   turnSummary(envelope.BodyText()),
				CreatedAt: now,
				UpdatedAt: now,
				Envelope:  envelope.Normalized(),
			})
			if err := s.writeSessionQueue(session); err != nil {
				return err
			}
		}
	}
	return s.persistSession(session, now)
}

// isRecoveryTurnRecord reports whether a turn record is an auto-generated
// "Resume interrupted turn …" recovery. The authoritative marker is the
// envelope metadata written when the recovery was queued; the summary prefix
// is a defensive fallback for records whose envelope was not persisted.
func isRecoveryTurnRecord(record TurnRecord) bool {
	if record.Envelope != nil && record.Envelope.Metadata != nil {
		if strings.EqualFold(strings.TrimSpace(record.Envelope.Metadata["kind"]), "interrupted_turn_recovery") {
			return true
		}
		if strings.TrimSpace(record.Envelope.Metadata["recovery_of_turn_id"]) != "" {
			return true
		}
	}
	return strings.HasPrefix(strings.TrimSpace(record.Summary), "Resume interrupted turn")
}

func interruptedTurnID(items []events.Event) string {
	started := make(map[string]bool)
	completed := make(map[string]bool)
	order := make([]string, 0)
	seen := make(map[string]bool)
	for _, item := range items {
		turnID := strings.TrimSpace(item.TurnID)
		if turnID == "" {
			continue
		}
		switch item.Type {
		case events.EventUserMessageAccepted:
			started[turnID] = true
			if !seen[turnID] {
				seen[turnID] = true
				order = append(order, turnID)
			}
		case events.EventTurnCompleted:
			completed[turnID] = true
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		turnID := order[i]
		if started[turnID] && !completed[turnID] {
			return turnID
		}
	}
	return ""
}
