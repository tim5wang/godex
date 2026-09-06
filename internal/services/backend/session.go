package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/services/sessionrepair"
	"github.com/tim5wang/godex/internal/sessiongraph"
	"github.com/tim5wang/godex/internal/sessionstore"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func newSessionStore(cfg *config.Config) (sessionstore.Store, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing config")
	}
	backend := strings.ToLower(strings.TrimSpace(cfg.Storage.SessionBackend))
	if backend == "sqlite" {
		path := strings.TrimSpace(cfg.Storage.SQLitePath)
		if path == "" {
			path = filepath.Join(cfg.StateDir, "session-store.sqlite")
		}
		store, err := sessionstore.NewSQLiteStore(path)
		if err == nil {
			return store, nil
		}
		return nil, err
	}
	return sessionstore.NewJSONStore(cfg.SessionsDir), nil
}

func (s *Service) sqliteSessionStore() sessionstore.Store {
	if s == nil || s.store == nil || s.cfg == nil {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(s.cfg.Storage.SessionBackend)) != "sqlite" {
		return nil
	}
	return s.store
}

func (s *Service) sqliteSessionStoreError() error {
	if s == nil || s.cfg == nil || strings.ToLower(strings.TrimSpace(s.cfg.Storage.SessionBackend)) != "sqlite" {
		return nil
	}
	if s.storeErr != nil {
		return s.storeErr
	}
	if s.store == nil {
		return fmt.Errorf("sqlite session store unavailable")
	}
	return nil
}

func (s *Service) SessionStoreDiagnostics(ctx context.Context) sessionstore.Diagnostics {
	if s == nil {
		return sessionstore.Diagnostics{Healthy: false, Error: "session store unavailable"}
	}
	if s.storeErr != nil {
		backend := ""
		sqlitePath := ""
		if s.cfg != nil {
			backend = strings.ToLower(strings.TrimSpace(s.cfg.Storage.SessionBackend))
			sqlitePath = strings.TrimSpace(s.cfg.Storage.SQLitePath)
			if backend == "sqlite" && sqlitePath == "" {
				sqlitePath = filepath.Join(s.cfg.StateDir, "session-store.sqlite")
			}
		}
		return sessionstore.Diagnostics{Backend: backend, SQLitePath: sqlitePath, Healthy: false, Error: s.storeErr.Error()}
	}
	if s.store == nil {
		return sessionstore.Diagnostics{Healthy: false, Error: "session store unavailable"}
	}
	return s.store.Diagnostics(ctx)
}

func (s *Service) ExportSessionToStore(ctx context.Context, sessionID string, dst sessionstore.Store) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("session store unavailable")
	}
	if s.storeErr != nil {
		return s.storeErr
	}
	return sessionstore.CopySession(ctx, dst, s.store, sessionID)
}

func (s *Service) ImportSessionFromStore(ctx context.Context, sessionID string, src sessionstore.Store) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("session store unavailable")
	}
	if s.storeErr != nil {
		return s.storeErr
	}
	if err := sessionstore.CopySession(ctx, s.store, src, sessionID); err != nil {
		return err
	}
	if s.cfg != nil {
		return sessionstore.CopySession(ctx, sessionstore.NewJSONStore(s.cfg.SessionsDir), s.store, sessionID)
	}
	return nil
}

func (s *Service) autoRepairSessions() {
	if s == nil || s.cfg == nil || !s.cfg.Runtime.Recovery.AutoRepairSessions || strings.TrimSpace(s.cfg.SessionsDir) == "" {
		return
	}
	_, _ = sessionrepair.Repair(sessionrepair.Request{
		SessionsDir: s.cfg.SessionsDir,
		Now:         s.now(),
	})
}

// DiagnoseSessions inspects persisted session state and reports low-risk
// deterministic repairs without mutating files.
func (s *Service) DiagnoseSessions(ctx context.Context, req sessionrepair.Request) (sessionrepair.Report, error) {
	_ = ctx
	if strings.TrimSpace(req.SessionsDir) == "" && s != nil && s.cfg != nil {
		req.SessionsDir = s.cfg.SessionsDir
	}
	req.DryRun = true
	if req.Now.IsZero() && s != nil {
		req.Now = s.now()
	}
	return sessionrepair.Diagnose(req)
}

// RepairSessions applies deterministic low-risk session state repairs.
func (s *Service) RepairSessions(ctx context.Context, req sessionrepair.Request) (sessionrepair.Report, error) {
	_ = ctx
	if strings.TrimSpace(req.SessionsDir) == "" && s != nil && s.cfg != nil {
		req.SessionsDir = s.cfg.SessionsDir
	}
	if req.Now.IsZero() && s != nil {
		req.Now = s.now()
	}
	return sessionrepair.Repair(req)
}

func (s *Service) recoverQueuedSessions() {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.SessionsDir) == "" {
		return
	}
	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(s.readSessionQueue(entry.Name())) == 0 {
			continue
		}
		manifest, _, err := s.readSessionFiles(entry.Name())
		if err != nil || manifest == nil {
			continue
		}
		s.mu.Lock()
		if s.sessions[entry.Name()] != nil {
			s.mu.Unlock()
			continue
		}
		loaded, err := s.loadSession(entry.Name(), normalizeLocator(manifest.Locator))
		if err != nil {
			s.mu.Unlock()
			continue
		}
		s.sessions[entry.Name()] = loaded
		s.mu.Unlock()
		s.startQueuedTurns(loaded)
	}
}

// ApplyConfig swaps the live runtime to a fresh config snapshot. Existing
// sessions keep their persisted conversation state while future turns use the
// updated clients, tools, and paths.
func (s *Service) ApplyConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("missing config")
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	s.mu.Lock()
	s.cfg = cfg
	s.store, s.storeErr = newSessionStore(cfg)
	s.shared.ApplyConfig(cfg)
	sessions := make([]*sessionState, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	if s.commands != nil {
		s.commands.SetConfig(cfg)
	}

	for _, session := range sessions {
		session.agent.ApplyConfig(cfg, s.shared)
		session.mu.RLock()
		profileID := strings.TrimSpace(session.modelProfileID)
		reasoningEffort := normalizeSessionReasoningEffort(session.reasoningEffort)
		session.mu.RUnlock()
		if profileID != "" {
			if profile, ok := cfg.ModelProfileByID(profileID); ok {
				if reasoningEffort != "" {
					profile.ReasoningEffort = reasoningEffort
				}
				session.agent.ApplyModelProfile(profile)
			}
		}
	}
	return nil
}

// OpenSession opens or resumes a persistent session for the locator.
func (s *Service) OpenSession(ctx context.Context, locator SessionLocator) (*OpenedSession, error) {
	_ = ctx
	locator = s.withDefaultLocatorMetadata(locator)
	// A caller-supplied project_dir is normalized at the boundary: resolve
	// to an absolute, cleaned path before it becomes part of the session
	// identity hash. Existence is only required when the session would be
	// created anew; reopening a persisted session whose workspace directory
	// has since been removed (e.g. an ACP temp cwd) must still succeed, or
	// the Web UI could never resume it.
	projectDir := strings.TrimSpace(locator.Metadata[sessionProjectDirMetadataKey])
	if projectDir != "" {
		dir, err := normalizeSessionProjectDir(projectDir)
		if err != nil {
			return nil, err
		}
		locator.Metadata[sessionProjectDirMetadataKey] = dir
		projectDir = dir
	}
	// Resolve "default" key to the latest session when a pointer exists.
	if strings.TrimSpace(locator.Key) == "default" {
		if latestKey := s.readLatestSessionKey(); latestKey != "" {
			locator.Key = latestKey
		}
	}
	normalized := normalizeLocator(locator)
	sessionID := stableSessionID(normalized)
	if legacySessionID := s.legacySessionIDIfPresent(normalized, sessionID); legacySessionID != "" {
		sessionID = legacySessionID
	}

	// New sessions must point at a real directory; a persisted session may
	// tolerate a project_dir whose backing directory no longer exists.
	if projectDir != "" && !s.sessionPersisted(sessionID) {
		if info, err := os.Stat(projectDir); err != nil {
			return nil, fmt.Errorf("%w %q: %v", ErrInvalidWorkspaceDir, projectDir, err)
		} else if !info.IsDir() {
			return nil, fmt.Errorf("%w %q: not a directory", ErrInvalidWorkspaceDir, projectDir)
		}
	}

	s.mu.Lock()
	if existing := s.sessions[sessionID]; existing != nil {
		if profile := strings.TrimSpace(normalized.Metadata["agent_profile"]); profile != "" {
			existing.mu.Lock()
			if existing.locator.Metadata == nil {
				existing.locator.Metadata = map[string]string{}
			}
			existing.locator.Metadata["agent_profile"] = config.NormalizeAgentProfile(profile)
			existing.mu.Unlock()
		}
		s.mu.Unlock()
		return s.describeSession(existing), nil
	}

	loaded, err := s.loadSession(sessionID, normalized)
	if err != nil {
		if s.cfg != nil && s.cfg.Runtime.Recovery.AutoRepairSessions {
			if report, repairErr := s.RepairSessions(ctx, sessionrepair.Request{SessionID: sessionID}); repairErr == nil && report.Changed {
				loaded, err = s.loadSession(sessionID, normalized)
			}
		}
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.sessions[sessionID] = loaded
	s.mu.Unlock()
	s.startQueuedTurns(loaded)
	return s.describeSession(loaded), nil
}

// CreateNewSession creates a new session with a timestamp-based key
// for the current workspace, writes it as the latest-session pointer,
// and returns the locator so the frontend can switch to it.
func (s *Service) CreateNewSession(ctx context.Context) (SessionLocator, error) {
	now := s.now()
	key := fmt.Sprintf("new-%s", now.Format("20060102-150405"))

	channel := "local"
	projectDir := ""
	if s.cfg != nil {
		projectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
		if projectDir == "" {
			projectDir = strings.TrimSpace(s.cfg.ProjectDir)
		}
	}
	projectDir = cleanProjectDir(projectDir)

	locator := SessionLocator{
		Channel: channel,
		Key:     key,
	}
	if projectDir != "" {
		locator.Metadata = map[string]string{
			sessionProjectDirMetadataKey: projectDir,
		}
	}

	if _, err := s.OpenSession(ctx, locator); err != nil {
		return SessionLocator{}, err
	}

	if err := s.writeLatestSessionKey(key); err != nil {
		return SessionLocator{}, err
	}

	return locator, nil
}

// readLatestSessionKey returns the latest session key for the current
// workspace from the .godex/latest-session pointer file.
func (s *Service) readLatestSessionKey() string {
	if s.cfg == nil {
		return ""
	}
	path := filepath.Join(s.cfg.StateDir, latestSessionFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	key := strings.TrimSpace(string(data))
	if key == "" || key == "default" {
		return ""
	}
	return key
}

// writeLatestSessionKey writes the latest session key pointer file
// so future godex/godex tui invocations open this session by default.
func (s *Service) writeLatestSessionKey(key string) error {
	if s.cfg == nil {
		return fmt.Errorf("missing config")
	}
	if err := os.MkdirAll(s.cfg.StateDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.StateDir, latestSessionFileName)
	return os.WriteFile(path, []byte(key+"\n"), 0644)
}

// cleanProjectDir normalises a project directory string for
// use as session identity input.  Two paths that resolve to
// the same physical directory should hash to the same session
// id, so we strip trailing slashes, collapse "a/./b" segments
// and remove doubled separators via filepath.Clean.  Empty
// input is preserved as the empty string so callers can still
// tell "no project" apart from a real path.
func cleanProjectDir(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

// normalizeSessionProjectDir resolves a caller-supplied project_dir to an
// absolute, cleaned path WITHOUT requiring it to exist on disk. It keeps the
// identity-hash behavior of validateSessionProjectDir (same directory always
// hashes to the same session id) while letting a persisted session reopen even
// after its workspace directory was deleted.
func normalizeSessionProjectDir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%w %q: expand home: %v", ErrInvalidWorkspaceDir, value, err)
		}
		if value == "~" {
			value = home
		} else if strings.HasPrefix(value, "~/") {
			value = filepath.Join(home, value[2:])
		} else {
			return "", fmt.Errorf("%w %q: unsupported ~user form", ErrInvalidWorkspaceDir, value)
		}
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w %q: %v", ErrInvalidWorkspaceDir, value, err)
	}
	return filepath.Clean(abs), nil
}

// validateSessionProjectDir validates a caller-supplied per-session
// working directory at the API boundary.  The returned path is cleaned
// and made absolute so the same physical directory always hashes to
// the same session id, and must exist as a directory.  An empty input
// means "no override" and is returned unchanged.
func validateSessionProjectDir(value string) (string, error) {
	abs, err := normalizeSessionProjectDir(value)
	if err != nil || abs == "" {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%w %q: %v", ErrInvalidWorkspaceDir, abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w %q: not a directory", ErrInvalidWorkspaceDir, abs)
	}
	return abs, nil
}

// sessionPersisted reports whether a session with the given id has been
// persisted on disk (session directory or SQLite store). It is used to
// decide whether a caller-supplied project_dir must still exist: brand-new
// sessions are checked at the boundary, while already-persisted sessions
// may be reopened even if their workspace was deleted.
func (s *Service) sessionPersisted(sessionID string) bool {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.SessionsDir) == "" {
		return false
	}
	if info, err := os.Stat(s.sessionDir(sessionID)); err == nil && info.IsDir() {
		return true
	}
	if store := s.sqliteSessionStore(); store != nil && s.storeErr == nil {
		if _, exists, err := store.Load(context.Background(), sessionID); err == nil && exists {
			return true
		}
	}
	return false
}

func (s *Service) withDefaultLocatorMetadata(locator SessionLocator) SessionLocator {
	projectDir := ""
	if s != nil && s.cfg != nil {
		projectDir = strings.TrimSpace(s.cfg.ProjectDir)
		if projectDir == "" {
			projectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
		}
	}
	if projectDir == "" {
		return locator
	}
	// Normalise once at the boundary so the same physical
	// directory always hashes to the same session id,
	// regardless of trailing slashes or "./" segments the
	// caller may have passed in via cfg.
	projectDir = cleanProjectDir(projectDir)
	if projectDir == "" {
		return locator
	}
	if locator.Metadata == nil {
		locator.Metadata = map[string]string{}
	} else {
		locator.Metadata = cloneStringMap(locator.Metadata)
	}
	if strings.TrimSpace(locator.Metadata[sessionProjectDirMetadataKey]) == "" {
		locator.Metadata[sessionProjectDirMetadataKey] = projectDir
	}
	return locator
}

func (s *Service) legacySessionIDIfPresent(locator SessionLocator, scopedSessionID string) string {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.SessionsDir) == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(s.cfg.SessionsDir, scopedSessionID)); err == nil {
		return ""
	}
	legacyLocator := locator
	if len(legacyLocator.Metadata) > 0 {
		legacyLocator.Metadata = cloneStringMap(legacyLocator.Metadata)
		delete(legacyLocator.Metadata, sessionProjectDirMetadataKey)
		if len(legacyLocator.Metadata) == 0 {
			legacyLocator.Metadata = nil
		}
	}
	legacySessionID := stableSessionID(legacyLocator)
	if legacySessionID == scopedSessionID {
		return ""
	}
	if _, err := os.Stat(filepath.Join(s.cfg.SessionsDir, legacySessionID)); err == nil {
		return legacySessionID
	}
	// Last-resort fallback: the computed id and the metadata-stripped id
	// are both missing on disk, but a sibling on-disk directory may still
	// represent the same logical (Channel, Key) pair under a different
	// hash. This happens in practice when a TUI/REPL session and a web
	// session inject different `project_dir` metadata, producing different
	// hashes for what the user perceives as the same conversation. Reuse
	// the existing directory when we find exactly one match so the web UI
	// can resume the REPL session instead of forking an empty one.
	if reused := s.findExistingOnDiskSessionIDForLocator(locator); reused != "" {
		return reused
	}
	return ""
}

// findExistingOnDiskSessionIDForLocator scans the sessions directory for a
// persisted session whose manifest locator matches the supplied locator's
// channel/key/user_id and returns its session id. It returns "" when zero
// or more than one directory match, to avoid silently merging unrelated
// sessions that happen to share the same (channel, key) pair.
func (s *Service) findExistingOnDiskSessionIDForLocator(locator SessionLocator) string {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.SessionsDir) == "" {
		return ""
	}
	normalized := normalizeLocator(locator)
	targetChannel := normalized.Channel
	targetKey := normalized.Key
	targetUserID := normalized.UserID
	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		return ""
	}
	var match string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(s.cfg.SessionsDir, entry.Name(), manifestFileName)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var manifest SessionManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}
		ml := normalizeLocator(manifest.Locator)
		if ml.Channel != targetChannel || ml.Key != targetKey || ml.UserID != targetUserID {
			continue
		}
		if match != "" {
			// Multiple matches: refuse to guess which one the caller meant.
			return ""
		}
		match = strings.TrimSpace(manifest.SessionID)
		if match == "" {
			match = entry.Name()
		}
	}
	return match
}

// ForkSession creates a new linked session from the current transcript.
func (s *Service) ForkSession(ctx context.Context, sessionID string, req ForkRequest) (*OpenedSession, error) {
	_ = ctx
	source, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	source.mu.RLock()
	if source.running {
		source.mu.RUnlock()
		return nil, newSessionBusyError(sessionID)
	}
	parentLocator := source.locator
	parentTitle := source.title
	modelProfileID := source.modelProfileID
	reasoningEffort := normalizeSessionReasoningEffort(source.reasoningEffort)
	source.mu.RUnlock()

	messages := source.agent.GetMessages()
	cut := len(messages)
	if req.MessageIndex != nil {
		cut = clampInt(*req.MessageIndex, 0, len(messages))
	} else if strings.TrimSpace(req.TurnID) != "" {
		if idx := forkMessageIndexForTurn(source.turnRecords(0), req.TurnID, len(messages)); idx >= 0 {
			cut = idx
		}
	}
	now := s.now()
	forkKey := forkSessionKey(parentLocator.Key, now)
	locator := parentLocator
	locator.Key = forkKey
	locator.Metadata = cloneStringMap(parentLocator.Metadata)
	if locator.Metadata == nil {
		locator.Metadata = map[string]string{}
	}
	locator.Metadata["parent_session_id"] = sessionID
	if strings.TrimSpace(req.TurnID) != "" {
		locator.Metadata["forked_from_turn_id"] = strings.TrimSpace(req.TurnID)
	}
	if req.MessageIndex != nil {
		locator.Metadata["forked_from_message_index"] = fmt.Sprintf("%d", *req.MessageIndex)
	}
	newID := stableSessionID(normalizeLocator(locator))
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Branch: " + strings.TrimSpace(parentTitle)
	}
	if strings.TrimSpace(title) == "Branch:" {
		title = "Branch"
	}

	a := agent.NewForSession(s.cfg, s.shared, newID)
	a.RegisterTools()
	if modelProfileID != "" {
		if profile, ok := s.cfg.ModelProfileByID(modelProfileID); ok {
			if reasoningEffort != "" {
				profile.ReasoningEffort = reasoningEffort
			}
			a.ApplyModelProfile(profile)
		}
	}
	state := source.agent.ExportStateForSession(sessionID)
	state.Messages = protocol.CloneMessages(messages[:cut])
	state.PendingResume = nil
	a.RestoreStateForSession(newID, state)
	fork := &sessionState{
		id:                     newID,
		locator:                normalizeLocator(locator),
		title:                  title,
		modelProfileID:         modelProfileID,
		reasoningEffort:        reasoningEffort,
		parentSessionID:        sessionID,
		forkedFromTurnID:       strings.TrimSpace(req.TurnID),
		forkedFromMessageIndex: cloneIntPtr(req.MessageIndex),
		branchTitle:            title,
		agent:                  a,
		events:                 events.NewBroadcaster(),
		gate:                   make(chan struct{}, 1),
		createdAt:              now,
		updatedAt:              now,
		lastActive:             now,
		graph:                  &sessiongraph.SessionGraph{},
		timeline:               events.NewRecorder(MaxTimelineEvents),
	}
	fork.graph.EnsureMainBranch()
	fork.gate <- struct{}{}
	fork.events.Attach(persistentTimelineSink{service: s, session: fork})
	if err := s.persistSession(fork, now); err != nil {
		return nil, err
	}
	_ = s.cloneSessionGraphBranch(source, sessiongraph.MainBranchID, sessiongraph.BranchID("branch:"+newID), "")
	s.appendSecurityEvent(security.SecurityEvent{
		At:        now,
		Category:  "knowledge",
		Action:    "fork_session",
		Severity:  "info",
		SessionID: newID,
		Summary:   "Created a forked session from " + sessionID,
		Metadata: map[string]string{
			"parent_session_id": sessionID,
			"turn_id":           strings.TrimSpace(req.TurnID),
		},
	})
	s.mu.Lock()
	s.sessions[newID] = fork
	s.mu.Unlock()
	return s.describeSession(fork), nil
}

// Submit appends an inbound envelope and runs one serialized agent turn.
func (s *Service) ListSessions(ctx context.Context, filter SessionListFilter) ([]ListedSession, error) {
	ids := map[string]struct{}{}
	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		for _, entry := range entries {
			if entry.IsDir() {
				ids[entry.Name()] = struct{}{}
			}
		}
	}
	if store := s.sqliteSessionStore(); store != nil && s.storeErr == nil {
		storeIDs, err := store.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, id := range storeIDs {
			ids[id] = struct{}{}
		}
	}

	sessionIDs := make([]string, 0, len(ids))
	for id := range ids {
		sessionIDs = append(sessionIDs, id)
	}
	sort.Strings(sessionIDs)
	listed := make([]ListedSession, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		manifest, err := s.readSessionListManifest(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if manifest == nil {
			continue
		}
		manifest.Locator = normalizeLocator(manifest.Locator)
		if filter.Channel != "" && manifest.Locator.Channel != filter.Channel {
			continue
		}

		title := strings.TrimSpace(manifest.Title)
		if title == "" || title == "New chat" {
			// Old sessions without a real title are the exceptional slow path:
			// load state once, derive the title, and persist it. Normal list calls
			// read manifests only and never deserialize full conversation state.
			fullManifest, state, err := s.readSessionListFiles(sessionID)
			if err != nil {
				return nil, err
			}
			if fullManifest != nil {
				manifest = fullManifest
				manifest.Locator = normalizeLocator(manifest.Locator)
			}
			if state == nil {
				return nil, newSessionCorruptError(sessionID, "missing %s while backfilling title", stateFileName)
			}
			derived := deriveSessionTitle(*state)
			if derived != "" && derived != "New chat" {
				title = derived
				manifest.Title = title
				stateData, err := json.Marshal(state)
				if err != nil {
					return nil, err
				}
				manifest.StateDigest = stateDigest(stateData)
				if err := s.writeManifest(*manifest); err != nil {
					return nil, err
				}
			}
		}

		item := ListedSession{
			SessionID:              manifest.SessionID,
			Locator:                manifest.Locator,
			Title:                  title,
			ModelProfileID:         strings.TrimSpace(manifest.ModelProfileID),
			ParentSessionID:        strings.TrimSpace(manifest.ParentSessionID),
			ForkedFromTurnID:       strings.TrimSpace(manifest.ForkedFromTurnID),
			ForkedFromMessageIndex: cloneIntPtr(manifest.ForkedFromMessageIndex),
			BranchTitle:            strings.TrimSpace(manifest.BranchTitle),
			CreatedAt:              manifest.CreatedAt,
			UpdatedAt:              manifest.UpdatedAt,
			LastActivityAt:         manifest.LastActivityAt,
		}
		if running := s.runningState(manifest.SessionID); running {
			item.Running = true
		}
		listed = append(listed, item)
	}

	sort.Slice(listed, func(i, j int) bool {
		if listed[i].UpdatedAt.Equal(listed[j].UpdatedAt) {
			return listed[i].SessionID < listed[j].SessionID
		}
		return listed[i].UpdatedAt.After(listed[j].UpdatedAt)
	})
	return listed, nil
}

// readSessionListManifest uses a store's metadata-only path when available.
// Falling back to the full repair-aware loader preserves compatibility with
// custom stores and damaged legacy sessions.
func (s *Service) readSessionListManifest(ctx context.Context, sessionID string) (*SessionManifest, error) {
	if s.storeErr != nil {
		return nil, s.storeErr
	}
	if loader, ok := s.store.(sessionstore.ManifestLoader); ok {
		data, exists, err := loader.LoadManifest(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if exists && len(data) > 0 {
			var manifest SessionManifest
			if err := json.Unmarshal(data, &manifest); err == nil {
				return &manifest, nil
			}
		}
	}
	manifest, _, err := s.readSessionListFiles(sessionID)
	return manifest, err
}

func (s *Service) readSessionListFiles(sessionID string) (*SessionManifest, *agent.SessionState, error) {
	manifest, state, err := s.readSessionFiles(sessionID)
	if err == nil || manifest != nil {
		return manifest, state, err
	}
	data, exists, readErr := readOptionalFile(filepath.Join(s.sessionDir(sessionID), manifestFileName))
	if readErr != nil || !exists {
		return nil, nil, readErr
	}
	var legacyManifest SessionManifest
	if decodeErr := json.Unmarshal(data, &legacyManifest); decodeErr != nil {
		return nil, nil, newSessionCorruptError(sessionID, "decode %s: %v", manifestFileName, decodeErr)
	}
	stateData, stateExists, readErr := readOptionalFile(filepath.Join(s.sessionDir(sessionID), stateFileName))
	if readErr != nil {
		return nil, nil, readErr
	}
	if !stateExists {
		return &legacyManifest, nil, nil
	}
	var legacyState agent.SessionState
	if decodeErr := json.Unmarshal(stateData, &legacyState); decodeErr != nil {
		return nil, nil, newSessionCorruptError(sessionID, "decode %s: %v", stateFileName, decodeErr)
	}
	return &legacyManifest, &legacyState, nil
}

// DeleteSession permanently removes one persisted session and its attachments.
// RenameSession updates a session's display title and persists it to both
// the JSON manifest (for the file-backed store) and the SQLite store so the
// session list reflects the new name across restarts. An empty title restores
// the auto-derived behavior (the list derives a title from state on next read).
func (s *Service) RenameSession(ctx context.Context, sessionID, title string) (*ListedSession, error) {
	title = strings.TrimSpace(title)

	// Prefer the in-memory session (it may be running) so the change is
	// immediately visible without a reload; otherwise patch the stored manifest.
	s.mu.Lock()
	current := s.sessions[sessionID]
	s.mu.Unlock()
	if current != nil {
		current.mu.Lock()
		current.title = title
		current.updatedAt = s.now()
		current.mu.Unlock()
		s.writeManifestForSession(current)
		return listedSessionFromState(current), nil
	}

	// Not loaded: update the persisted manifest only.
	manifest, err := s.readSessionListManifest(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, newSessionNotFoundError(sessionID)
	}
	manifest.Title = title
	manifest.UpdatedAt = s.now()
	if err := s.writeManifest(*manifest); err != nil {
		return nil, err
	}
	// Keep the SQLite store's manifest blob in sync when it is active.
	if store := s.sqliteSessionStore(); store != nil && s.storeErr == nil {
		data, ok, err := store.Load(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if ok {
			manifestData, err := json.Marshal(manifest)
			if err != nil {
				return nil, err
			}
			data.Manifest = manifestData
			if err := store.Save(ctx, data); err != nil {
				return nil, err
			}
		}
	}
	return &ListedSession{
		SessionID:       manifest.SessionID,
		Locator:         manifest.Locator,
		Title:           manifest.Title,
		BranchTitle:     strings.TrimSpace(manifest.BranchTitle),
		CreatedAt:       manifest.CreatedAt,
		UpdatedAt:       manifest.UpdatedAt,
		LastActivityAt:  manifest.LastActivityAt,
	}, nil
}

func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	_ = ctx
	dir := s.sessionDir(sessionID)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			_, loaded := s.sessions[sessionID]
			s.mu.Unlock()
			storeFound := false
			if s.store != nil && s.storeErr == nil {
				if _, ok, loadErr := s.store.Load(ctx, sessionID); loadErr != nil {
					return loadErr
				} else {
					storeFound = ok
				}
			}
			if !loaded && !storeFound {
				return newSessionNotFoundError(sessionID)
			}
		} else {
			return err
		}
	}

	var (
		loadedSession *sessionState
		loadedRefs    []string
	)
	s.mu.Lock()
	if current := s.sessions[sessionID]; current != nil {
		current.mu.RLock()
		running := current.running
		current.mu.RUnlock()
		if running {
			s.mu.Unlock()
			return newSessionBusyError(sessionID)
		}
		loadedSession = current
		loadedRefs = append([]string{}, current.agent.TranscriptRefs()...)
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	_, state, err := s.readSessionFiles(sessionID)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		return err
	}
	targetRefs := sessionTranscriptRefs(state)
	targetRefs = stringutil.Unique(append(targetRefs, loadedRefs...))
	if err := s.deleteUniqueTranscriptRefs(sessionID, targetRefs); err != nil {
		return err
	}
	if err := s.deleteSessionToolResultArtifacts(sessionID); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.sqliteSessionStoreError(); err != nil {
		return err
	}
	if s.store != nil {
		if err := s.store.Delete(ctx, sessionID); err != nil {
			return err
		}
	}
	_ = loadedSession
	return nil
}

func (s *Service) deleteSessionToolResultArtifacts(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || filepath.Base(sessionID) != sessionID || s.cfg == nil || strings.TrimSpace(s.cfg.StateDir) == "" {
		return nil
	}
	dir := filepath.Join(s.cfg.StateDir, ".tool-results", sessionID)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Subscribe attaches a live sink to the session event stream.
func (s *Service) Subscribe(ctx context.Context, sessionID string, sink events.Sink) error {
	return s.SubscribeReplay(ctx, sessionID, sink, EventReplayOptions{})
}

// SubscribeReplay attaches a live sink and optionally replays recent timeline
// events before streaming new events. Live events that arrive during replay are
// buffered and delivered after replay, so reconnects do not miss the current turn.
func (s *Service) SubscribeReplay(ctx context.Context, sessionID string, sink events.Sink, replay EventReplayOptions) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	if sink == nil {
		sink = events.NopSink
	}
	liveCh := make(chan events.Event, 256)
	unsubscribe := session.events.Attach(events.SinkFunc(func(event events.Event) {
		select {
		case <-ctx.Done():
		case liveCh <- event:
		}
	}))
	defer unsubscribe()

	replayed := make(map[string]struct{})
	for _, event := range session.replayEvents(replay) {
		replayed[eventReplayKey(event)] = struct{}{}
		sink.Emit(event)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-liveCh:
			key := eventReplayKey(event)
			if _, ok := replayed[key]; ok {
				delete(replayed, key)
				continue
			}
			sink.Emit(event)
		}
	}
}

func (s *Service) emitSkillRefresh(session *sessionState, updatedAt time.Time) {
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    "",
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
}

// AttachSink registers a live in-process sink and returns an unsubscribe function.
func (s *Service) AttachSink(sessionID string, sink events.Sink) (func(), error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.events.Attach(sink), nil
}

func (s *Service) requireSession(sessionID string) (*sessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session := s.sessions[sessionID]
	if session == nil {
		return nil, newSessionNotFoundError(sessionID)
	}
	return session, nil
}

// SetActiveSessionTools narrows a session's active tool set to the tools
// permitted by a business key (Agent Step Platform): only MCP tools from
// allowed servers plus sandbox tools in the allowlist. Always-active tools
// are preserved. The session must already be open (OpenSession first).
func (s *Service) SetActiveSessionTools(sessionID string, allowedServers []string, allowedSandbox []string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	session.mu.RLock()
	agentRef := session.agent
	session.mu.RUnlock()
	if agentRef == nil {
		return fmt.Errorf("session %s has no agent", sessionID)
	}
	agentRef.ApplyToolAllowlist(allowedServers, allowedSandbox)
	return nil
}

// ApplyTemplateToSession applies and persists an agent template on an already-open
// session. The next turn uses the template's capability baseline and harness.
// The session must already be open (OpenSession first).
func (s *Service) ApplyTemplateToSession(sessionID, templateID string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	session.mu.RLock()
	agentRef := session.agent
	session.mu.RUnlock()
	if agentRef == nil {
		return fmt.Errorf("session %s has no agent", sessionID)
	}
	t, _, err := s.templateManager().Resolve(templateID)
	if err != nil {
		return err
	}
	agentRef.ApplyTemplate(t)
	session.mu.Lock()
	if session.locator.Metadata == nil {
		session.locator.Metadata = map[string]string{}
	}
	session.locator.Metadata["template"] = t.ID
	session.mu.Unlock()
	return nil
}

// ApplySessionToolOverlay merges a business key's override layer onto the
// session's active tool set (the template baseline). "!x" removes, plain
// entries append, "*" activates everything of that category — see
// agent.ApplyToolOverlay.
func (s *Service) ApplySessionToolOverlay(sessionID string, allowedServers []string, allowedSandbox []string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	session.mu.RLock()
	agentRef := session.agent
	session.mu.RUnlock()
	if agentRef == nil {
		return fmt.Errorf("session %s has no agent", sessionID)
	}
	agentRef.ApplyToolOverlay(allowedServers, allowedSandbox)
	return nil
}

// ApplySessionStepNarrow narrows the session's active tool set (template
// baseline + key overlay) to what the step request permits. The request can
// only narrow — see agent.ApplyStepListNarrow.
func (s *Service) ApplySessionStepNarrow(sessionID string, reqServers []string, reqSandbox []string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	session.mu.RLock()
	agentRef := session.agent
	session.mu.RUnlock()
	if agentRef == nil {
		return fmt.Errorf("session %s has no agent", sessionID)
	}
	agentRef.ApplyStepListNarrow(reqServers, reqSandbox)
	return nil
}

func (s *Service) runningState(sessionID string) bool {
	s.mu.Lock()
	session := s.sessions[sessionID]
	s.mu.Unlock()
	if session == nil {
		return false
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.running
}
