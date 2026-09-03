package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/storagegc"
	"github.com/tim5wang/godex/internal/sessionstore"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) persistSession(session *sessionState, updatedAt time.Time) error {
	state := session.agent.ExportStateForSession(session.id)

	session.mu.Lock()
	session.updatedAt = updatedAt
	session.lastActive = updatedAt
	modelProfileID := strings.TrimSpace(session.modelProfileID)
	reasoningEffort := normalizeSessionReasoningEffort(session.reasoningEffort)
	acpModel := strings.TrimSpace(session.acpModel)
	acpSessionID := strings.TrimSpace(session.acpSessionID)
	parentSessionID := strings.TrimSpace(session.parentSessionID)
	forkedFromTurnID := strings.TrimSpace(session.forkedFromTurnID)
	forkedFromMessageIndex := cloneIntPtr(session.forkedFromMessageIndex)
	branchTitle := strings.TrimSpace(session.branchTitle)
	identity := session.identity
	identity.UpdatedAt = updatedAt
	session.identity = identity
	manifest := SessionManifest{
		SessionID:              session.id,
		Locator:                session.locator,
		Identity:               identity,
		Title:                  session.title,
		ModelProfileID:         modelProfileID,
		ReasoningEffort:        reasoningEffort,
		AcpModel:               acpModel,
		AcpSessionID:           acpSessionID,
		ParentSessionID:        parentSessionID,
		ForkedFromTurnID:       forkedFromTurnID,
		ForkedFromMessageIndex: forkedFromMessageIndex,
		BranchTitle:            branchTitle,
		CreatedAt:              session.createdAt,
		UpdatedAt:              session.updatedAt,
		LastActivityAt:         session.lastActive,
	}
	session.mu.Unlock()

	dir := s.sessionDir(session.id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	manifest.StateDigest = stateDigest(stateData)
	timeline := session.timeline.Entries(0)
	turns := session.turnRecords(0)
	queue := session.queuedTurns(0)
	checkpointID, err := s.writeSessionCheckpoint(session.id, manifest, stateData, timeline, turns, queue, updatedAt)
	if err != nil {
		return err
	}
	_ = fsutil.WriteFileAtomic(filepath.Join(dir, stateFileName), stateData, 0644)
	_ = s.writeManifest(manifest)
	_ = fsutil.WriteJSONAtomic(filepath.Join(dir, timelineFileName), timeline, 0644)
	_ = fsutil.WriteJSONAtomic(filepath.Join(dir, turnsFileName), turns, 0644)
	_ = fsutil.WriteJSONAtomic(filepath.Join(dir, turnQueueFileName), queue, 0644)
	_ = s.appendSessionGraphCheckpoint(session, checkpointID, session.title)
	return s.saveSessionToStore(session, manifest, stateData, timeline, turns, queue, checkpointID, updatedAt)
}

func (s *Service) saveSessionToStore(session *sessionState, manifest SessionManifest, stateData []byte, timeline []events.Event, turns []TurnRecord, queue []QueuedTurn, checkpointID string, updatedAt time.Time) error {
	if err := s.sqliteSessionStoreError(); err != nil {
		return err
	}
	store := s.sqliteSessionStore()
	if store == nil || session == nil {
		return nil
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	timelineData, err := json.MarshalIndent(timeline, "", "  ")
	if err != nil {
		return err
	}
	turnsData, err := json.MarshalIndent(turns, "", "  ")
	if err != nil {
		return err
	}
	queueData, err := json.MarshalIndent(queue, "", "  ")
	if err != nil {
		return err
	}
	var graphData []byte
	session.mu.RLock()
	if session.graph != nil {
		graphData, _ = json.MarshalIndent(session.graph.Clone(), "", "  ")
	}
	sessionID := session.id
	session.mu.RUnlock()
	pointerData, err := json.MarshalIndent(sessionCheckpointPointer{Current: checkpointID, CreatedAt: updatedAt}, "", "  ")
	if err != nil {
		return err
	}
	data := sessionstore.SessionData{
		SessionID: sessionID,
		Manifest:  manifestData,
		State:     append([]byte{}, stateData...),
		Timeline:  timelineData,
		Turns:     turnsData,
		Queue:     queueData,
		Graph:     graphData,
		Checkpoint: &sessionstore.CheckpointData{
			ID:       checkpointID,
			Pointer:  pointerData,
			Manifest: manifestData,
			State:    append([]byte{}, stateData...),
			Timeline: timelineData,
			Turns:    turnsData,
			Queue:    queueData,
		},
	}
	if journal, exists, err := readOptionalFile(filepath.Join(s.sessionDir(sessionID), eventJournalFileName)); err == nil && exists {
		data.EventJournal = journal
	}
	return store.Save(context.Background(), data)
}

func (s *Service) writeSessionCheckpoint(sessionID string, manifest SessionManifest, stateData []byte, timeline []events.Event, turns []TurnRecord, queue []QueuedTurn, at time.Time) (string, error) {
	_ = timeline
	suffix := randomSuffix(4)
	if suffix == "" {
		suffix = fmt.Sprintf("%x", at.UnixNano())
	}
	checkpointID := at.UTC().Format("20060102T150405.000000000Z") + "-" + suffix
	checkpointDir := filepath.Join(s.sessionDir(sessionID), checkpointsDirName, checkpointID)
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		return "", err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(checkpointDir, stateFileName), stateData, 0644); err != nil {
		return "", err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(checkpointDir, manifestFileName), manifest, 0644); err != nil {
		return "", err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(checkpointDir, turnsFileName), turns, 0644); err != nil {
		return "", err
	}
	if err := fsutil.WriteJSONAtomic(filepath.Join(checkpointDir, turnQueueFileName), queue, 0644); err != nil {
		return "", err
	}
	pointer := sessionCheckpointPointer{Current: checkpointID, CreatedAt: at}
	if err := fsutil.WriteJSONAtomic(filepath.Join(s.sessionDir(sessionID), checkpointPointerName), pointer, 0644); err != nil {
		return "", err
	}
	if s.cfg != nil && s.cfg.Storage.SessionCheckpointAutoPrune {
		_, err := storagegc.CleanSessionCheckpoints(storagegc.Options{
			SessionsDir:                 s.cfg.SessionsDir,
			SessionCheckpointKeepLatest: s.cfg.Storage.SessionCheckpointKeepLatest,
			SessionCheckpointTTL:        time.Duration(s.cfg.Storage.SessionCheckpointTTLHours) * time.Hour,
			Now:                         at,
		})
		return checkpointID, err
	}
	return checkpointID, nil
}

func (s *Service) writeManifest(manifest SessionManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(s.sessionDir(manifest.SessionID), manifestFileName), data, 0644)
}

func (s *Service) snapshotFromSession(session *sessionState) Snapshot {
	session.mu.RLock()
	locator := session.locator
	running := session.running
	activeTurnID := ""
	activePhase := ""
	if session.active != nil {
		activeTurnID = session.active.id
		activePhase = session.active.phase
	}
	modelProfileID := strings.TrimSpace(session.modelProfileID)
	reasoningEffort := normalizeSessionReasoningEffort(session.reasoningEffort)
	identity := session.identity
	updatedAt := session.updatedAt
	session.mu.RUnlock()

	modelMessages := session.agent.GetMessages()
	displayMessages := snapshotDisplayMessages(modelMessages)
	pendingPermissions := session.agent.PendingPermissions(session.id)
	turns := session.snapshotTurnRecords(snapshotTurnLimit)
	return Snapshot{
		SessionID:               session.id,
		Locator:                 locator,
		Messages:                displayMessages,
		DisplayMessages:         displayMessages,
		Tasks:                   session.agent.TaskMgr().List(),
		Todos:                   session.agent.TodoMgr().List(),
		Team:                    session.agent.TeamMgr().List(),
		ActiveSkills:            session.agent.ActiveSkillNames(),
		ToolCatalog:             session.agent.ToolCatalog(),
		PendingPermissions:      pendingPermissions,
		ActivePermissionBlocker: activePermissionBlocker(pendingPermissions, turns, s.now()),
		Timeline:                session.timeline.Entries(snapshotTimelineLimit),
		Turns:                   turns,
		Running:                 running,
		ActiveTurnID:            activeTurnID,
		ActivePhase:             activePhase,
		Identity:                identity,
		ModelProfileID:          modelProfileID,
		ReasoningEffort:         reasoningEffort,
		QueuedTurns:             session.snapshotQueuedTurns(snapshotTurnLimit),
		UpdatedAt:               updatedAt,
	}
}
