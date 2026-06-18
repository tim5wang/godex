package backend

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/insights"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/security"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/platform/fsutil"
	"github.com/tim5wang/godex/internal/platform/storagegc"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/sessionrepair"
	"github.com/tim5wang/godex/internal/sessiongraph"
	"github.com/tim5wang/godex/internal/sessionstore"
	"github.com/tim5wang/godex/internal/tools"
)

const (
	manifestFileName              = "manifest.json"
	stateFileName                 = "state.json"
	timelineFileName              = "timeline.json"
	eventJournalFileName          = "events.jsonl"
	turnsFileName                 = "turns.json"
	turnQueueFileName             = "turn_queue.json"
	sessionGraphFileName          = "graph.json"
	checkpointPointerName         = "checkpoint.json"
	checkpointsDirName            = "checkpoints"
	securityAuditFileName         = "security/audit.jsonl"
	attachmentsDir                = "attachments"
	snapshotTimelineLimit         = 80
	snapshotTurnLimit             = 20
	persistedTurnLimit            = 200
	sessionProjectDirMetadataKey  = "project_dir"
	sessionGraphBranchMetadataKey = "session_graph_branch_id"
	sessionGraphNodeMetadataKey   = "session_graph_node_id"
	latestSessionFileName         = "latest-session"
)

var maxAttachmentBytes int64 = 128 << 20

// MaxAttachmentUploadBytes returns the per-attachment hard cap enforced by the backend.
func MaxAttachmentUploadBytes() int64 {
	if maxAttachmentBytes <= 0 {
		return 128 << 20
	}
	return maxAttachmentBytes
}

// SessionLocator identifies one frontend-facing session namespace.
type SessionLocator struct {
	Channel  string            `json:"channel"`
	Key      string            `json:"key,omitempty"`
	UserID   string            `json:"user_id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SessionManifest is the persisted session metadata file.
type SessionManifest struct {
	SessionID              string              `json:"session_id"`
	Locator                SessionLocator      `json:"locator"`
	Identity               agent.AgentIdentity `json:"identity,omitempty"`
	Title                  string              `json:"title,omitempty"`
	ModelProfileID         string              `json:"model_profile_id,omitempty"`
	ReasoningEffort        string              `json:"reasoning_effort,omitempty"`
	ParentSessionID        string              `json:"parent_session_id,omitempty"`
	ForkedFromTurnID       string              `json:"forked_from_turn_id,omitempty"`
	ForkedFromMessageIndex *int                `json:"forked_from_message_index,omitempty"`
	BranchTitle            string              `json:"branch_title,omitempty"`
	StateDigest            string              `json:"state_digest"`
	CreatedAt              time.Time           `json:"created_at"`
	UpdatedAt              time.Time           `json:"updated_at"`
	LastActivityAt         time.Time           `json:"last_activity_at"`
}

// TimelinePageRequest selects a durable, paged slice of one session timeline.
type TimelinePageRequest struct {
	Limit  int
	Cursor string
	Types  []string
	Query  string
	JobID  string
	TurnID string
}

// TimelinePage returns timeline items newest-first with an offset cursor.
type TimelinePage struct {
	Items      []events.Event `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	HasMore    bool           `json:"has_more"`
	Total      int            `json:"total"`
}

type sessionCheckpointPointer struct {
	Current   string    `json:"current"`
	CreatedAt time.Time `json:"created_at"`
}

type sessionCheckpointSnapshot struct {
	Manifest *SessionManifest
	State    *agent.SessionState
	Timeline []events.Event
	Turns    []TurnRecord
	Queue    []QueuedTurn
}

// SessionListFilter narrows ListSessions results.
type SessionListFilter struct {
	Channel string
}

// ListedSession is the lightweight session list item returned to frontends.
type ListedSession struct {
	SessionID              string         `json:"session_id"`
	Locator                SessionLocator `json:"locator"`
	Title                  string         `json:"title,omitempty"`
	ModelProfileID         string         `json:"model_profile_id,omitempty"`
	ReasoningEffort        string         `json:"reasoning_effort,omitempty"`
	ParentSessionID        string         `json:"parent_session_id,omitempty"`
	ForkedFromTurnID       string         `json:"forked_from_turn_id,omitempty"`
	ForkedFromMessageIndex *int           `json:"forked_from_message_index,omitempty"`
	BranchTitle            string         `json:"branch_title,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
	LastActivityAt         time.Time      `json:"last_activity_at"`
	Running                bool           `json:"running,omitempty"`
}

// OpenedSession describes one opened or resumed session.
type OpenedSession struct {
	SessionID              string         `json:"session_id"`
	Locator                SessionLocator `json:"locator"`
	ModelProfileID         string         `json:"model_profile_id,omitempty"`
	ReasoningEffort        string         `json:"reasoning_effort,omitempty"`
	ParentSessionID        string         `json:"parent_session_id,omitempty"`
	ForkedFromTurnID       string         `json:"forked_from_turn_id,omitempty"`
	ForkedFromMessageIndex *int           `json:"forked_from_message_index,omitempty"`
	BranchTitle            string         `json:"branch_title,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

// ModelProfile describes one selectable model profile for HTTP/UI callers.
type ModelProfile struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	BaseURL           string `json:"base_url"`
	MaxTokens         int    `json:"max_tokens"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	SupportsStreaming bool   `json:"supports_streaming"`
	SupportsVision    bool   `json:"supports_vision"`
	ReasoningEffort   string `json:"reasoning_effort,omitempty"`
	Default           bool   `json:"default,omitempty"`
	Selected          bool   `json:"selected,omitempty"`
}

// ModelsView is returned by GET /models.
type ModelsView struct {
	DefaultProfileID string         `json:"default_profile_id"`
	SessionProfileID string         `json:"session_profile_id,omitempty"`
	ReasoningEffort  string         `json:"reasoning_effort,omitempty"`
	Profiles         []ModelProfile `json:"profiles"`
}

// SubmitResult summarizes one submitted user turn.
type SubmitResult struct {
	SessionID        string    `json:"session_id"`
	TurnID           string    `json:"turn_id"`
	RetryOf          string    `json:"retry_of,omitempty"`
	Completed        bool      `json:"completed"`
	Status           string    `json:"status,omitempty"`
	PendingApproval  bool      `json:"pending_approval,omitempty"`
	PendingRequestID string    `json:"pending_request_id,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// AttachmentUpload is one inbound attachment stream to persist into a session.
type AttachmentUpload struct {
	Name     string
	MIMEType string
	Reader   io.Reader
}

// Snapshot is the unified frontend view of one session.
type Snapshot struct {
	SessionID               string                    `json:"session_id"`
	Locator                 SessionLocator            `json:"locator"`
	Messages                []protocol.Message        `json:"messages"`
	DisplayMessages         []protocol.Message        `json:"display_messages,omitempty"`
	Tasks                   []*task.FileTask          `json:"tasks"`
	Todos                   []todo.Item               `json:"todos"`
	Team                    []*teammate.Teammate      `json:"team"`
	ActiveSkills            []string                  `json:"active_skills"`
	ToolCatalog             tools.ToolCatalog         `json:"tool_catalog"`
	PendingPermissions      []tools.PendingPermission `json:"pending_permissions,omitempty"`
	ActivePermissionBlocker *PermissionBlocker        `json:"active_permission_blocker,omitempty"`
	Timeline                []events.Event            `json:"timeline,omitempty"`
	Turns                   []TurnRecord              `json:"turns,omitempty"`
	Running                 bool                      `json:"running"`
	ActiveTurnID            string                    `json:"active_turn_id,omitempty"`
	ActivePhase             string                    `json:"active_phase,omitempty"`
	Identity                agent.AgentIdentity       `json:"identity,omitempty"`
	ModelProfileID          string                    `json:"model_profile_id,omitempty"`
	ReasoningEffort         string                    `json:"reasoning_effort,omitempty"`
	QueuedTurns             []QueuedTurn              `json:"queued_turns,omitempty"`
	UpdatedAt               time.Time                 `json:"updated_at"`
}

// PermissionBlocker is the frontend-facing current approval blocker for one session.
type PermissionBlocker struct {
	RequestID string                 `json:"request_id"`
	Status    tools.PermissionStatus `json:"status"`
	TurnID    string                 `json:"turn_id,omitempty"`
	Intent    string                 `json:"intent,omitempty"`
	Risk      string                 `json:"risk,omitempty"`
	Expiry    string                 `json:"expiry,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	Action    string                 `json:"action,omitempty"`
	Command   string                 `json:"command,omitempty"`
	Paths     []string               `json:"paths,omitempty"`
	Source    string                 `json:"source,omitempty"`
	Sender    string                 `json:"sender,omitempty"`
	CreatedAt time.Time              `json:"created_at,omitempty"`
	ExpiresAt time.Time              `json:"expires_at,omitempty"`
}

// CancelTurnResult summarizes a requested turn cancellation.
type CancelTurnResult struct {
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TurnRecord is the persisted lifecycle state for one user turn.
type TurnRecord struct {
	ID                    string                 `json:"id"`
	Status                string                 `json:"status"`
	Source                string                 `json:"source,omitempty"`
	Sender                string                 `json:"sender,omitempty"`
	Summary               string                 `json:"summary,omitempty"`
	StartedAt             time.Time              `json:"started_at"`
	UpdatedAt             time.Time              `json:"updated_at"`
	CompletedAt           *time.Time             `json:"completed_at,omitempty"`
	PendingRequestID      string                 `json:"pending_request_id,omitempty"`
	BlockedByPermissionID string                 `json:"blocked_by_permission_id,omitempty"`
	PermissionStatus      tools.PermissionStatus `json:"permission_status,omitempty"`
	Error                 string                 `json:"error,omitempty"`
	RetryOf               string                 `json:"retry_of,omitempty"`
	CanRetry              bool                   `json:"can_retry,omitempty"`
	CanResume             bool                   `json:"can_resume,omitempty"`
	ResumeAvailable       bool                   `json:"resume_available,omitempty"`
	RecoveryHint          string                 `json:"recovery_hint,omitempty"`
	Phase                 string                 `json:"phase,omitempty"`
	InjectionCount        int                    `json:"injection_count,omitempty"`
	LastToolName          string                 `json:"last_tool_name,omitempty"`

	PriorMessageCount int                `json:"prior_message_count,omitempty"`
	Envelope          *message.Envelope  `json:"envelope,omitempty"`
	Injections        []message.Envelope `json:"injections,omitempty"`
}

// QueueMode controls how a message submitted during an active turn is handled.
type QueueMode string

const (
	QueueModeFollowUp QueueMode = "follow_up"
	QueueModeSteering QueueMode = "steering"
)

// QueuedTurn is one persisted queued user turn.
type QueuedTurn struct {
	ID        string           `json:"id"`
	Mode      QueueMode        `json:"mode"`
	Status    string           `json:"status"`
	Source    string           `json:"source,omitempty"`
	Sender    string           `json:"sender,omitempty"`
	Summary   string           `json:"summary,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Envelope  message.Envelope `json:"envelope,omitempty"`
}

// SubmitOptions controls queued submission behavior.
type SubmitOptions struct {
	QueueMode QueueMode
}

// ForkRequest describes a linked session fork operation.
type ForkRequest struct {
	TurnID       string `json:"turn_id,omitempty"`
	MessageIndex *int   `json:"message_index,omitempty"`
	Title        string `json:"title,omitempty"`
}

// Service is the persistent multi-entrypoint runtime backend.
type Service struct {
	cfg      *config.Config
	shared   *agent.SharedDependencies
	commands *commands.Service
	analyze  func(insights.Input) (*insights.Report, error)
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]*sessionState
	store    sessionstore.Store
	storeErr error
}

type sessionLockContextKey struct{}

type sessionState struct {
	id                     string
	locator                SessionLocator
	title                  string
	agent                  *agent.Agent
	events                 *events.Broadcaster
	gate                   chan struct{}
	createdAt              time.Time
	updatedAt              time.Time
	lastActive             time.Time
	modelProfileID         string
	reasoningEffort        string
	parentSessionID        string
	forkedFromTurnID       string
	forkedFromMessageIndex *int
	branchTitle            string
	identity               agent.AgentIdentity
	graph                  *sessiongraph.SessionGraph
	turnCounter            uint64

	mu       sync.RWMutex
	running  bool
	timeline *events.Recorder
	active   *activeTurn

	timelineMu sync.Mutex
	turnsMu    sync.RWMutex
	turns      []TurnRecord
	queueMu    sync.RWMutex
	queue      []QueuedTurn
}

type activeTurn struct {
	id        string
	cancel    context.CancelCauseFunc
	startedAt time.Time
	phase     string
}

// EventReplayOptions controls which recorded events are replayed when a client
// reconnects to a session event stream.
type EventReplayOptions struct {
	ActiveOnly bool
	Limit      int
}

type persistentTimelineSink struct {
	service *Service
	session *sessionState
}

func (s persistentTimelineSink) Emit(event events.Event) {
	if s.session == nil || s.session.timeline == nil {
		return
	}
	s.session.timeline.Emit(event)
	if !events.RecordableEvent(event) {
		return
	}
	_ = s.service.appendSessionEventJournal(s.session, event)
	_ = s.service.writeSessionTimeline(s.session)
}

type artifactCollector struct {
	mu    sync.Mutex
	paths []string
}

func (c *artifactCollector) Emit(event events.Event) {
	if event.Type != events.EventToolCallFinished {
		return
	}
	payload, ok := event.Payload.(events.ToolCallPayload)
	if !ok || len(payload.ArtifactPaths) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, payload.ArtifactPaths...)
}

func (c *artifactCollector) Paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.paths...)
}

// NewService creates the shared persistent runtime backend.
func NewService(cfg *config.Config, shared *agent.SharedDependencies, commandService *commands.Service) *Service {
	if shared == nil {
		shared = agent.NewSharedDependencies(cfg)
	}
	if commandService == nil {
		commandService = commands.NewService(cfg)
	}
	service := &Service{
		cfg:      cfg,
		shared:   shared,
		commands: commandService,
		analyze: func(input insights.Input) (*insights.Report, error) {
			return insights.NewAnalyzer(cfg.TranscriptsDir, cfg.TempDir, cfg.MemoryDir).Analyze(input)
		},
		now:      time.Now,
		sessions: make(map[string]*sessionState),
	}
	service.store, service.storeErr = newSessionStore(cfg)
	service.autoRepairSessions()
	service.recoverQueuedSessions()
	service.wireSlashCommandHandlers()
	return service
}

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
		timeline:               events.NewRecorder(200),
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
func (s *Service) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*SubmitResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return nil, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	ctx = withSessionLock(ctx, sessionID)
	result, err := s.runUserTurnLocked(ctx, session, envelope)
	release()
	released = true
	return result, err
}

// SubmitAsync appends an inbound envelope, immediately returns the accepted turn,
// and continues the agent turn on a service-owned background context.
func (s *Service) SubmitAsync(ctx context.Context, sessionID string, envelope message.Envelope, options ...SubmitOptions) (*SubmitResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, ok := session.tryAcquire()
	if !ok {
		mode := QueueModeFollowUp
		if len(options) > 0 && strings.TrimSpace(string(options[0].QueueMode)) != "" {
			mode = normalizeQueueMode(options[0].QueueMode)
		}
		if result, injected, err := s.injectActiveTurn(session, envelope, mode); injected || err != nil {
			return result, err
		}
		return s.enqueueTurn(session, envelope, mode)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	turn, result, err := s.startUserTurnLocked(session, envelope, true)
	if err != nil {
		return nil, err
	}

	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(turn.TurnID, cancel, result.UpdatedAt)
	go func() {
		defer func() {
			session.clearActiveTurn(turn.TurnID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, sessionID)
		_, _ = s.finishAgentTurnLocked(runCtx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	}()
	released = true
	return result, nil
}

func (s *Service) enqueueTurn(session *sessionState, envelope message.Envelope, mode QueueMode) (*SubmitResult, error) {
	if session == nil {
		return nil, ErrSessionNotFound
	}
	now := s.now()
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = now
	}
	envelope.SessionID = session.id
	normalized := envelope.Normalized()
	turnID := session.nextTurnID(now)
	item := QueuedTurn{
		ID:        turnID,
		Mode:      normalizeQueueMode(mode),
		Status:    "queued",
		Source:    string(normalized.Source),
		Sender:    strings.TrimSpace(normalized.Sender),
		Summary:   turnSummary(normalized.BodyText()),
		CreatedAt: now,
		UpdatedAt: now,
		Envelope:  normalized,
	}
	session.enqueue(item)
	if err := s.writeSessionQueue(session); err != nil {
		return nil, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        now,
		Category:  "capability",
		Action:    "queue_turn",
		Severity:  "info",
		SessionID: session.id,
		Source:    item.Source,
		Summary:   "Queued " + string(item.Mode) + " message while session was running.",
		Metadata: map[string]string{
			"turn_id": item.ID,
			"mode":    string(item.Mode),
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)
	return &SubmitResult{
		SessionID: session.id,
		TurnID:    turnID,
		Completed: false,
		Status:    "queued",
		UpdatedAt: now,
	}, nil
}

func (s *Service) injectActiveTurn(session *sessionState, envelope message.Envelope, mode QueueMode) (*SubmitResult, bool, error) {
	if session == nil {
		return nil, false, ErrSessionNotFound
	}
	activeTurnID := session.activeTurnID()
	if activeTurnID == "" {
		return nil, false, nil
	}
	now := s.now()
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = now
	}
	envelope.SessionID = session.id
	normalized := envelope.Normalized()
	if normalized.Metadata == nil {
		normalized.Metadata = map[string]string{}
	}
	normalized.Metadata["queue_mode"] = string(normalizeQueueMode(mode))
	injectionID := session.nextTurnID(now)
	item := QueuedTurn{
		ID:        injectionID,
		Mode:      normalizeQueueMode(mode),
		Status:    "injected",
		Source:    string(normalized.Source),
		Sender:    strings.TrimSpace(normalized.Sender),
		Summary:   turnSummary(normalized.BodyText()),
		CreatedAt: now,
		UpdatedAt: now,
		Envelope:  normalized,
	}
	remaining := session.addTurnInjection(activeTurnID, item, now)
	if err := s.writeSessionTurns(session); err != nil {
		return nil, true, err
	}
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Type:      events.EventMessageInjected,
		Timestamp: now,
		Payload: events.MessageInjectedPayload{
			Count:     1,
			Mode:      string(item.Mode),
			Remaining: remaining,
			Summary:   item.Summary,
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)
	return &SubmitResult{
		SessionID: session.id,
		TurnID:    activeTurnID,
		Completed: false,
		Status:    "injected",
		UpdatedAt: now,
	}, true, nil
}

func (s *Service) startQueuedTurns(session *sessionState) {
	if session == nil {
		return
	}
	next, ok := session.peekQueued()
	if !ok {
		return
	}
	release, acquired := session.tryAcquire()
	if !acquired {
		return
	}
	if !session.dropQueued(next.ID) {
		release()
		return
	}
	_ = s.writeSessionQueue(session)
	turn, result, err := s.startUserTurnLocked(session, next.Envelope.Normalized(), true)
	if err != nil {
		release()
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    next.ID,
			Type:      events.EventErrorRaised,
			Timestamp: s.now(),
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to start queued turn: %v", err)},
		})
		return
	}
	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(turn.TurnID, cancel, result.UpdatedAt)
	go func() {
		defer func() {
			session.clearActiveTurn(turn.TurnID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, session.id)
		_, _ = s.finishAgentTurnLocked(runCtx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	}()
}

func normalizeQueueMode(mode QueueMode) QueueMode {
	switch QueueMode(strings.TrimSpace(string(mode))) {
	case QueueModeSteering:
		return QueueModeSteering
	default:
		return QueueModeFollowUp
	}
}

// CancelTurn requests cancellation of the active asynchronous turn.
func (s *Service) CancelTurn(ctx context.Context, sessionID, turnID string) (*CancelTurnResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	activeTurnID, ok := session.cancelActiveTurn(turnID)
	if !ok {
		return nil, newTurnNotFoundError(turnID)
	}
	now := s.now()
	session.updateTurnStatus(activeTurnID, "canceling", "", "", now)
	if err := s.writeSessionTurns(session); err != nil {
		return nil, err
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    activeTurnID,
		Type:      events.EventWarningRaised,
		Timestamp: now,
		Payload:   events.NoticePayload{Message: "Turn cancellation requested."},
	})
	return &CancelTurnResult{
		SessionID: sessionID,
		TurnID:    activeTurnID,
		Status:    "canceling",
		UpdatedAt: now,
	}, nil
}

// RetryTurnAsync replays the latest retryable turn from its persisted input and
// continues execution on a service-owned background context.
func (s *Service) RetryTurnAsync(ctx context.Context, sessionID, turnID string) (*SubmitResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, ok := session.tryAcquire()
	if !ok {
		return nil, newSessionBusyError(sessionID)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	previous, err := session.retryableTurnRecord(turnID)
	if err != nil {
		return nil, err
	}
	if previous.Envelope == nil {
		return nil, newTurnNotRetryableError(turnID, "original input was not persisted")
	}
	currentMessages := session.agent.GetMessages()
	if previous.PriorMessageCount < 0 || previous.PriorMessageCount > len(currentMessages) {
		return nil, newTurnNotRetryableError(turnID, "conversation has changed since the original turn")
	}

	session.agent.TruncateMessages(previous.PriorMessageCount)
	session.agent.ClearPendingResume()
	turn, result, err := s.startUserTurnLocked(session, previous.Envelope.Normalized(), true)
	if err != nil {
		return nil, err
	}
	session.markTurnRetry(turn.TurnID, previous.ID, s.now())
	if err := s.writeSessionTurns(session); err != nil {
		return nil, err
	}
	result.RetryOf = previous.ID

	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(turn.TurnID, cancel, result.UpdatedAt)
	go func() {
		defer func() {
			session.clearActiveTurn(turn.TurnID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, sessionID)
		_, _ = s.finishAgentTurnLocked(runCtx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	}()
	released = true
	return result, nil
}

// ResumeTurnAsync continues the latest interrupted turn from the persisted
// transcript checkpoint instead of replaying the original input from scratch.
func (s *Service) ResumeTurnAsync(ctx context.Context, sessionID, turnID string) (*SubmitResult, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, ok := session.tryAcquire()
	if !ok {
		return nil, newSessionBusyError(sessionID)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	record, err := session.resumableTurnRecord(turnID)
	if err != nil {
		return nil, err
	}
	envelope := record.Envelope.Normalized()
	runtimeCtx := s.buildRuntimeContext(sessionID, session.locator, envelope)
	runtimeCtx.ProjectLedger = s.compactProjectLedgerForSession(sessionID)
	now := s.now()
	session.agent.ClearPendingResume()
	session.updateTurnStatus(record.ID, "running", "", "", now)
	if err := s.persistSession(session, now); err != nil {
		return nil, err
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    record.ID,
		Type:      events.EventWarningRaised,
		Timestamp: now,
		Payload: events.NoticePayload{
			Message: "Resuming interrupted turn from persisted checkpoint.",
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    record.ID,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)

	finishCtx, cancel := context.WithCancelCause(context.Background())
	session.setActiveTurn(record.ID, cancel, now)
	go func() {
		defer func() {
			session.clearActiveTurn(record.ID)
			release()
			s.startQueuedTurns(session)
		}()
		runCtx := withSessionLock(finishCtx, sessionID)
		_, _ = s.finishAgentTurnLocked(runCtx, session, record.ID, envelope, runtimeCtx, record.PriorMessageCount)
	}()
	released = true
	return &SubmitResult{
		SessionID: sessionID,
		TurnID:    record.ID,
		Completed: false,
		Status:    "running",
		UpdatedAt: now,
	}, nil
}

type preparedUserTurn struct {
	TurnID            string
	Envelope          message.Envelope
	RuntimeContext    automation.SessionContext
	PriorMessageCount int
}

func (s *Service) runUserTurnLocked(ctx context.Context, session *sessionState, envelope message.Envelope) (*SubmitResult, error) {
	turn, _, err := s.startUserTurnLocked(session, envelope, false)
	if err != nil {
		return nil, err
	}
	result, err := s.finishAgentTurnLocked(ctx, session, turn.TurnID, turn.Envelope, turn.RuntimeContext, turn.PriorMessageCount)
	// Fire async title generation only when the turn completes cleanly (no
	// pending permission). This avoids racing with recovery/continuation LLM
	// calls in tests and real scenarios.
	if result != nil && !result.PendingApproval && err == nil {
		s.maybeGenerateTitleAsync(session, envelope)
	}
	return result, err
}

func (s *Service) startUserTurnLocked(session *sessionState, envelope message.Envelope, persistRunning bool) (preparedUserTurn, *SubmitResult, error) {
	sessionID := session.id
	now := s.now()
	s.reconcileExpiredPermissionResume(session, now)
	turnID := session.nextTurnID(now)
	runtimeCtx := s.buildRuntimeContext(sessionID, session.locator, envelope)
	if runtimeCtx.Metadata == nil {
		runtimeCtx.Metadata = map[string]string{}
	}
	runtimeCtx.Metadata["turn_id"] = turnID
	attachSessionGraphContext(session, &runtimeCtx)
	runtimeCtx.ProjectLedger = s.compactProjectLedgerForSession(sessionID)
	priorMessageCount := len(session.agent.GetMessages())
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = now
	}
	envelope.SessionID = sessionID
	modelEnvelope, eventMetadata, err := s.envelopeWithNoteContext(envelope)
	if err != nil {
		return preparedUserTurn{}, nil, err
	}
	modelEnvelope.SessionID = sessionID
	session.agent.AddEnvelope(modelEnvelope)
	session.setTitleIfEmpty(sessionTitleFromEnvelope(envelope))
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventUserMessageAccepted,
		Timestamp: now,
		Payload: events.MessagePayload{
			Source:      string(envelope.Source),
			Sender:      envelope.Sender,
			Text:        envelope.BodyText(),
			Attachments: envelope.ProtocolAttachments(),
			Metadata:    eventMetadata,
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventAgentIdentityUpdated,
		Timestamp: now,
		Payload: events.AgentIdentityPayload{
			ID:                session.identity.ID,
			Name:              session.identity.Name,
			Kind:              session.identity.Kind,
			Role:              session.identity.Role,
			ParentID:          session.identity.ParentID,
			SessionID:         session.identity.SessionID,
			Source:            session.identity.Source,
			CapabilitySummary: append([]string{}, session.identity.CapabilitySummary...),
			ModelHint:         session.identity.ModelHint,
			BudgetHint:        session.identity.BudgetHint,
			Display:           cloneMapStringString(session.identity.Display),
			LastActivityAt:    now,
		},
	})
	turn := preparedUserTurn{
		TurnID:            turnID,
		Envelope:          envelope,
		RuntimeContext:    runtimeCtx,
		PriorMessageCount: priorMessageCount,
	}
	session.recordTurnStarted(turnID, envelope, priorMessageCount, now)
	if err := s.writeSessionTurns(session); err != nil {
		return preparedUserTurn{}, nil, err
	}
	if !persistRunning {
		return turn, nil, nil
	}

	updatedAt := s.now()
	if err := s.persistSession(session, updatedAt); err != nil {
		return preparedUserTurn{}, nil, err
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   true,
		},
	})
	_ = s.writeSessionTimeline(session)
	return turn, &SubmitResult{
		SessionID: sessionID,
		TurnID:    turnID,
		Completed: false,
		Status:    "running",
		UpdatedAt: updatedAt,
	}, nil
}

func (s *Service) finishAgentTurnLocked(ctx context.Context, session *sessionState, turnID string, envelope message.Envelope, runtimeCtx automation.SessionContext, priorMessageCount int) (*SubmitResult, error) {
	sessionID := session.id
	artifactEvents := &artifactCollector{}
	unsubscribeArtifacts := session.events.Attach(artifactEvents)
	defer unsubscribeArtifacts()
	runSink := events.SinkFunc(func(event events.Event) {
		if event.Type == events.EventRunnerPhaseChanged {
			if payload, ok := event.Payload.(events.RunnerPhasePayload); ok {
				now := event.Timestamp
				if now.IsZero() {
					now = s.now()
				}
				session.updateActivePhase(turnID, payload.Phase)
				session.updateTurnPhase(turnID, payload.Phase, payload.RecoveryHint, payload.ToolName, now)
			}
		}
		session.events.Emit(event)
	})

	runErr := session.agent.RunWithOptions(ctx, agent.RunOptions{
		SessionID:        sessionID,
		TurnID:           turnID,
		ActorID:          session.identity.ID,
		ActorKind:        "main",
		EmitRunnerPhases: true,
		Sink:             runSink,
		RuntimeContext:   runtimeCtx,
		Checkpoint: func() {
			s.checkpointRunningTurn(session, turnID)
		},
		DrainInjections: func(ctx context.Context, limit int) (conversation.InjectionDrain, error) {
			_ = ctx
			now := s.now()
			injected := session.drainTurnInjections(turnID, limit, now)
			if len(injected) == 0 {
				return conversation.InjectionDrain{}, nil
			}
			messages := make([]protocol.Message, 0, len(injected))
			summaries := make([]string, 0, len(injected))
			mode := ""
			for _, envelope := range injected {
				messages = append(messages, envelope.ToProtocolMessage(protocol.RoleUser, "", false))
				if summary := turnSummary(envelope.BodyText()); summary != "" {
					summaries = append(summaries, summary)
				}
				if envelope.Metadata != nil && mode == "" {
					mode = envelope.Metadata["queue_mode"]
				}
			}
			remaining := len(session.pendingTurnInjections(turnID))
			_ = s.writeSessionTurns(session)
			session.events.Emit(events.Event{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      events.EventMessageInjected,
				Timestamp: now,
				Payload: events.MessageInjectedPayload{
					Count:     len(injected),
					Mode:      mode,
					Remaining: remaining,
					Summary:   strings.Join(summaries, " | "),
				},
			})
			return conversation.InjectionDrain{
				Messages:  messages,
				Count:     len(injected),
				Remaining: remaining,
				Mode:      mode,
				Summary:   strings.Join(summaries, " | "),
			}, nil
		},
	})
	returnErr := runErr
	submitStatus := "completed"
	pendingApproval := false
	pendingRequestID := ""
	var pendingErr tools.ErrPermissionPending
	turnCanceled := runErr != nil && errors.Is(context.Cause(ctx), ErrTurnCanceled)
	if errors.As(runErr, &pendingErr) {
		pendingApproval = true
		pendingRequestID = strings.TrimSpace(pendingErr.RequestID)
		submitStatus = "pending_approval"
		returnErr = nil
		session.agent.SetPendingResume(pendingRequestID, priorMessageCount, envelope, runtimeCtx, session.pendingTurnInjections(turnID)...)
	} else if turnCanceled {
		submitStatus = "canceled"
		returnErr = nil
		session.agent.ClearPendingResume()
	} else {
		session.agent.ClearPendingResume()
	}
	artifactAttachments, artifactWarnings := s.materializeArtifactPaths(sessionID, artifactEvents.Paths())
	if len(artifactAttachments) > 0 {
		session.agent.AppendAssistantDelivery("", "", artifactAttachments)
	}
	updatedAt := s.now()
	status := submitStatus
	if runErr != nil && !pendingApproval && !turnCanceled {
		status = "error"
	}
	if status != "pending_approval" {
		session.promoteTurnInjectionsToQueue(turnID, updatedAt)
		_ = s.writeSessionQueue(session)
	}
	errorText := ""
	if status == "error" && runErr != nil {
		errorText = runErr.Error()
	}
	session.updateTurnStatus(turnID, status, pendingRequestID, errorText, updatedAt)
	if status == "error" {
		session.updateTurnPhase(turnID, conversation.PhaseError, "", "", updatedAt)
	} else if status == "canceled" || status == "interrupted" {
		session.updateTurnPhase(turnID, conversation.PhaseInterrupted, "", "", updatedAt)
	}
	ledgerErr := s.updateProjectLedgerFromTurn(session, turnID, envelope, status, runErr, priorMessageCount, updatedAt)
	turnWriteErr := s.writeSessionTurns(session)
	persistErr := s.persistSession(session, updatedAt)
	for _, err := range []error{ledgerErr, turnWriteErr, persistErr} {
		if err == nil {
			continue
		}
		if returnErr == nil {
			returnErr = err
		} else {
			session.events.Emit(events.Event{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      events.EventWarningRaised,
				Timestamp: updatedAt,
				Payload: events.NoticePayload{
					Message: fmt.Sprintf("failed to persist session state: %v", err),
				},
			})
		}
	}
	for _, warning := range artifactWarnings {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: warning},
		})
	}

	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})

	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventTurnCompleted,
		Timestamp: updatedAt,
		Payload:   events.TurnPayload{Status: status},
	})
	s.captureSessionSignalCandidates(session, updatedAt, turnID)
	_ = s.writeSessionTimeline(session)

	return &SubmitResult{
		SessionID:        sessionID,
		TurnID:           turnID,
		Completed:        runErr == nil,
		Status:           status,
		PendingApproval:  pendingApproval,
		PendingRequestID: pendingRequestID,
		UpdatedAt:        updatedAt,
	}, returnErr
}

func (s *Service) checkpointRunningTurn(session *sessionState, turnID string) {
	if session == nil {
		return
	}
	now := s.now()
	session.updateTurnStatus(turnID, "running", "", "", now)
	if err := s.persistSession(session, now); err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload: events.NoticePayload{
				Message: fmt.Sprintf("failed to persist turn checkpoint: %v", err),
			},
		})
	}
}

func (s *Service) captureSessionSignalCandidates(session *sessionState, now time.Time, turnID string) {
	if session == nil || session.agent == nil {
		return
	}
	timeline := session.timeline.Entries(snapshotTimelineLimit)
	if err := session.agent.CaptureTimelineMemoryCandidates(timeline); err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to capture timeline memory candidates: %v", err)},
		})
	}

	if s.analyze == nil {
		return
	}
	report, err := s.analyze(buildInsightsInput(collectSessionInsightsSnapshot(session, timeline)))
	if err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to analyze insights for memory bridge: %v", err)},
		})
		return
	}
	if err := session.agent.CaptureInsightMemoryCandidates(report); err != nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: now,
			Payload:   events.NoticePayload{Message: fmt.Sprintf("failed to capture insight memory candidates: %v", err)},
		})
	}
}

type sessionInsightsSnapshot struct {
	Messages     []protocol.Message
	ActiveSkills []string
	ToolCatalog  tools.ToolCatalog
	Todos        []todo.Item
	Tasks        []*task.FileTask
	Timeline     []events.Event
}

func collectSessionInsightsSnapshot(session *sessionState, timeline []events.Event) sessionInsightsSnapshot {
	return sessionInsightsSnapshot{
		Messages:     session.agent.GetMessages(),
		ActiveSkills: session.agent.ActiveSkillNames(),
		ToolCatalog:  session.agent.ToolCatalog(),
		Todos:        session.agent.TodoMgr().List(),
		Tasks:        session.agent.TaskMgr().List(),
		Timeline:     append([]events.Event{}, timeline...),
	}
}

func buildInsightsInput(snapshot sessionInsightsSnapshot) insights.Input {
	input := insights.Input{
		CurrentMessages: make([]insights.Message, 0, len(snapshot.Messages)),
		ActiveSkills:    append([]string{}, snapshot.ActiveSkills...),
		ToolCatalog: insights.ToolCatalog{
			ActiveBundles: append([]string{}, snapshot.ToolCatalog.ActiveBundles...),
		},
		Todos: make([]insights.WorkItem, 0, len(snapshot.Todos)),
		Tasks: make([]insights.WorkItem, 0, len(snapshot.Tasks)),
	}

	for _, msg := range snapshot.Messages {
		textParts := make([]string, 0, len(msg.Content))
		toolNames := make([]string, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch string(block.Type) {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_use":
				if block.Name != "" {
					toolNames = append(toolNames, block.Name)
				}
			}
		}
		input.CurrentMessages = append(input.CurrentMessages, insights.Message{
			Text:      strings.Join(textParts, ""),
			ToolNames: toolNames,
		})
	}

	for _, item := range snapshot.Todos {
		input.Todos = append(input.Todos, insights.WorkItem{Status: string(item.Status)})
	}
	for _, item := range snapshot.Tasks {
		input.Tasks = append(input.Tasks, insights.WorkItem{Status: string(item.Status)})
	}
	return input
}

func (s *Service) resumePendingTurnLocked(ctx context.Context, session *sessionState, requestID, turnID string) (*SubmitResult, error) {
	pending := session.agent.PendingResumeState()
	if pending == nil {
		return nil, nil
	}
	if reqID := strings.TrimSpace(requestID); reqID != "" && strings.TrimSpace(pending.RequestID) != "" && strings.TrimSpace(pending.RequestID) != reqID {
		return nil, fmt.Errorf("permission request does not match the blocked turn")
	}
	currentMessages := session.agent.GetMessages()
	if pending.PriorMessageCount < 0 || pending.PriorMessageCount > len(currentMessages) {
		return nil, fmt.Errorf("blocked turn state is no longer resumable")
	}
	session.agent.ClearPendingResume()
	envelope := pending.Envelope.Normalized()
	if envelope.Timestamp.IsZero() {
		envelope.Timestamp = s.now()
	}
	envelope.SessionID = session.id
	resumePriorMessageCount := len(currentMessages)
	session.agent.AppendRuntimeFeedback("The previously blocked tool permission has been approved. Continue from the current transcript and retry only the approved tool call if needed. Do not repeat completed analysis, reread files already read, or restart the user's task from the beginning.")
	for _, injected := range pending.Injections {
		mode := QueueModeFollowUp
		if injected.Metadata != nil && strings.EqualFold(injected.Metadata["queue_mode"], string(QueueModeSteering)) {
			mode = QueueModeSteering
		}
		now := s.now()
		session.addTurnInjection(turnID, QueuedTurn{
			ID:        session.nextTurnID(now),
			Mode:      mode,
			Status:    "injected",
			Source:    string(injected.Source),
			Sender:    strings.TrimSpace(injected.Sender),
			Summary:   turnSummary(injected.BodyText()),
			CreatedAt: now,
			UpdatedAt: now,
			Envelope:  injected.Normalized(),
		}, now)
	}
	session.setTitleIfEmpty(sessionTitleFromEnvelope(envelope))
	return s.finishAgentTurnLocked(ctx, session, turnID, envelope, pending.RuntimeContext.Clone(), resumePriorMessageCount)
}

func (s *Service) reconcileExpiredPermissionResume(session *sessionState, now time.Time) {
	if session == nil {
		return
	}
	pending := session.agent.PendingResumeState()
	if pending == nil || strings.TrimSpace(pending.RequestID) == "" {
		return
	}
	requestID := strings.TrimSpace(pending.RequestID)
	for _, item := range session.agent.PendingPermissions(session.id) {
		if strings.TrimSpace(item.ID) == requestID {
			return
		}
	}
	session.updateTurnPermissionStatus(requestID, tools.PermissionStatusExpired, now)
	session.agent.AppendRuntimeFeedback("The previously blocked tool permission expired before it was approved. Do not retry that blocked tool call automatically. Continue from the current transcript with a safer alternative, or ask for fresh approval if the tool call is still necessary.")
	session.agent.ClearPendingResume()
	_ = s.writeSessionTurns(session)
}

// PostRuntimeReply appends a background/runtime assistant reply into an existing session.
func (s *Service) PostRuntimeReply(ctx context.Context, sessionID, text string) error {
	return s.PostRuntimeReplyWithArtifactPaths(ctx, sessionID, text, nil)
}

// PostRuntimeReplyWithArtifactPaths appends a background/runtime assistant
// reply plus any generated local files into an existing session.
func (s *Service) PostRuntimeReplyWithArtifactPaths(ctx context.Context, sessionID, text string, artifactPaths []string) error {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	attachments, warnings := s.materializeArtifactPaths(sessionID, artifactPaths)
	if text == "" && len(attachments) == 0 {
		if len(warnings) > 0 {
			return errors.New(strings.Join(warnings, "; "))
		}
		return nil
	}
	release, err := session.acquire(context.Background())
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	now := s.now()
	turnID := session.nextTurnID(now)
	session.agent.AppendAssistantDelivery(text, protocol.KindBackground, attachments)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil {
		return persistErr
	}

	if text != "" {
		payload := events.TextPayload{Role: protocol.RoleAssistant, Text: text}
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventAssistantTextDelta,
			Timestamp: updatedAt,
			Payload:   payload,
		})
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventAssistantMessageComplete,
			Timestamp: updatedAt,
			Payload:   payload,
		})
	}
	for _, warning := range warnings {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: warning},
		})
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventTurnCompleted,
		Timestamp: updatedAt,
		Payload:   events.TurnPayload{Status: "completed"},
	})
	_ = s.writeSessionTimeline(session)
	return nil
}

// ExecuteCommand runs one serialized slash command against the session.
func (s *Service) ExecuteCommand(ctx context.Context, sessionID string, cmd commands.Command) (commands.Result, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return commands.Result{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return commands.Result{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	now := s.now()
	turnID := session.nextTurnID(now)
	ctx = withSessionLock(ctx, sessionID)
	ctx = commands.WithSessionContext(ctx, commands.SessionContext{
		SessionID: sessionID,
		Channel:   session.locator.Channel,
		Key:       session.locator.Key,
		UserID:    session.locator.UserID,
		Metadata:  mergeCommandContextMetadata(session.locator.Metadata, cmd.Metadata),
	})
	if cmd.Name == "ledger" {
		if len(cmd.Args) > 0 {
			return commands.Result{}, fmt.Errorf("command /ledger does not accept arguments")
		}
		ledger, err := s.ProjectLedger(sessionID)
		if err != nil {
			return commands.Result{}, err
		}
		output := ledger.Compact
		if strings.TrimSpace(output) == "" {
			output = "Project ledger is empty."
		}
		return commands.Result{Name: "ledger", Output: output}, nil
	}
	result, execErr := s.commands.Execute(ctx, session.agent, cmd)
	if result.Name == "" {
		result.Name = cmd.Name
	}
	if execErr == nil && strings.EqualFold(cmd.Name, "clear") {
		session.clearQueue()
		if err := s.writeSessionQueue(session); err != nil {
			execErr = err
		}
	}
	if execErr == nil && result.Dispatch != nil && result.Dispatch.Mode == "subagent_job" {
		jobID, err := s.dispatchPackageSubagent(ctx, session, turnID, result.Dispatch)
		if err != nil {
			execErr = err
			result.DispatchStatus = "failed"
			result.DispatchError = err.Error()
			result.Diagnostics = append(result.Diagnostics, err.Error())
		} else {
			result.DispatchedJobID = jobID
			result.DispatchStatus = "dispatched"
			result.RefreshSnapshot = true
			if strings.TrimSpace(result.Output) != "" {
				result.Output += "\n"
			}
			result.Output += "Started durable subagent job " + jobID + "."
		}
	}
	commandAttachments, artifactWarnings := s.materializeArtifactPaths(sessionID, []string{strings.TrimSpace(result.ArtifactPath)})
	if len(commandAttachments) > 0 {
		session.agent.AppendAssistantDelivery("", "", commandAttachments)
	}
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true

	if persistErr != nil {
		if execErr == nil {
			execErr = persistErr
		} else {
			session.events.Emit(events.Event{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      events.EventWarningRaised,
				Timestamp: updatedAt,
				Payload: events.NoticePayload{
					Message: fmt.Sprintf("failed to persist session state: %v", persistErr),
				},
			})
		}
	}
	if execErr == nil && result.Dispatch != nil && result.Dispatch.Mode == "agent_turn" {
		dispatchedTurnID, err := s.dispatchPackageAgentTurn(context.Background(), sessionID, result.Dispatch)
		if err != nil {
			execErr = err
			result.DispatchStatus = "failed"
			result.DispatchError = err.Error()
			result.Diagnostics = append(result.Diagnostics, err.Error())
		} else {
			result.DispatchedTurnID = dispatchedTurnID
			result.DispatchStatus = "dispatched"
			result.RefreshSnapshot = true
			if strings.TrimSpace(result.Output) != "" {
				result.Output += "\n"
			}
			result.Output += "Queued agent turn " + dispatchedTurnID + "."
		}
	}
	if execErr != nil {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventErrorRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: execErr.Error()},
		})
	}
	for _, warning := range artifactWarnings {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventWarningRaised,
			Timestamp: updatedAt,
			Payload:   events.NoticePayload{Message: warning},
		})
	}
	if result.RefreshSnapshot {
		session.events.Emit(events.Event{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      events.EventSnapshotReady,
			Timestamp: updatedAt,
			Payload: events.SnapshotPayload{
				UpdatedAt: updatedAt,
				Running:   false,
			},
		})
	}
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventCommandCompleted,
		Timestamp: updatedAt,
		Payload: events.CommandPayload{
			Name:               result.Name,
			Output:             result.Output,
			ArtifactPath:       result.ArtifactPath,
			RefreshSnapshot:    result.RefreshSnapshot,
			DispatchMode:       commandDispatchMode(result.Dispatch),
			DispatchInvocation: commandDispatchInvocation(result.Dispatch),
			DispatchStatus:     result.DispatchStatus,
			DispatchError:      result.DispatchError,
			DispatchedTurnID:   result.DispatchedTurnID,
			DispatchedJobID:    result.DispatchedJobID,
			Error:              errorString(execErr),
		},
	})
	_ = s.writeSessionTimeline(session)

	return result, execErr
}

func commandDispatchMode(dispatch *commands.PackageCommandDispatch) string {
	if dispatch == nil {
		return ""
	}
	return strings.TrimSpace(dispatch.Mode)
}

func commandDispatchInvocation(dispatch *commands.PackageCommandDispatch) string {
	if dispatch == nil {
		return ""
	}
	return strings.TrimSpace(dispatch.Invocation)
}

func (s *Service) dispatchPackageSubagent(ctx context.Context, session *sessionState, turnID string, dispatch *commands.PackageCommandDispatch) (string, error) {
	if session == nil || dispatch == nil {
		return "", fmt.Errorf("missing package command dispatch")
	}
	prompt := strings.TrimSpace(dispatch.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("package command dispatch missing prompt")
	}
	agentType := strings.TrimSpace(dispatch.AgentType)
	if agentType == "" && len(dispatch.Roles) > 0 {
		agentType = strings.TrimSpace(dispatch.Roles[0])
	}
	if agentType == "" {
		agentType = "Explore"
	}
	dispatchCtx := agent.WithSubagentEvents(ctx, session.id, turnID, session.events)
	job, err := session.agent.StartDurableSubagentWithContext(dispatchCtx, prompt, agentType, dispatch.WriteScope)
	if err != nil {
		return "", err
	}
	return job.IDString(), nil
}

func (s *Service) dispatchPackageAgentTurn(ctx context.Context, sessionID string, dispatch *commands.PackageCommandDispatch) (string, error) {
	if dispatch == nil {
		return "", fmt.Errorf("missing package command dispatch")
	}
	prompt := strings.TrimSpace(dispatch.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("package command dispatch missing prompt")
	}
	metadata := map[string]string{
		"package": dispatch.PackageName,
		"command": dispatch.CommandName,
		"mode":    dispatch.Mode,
	}
	if dispatch.Namespace != "" {
		metadata["namespace"] = dispatch.Namespace
	}
	if dispatch.Invocation != "" {
		metadata["invocation"] = dispatch.Invocation
	}
	envelope := message.NewRuntimeEnvelope(message.SourceCommand, sessionID, "package-command", prompt, s.now(), metadata)
	result, err := s.SubmitAsync(ctx, sessionID, envelope, SubmitOptions{QueueMode: QueueModeFollowUp})
	if err != nil {
		return "", err
	}
	return result.TurnID, nil
}

func (s *Service) materializeArtifactPaths(sessionID string, paths []string) ([]message.AttachmentRef, []string) {
	if len(paths) == 0 {
		return nil, nil
	}
	attachments := make([]message.AttachmentRef, 0, len(paths))
	warnings := make([]string, 0)
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		absolutePath, err := filepath.Abs(path)
		if err == nil {
			path = absolutePath
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		attachment, err := s.storeArtifactPath(sessionID, path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to attach generated file %s: %v", path, err))
			continue
		}
		attachments = append(attachments, attachment)
	}
	return attachments, warnings
}

func (s *Service) storeArtifactPath(sessionID, path string) (message.AttachmentRef, error) {
	file, err := os.Open(path)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	defer file.Close()

	name := filepath.Base(path)
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	return s.StoreAttachment(context.Background(), sessionID, AttachmentUpload{
		Name:     name,
		MIMEType: mimeType,
		Reader:   file,
	})
}

// StoreAttachment persists one uploaded file inside the session attachment directory.
func (s *Service) StoreAttachment(ctx context.Context, sessionID string, upload AttachmentUpload) (message.AttachmentRef, error) {
	_ = ctx
	if _, err := s.requireSession(sessionID); err != nil {
		return message.AttachmentRef{}, err
	}
	if upload.Reader == nil {
		return message.AttachmentRef{}, fmt.Errorf("missing attachment reader")
	}

	name := strings.TrimSpace(upload.Name)
	if name == "" {
		name = "attachment"
	}
	attachmentID, err := newAttachmentID()
	if err != nil {
		return message.AttachmentRef{}, err
	}

	dir := s.sessionAttachmentsDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return message.AttachmentRef{}, err
	}

	storedName := fmt.Sprintf("%s-%s", attachmentID, sanitizeAttachmentName(name))
	absolutePath := filepath.Join(dir, storedName)
	file, err := os.Create(absolutePath)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	limit := MaxAttachmentUploadBytes()
	limitedReader := &io.LimitedReader{R: upload.Reader, N: limit + 1}
	size, copyErr := io.Copy(file, limitedReader)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(absolutePath)
		return message.AttachmentRef{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(absolutePath)
		return message.AttachmentRef{}, closeErr
	}
	if size > limit {
		_ = os.Remove(absolutePath)
		return message.AttachmentRef{}, fmt.Errorf("attachment %q exceeds max size of %d bytes", name, limit)
	}

	mimeType := strings.TrimSpace(upload.MIMEType)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}

	return message.AttachmentRef{
		ID:        attachmentID,
		Name:      name,
		MIMEType:  mimeType,
		Path:      s.relativePath(absolutePath),
		URL:       fmt.Sprintf("/sessions/%s/attachments/%s", sessionID, attachmentID),
		SizeBytes: size,
	}, nil
}

// ResolveAttachment finds one persisted attachment by session and attachment ID.
func (s *Service) ResolveAttachment(sessionID, attachmentID string) (message.AttachmentRef, string, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return message.AttachmentRef{}, "", fmt.Errorf("missing attachment id")
	}

	dir := s.sessionAttachmentsDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return message.AttachmentRef{}, "", newAttachmentNotFoundError(attachmentID)
		}
		return message.AttachmentRef{}, "", err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != attachmentID && !strings.HasPrefix(name, attachmentID+"-") {
			continue
		}
		absolutePath := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			return message.AttachmentRef{}, "", err
		}
		displayName := strings.TrimPrefix(name, attachmentID+"-")
		if displayName == "" {
			displayName = name
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(displayName)))
		return message.AttachmentRef{
			ID:        attachmentID,
			Name:      displayName,
			MIMEType:  mimeType,
			Path:      s.relativePath(absolutePath),
			URL:       fmt.Sprintf("/sessions/%s/attachments/%s", sessionID, attachmentID),
			SizeBytes: info.Size(),
		}, absolutePath, nil
	}
	return message.AttachmentRef{}, "", newAttachmentNotFoundError(attachmentID)
}

// Snapshot returns the unified current session view.
func (s *Service) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	return s.snapshotFromSession(session), nil
}

// Models returns the available model profiles and optional session override.
func (s *Service) Models(ctx context.Context, sessionID string) (ModelsView, error) {
	_ = ctx
	sessionProfileID := ""
	reasoningEffort := ""
	if strings.TrimSpace(sessionID) != "" {
		session, err := s.requireSession(sessionID)
		if err != nil {
			return ModelsView{}, err
		}
		session.mu.RLock()
		sessionProfileID = strings.TrimSpace(session.modelProfileID)
		reasoningEffort = normalizeSessionReasoningEffort(session.reasoningEffort)
		session.mu.RUnlock()
	}
	return s.modelsView(sessionProfileID, reasoningEffort), nil
}

// SetSessionModelProfile persists and applies a session-specific model profile.
func (s *Service) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (ModelsView, error) {
	return s.SetSessionModelProfileWithReasoning(ctx, sessionID, profileID, "")
}

// SetSessionModelProfileWithReasoning persists and applies a session-specific model profile plus optional reasoning effort override.
func (s *Service) SetSessionModelProfileWithReasoning(ctx context.Context, sessionID, profileID, reasoningEffort string) (ModelsView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return ModelsView{}, err
	}
	profile, ok := s.cfg.ModelProfileByID(profileID)
	if !ok {
		return ModelsView{}, fmt.Errorf("model profile not found: %s", profileID)
	}
	reasoningEffort = normalizeSessionReasoningEffort(reasoningEffort)
	appliedProfile := profile
	if reasoningEffort != "" {
		appliedProfile.ReasoningEffort = reasoningEffort
	}
	session.mu.Lock()
	session.modelProfileID = profile.ID
	session.reasoningEffort = reasoningEffort
	session.mu.Unlock()
	session.agent.ApplyModelProfile(appliedProfile)
	now := s.now()
	if err := s.persistSession(session, now); err != nil {
		return ModelsView{}, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        now,
		Category:  "capability",
		Action:    "set_session_model",
		Severity:  "info",
		SessionID: session.id,
		Summary:   "Session model profile changed to " + profile.ID,
		Metadata: map[string]string{
			"profile_id":       profile.ID,
			"provider":         profile.Provider,
			"model":            profile.Model,
			"reasoning_effort": reasoningEffort,
		},
	})
	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: now,
		Payload: events.SnapshotPayload{
			UpdatedAt: now,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	return s.modelsView(profile.ID, reasoningEffort), nil
}

func (s *Service) modelsView(sessionProfileID, reasoningEffort string) ModelsView {
	cfg := s.cfg
	if cfg == nil {
		return ModelsView{}
	}
	defaultID := strings.TrimSpace(cfg.DefaultProfileID)
	reasoningEffort = normalizeSessionReasoningEffort(reasoningEffort)
	profiles := make([]ModelProfile, 0, len(cfg.ModelProfiles))
	for id := range cfg.ModelProfiles {
		profile, ok := cfg.ModelProfileByID(id)
		if !ok {
			continue
		}
		profiles = append(profiles, ModelProfile{
			ID:                profile.ID,
			Name:              profile.Name,
			Provider:          profile.Provider,
			Model:             profile.Model,
			BaseURL:           profile.BaseURL,
			MaxTokens:         profile.MaxTokens,
			TimeoutSeconds:    profile.TimeoutSeconds,
			SupportsStreaming: profile.SupportsStreaming,
			SupportsVision:    profile.SupportsVision,
			ReasoningEffort:   profile.ReasoningEffort,
			Default:           profile.ID == defaultID,
			Selected:          strings.TrimSpace(sessionProfileID) != "" && profile.ID == strings.TrimSpace(sessionProfileID),
		})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Default != profiles[j].Default {
			return profiles[i].Default
		}
		return profiles[i].ID < profiles[j].ID
	})
	return ModelsView{
		DefaultProfileID: defaultID,
		SessionProfileID: strings.TrimSpace(sessionProfileID),
		ReasoningEffort:  reasoningEffort,
		Profiles:         profiles,
	}
}

func normalizeSessionReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

// SecuritySummary returns a lightweight Capability/Identity/Knowledge risk view.
func (s *Service) SecuritySummary(ctx context.Context) (security.CIKSummary, error) {
	_ = ctx
	cfg := s.cfg
	if cfg == nil {
		return security.CIKSummary{}, fmt.Errorf("missing config")
	}
	recent, _ := s.SecurityAudit(context.Background(), 10)
	policy := security.SecurityPolicy{
		InteractiveApprovalEnabled: cfg.Tools.Permissions.InteractiveApprovalEnabled,
		InteractiveApprovalMode:    cfg.Tools.Permissions.InteractiveApprovalMode,
		ApprovalSources:            append([]string{}, cfg.Tools.Permissions.InteractiveApprovalSources...),
		ApprovalTools:              append([]string{}, cfg.Tools.Permissions.InteractiveApprovalTools...),
		PendingTTLSeconds:          cfg.Tools.Permissions.PendingTTLSeconds,
		TrustedPathPrefixes:        append([]string{}, cfg.Tools.Permissions.TrustedPathPrefixes...),
		TrustedCommandPrefixes:     append([]string{}, cfg.Tools.Permissions.TrustedCommandPrefixes...),
		BlockAutomationMutations:   cfg.Tools.Permissions.BlockAutomationMutations,
		MemoryIdentityReview:       true,
		PackageInstallReview:       true,
		SubagentWorkspaceIsolation: true,
	}
	capabilityItems := []string{}
	if cfg.Tools.WebSearch.Enabled {
		capabilityItems = append(capabilityItems, "web_search")
	}
	if cfg.Tools.WebFetch.Enabled {
		capabilityItems = append(capabilityItems, "web_fetch")
	}
	if cfg.Tools.Browser.Enabled {
		capabilityItems = append(capabilityItems, "browser")
	}
	if cfg.Cron.Enabled {
		capabilityItems = append(capabilityItems, "cron")
	}
	if cfg.Heartbeat.Enabled {
		capabilityItems = append(capabilityItems, "heartbeat")
	}
	identityItems := []string{"team.lead_name=" + cfg.LeadName}
	if cfg.Feishu.Enabled {
		identityItems = append(identityItems, "feishu channel")
	}
	if cfg.Weixin.Enabled {
		identityItems = append(identityItems, "weixin channel")
	}
	knowledgeItems := []string{"memory", "skills", "history_search"}
	if cfg.Tools.History.Enabled {
		knowledgeItems = append(knowledgeItems, "session archives")
	}
	return security.CIKSummary{
		GeneratedAt: s.now(),
		Policy:      policy,
		Capability:  buildRiskSummary("capability", capabilityItems, cfg.Tools.Permissions.InteractiveApprovalEnabled),
		Identity:    buildRiskSummary("identity", identityItems, cfg.Tools.Permissions.InteractiveApprovalEnabled),
		Knowledge:   buildRiskSummary("knowledge", knowledgeItems, cfg.Tools.History.Enabled),
		Recent:      recent,
	}, nil
}

// SecurityAudit returns recent security audit events.
func (s *Service) SecurityAudit(ctx context.Context, limit int) ([]security.SecurityEvent, error) {
	_ = ctx
	if limit <= 0 {
		limit = 50
	}
	path := filepath.Join(s.cfg.StateDir, securityAuditFileName)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var eventsOut []security.SecurityEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event security.SecurityEvent
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			eventsOut = append(eventsOut, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(eventsOut) > limit {
		eventsOut = eventsOut[len(eventsOut)-limit:]
	}
	for i, j := 0, len(eventsOut)-1; i < j; i, j = i+1, j-1 {
		eventsOut[i], eventsOut[j] = eventsOut[j], eventsOut[i]
	}
	return eventsOut, nil
}

// ListPackages returns installed declaration-only Godex packages.
func (s *Service) ListPackages(ctx context.Context) ([]pkgregistry.Entry, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).List()
}

// InstallPackage installs one declaration-only Godex package.
func (s *Service) InstallPackage(ctx context.Context, source string) (pkgregistry.Entry, error) {
	_ = ctx
	entry, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).Install(source)
	if err != nil {
		return pkgregistry.Entry{}, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:       s.now(),
		Category: "capability",
		Action:   "install_package",
		Severity: "warning",
		Summary:  "Installed package " + entry.Name,
		Metadata: map[string]string{
			"package": entry.Name,
			"source":  entry.Source,
			"digest":  entry.Digest,
			"trust":   entry.Trust,
		},
	})
	return entry, nil
}

// ReinstallPackage reinstalls one package from its recorded source without
// removing the currently installed copy unless the reinstall succeeds.
func (s *Service) ReinstallPackage(ctx context.Context, name string) (pkgregistry.Entry, error) {
	_ = ctx
	entry, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).Reinstall(name)
	if err != nil {
		return pkgregistry.Entry{}, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:       s.now(),
		Category: "capability",
		Action:   "reinstall_package",
		Severity: "warning",
		Summary:  "Reinstalled package " + entry.Name,
		Metadata: map[string]string{
			"package": entry.Name,
			"source":  entry.Source,
			"digest":  entry.Digest,
			"version": entry.Version,
		},
	})
	return entry, nil
}

// RemovePackage removes one installed Godex package.
func (s *Service) RemovePackage(ctx context.Context, name string) (pkgregistry.Entry, error) {
	_ = ctx
	entry, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).Remove(name)
	if err != nil {
		return pkgregistry.Entry{}, err
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:       s.now(),
		Category: "capability",
		Action:   "remove_package",
		Severity: "info",
		Summary:  "Removed package " + entry.Name,
		Metadata: map[string]string{
			"package": entry.Name,
			"digest":  entry.Digest,
		},
	})
	return entry, nil
}

// ListPrompts returns prompt templates installed by packages.
func (s *Service) ListPrompts(ctx context.Context, includeContent bool) ([]pkgregistry.Prompt, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).ListPrompts(includeContent)
}

// ListPackageCommands returns package-provided slash-command workflow declarations.
func (s *Service) ListPackageCommands(ctx context.Context, includeContent bool) ([]pkgregistry.Command, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).ListCommands(includeContent)
}

// ListPackageRoles returns package-provided named subagent role declarations.
func (s *Service) ListPackageRoles(ctx context.Context, includeContent bool) ([]pkgregistry.Role, error) {
	_ = ctx
	return pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).ListRoles(includeContent)
}

// PackageQuality returns declaration health plus recent tool reliability.
func (s *Service) PackageQuality(ctx context.Context) (pkgregistry.QualityReport, error) {
	_ = ctx
	toolHealth, err := s.packageToolHealth()
	if err != nil {
		return pkgregistry.QualityReport{}, err
	}
	report, err := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir).BuildQualityReport(s.now().Format(time.RFC3339), toolHealth, knownToolBundles())
	if err != nil {
		return pkgregistry.QualityReport{}, err
	}
	return report, nil
}

// RunPackageSmoke runs one explicitly selected package smoke declaration
// through a backend session and the normal shell permission path.
func (s *Service) RunPackageSmoke(ctx context.Context, packageName, smokeName, sessionID string) (pkgregistry.SmokeRun, error) {
	manager := pkgregistry.NewManager(s.cfg.StateDir, s.cfg.SkillsDir)
	entry, smoke, err := manager.GetSmokeTest(packageName, smokeName)
	if err != nil {
		return pkgregistry.SmokeRun{}, err
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		opened, err := s.OpenSession(ctx, SessionLocator{
			Channel: "web",
			Key:     "package-smoke",
			UserID:  "package-smoke",
			Metadata: map[string]string{
				"purpose": "package_smoke",
			},
		})
		if err != nil {
			return pkgregistry.SmokeRun{}, err
		}
		sessionID = opened.SessionID
	}
	session, err := s.requireSession(sessionID)
	if err != nil {
		return pkgregistry.SmokeRun{}, err
	}

	now := s.now()
	run := pkgregistry.SmokeRun{
		RunID:       pkgregistry.NewSmokeRunID(entry.Name, smoke.Name, now),
		PackageName: entry.Name,
		SmokeName:   smoke.Name,
		SessionID:   sessionID,
		Status:      "running",
		StartedAt:   now,
	}
	if recordErr := manager.RecordSmokeRun(run); recordErr != nil {
		return pkgregistry.SmokeRun{}, recordErr
	}

	complete := func(status string, result tools.ToolResult, runErr error) (pkgregistry.SmokeRun, error) {
		run.Status = status
		run.CompletedAt = s.now()
		run.ArtifactPaths = append([]string{}, result.ArtifactPaths...)
		if output, outputErr := result.OutputString(); outputErr == nil {
			run.Output = output
		}
		if runErr != nil {
			run.Error = runErr.Error()
		}
		_ = manager.RecordSmokeRun(run)
		s.appendSecurityEvent(security.SecurityEvent{
			At:        run.CompletedAt,
			Category:  "capability",
			Action:    "run_package_smoke",
			Severity:  smokeRunSeverity(run),
			SessionID: sessionID,
			Summary:   fmt.Sprintf("Package smoke %s/%s %s", run.PackageName, run.SmokeName, run.Status),
			Metadata: map[string]string{
				"package": run.PackageName,
				"smoke":   run.SmokeName,
				"run_id":  run.RunID,
				"status":  run.Status,
			},
		})
		return run, runErr
	}

	if issues := pkgregistry.SmokeQuickCheck(entry, smoke); len(issues) > 0 {
		run.Output = strings.Join(issues, "\n")
		run.Error = strings.Join(issues, "; ")
		return complete("invalid", tools.ToolResult{Text: run.Output}, nil)
	}

	release, err := session.acquire(ctx)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.CompletedAt = s.now()
		_ = manager.RecordSmokeRun(run)
		return run, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	execCtx := ctx
	cancel := func() {}
	if smoke.TimeoutSeconds > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(smoke.TimeoutSeconds)*time.Second)
	}
	defer cancel()

	turnID := session.nextTurnID(now)
	command := packageSmokeShellCommand(entry, smoke)
	runtimeCtx := automation.SessionContext{
		SessionID:      sessionID,
		LocatorChannel: session.locator.Channel,
		LocatorKey:     session.locator.Key,
		LocatorUserID:  session.locator.UserID,
		Source:         string(message.SourceCommand),
		Sender:         "package-smoke",
		Metadata: map[string]string{
			"package": entry.Name,
			"smoke":   smoke.Name,
			"run_id":  run.RunID,
		},
	}
	result, execErr := session.agent.RunPackageSmokeCommand(execCtx, runtimeCtx, command)
	status := packageSmokeStatus(result, execErr, smoke.ExpectedExitCode)
	var pending tools.ErrPermissionPending
	if execErr != nil && errors.As(execErr, &pending) {
		run.PendingApproval = true
		run.RequestID = pending.RequestID
		status = "pending_approval"
	}
	run.Status = status
	run.CompletedAt = s.now()
	run.ArtifactPaths = append([]string{}, result.ArtifactPaths...)
	if output, outputErr := result.OutputString(); outputErr == nil {
		run.Output = output
	}
	if execErr != nil {
		run.Error = execErr.Error()
	}
	if recordErr := manager.RecordSmokeRun(run); recordErr != nil && execErr == nil {
		execErr = recordErr
		run.Error = recordErr.Error()
	}
	persistErr := s.persistSession(session, run.CompletedAt)
	release()
	released = true
	if persistErr != nil && execErr == nil {
		execErr = persistErr
		run.Error = persistErr.Error()
		run.Status = "failed"
		_ = manager.RecordSmokeRun(run)
	}

	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventCommandCompleted,
		Timestamp: run.CompletedAt,
		Payload: events.CommandPayload{
			Name:            "package_smoke",
			Output:          run.Output,
			RefreshSnapshot: true,
			DispatchMode:    "smoke",
			DispatchStatus:  run.Status,
			DispatchError:   run.Error,
			Error:           errorString(execErr),
		},
	})
	session.events.Emit(events.Event{
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      events.EventSnapshotReady,
		Timestamp: run.CompletedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: run.CompletedAt,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	s.appendSecurityEvent(security.SecurityEvent{
		At:        run.CompletedAt,
		Category:  "capability",
		Action:    "run_package_smoke",
		Severity:  smokeRunSeverity(run),
		SessionID: sessionID,
		Summary:   fmt.Sprintf("Package smoke %s/%s %s", run.PackageName, run.SmokeName, run.Status),
		Metadata: map[string]string{
			"package": run.PackageName,
			"smoke":   run.SmokeName,
			"run_id":  run.RunID,
			"status":  run.Status,
		},
	})
	return run, nil
}

func packageSmokeShellCommand(entry pkgregistry.Entry, smoke pkgregistry.SmokeTest) string {
	workingDir := strings.TrimSpace(smoke.WorkingDir)
	dir := entry.Path
	if workingDir != "" {
		dir = filepath.Join(entry.Path, filepath.Clean(workingDir))
	}
	return "cd " + shellQuote(dir) + " && " + strings.TrimSpace(smoke.Command)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func packageSmokeStatus(result tools.ToolResult, err error, expected *int) string {
	want := 0
	if expected != nil {
		want = *expected
	}
	if got, ok := toolResultExitCode(result); ok {
		if got == want {
			return "passed"
		}
		return "failed"
	}
	if err == nil && want == 0 {
		return "passed"
	}
	return "failed"
}

func toolResultExitCode(result tools.ToolResult) (int, bool) {
	if result.Metadata == nil {
		return 0, false
	}
	value, ok := result.Metadata["exit_code"]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func smokeRunSeverity(run pkgregistry.SmokeRun) string {
	switch run.Status {
	case "passed":
		return "info"
	case "pending_approval":
		return "warning"
	default:
		return "warning"
	}
}

func (s *Service) appendSecurityEvent(event security.SecurityEvent) {
	if s == nil || s.cfg == nil {
		return
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = randomSuffix(8)
	}
	if event.At.IsZero() {
		event.At = s.now()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')
	path := filepath.Join(s.cfg.StateDir, securityAuditFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return
	}
}

func (s *Service) appendPermissionAuditEvent(action, severity, sessionID string, resolution tools.PermissionResolution) {
	if s == nil {
		return
	}
	req := resolution.Request
	metadata := map[string]string{
		"request_id": strings.TrimSpace(resolution.RequestID),
		"scope":      strings.TrimSpace(string(resolution.Scope)),
		"decision":   strings.TrimSpace(string(resolution.Decision)),
		"tool":       strings.TrimSpace(req.ToolName),
		"action":     strings.TrimSpace(req.Action),
		"source":     strings.TrimSpace(req.Source),
		"risk":       tools.PermissionRiskSummary(req),
	}
	if command := strings.TrimSpace(req.Command); command != "" {
		metadata["command"] = command
	}
	if len(req.Paths) > 0 {
		metadata["paths"] = strings.Join(req.Paths, ",")
	}
	summary := strings.TrimSpace(tools.PermissionIntentSummary(tools.PendingPermission{Request: req}))
	if summary == "" {
		summary = action + " " + strings.TrimSpace(resolution.RequestID)
	}
	s.appendSecurityEvent(security.SecurityEvent{
		At:        resolution.ResolvedAt,
		Category:  "capability",
		Action:    action,
		Severity:  severity,
		SessionID: strings.TrimSpace(sessionID),
		Source:    strings.TrimSpace(req.Source),
		Summary:   summary,
		Metadata:  metadata,
	})
}

// AppendSecurityEvent records an audit-relevant event from runtime adapters.
func (s *Service) AppendSecurityEvent(event security.SecurityEvent) {
	s.appendSecurityEvent(event)
}

func (s *Service) packageToolHealth() (pkgregistry.ToolHealthSummary, error) {
	sessions, err := s.ListSessions(context.Background(), SessionListFilter{})
	if err != nil {
		return pkgregistry.ToolHealthSummary{}, err
	}
	if len(sessions) > 30 {
		sessions = sessions[:30]
	}
	byTool := map[string]*pkgregistry.ToolStat{}
	summary := pkgregistry.ToolHealthSummary{InspectedSessions: len(sessions)}
	for _, session := range sessions {
		for _, event := range s.readSessionTimeline(session.SessionID) {
			if event.Type != events.EventToolCallFinished {
				continue
			}
			name, errText := toolPayloadNameAndError(event.Payload)
			if name == "" {
				continue
			}
			row := byTool[name]
			if row == nil {
				row = &pkgregistry.ToolStat{Name: name}
				byTool[name] = row
			}
			row.Total++
			summary.TotalRuns++
			if errText != "" {
				row.Failure++
				row.LastFailure = normalizeFailureReason(errText)
				summary.FailureRuns++
			} else {
				row.Success++
				summary.SuccessRuns++
			}
		}
	}
	rows := make([]pkgregistry.ToolStat, 0, len(byTool))
	for _, row := range byTool {
		row.SuccessRate = percentFloat(row.Success, row.Total)
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Total == rows[j].Total {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Total > rows[j].Total
	})
	summary.ByTool = rows
	summary.SuccessRate = percentFloat(summary.SuccessRuns, summary.TotalRuns)
	return summary, nil
}

func toolPayloadNameAndError(payload any) (string, string) {
	switch value := payload.(type) {
	case events.ToolCallPayload:
		return strings.TrimSpace(value.Name), strings.TrimSpace(value.Error)
	case map[string]any:
		return stringFromAny(value["name"]), stringFromAny(value["error"])
	default:
		data, _ := json.Marshal(payload)
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			return "", ""
		}
		return stringFromAny(decoded["name"]), stringFromAny(decoded["error"])
	}
}

func stringFromAny(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func normalizeFailureReason(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 120 {
			return line[:117] + "..."
		}
		return line
	}
	return "Unknown failure"
}

func percentFloat(value, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value*1000/total) / 10
}

func knownToolBundles() []string {
	return []string{"core_code", "planning", "background", "task_board", "team", "subagent", "mcp", "web", "browser", "desktop", "packages"}
}

func buildRiskSummary(axis string, items []string, guarded bool) security.RiskSummary {
	score := len(items)
	level := "low"
	if score >= 6 {
		level = "high"
	} else if score >= 3 {
		level = "medium"
	}
	advice := []string{}
	if !guarded {
		advice = append(advice, "enable review gates for untrusted sources")
	}
	return security.RiskSummary{
		Axis:   axis,
		Level:  level,
		Score:  score,
		Items:  append([]string{}, items...),
		Advice: advice,
	}
}

// ContextSummary returns a non-mutating prompt-budget summary for one session.
func (s *Service) ContextSummary(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.ContextInspection{}, err
	}
	return session.agent.InspectContext(ctx, sessionID)
}

// SessionSummary returns a lightweight runtime summary for one session.
func (s *Service) SessionSummary(ctx context.Context, sessionID string) (tools.SessionSummary, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SessionSummary{}, err
	}
	snapshot := s.snapshotFromSession(session)
	return tools.SessionSummary{
		SessionID:              snapshot.SessionID,
		Channel:                strings.TrimSpace(snapshot.Locator.Channel),
		Key:                    strings.TrimSpace(snapshot.Locator.Key),
		UserID:                 strings.TrimSpace(snapshot.Locator.UserID),
		MessageCount:           len(snapshot.Messages),
		ActiveSkillCount:       len(snapshot.ActiveSkills),
		PendingPermissionCount: len(snapshot.PendingPermissions),
		Running:                snapshot.Running,
		UpdatedAt:              snapshot.UpdatedAt,
	}, nil
}

// Timeline returns recent structured runtime events for one session.
func (s *Service) Timeline(ctx context.Context, sessionID string, limit int) ([]events.Event, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	if session.timeline == nil {
		return nil, nil
	}
	return session.timeline.Entries(limit), nil
}

// TimelinePage returns a durable newest-first page sourced from the full
// session event journal when available.
func (s *Service) TimelinePage(ctx context.Context, sessionID string, req TimelinePageRequest) (TimelinePage, error) {
	_ = ctx
	if _, err := s.requireSession(sessionID); err != nil {
		return TimelinePage{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if strings.TrimSpace(req.Cursor) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.Cursor))
		if err != nil || parsed < 0 {
			return TimelinePage{}, fmt.Errorf("invalid timeline cursor")
		}
		offset = parsed
	}

	filtered := filterTimelineEvents(s.readSessionTimeline(sessionID), req)
	reverseTimelineEvents(filtered)
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := append([]events.Event(nil), filtered[offset:end]...)
	page := TimelinePage{
		Items:   items,
		Total:   total,
		HasMore: end < total,
	}
	if page.HasMore {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func filterTimelineEvents(items []events.Event, req TimelinePageRequest) []events.Event {
	typeSet := make(map[string]struct{})
	for _, typ := range req.Types {
		typ = strings.TrimSpace(typ)
		if typ != "" {
			typeSet[typ] = struct{}{}
		}
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	jobID := strings.TrimSpace(req.JobID)
	turnID := strings.TrimSpace(req.TurnID)
	out := make([]events.Event, 0, len(items))
	for _, item := range items {
		if len(typeSet) > 0 {
			if _, ok := typeSet[string(item.Type)]; !ok {
				continue
			}
		}
		if turnID != "" && item.TurnID != turnID {
			continue
		}
		if jobID != "" && timelinePayloadString(item.Payload, "job_id") != jobID {
			continue
		}
		if query != "" && !strings.Contains(timelineSearchText(item), query) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func reverseTimelineEvents(items []events.Event) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func timelineSearchText(item events.Event) string {
	parts := []string{
		string(item.Type),
		item.SessionID,
		item.TurnID,
		item.Timestamp.Format(time.RFC3339Nano),
	}
	if data, err := json.Marshal(item.Payload); err == nil {
		parts = append(parts, string(data))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func timelinePayloadString(payload any, key string) string {
	if payload == nil || key == "" {
		return ""
	}
	if values, ok := payload.(map[string]any); ok {
		if value, ok := values[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return ""
	}
	if value, ok := values[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

// ListSubagents returns durable subagent jobs scoped to one session.
func (s *Service) ListSubagents(ctx context.Context, sessionID string) ([]agent.DurableSubagentJobView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListDurableSubagents(sessionID), nil
}

// GetSubagent returns one durable subagent job scoped to a session.
func (s *Service) GetSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentJobView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	return session.agent.GetDurableSubagent(sessionID, jobID)
}

// ReviewSubagent returns the merge review for one durable subagent job.
func (s *Service) ReviewSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentReviewView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentReviewView{}, err
	}
	return session.agent.ReviewDurableSubagentView(sessionID, jobID)
}

// CancelSubagent requests cancellation of one durable subagent job.
func (s *Service) CancelSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentJobView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	result, err := session.agent.CancelDurableSubagentWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	return result, nil
}

// ResumeSubagent resumes one interrupted, canceled, or errored durable subagent.
func (s *Service) ResumeSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentJobView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	result, err := session.agent.ResumeDurableSubagentViewWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
	if err != nil {
		return agent.DurableSubagentJobView{}, err
	}
	return result, nil
}

// MergeSubagent applies reviewed subagent changes into the main workspace.
func (s *Service) MergeSubagent(ctx context.Context, sessionID, jobID string) (agent.DurableSubagentMergeView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	defer release()
	result, err := session.agent.MergeDurableSubagentViewWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
	if err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	job, jobErr := session.agent.GetDurableSubagent(sessionID, jobID)
	if jobErr == nil {
		_ = s.appendSessionGraphMerge(session, job, "merged durable subagent "+jobID)
	}
	updatedAt := s.now()
	if err := s.persistSession(session, updatedAt); err != nil {
		return agent.DurableSubagentMergeView{}, err
	}
	return result, nil
}

// ListLongTasks returns durable LongTasks scoped to a session.
func (s *Service) ListLongTasks(ctx context.Context, sessionID string) ([]agent.LongTaskView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListLongTasks(sessionID)
}

// GetLongTask returns one durable LongTask.
func (s *Service) GetLongTask(ctx context.Context, sessionID, workflowID string) (agent.LongTaskView, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	return session.agent.GetLongTask(workflowID)
}

// MintuiListLongTasks returns durable LongTasks in the mintui
// projection (LongTaskRow) used by the Ctrl+B background-task
// popup.  The mintui package deliberately does not import
// internal/agent, so we translate agent.LongTaskView → LongTaskRow
// at this boundary.
func (s *Service) MintuiListLongTasks(ctx context.Context, sessionID string) ([]LongTaskRow, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	views, err := session.agent.ListLongTasks(sessionID)
	if err != nil {
		return nil, err
	}
	rows := make([]LongTaskRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, projectLongTaskView(v))
	}
	return rows, nil
}

// MintuiGetLongTask returns the detailed snapshot for one
// durable LongTask in the mintui projection.
func (s *Service) MintuiGetLongTask(ctx context.Context, sessionID, workflowID string) (LongTaskDetail, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskDetail{}, err
	}
	view, err := session.agent.GetLongTask(workflowID)
	if err != nil {
		return LongTaskDetail{}, err
	}
	return projectLongTaskDetail(view), nil
}

// MintuiCancelLongTask cancels a running LongTask.  The agent
// CancelLongTask signature requires a non-empty nodeID; the
// mintui popup only knows about workflow-level cancellation
// today, so we delegate to CancelLongTaskAll which cancels
// every in-flight node under the workflow.
func (s *Service) MintuiCancelLongTask(ctx context.Context, sessionID, workflowID string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = session.agent.CancelLongTaskAll(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID)
	return err
}

// MintuiListSubagents returns durable subagents for one session
// in the mintui projection (SubagentRow).
func (s *Service) MintuiListSubagents(ctx context.Context, sessionID string) ([]SubagentRow, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	views := session.agent.ListDurableSubagents(sessionID)
	rows := make([]SubagentRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, projectSubagentRow(v))
	}
	return rows, nil
}

// MintuiLookupLongTask looks up commits or stories in a longtask
// and returns the results in the mintui projection.
func (s *Service) MintuiLookupLongTask(ctx context.Context, sessionID, commit, longtaskID string) (LongTaskLookupResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskLookupResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return LongTaskLookupResult{}, err
	}
	defer release()
	entries, err := session.agent.LongTaskLookupByCommit(commit, longtaskID)
	if err != nil {
		return LongTaskLookupResult{Error: err.Error()}, nil
	}
	result := LongTaskLookupResult{}
	for _, e := range entries {
		result.Entries = append(result.Entries, LongTaskLookupEntry{
			LongTaskID: e.LongTaskID,
			StoryID:    e.StoryID,
			Status:     e.CommitHash,
			Title:      e.NodeID,
		})
	}
	return result, nil
}

// MintuiRollbackLongTaskStory rolls back a longtask story and
// returns the result in the mintui projection.
func (s *Service) MintuiRollbackLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID, reason string) (LongTaskRollbackResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	defer release()
	result, err := session.agent.RollbackLongTaskStory(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID, reason)
	if err != nil {
		return LongTaskRollbackResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return LongTaskRollbackResult{}, err
	}
	return projectRollbackResult(result), nil
}

// MintuiGCLongTaskArtifacts sweeps old longtask artifacts and
// returns the result in the mintui projection.
func (s *Service) MintuiGCLongTaskArtifacts(ctx context.Context, sessionID, workflowID string, olderThanSeconds int, apply bool) (LongTaskGCSweepResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return LongTaskGCSweepResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return LongTaskGCSweepResult{}, err
	}
	defer release()
	olderThan := time.Time{}
	if olderThanSeconds > 0 {
		olderThan = s.now().Add(-time.Duration(olderThanSeconds) * time.Second)
	}
	result, err := session.agent.SweepLongTaskArtifacts(workflowID, olderThan, apply)
	if err != nil {
		return LongTaskGCSweepResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return LongTaskGCSweepResult{}, err
	}
	return projectGCSweepResult(result), nil
}

// CreateLongTask creates a durable LongTask and backing workflow.
func (s *Service) CreateLongTask(ctx context.Context, sessionID string, args agent.LongTaskArgs) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.CreateLongTask(sessionID, args)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// RunLongTask drives a durable LongTask.
func (s *Service) RunLongTask(ctx context.Context, sessionID, workflowID string, args agent.LongTaskArgs) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.RunLongTask(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, args)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// CancelLongTask cancels one LongTask workflow node.
func (s *Service) CancelLongTask(ctx context.Context, sessionID, workflowID, nodeID string) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.CancelLongTask(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// CancelLongTaskAll cascades a cancel across every story in a longtask.
// Used by `godex longtask cancel --all` and the matching HTTP body
// `{"cancel_all": true}`.
func (s *Service) CancelLongTaskAll(ctx context.Context, sessionID, workflowID string) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.CancelLongTaskAll(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// FinalizeLongTaskStory validates, merges, and commits one completed LongTask story node.
func (s *Service) FinalizeLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID string) (agent.LongTaskView, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	defer release()
	result, err := session.agent.FinalizeLongTaskStory(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID)
	if err != nil {
		return agent.LongTaskView{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskView{}, err
	}
	return result, nil
}

// LookupLongTask is the commit-hash reverse-lookup entry point.
// The result is a small wrapper that holds the matches and the
// queried commit so the CLI / TUI / Web can render a single
// "this commit came from longtask X, story Y" line.
func (s *Service) LookupLongTask(ctx context.Context, sessionID, commit, longtaskID string) (interface{}, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := session.agent.LongTaskLookupByCommit(commit, longtaskID)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"commit":   commit,
		"longtask": longtaskID,
		"matches":  entries,
	}, nil
}

// RollbackLongTaskStory is the agent-level entry point for
// `godex longtask rollback`. The reason byte cap is enforced at
// the CLI / HTTP boundary AND inside the agent (defense in depth)
// so a misbehaving client cannot bypass the cap by talking
// directly to the HTTP API.
func (s *Service) RollbackLongTaskStory(ctx context.Context, sessionID, workflowID, nodeID, reason string) (agent.LongTaskRollbackResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	defer release()
	result, err := session.agent.RollbackLongTaskStory(agent.WithSubagentEvents(ctx, session.id, "", session.events), workflowID, nodeID, reason)
	if err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskRollbackResult{}, err
	}
	return result, nil
}

// GCLongTaskArtifacts drives the explicit lazy GC for longtask
// run records. olderThanSeconds == 0 means permanent retention
// (T12 default); only an explicit --older-than triggers deletes,
// and --apply is the only path that mutates disk.
func (s *Service) GCLongTaskArtifacts(ctx context.Context, sessionID, workflowID string, olderThanSeconds int, apply bool) (agent.LongTaskGCSweepResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	defer release()
	olderThan := time.Time{}
	if olderThanSeconds > 0 {
		olderThan = s.now().Add(-time.Duration(olderThanSeconds) * time.Second)
	}
	result, err := session.agent.SweepLongTaskArtifacts(workflowID, olderThan, apply)
	if err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	if err := s.persistSession(session, s.now()); err != nil {
		return agent.LongTaskGCSweepResult{}, err
	}
	return result, nil
}

// PendingPermissions returns pending approval requests for one session.
func (s *Service) PendingPermissions(ctx context.Context, sessionID string) ([]tools.PendingPermission, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.PendingPermissions(sessionID), nil
}

// ApprovePermission resolves one pending permission request.
func (s *Service) ApprovePermission(ctx context.Context, sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	release, lockedHere, err := s.acquireSessionIfNeeded(ctx, sessionID, session)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	if release != nil {
		defer release()
	}
	resolution, err := session.agent.ApprovePendingPermission(sessionID, requestID, scope)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	now := s.now()
	resolvedRequestID := strings.TrimSpace(resolution.RequestID)
	if resolvedRequestID == "" {
		resolvedRequestID = strings.TrimSpace(requestID)
	}
	session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusApproved, now)
	s.appendPermissionAuditEvent("approve_permission", "info", sessionID, resolution)
	if pending := session.agent.PendingResumeState(); pending != nil && strings.TrimSpace(pending.RequestID) == resolvedRequestID {
		beforeMessages := session.agent.GetMessages()
		beforeCount := len(beforeMessages)
		resumeStart := beforeCount
		for i := beforeCount - 1; i >= 0; i-- {
			if beforeMessages[i].Role == protocol.RoleAssistant {
				resumeStart = i
				break
			}
		}
		resumeTurnID := session.nextTurnID(s.now())
		resolution.ResumeTurnID = resumeTurnID
		resumeResult, resumeErr := s.resumePendingTurnLocked(ctx, session, requestID, resumeTurnID)
		resolution.Resumed = true
		if resumeResult != nil {
			resolution.ResumeStatus = strings.TrimSpace(resumeResult.Status)
			resolution.ResumePendingRequestID = strings.TrimSpace(resumeResult.PendingRequestID)
		}
		if output := strings.TrimSpace(assistantTextSince(session.agent.GetMessages(), resumeStart)); output != "" {
			resolution.ResumeOutput = output
		}
		if resumeErr != nil {
			resolution.ResumeStatus = "error"
			resolution.ResumeError = resumeErr.Error()
		} else {
			resolution.Status = tools.PermissionStatusResumed
			session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusResumed, s.now())
		}
	} else if jobID := subagentJobIDFromPermissionRequest(resolution.Request); jobID != "" {
		resolution.Resumed = true
		view, resumeErr := session.agent.ResumeDurableSubagentViewWithContext(agent.WithSubagentEvents(ctx, session.id, "", session.events), sessionID, jobID)
		if resumeErr != nil {
			resolution.ResumeStatus = "error"
			resolution.ResumeError = resumeErr.Error()
		} else {
			resolution.Status = tools.PermissionStatusResumed
			session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusResumed, s.now())
			if status := strings.TrimSpace(view.Status); status != "" {
				resolution.ResumeStatus = "subagent_" + status
			} else {
				resolution.ResumeStatus = "subagent_resumed"
			}
		}
	} else if lockedHere {
		if err := s.touchSession(session, s.now()); err != nil {
			return tools.PermissionResolution{}, err
		}
	}
	if lockedHere {
		_ = s.writeSessionTurns(session)
	}
	return resolution, nil
}

func subagentJobIDFromPermissionRequest(req tools.PermissionRequest) string {
	sender := strings.TrimSpace(req.Sender)
	if !strings.HasPrefix(sender, "subagent:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(sender, "subagent:"))
}

// DenyPermission resolves one pending permission request with denial.
func (s *Service) DenyPermission(ctx context.Context, sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	release, lockedHere, err := s.acquireSessionIfNeeded(ctx, sessionID, session)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	if release != nil {
		defer release()
	}
	resolution, err := session.agent.DenyPendingPermission(sessionID, requestID, reason)
	if err != nil {
		return tools.PermissionResolution{}, err
	}
	s.appendPermissionAuditEvent("deny_permission", "warning", sessionID, resolution)
	now := s.now()
	resolvedRequestID := strings.TrimSpace(resolution.RequestID)
	if resolvedRequestID == "" {
		resolvedRequestID = strings.TrimSpace(requestID)
	}
	session.updateTurnPermissionStatus(resolvedRequestID, tools.PermissionStatusDenied, now)
	if pending := session.agent.PendingResumeState(); pending != nil && strings.TrimSpace(pending.RequestID) == resolvedRequestID {
		session.agent.AppendRuntimeFeedback("The previously blocked tool permission was denied. Do not retry that blocked tool call. Explain the denial and continue with a safer alternative if possible.")
		session.agent.ClearPendingResume()
	}
	_ = s.writeSessionTurns(session)
	if lockedHere {
		if err := s.touchSession(session, now); err != nil {
			return tools.PermissionResolution{}, err
		}
		return resolution, nil
	}
	return resolution, nil
}

// ClearMessages clears the current session conversation history and persists the result.
func (s *Service) ClearMessages(ctx context.Context, sessionID string) error {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	session.agent.ClearMessages()
	session.clearQueue()
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	if queueErr := s.writeSessionQueue(session); persistErr == nil && queueErr != nil {
		persistErr = queueErr
	}
	release()
	released = true
	if persistErr != nil {
		return persistErr
	}

	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   false,
		},
	})
	_ = s.writeSessionTimeline(session)
	return nil
}

// ListSessionSkills returns the discoverable skill catalog for the session workspace.
func (s *Service) ListSessionSkills(ctx context.Context, sessionID string) ([]skill.CatalogEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListSkills()
}

// ListSessionSkillSources returns curated install sources for the session workspace.
func (s *Service) ListSessionSkillSources(ctx context.Context, sessionID string) ([]tools.SkillSourceEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ListSkillSources()
}

// SearchSessionSkillSources returns curated install sources plus search-backed marketplace results.
func (s *Service) SearchSessionSkillSources(ctx context.Context, sessionID, query string) ([]tools.SkillSourceEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.SearchSkillSources(query)
}

// ListTrendingSessionSkillSources returns popular skills.sh entries for the session workspace.
func (s *Service) ListTrendingSessionSkillSources(ctx context.Context, sessionID string) ([]tools.SkillSourceEntry, error) {
	_ = ctx
	if _, err := s.requireSession(sessionID); err != nil {
		return nil, err
	}
	items, err := skill.TrendingSourceCatalog(s.cfg.WorkspaceDir, s.cfg.SkillsDir)
	if err != nil {
		return nil, err
	}
	result := make([]tools.SkillSourceEntry, 0, len(items))
	for _, item := range items {
		result = append(result, tools.SkillSourceEntry{
			ID:               item.ID,
			Name:             item.Name,
			Summary:          item.Summary,
			Source:           item.Source,
			SkillName:        item.SkillName,
			Tags:             append([]string{}, item.Tags...),
			Categories:       append([]string{}, item.Categories...),
			Version:          item.Version,
			Trust:            item.Trust,
			Origin:           item.Origin,
			Installs:         item.Installs,
			Warnings:         append([]string{}, item.Warnings...),
			InstallSupported: item.InstallSupported,
			InstallSource:    item.InstallSource,
			InstallName:      item.InstallName,
			InstallReason:    item.InstallReason,
			Installed:        item.Installed,
			InstalledPath:    item.InstalledPath,
			InstallMemory:    cloneInstallMemory(item.InstallMemory),
		})
	}
	return result, nil
}

// GetSessionSkill returns one discoverable skill's lightweight metadata.
func (s *Service) GetSessionSkill(ctx context.Context, sessionID, name string) (skill.CatalogEntry, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	return session.agent.GetSkill(name)
}

// ActiveSessionSkills returns the currently active skills for a session.
func (s *Service) ActiveSessionSkills(ctx context.Context, sessionID string) ([]tools.SkillActivation, error) {
	_ = ctx
	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	return session.agent.ActiveSkills()
}

// InstallSessionSkill installs a new skill source into the session workspace skills directory.
func (s *Service) InstallSessionSkill(ctx context.Context, sessionID, source, name string) (tools.SkillInstallResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillInstallResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillInstallResult{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.InstallSkill(source, name)
	updatedAt := s.now()
	release()
	released = true
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "installed",
				ID:                 result.ID,
				Name:               result.Name,
				Source:             result.Source,
				Sections:           append([]string{}, result.Sections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// NormalizeSessionSkill explicitly runs LLM-backed normalization for one installed skill.
func (s *Service) NormalizeSessionSkill(ctx context.Context, sessionID, name string) (skill.CatalogEntry, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	defer release()

	item, err := session.agent.NormalizeSkill(ctx, name)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	now := s.now()
	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSkillStateChanged,
		Timestamp: now,
		Payload: events.SkillPayload{
			Action:             "normalized",
			ID:                 item.ID,
			Name:               item.Name,
			Sections:           append([]string{}, item.Sections...),
			RecommendedBundles: append([]string{}, item.RecommendedBundles...),
		},
	})
	s.emitSkillRefresh(session, now)
	return item, nil
}

// RemoveSessionSkill deletes an installed skill source and persists the updated active stack.
func (s *Service) RemoveSessionSkill(ctx context.Context, sessionID, name string) (tools.SkillRemoveResult, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillRemoveResult{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillRemoveResult{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.RemoveSkill(name)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action: "removed",
				ID:     result.ID,
				Name:   result.Name,
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// ActivateSessionSkill loads a skill core into the session and persists the updated state.
func (s *Service) ActivateSessionSkill(ctx context.Context, sessionID, name string) (tools.SkillActivation, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.ActivateSkill(name)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "activated",
				ID:                 result.ID,
				Name:               result.Name,
				Sections:           append([]string{}, result.LoadedSections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// ExpandSessionSkill loads additional skill sections into the session and persists the updated state.
func (s *Service) ExpandSessionSkill(ctx context.Context, sessionID, name string, sections []string) (tools.SkillExpansion, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillExpansion{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillExpansion{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.ExpandSkill(name, sections)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "expanded",
				ID:                 result.ID,
				Name:               result.Name,
				Sections:           append([]string{}, result.ExpandedSections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// UnloadSessionSkill removes an active skill from the session and persists the updated state.
func (s *Service) UnloadSessionSkill(ctx context.Context, sessionID, name string) (tools.SkillActivation, error) {
	session, err := s.requireSession(sessionID)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	result, err := session.agent.UnloadSkill(name)
	updatedAt := s.now()
	persistErr := s.persistSession(session, updatedAt)
	release()
	released = true
	if persistErr != nil && err == nil {
		err = persistErr
	}
	if err == nil {
		session.events.Emit(events.Event{
			SessionID: session.id,
			Type:      events.EventSkillStateChanged,
			Timestamp: updatedAt,
			Payload: events.SkillPayload{
				Action:             "unloaded",
				ID:                 result.ID,
				Name:               result.Name,
				Sections:           append([]string{}, result.LoadedSections...),
				RecommendedBundles: append([]string{}, result.RecommendedBundles...),
			},
		})
		s.emitSkillRefresh(session, updatedAt)
	}
	return result, err
}

// wireSlashCommandHandlers installs session management slash-command handlers
// so the commands service can delegate /new and /resume to the backend.
func (s *Service) wireSlashCommandHandlers() {
	if s.commands == nil {
		return
	}
	s.commands.SetNewSession(func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		locator, err := s.CreateNewSession(ctx)
		if err != nil {
			return commands.Result{}, err
		}

		projectDir := ""
		if s.cfg != nil {
			projectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
			if projectDir == "" {
				projectDir = strings.TrimSpace(s.cfg.ProjectDir)
			}
		}

		output := fmt.Sprintf("✓ New session created.\n\nSession: %s:%s", locator.Channel, locator.Key)
		if projectDir != "" {
			output += fmt.Sprintf("\nWorkspace: %s", projectDir)
		}
		output += "\n\nSwitched to the new session. Next time you run godex in this directory, it will open this session."

		return commands.Result{
			Name:   "new",
			Output: output,
		}, nil
	})

	s.commands.SetResumeSession(func(ctx context.Context, a *agent.Agent, cmd commands.Command) (commands.Result, error) {
		allSessions, err := s.ListSessions(ctx, SessionListFilter{})
		if err != nil {
			return commands.Result{}, err
		}

		// If args are provided, filter by session ID or name/key
		query := strings.TrimSpace(strings.Join(cmd.Args, " "))
		if query != "" {
			var matched []ListedSession
			queryLower := strings.ToLower(query)
			for _, session := range allSessions {
				if strings.HasPrefix(strings.ToLower(session.SessionID), queryLower) ||
					strings.EqualFold(session.Locator.Key, query) ||
					strings.Contains(strings.ToLower(session.Title), queryLower) {
					matched = append(matched, session)
				}
			}
			if len(matched) == 0 {
				return commands.Result{Name: "resume", Output: fmt.Sprintf("No session found matching %q.", query)}, nil
			}
			var lines []string
			lines = append(lines, fmt.Sprintf("Sessions matching %q:", query))
			for _, session := range matched {
				lines = append(lines, formatSessionLine(session))
			}
			lines = append(lines, "", "To resume a session, restart godex with: godex tui --session <channel:key>")
			return commands.Result{
				Name:   "resume",
				Output: strings.Join(lines, "\n"),
			}, nil
		}

		currentProjectDir := ""
		if s.cfg != nil {
			currentProjectDir = strings.TrimSpace(s.cfg.WorkspaceDir)
			if currentProjectDir == "" {
				currentProjectDir = strings.TrimSpace(s.cfg.ProjectDir)
			}
		}
		currentProjectDir = cleanProjectDir(currentProjectDir)

		var current, others []ListedSession
		for _, session := range allSessions {
			sessionProjectDir := ""
			if session.Locator.Metadata != nil {
				sessionProjectDir = cleanProjectDir(session.Locator.Metadata[sessionProjectDirMetadataKey])
			}
			if currentProjectDir != "" && sessionProjectDir == currentProjectDir {
				current = append(current, session)
			} else {
				others = append(others, session)
			}
		}

		var lines []string
		if len(current) > 0 {
			lines = append(lines, fmt.Sprintf("Sessions for %s:", currentProjectDir))
			for _, session := range current {
				lines = append(lines, formatSessionLine(session))
			}
		}
		if len(others) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "Other sessions:")
			for _, session := range others {
				lines = append(lines, formatSessionLine(session))
			}
		}
		if len(lines) == 0 {
			return commands.Result{Name: "resume", Output: "No saved sessions found."}, nil
		}
		lines = append(lines, "", "To resume a session, restart godex with: godex tui --session <channel:key>")

		return commands.Result{
			Name:   "resume",
			Output: strings.Join(lines, "\n"),
		}, nil
	})
}

// formatSessionLine renders one listed session as: name · date · ID · working-dir.
func formatSessionLine(session ListedSession) string {
	name := strings.TrimSpace(session.Title)
	if name == "" || name == "-" {
		name = fmt.Sprintf("%s:%s", session.Locator.Channel, session.Locator.Key)
	}
	line := fmt.Sprintf("- %s", name)

	if !session.LastActivityAt.IsZero() {
		line += fmt.Sprintf(" · %s", session.LastActivityAt.Format("2006-01-02 15:04"))
	}

	line += fmt.Sprintf(" · %s:%s", session.Locator.Channel, session.Locator.Key)

	if session.Locator.Metadata != nil {
		if projectDir := strings.TrimSpace(session.Locator.Metadata[sessionProjectDirMetadataKey]); projectDir != "" {
			line += fmt.Sprintf(" · %s", truncatePathTail(projectDir, 40))
		}
	}

	if session.Running {
		line += " [running]"
	}
	return line
}

// truncatePathTail keeps only the last maxLen characters of a path, adding
// "..." prefix when truncation occurs. Useful for dense directory display.
func truncatePathTail(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen:]
}

// ListSessions returns persisted sessions ordered by most recent update first.
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
		manifest, state, err := s.readSessionListFiles(sessionID)
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
		if title == "" {
			if state == nil {
				return nil, newSessionCorruptError(sessionID, "missing %s while backfilling title", stateFileName)
			}
			title = deriveSessionTitle(*state)
			if title != "" {
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

	a := agent.NewForSession(s.cfg, s.shared, sessionID)
	a.RegisterTools()
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
		a.LoadDefaultSkills()
	}
	session.agent = a

	persistSession := false
	if strings.TrimSpace(session.title) == "" && state != nil {
		session.title = deriveSessionTitle(*state)
		persistSession = true
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
	return s.persistSession(session, now)
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

func (s *Service) persistSession(session *sessionState, updatedAt time.Time) error {
	state := session.agent.ExportStateForSession(session.id)

	session.mu.Lock()
	session.updatedAt = updatedAt
	session.lastActive = updatedAt
	modelProfileID := strings.TrimSpace(session.modelProfileID)
	reasoningEffort := normalizeSessionReasoningEffort(session.reasoningEffort)
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
	pendingPermissions := session.agent.PendingPermissions(session.id)
	turns := session.snapshotTurnRecords(snapshotTurnLimit)
	return Snapshot{
		SessionID:               session.id,
		Locator:                 locator,
		Messages:                modelMessages,
		DisplayMessages:         s.displayMessages(modelMessages),
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

func (s *Service) displayMessages(messages []protocol.Message) []protocol.Message {
	expanded := s.expandSummaryMessages(messages, map[string]struct{}{})
	if len(expanded) == 0 {
		return nil
	}
	return expanded
}

func activePermissionBlocker(pending []tools.PendingPermission, turns []TurnRecord, now time.Time) *PermissionBlocker {
	// Only consider permissions that are still actually pending (not approved/denied/expired).
	var stillPending []tools.PendingPermission
	for _, p := range pending {
		if p.Status == "" || p.Status == tools.PermissionStatusPending {
			stillPending = append(stillPending, p)
		}
	}
	if len(stillPending) == 0 {
		return nil
	}
	item := stillPending[0]
	for _, candidate := range stillPending {
		if candidate.CreatedAt.Before(item.CreatedAt) {
			item = candidate
		}
	}
	turnID := ""
	for i := len(turns) - 1; i >= 0; i-- {
		if strings.TrimSpace(turns[i].BlockedByPermissionID) == strings.TrimSpace(item.ID) || strings.TrimSpace(turns[i].PendingRequestID) == strings.TrimSpace(item.ID) {
			turnID = strings.TrimSpace(turns[i].ID)
			break
		}
	}
	status := item.Status
	if status == "" {
		status = tools.PermissionStatusPending
	}
	return &PermissionBlocker{
		RequestID: strings.TrimSpace(item.ID),
		Status:    status,
		TurnID:    turnID,
		Intent:    strings.TrimSpace(tools.PermissionIntentSummary(item)),
		Risk:      strings.TrimSpace(tools.PermissionRiskSummary(item.Request)),
		Expiry:    strings.TrimSpace(tools.PermissionExpirySummary(item, now)),
		ToolName:  strings.TrimSpace(item.Request.ToolName),
		Action:    strings.TrimSpace(item.Request.Action),
		Command:   strings.TrimSpace(item.Request.Command),
		Paths:     append([]string{}, item.Request.Paths...),
		Source:    strings.TrimSpace(item.Request.Source),
		Sender:    strings.TrimSpace(item.Request.Sender),
		CreatedAt: item.CreatedAt,
		ExpiresAt: item.ExpiresAt,
	}
}

func (s *Service) expandSummaryMessages(messages []protocol.Message, seen map[string]struct{}) []protocol.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]protocol.Message, 0, len(messages))
	for index, msg := range messages {
		if msg.Metadata != nil && msg.Metadata.Kind == protocol.KindSummary && strings.TrimSpace(msg.Metadata.Transcript) != "" {
			transcriptMessages, ok := s.readTranscriptMessages(msg.Metadata.Transcript, seen)
			if ok {
				out = appendMergedMessages(out, s.expandSummaryMessages(transcriptMessages, seen)...)
				return appendMergedMessages(out, s.expandSummaryMessages(messages[index+1:], seen)...)
			}
		}
		out = appendMergedMessages(out, msg)
	}
	return out
}

func (s *Service) readTranscriptMessages(ref string, seen map[string]struct{}) ([]protocol.Message, bool) {
	name := filepath.Base(strings.TrimSpace(ref))
	if name == "." || name == "" || name != strings.TrimSpace(ref) || strings.TrimSpace(s.cfg.TranscriptsDir) == "" {
		return nil, false
	}
	if _, ok := seen[name]; ok {
		return nil, false
	}
	seen[name] = struct{}{}
	data, err := os.ReadFile(filepath.Join(s.cfg.TranscriptsDir, name))
	if err != nil {
		return nil, false
	}
	var messages []protocol.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, false
	}
	return messages, true
}

func appendMergedMessages(existing []protocol.Message, incoming ...protocol.Message) []protocol.Message {
	if len(incoming) == 0 {
		return existing
	}
	overlap := messageOverlap(existing, incoming)
	return append(existing, protocol.CloneMessages(incoming[overlap:])...)
}

func messageOverlap(left, right []protocol.Message) int {
	maxOverlap := len(left)
	if len(right) < maxOverlap {
		maxOverlap = len(right)
	}
	for n := maxOverlap; n > 0; n-- {
		matched := true
		for i := 0; i < n; i++ {
			if !sameProtocolMessage(left[len(left)-n+i], right[i]) {
				matched = false
				break
			}
		}
		if matched {
			return n
		}
	}
	return 0
}

func sameProtocolMessage(left, right protocol.Message) bool {
	if left.Role != right.Role || protocol.MessageText(left) != protocol.MessageText(right) {
		return false
	}
	leftMeta, rightMeta := left.Metadata, right.Metadata
	if leftMeta == nil || rightMeta == nil {
		return leftMeta == nil && rightMeta == nil
	}
	if leftMeta.Kind != rightMeta.Kind ||
		leftMeta.Source != rightMeta.Source ||
		leftMeta.Sender != rightMeta.Sender ||
		leftMeta.Timestamp != rightMeta.Timestamp ||
		leftMeta.Transcript != rightMeta.Transcript {
		return false
	}
	leftAttachments, leftErr := json.Marshal(leftMeta.Attachments)
	rightAttachments, rightErr := json.Marshal(rightMeta.Attachments)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftAttachments, rightAttachments)
}

func (s *Service) mainCapabilitySummary() []string {
	// Session-level identity uses a compact summary; detailed execution policy
	// remains enforced by the existing tool permission manager.
	return []string{"tool:*", "file:read:*", "file:write:workspace", "shell:approval_policy", "network:configured_tools"}
}

func cloneMapStringString(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (s *Service) touchSession(session *sessionState, updatedAt time.Time) error {
	session.mu.Lock()
	session.updatedAt = updatedAt
	session.lastActive = updatedAt
	running := session.running
	session.mu.Unlock()

	if err := s.persistSession(session, updatedAt); err != nil {
		return err
	}

	session.events.Emit(events.Event{
		SessionID: session.id,
		Type:      events.EventSnapshotReady,
		Timestamp: updatedAt,
		Payload: events.SnapshotPayload{
			UpdatedAt: updatedAt,
			Running:   running,
		},
	})
	_ = s.writeSessionTimeline(session)
	return nil
}

func (s *Service) describeSession(session *sessionState) *OpenedSession {
	session.mu.RLock()
	defer session.mu.RUnlock()

	return &OpenedSession{
		SessionID:              session.id,
		Locator:                session.locator,
		ModelProfileID:         strings.TrimSpace(session.modelProfileID),
		ReasoningEffort:        normalizeSessionReasoningEffort(session.reasoningEffort),
		ParentSessionID:        strings.TrimSpace(session.parentSessionID),
		ForkedFromTurnID:       strings.TrimSpace(session.forkedFromTurnID),
		ForkedFromMessageIndex: cloneIntPtr(session.forkedFromMessageIndex),
		BranchTitle:            strings.TrimSpace(session.branchTitle),
		CreatedAt:              session.createdAt,
		UpdatedAt:              session.updatedAt,
	}
}

func (s *sessionState) setTitleIfEmpty(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.title) != "" {
		return
	}
	s.title = title
}

// maybeGenerateTitleAsync fires an async LLM call to generate a better title
// when the first user message is received. On success the session title and
// manifest are updated. Best-effort: failures and panics are silently ignored.
func (s *Service) maybeGenerateTitleAsync(session *sessionState, envelope message.Envelope) {
	if session == nil || session.agent == nil {
		return
	}
	firstMessage := strings.TrimSpace(envelope.BodyText())
	if firstMessage == "" {
		return
	}
	go func() {
		defer func() { recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Additional recover because client.Call may panic in test stubs
		var title string
		func() {
			defer func() { recover() }()
			t, err := session.agent.GenerateTitle(ctx, firstMessage)
			if err == nil {
				title = t
			}
		}()
		if title == "" {
			return
		}
		session.mu.Lock()
		session.title = title
		session.mu.Unlock()
		s.writeManifestForSession(session)
	}()
}

func (s *Service) writeManifestForSession(session *sessionState) {
	if session == nil || s == nil {
		return
	}
	session.mu.RLock()
	manifest := SessionManifest{
		SessionID:              session.id,
		Locator:                session.locator,
		Identity:               session.identity,
		Title:                  session.title,
		ModelProfileID:         session.modelProfileID,
		ReasoningEffort:        session.reasoningEffort,
		ParentSessionID:        session.parentSessionID,
		ForkedFromTurnID:       session.forkedFromTurnID,
		ForkedFromMessageIndex: session.forkedFromMessageIndex,
		BranchTitle:            session.branchTitle,
		StateDigest:            "",
		CreatedAt:              session.createdAt,
		UpdatedAt:              session.updatedAt,
		LastActivityAt:         session.lastActive,
	}
	session.mu.RUnlock()
	_ = s.writeManifest(manifest)
}

func (s *Service) sessionDir(sessionID string) string {
	return filepath.Join(s.cfg.SessionsDir, sessionID)
}

func (s *Service) sessionAttachmentsDir(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), attachmentsDir)
}

func (s *Service) deleteUniqueTranscriptRefs(sessionID string, refs []string) error {
	if len(refs) == 0 || strings.TrimSpace(s.cfg.TranscriptsDir) == "" {
		return nil
	}
	others := s.collectTranscriptRefs(sessionID)
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, shared := others[ref]; shared {
			continue
		}
		name := filepath.Base(ref)
		if name == "." || name == "" || name != ref {
			continue
		}
		path := filepath.Join(s.cfg.TranscriptsDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Service) collectTranscriptRefs(excludeSessionID string) map[string]struct{} {
	refs := make(map[string]struct{})

	s.mu.Lock()
	for id, session := range s.sessions {
		if id == excludeSessionID || session == nil {
			continue
		}
		for _, ref := range session.agent.TranscriptRefs() {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			refs[ref] = struct{}{}
		}
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(s.cfg.SessionsDir)
	if err != nil {
		return refs
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == excludeSessionID {
			continue
		}
		state, err := s.readSessionState(entry.Name())
		if err != nil {
			continue
		}
		for _, ref := range sessionTranscriptRefs(state) {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			refs[ref] = struct{}{}
		}
	}
	return refs
}

func sessionTranscriptRefs(state *agent.SessionState) []string {
	if state == nil {
		return nil
	}
	return stringutil.Unique(append([]string{}, state.TranscriptRefs...))
}

func cloneInstallMemory(memory *skill.InstallMemory) *skill.InstallMemory {
	if memory == nil {
		return nil
	}
	cloned := *memory
	cloned.Categories = append([]string{}, memory.Categories...)
	return &cloned
}

func (s *Service) relativePath(path string) string {
	if rel, err := filepath.Rel(s.cfg.WorkspaceDir, path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func (s *sessionState) acquire(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.gate:
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.gate <- struct{}{}
	}, nil
}

func (s *sessionState) tryAcquire() (func(), bool) {
	select {
	case <-s.gate:
	default:
		return nil, false
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		s.gate <- struct{}{}
	}, true
}

func (s *sessionState) setActiveTurn(turnID string, cancel context.CancelCauseFunc, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = &activeTurn{id: turnID, cancel: cancel, startedAt: startedAt}
}

func (s *sessionState) activeTurnID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return ""
	}
	return s.active.id
}

func (s *sessionState) updateActivePhase(turnID, phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || strings.TrimSpace(s.active.id) != strings.TrimSpace(turnID) {
		return
	}
	s.active.phase = strings.TrimSpace(phase)
}

func (s *sessionState) clearActiveTurn(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.id != turnID {
		return
	}
	s.active = nil
}

func (s *sessionState) cancelActiveTurn(turnID string) (string, bool) {
	s.mu.RLock()
	active := s.active
	if active == nil || (strings.TrimSpace(turnID) != "" && active.id != strings.TrimSpace(turnID)) {
		s.mu.RUnlock()
		return "", false
	}
	activeTurnID := active.id
	cancel := active.cancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel(ErrTurnCanceled)
	}
	return activeTurnID, true
}

func (s *sessionState) seedTurns(records []TurnRecord) {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	s.turns = normalizeTurnRecords(records)
}

func (s *sessionState) seedQueue(items []QueuedTurn) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.queue = normalizeQueuedTurns(items)
}

func (s *sessionState) clearQueue() {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.queue = nil
}

func (s *sessionState) enqueue(item QueuedTurn) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.queue = append(s.queue, normalizeQueuedTurn(item))
}

func (s *sessionState) peekQueued() (QueuedTurn, bool) {
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	if len(s.queue) == 0 {
		return QueuedTurn{}, false
	}
	return cloneQueuedTurn(s.queue[0]), true
}

func (s *sessionState) dropQueued(id string) bool {
	id = strings.TrimSpace(id)
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if len(s.queue) == 0 || strings.TrimSpace(s.queue[0].ID) != id {
		return false
	}
	s.queue = append([]QueuedTurn{}, s.queue[1:]...)
	return true
}

func (s *sessionState) recordTurnStarted(turnID string, envelope message.Envelope, priorMessageCount int, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	normalized := envelope.Normalized()
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	record := TurnRecord{
		ID:                turnID,
		Status:            "running",
		Source:            string(normalized.Source),
		Sender:            strings.TrimSpace(normalized.Sender),
		Summary:           turnSummary(normalized.BodyText()),
		StartedAt:         now,
		UpdatedAt:         now,
		PriorMessageCount: priorMessageCount,
		Envelope:          &normalized,
	}
	if idx >= 0 {
		existing := s.turns[idx]
		if !existing.StartedAt.IsZero() {
			record.StartedAt = existing.StartedAt
		}
		s.turns[idx] = mergeTurnRecord(existing, record)
	} else {
		s.turns = append(s.turns, record)
	}
	s.turns = trimTurnRecords(s.turns, persistedTurnLimit)
}

func (s *sessionState) updateTurnStatus(turnID, status, pendingRequestID, errorText string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "unknown"
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	record := TurnRecord{ID: turnID, StartedAt: now}
	if idx >= 0 {
		record = s.turns[idx]
	}
	record.Status = status
	record.UpdatedAt = now
	record.PendingRequestID = strings.TrimSpace(pendingRequestID)
	if record.PendingRequestID != "" {
		record.BlockedByPermissionID = record.PendingRequestID
		record.PermissionStatus = tools.PermissionStatusPending
	}
	record.Error = strings.TrimSpace(errorText)
	if isTerminalTurnStatus(status) {
		completedAt := now
		record.CompletedAt = &completedAt
	} else {
		record.CompletedAt = nil
	}
	if idx >= 0 {
		s.turns[idx] = record
	} else {
		s.turns = append(s.turns, record)
	}
	s.turns = trimTurnRecords(s.turns, persistedTurnLimit)
}

func (s *sessionState) updateTurnPermissionStatus(requestID string, status tools.PermissionStatus, now time.Time) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || status == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	for idx := range s.turns {
		if strings.TrimSpace(s.turns[idx].BlockedByPermissionID) != requestID && strings.TrimSpace(s.turns[idx].PendingRequestID) != requestID {
			continue
		}
		s.turns[idx].BlockedByPermissionID = requestID
		s.turns[idx].PermissionStatus = status
		s.turns[idx].UpdatedAt = now
	}
}

func (s *sessionState) markTurnResumeAvailable(turnID, hint string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return
	}
	s.turns[idx].ResumeAvailable = true
	s.turns[idx].RecoveryHint = strings.TrimSpace(hint)
	if now.After(s.turns[idx].UpdatedAt) {
		s.turns[idx].UpdatedAt = now
	}
}

func (s *sessionState) markTurnRetry(turnID, retryOf string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	retryOf = strings.TrimSpace(retryOf)
	if turnID == "" || retryOf == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return
	}
	s.turns[idx].RetryOf = retryOf
	if now.After(s.turns[idx].UpdatedAt) {
		s.turns[idx].UpdatedAt = now
	}
}

func (s *sessionState) updateTurnPhase(turnID, phase, recoveryHint, lastToolName string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return
	}
	record := s.turns[idx]
	if strings.TrimSpace(phase) != "" {
		record.Phase = strings.TrimSpace(phase)
	}
	if strings.TrimSpace(recoveryHint) != "" {
		record.RecoveryHint = strings.TrimSpace(recoveryHint)
	}
	if strings.TrimSpace(lastToolName) != "" {
		record.LastToolName = strings.TrimSpace(lastToolName)
	}
	if now.After(record.UpdatedAt) {
		record.UpdatedAt = now
	}
	s.turns[idx] = record
}

func (s *sessionState) addTurnInjection(turnID string, item QueuedTurn, now time.Time) int {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return 0
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return 0
	}
	normalized := normalizeQueuedTurn(item)
	s.turns[idx].Injections = append(s.turns[idx].Injections, normalized.Envelope.Normalized())
	s.turns[idx].InjectionCount++
	if now.After(s.turns[idx].UpdatedAt) {
		s.turns[idx].UpdatedAt = now
	}
	return len(s.turns[idx].Injections)
}

func (s *sessionState) drainTurnInjections(turnID string, limit int, now time.Time) []message.Envelope {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || limit <= 0 {
		return nil
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 || len(s.turns[idx].Injections) == 0 {
		return nil
	}
	items := append([]message.Envelope{}, s.turns[idx].Injections...)
	sort.SliceStable(items, func(i, j int) bool {
		return strings.EqualFold(items[i].Metadata["queue_mode"], string(QueueModeSteering)) && !strings.EqualFold(items[j].Metadata["queue_mode"], string(QueueModeSteering))
	})
	if len(items) > limit {
		drained := append([]message.Envelope{}, items[:limit]...)
		s.turns[idx].Injections = append([]message.Envelope{}, items[limit:]...)
		s.turns[idx].UpdatedAt = now
		return drained
	}
	s.turns[idx].Injections = nil
	s.turns[idx].UpdatedAt = now
	return items
}

func (s *sessionState) pendingTurnInjections(turnID string) []message.Envelope {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return nil
	}
	return append([]message.Envelope{}, s.turns[idx].Injections...)
}

func (s *sessionState) promoteTurnInjectionsToQueue(turnID string, now time.Time) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	var pending []message.Envelope
	s.turnsMu.Lock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx >= 0 && len(s.turns[idx].Injections) > 0 {
		pending = append([]message.Envelope{}, s.turns[idx].Injections...)
		s.turns[idx].Injections = nil
		s.turns[idx].UpdatedAt = now
	}
	s.turnsMu.Unlock()
	for _, envelope := range pending {
		mode := QueueModeFollowUp
		if envelope.Metadata != nil && strings.EqualFold(envelope.Metadata["queue_mode"], string(QueueModeSteering)) {
			mode = QueueModeSteering
		}
		s.enqueue(QueuedTurn{
			ID:        s.nextTurnID(now),
			Mode:      mode,
			Status:    "queued",
			Source:    string(envelope.Source),
			Sender:    strings.TrimSpace(envelope.Sender),
			Summary:   turnSummary(envelope.BodyText()),
			CreatedAt: now,
			UpdatedAt: now,
			Envelope:  envelope.Normalized(),
		})
	}
}

func (s *sessionState) retryableTurnRecord(turnID string) (TurnRecord, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	if idx != len(s.turns)-1 {
		return TurnRecord{}, newTurnNotRetryableError(turnID, "only the latest turn can be retried")
	}
	record := cloneTurnRecord(s.turns[idx])
	if !canRetryTurnStatus(record.Status) {
		return TurnRecord{}, newTurnNotRetryableError(turnID, fmt.Sprintf("status %q cannot be retried", record.Status))
	}
	if record.Envelope == nil {
		return TurnRecord{}, newTurnNotRetryableError(turnID, "original input was not persisted")
	}
	return record, nil
}

func (s *sessionState) resumableTurnRecord(turnID string) (TurnRecord, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	idx := turnRecordIndex(s.turns, turnID)
	if idx < 0 {
		return TurnRecord{}, newTurnNotFoundError(turnID)
	}
	if idx != len(s.turns)-1 {
		return TurnRecord{}, newTurnNotResumableError(turnID, "only the latest turn can be resumed")
	}
	record := cloneTurnRecord(s.turns[idx])
	if !canResumeTurnStatus(record.Status) {
		return TurnRecord{}, newTurnNotResumableError(turnID, fmt.Sprintf("status %q cannot be resumed", record.Status))
	}
	if record.Envelope == nil {
		return TurnRecord{}, newTurnNotResumableError(turnID, "original input was not persisted")
	}
	return record, nil
}

func (s *sessionState) turnRecords(limit int) []TurnRecord {
	if s == nil {
		return nil
	}
	s.turnsMu.RLock()
	defer s.turnsMu.RUnlock()
	records := cloneTurnRecords(s.turns)
	if limit <= 0 || len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}

func (s *sessionState) queuedTurns(limit int) []QueuedTurn {
	if s == nil {
		return nil
	}
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	items := cloneQueuedTurns(s.queue)
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func (s *sessionState) hasQueuedRecoveryFor(turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return false
	}
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	for _, item := range s.queue {
		if item.Envelope.Metadata["recovery_of_turn_id"] == turnID {
			return true
		}
	}
	return false
}

func (s *sessionState) snapshotQueuedTurns(limit int) []QueuedTurn {
	items := s.queuedTurns(limit)
	for i := range items {
		items[i].Envelope = message.Envelope{}
	}
	return items
}

func (s *sessionState) snapshotTurnRecords(limit int) []TurnRecord {
	records := s.turnRecords(limit)
	for i := range records {
		records[i].CanRetry = false
		records[i].CanResume = false
		if i == len(records)-1 && canRetryTurnStatus(records[i].Status) && records[i].Envelope != nil {
			records[i].CanRetry = true
		}
		if i == len(records)-1 && canResumeTurnStatus(records[i].Status) && records[i].Envelope != nil {
			records[i].CanResume = true
		}
		records[i].PriorMessageCount = 0
		records[i].Envelope = nil
	}
	return records
}

func (s *sessionState) interruptedTurnIDFromRecords() string {
	records := s.turnRecords(0)
	for i := len(records) - 1; i >= 0; i-- {
		switch records[i].Status {
		case "running", "canceling":
			return records[i].ID
		}
	}
	return ""
}

func (s *sessionState) replayEvents(opts EventReplayOptions) []events.Event {
	if s == nil || s.timeline == nil {
		return nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = snapshotTimelineLimit
	}
	entries := s.timeline.Entries(limit)
	if !opts.ActiveOnly {
		return entries
	}

	s.mu.RLock()
	activeTurnID := ""
	if s.active != nil {
		activeTurnID = s.active.id
	}
	s.mu.RUnlock()
	if activeTurnID == "" {
		return nil
	}

	filtered := make([]events.Event, 0, len(entries))
	for _, event := range entries {
		if event.TurnID == activeTurnID {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func eventReplayKey(event events.Event) string {
	return fmt.Sprintf("%s|%s|%s", event.Type, event.TurnID, event.Timestamp.Format(time.RFC3339Nano))
}

func normalizeTurnRecords(records []TurnRecord) []TurnRecord {
	out := make([]TurnRecord, 0, len(records))
	seen := make(map[string]int, len(records))
	for _, record := range records {
		record.ID = strings.TrimSpace(record.ID)
		if record.ID == "" {
			continue
		}
		record.Status = strings.TrimSpace(record.Status)
		if record.Status == "" {
			record.Status = "unknown"
		}
		record.Source = strings.TrimSpace(record.Source)
		record.Sender = strings.TrimSpace(record.Sender)
		record.Summary = turnSummary(record.Summary)
		record.PendingRequestID = strings.TrimSpace(record.PendingRequestID)
		record.BlockedByPermissionID = strings.TrimSpace(record.BlockedByPermissionID)
		record.Error = strings.TrimSpace(record.Error)
		record.RetryOf = strings.TrimSpace(record.RetryOf)
		record.CanRetry = false
		record.CanResume = false
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = record.StartedAt
		}
		if record.Envelope != nil {
			normalized := record.Envelope.Normalized()
			record.Envelope = &normalized
		}
		if idx, ok := seen[record.ID]; ok {
			out[idx] = mergeTurnRecord(out[idx], record)
			continue
		}
		seen[record.ID] = len(out)
		out = append(out, record)
	}
	return trimTurnRecords(out, persistedTurnLimit)
}

func normalizeQueuedTurns(items []QueuedTurn) []QueuedTurn {
	out := make([]QueuedTurn, 0, len(items))
	for _, item := range items {
		normalized := normalizeQueuedTurn(item)
		if strings.TrimSpace(normalized.ID) == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func normalizeQueuedTurn(item QueuedTurn) QueuedTurn {
	item.ID = strings.TrimSpace(item.ID)
	item.Mode = normalizeQueueMode(item.Mode)
	item.Status = strings.TrimSpace(item.Status)
	if item.Status == "" {
		item.Status = "queued"
	}
	item.Source = strings.TrimSpace(item.Source)
	item.Sender = strings.TrimSpace(item.Sender)
	item.Summary = turnSummary(item.Summary)
	item.Envelope = item.Envelope.Normalized()
	if item.Source == "" {
		item.Source = string(item.Envelope.Source)
	}
	if item.Sender == "" {
		item.Sender = strings.TrimSpace(item.Envelope.Sender)
	}
	if item.Summary == "" {
		item.Summary = turnSummary(item.Envelope.BodyText())
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	return item
}

func cloneQueuedTurn(item QueuedTurn) QueuedTurn {
	item.Envelope = item.Envelope.Normalized()
	return item
}

func cloneQueuedTurns(items []QueuedTurn) []QueuedTurn {
	out := make([]QueuedTurn, len(items))
	for i, item := range items {
		out[i] = cloneQueuedTurn(item)
	}
	return out
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mergeTurnRecord(existing, next TurnRecord) TurnRecord {
	merged := existing
	if next.Status != "" {
		merged.Status = next.Status
	}
	if next.Source != "" {
		merged.Source = next.Source
	}
	if next.Sender != "" {
		merged.Sender = next.Sender
	}
	if next.Summary != "" {
		merged.Summary = next.Summary
	}
	if merged.StartedAt.IsZero() || (!next.StartedAt.IsZero() && next.StartedAt.Before(merged.StartedAt)) {
		merged.StartedAt = next.StartedAt
	}
	if next.UpdatedAt.After(merged.UpdatedAt) || merged.UpdatedAt.IsZero() {
		merged.UpdatedAt = next.UpdatedAt
	}
	if next.CompletedAt != nil {
		completedAt := *next.CompletedAt
		merged.CompletedAt = &completedAt
	}
	if next.PendingRequestID != "" {
		merged.PendingRequestID = next.PendingRequestID
	}
	if next.BlockedByPermissionID != "" {
		merged.BlockedByPermissionID = next.BlockedByPermissionID
	}
	if next.PermissionStatus != "" {
		merged.PermissionStatus = next.PermissionStatus
	}
	if next.Error != "" {
		merged.Error = next.Error
	}
	if next.RetryOf != "" {
		merged.RetryOf = next.RetryOf
	}
	if next.ResumeAvailable {
		merged.ResumeAvailable = true
	}
	if next.RecoveryHint != "" {
		merged.RecoveryHint = next.RecoveryHint
	}
	if next.Phase != "" {
		merged.Phase = next.Phase
	}
	if next.InjectionCount != 0 {
		merged.InjectionCount = next.InjectionCount
	}
	if next.LastToolName != "" {
		merged.LastToolName = next.LastToolName
	}
	if next.Envelope != nil {
		envelope := next.Envelope.Normalized()
		merged.Envelope = &envelope
	}
	if len(next.Injections) > 0 {
		merged.Injections = append([]message.Envelope{}, next.Injections...)
	}
	if next.Envelope != nil || next.PriorMessageCount != 0 {
		merged.PriorMessageCount = next.PriorMessageCount
	}
	return merged
}

func cloneTurnRecord(record TurnRecord) TurnRecord {
	cloned := record
	if record.CompletedAt != nil {
		completedAt := *record.CompletedAt
		cloned.CompletedAt = &completedAt
	}
	if record.Envelope != nil {
		envelope := record.Envelope.Normalized()
		cloned.Envelope = &envelope
	}
	if len(record.Injections) > 0 {
		cloned.Injections = append([]message.Envelope{}, record.Injections...)
	}
	return cloned
}

func cloneTurnRecords(records []TurnRecord) []TurnRecord {
	out := make([]TurnRecord, len(records))
	for i, record := range records {
		out[i] = cloneTurnRecord(record)
	}
	return out
}

func trimTurnRecords(records []TurnRecord, limit int) []TurnRecord {
	if limit <= 0 || len(records) <= limit {
		return records
	}
	return append([]TurnRecord{}, records[len(records)-limit:]...)
}

func turnRecordIndex(records []TurnRecord, turnID string) int {
	for i := range records {
		if records[i].ID == turnID {
			return i
		}
	}
	return -1
}

func isTerminalTurnStatus(status string) bool {
	switch status {
	case "running", "canceling":
		return false
	default:
		return true
	}
}

func canRetryTurnStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "canceled", "error", "interrupted":
		return true
	default:
		return false
	}
}

func canResumeTurnStatus(status string) bool {
	return strings.TrimSpace(status) == "interrupted"
}

func turnSummary(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= 160 {
		return text
	}
	return string(runes[:160])
}

func forkMessageIndexForTurn(records []TurnRecord, turnID string, fallback int) int {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return fallback
	}
	for _, record := range records {
		if strings.TrimSpace(record.ID) == turnID {
			if record.PriorMessageCount < 0 {
				return 0
			}
			if record.PriorMessageCount > fallback {
				return fallback
			}
			return record.PriorMessageCount
		}
	}
	return fallback
}

func forkSessionKey(parent string, now time.Time) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		parent = "session"
	}
	suffix := randomSuffix(4)
	if suffix == "" {
		suffix = fmt.Sprintf("%x", now.UnixNano())
	}
	return fmt.Sprintf("%s-fork-%s", parent, suffix)
}

func randomSuffix(bytesLen int) string {
	if bytesLen <= 0 {
		bytesLen = 4
	}
	buf := make([]byte, bytesLen)
	if _, err := crand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (s *sessionState) nextTurnID(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCounter++
	return fmt.Sprintf("turn-%d-%d", now.UnixNano(), s.turnCounter)
}

func withSessionLock(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionLockContextKey{}, strings.TrimSpace(sessionID))
}

func hasSessionLock(ctx context.Context, sessionID string) bool {
	if ctx == nil {
		return false
	}
	held, _ := ctx.Value(sessionLockContextKey{}).(string)
	return strings.TrimSpace(held) != "" && strings.TrimSpace(held) == strings.TrimSpace(sessionID)
}

func (s *Service) acquireSessionIfNeeded(ctx context.Context, sessionID string, session *sessionState) (func(), bool, error) {
	if hasSessionLock(ctx, sessionID) {
		return nil, false, nil
	}
	release, err := session.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	return release, true, nil
}

func normalizeLocator(locator SessionLocator) SessionLocator {
	normalized := SessionLocator{
		Channel: strings.ToLower(strings.TrimSpace(locator.Channel)),
		Key:     strings.TrimSpace(locator.Key),
		UserID:  strings.TrimSpace(locator.UserID),
	}
	if normalized.Channel == "" {
		normalized.Channel = "local"
	}
	if normalized.Key == "" {
		normalized.Key = "default"
	}
	if len(locator.Metadata) > 0 {
		normalized.Metadata = make(map[string]string, len(locator.Metadata))
		for key, value := range locator.Metadata {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "" {
				continue
			}
			normalized.Metadata[key] = value
		}
		if len(normalized.Metadata) == 0 {
			normalized.Metadata = nil
		}
	}
	return normalized
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func mergeCommandContextMetadata(base map[string]string, override map[string]string) map[string]string {
	merged := cloneStringMap(base)
	if len(override) == 0 {
		return merged
	}
	if merged == nil {
		merged = map[string]string{}
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func stableSessionID(locator SessionLocator) string {
	normalized := normalizeLocator(locator)
	// Clean the project dir on the way into the hash so
	// "/a/b" and "/a/b/" or "/a/./b" — the same directory,
	// different surface forms — all map to the same session
	// id.  This keeps the session identity stable across
	// shells, editors, and CI scripts that often normalise
	// paths differently.
	data, _ := json.Marshal(struct {
		Channel    string `json:"channel"`
		Key        string `json:"key,omitempty"`
		UserID     string `json:"user_id,omitempty"`
		ProjectDir string `json:"project_dir,omitempty"`
	}{
		Channel:    normalized.Channel,
		Key:        normalized.Key,
		UserID:     normalized.UserID,
		ProjectDir: cleanProjectDir(normalized.Metadata[sessionProjectDirMetadataKey]),
	})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s-%s", normalized.Channel, hex.EncodeToString(sum[:8]))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func (s *Service) buildRuntimeContext(sessionID string, locator SessionLocator, envelope message.Envelope) automation.SessionContext {
	ctx := automation.SessionContext{
		SessionID:       sessionID,
		LocatorChannel:  locator.Channel,
		LocatorKey:      locator.Key,
		LocatorUserID:   locator.UserID,
		Source:          string(envelope.Source),
		Sender:          envelope.Sender,
		AgentProfile:    s.effectiveAgentProfile(locator, envelope),
		SecurityProfile: s.effectiveSecurityProfile(),
		Metadata:        cloneStringMap(envelope.Metadata),
	}
	ctx.DefaultDelivery = defaultDeliveryTarget(sessionID, locator, envelope)
	return ctx
}

func attachSessionGraphContext(session *sessionState, ctx *automation.SessionContext) {
	if session == nil || ctx == nil {
		return
	}
	session.mu.RLock()
	graph := session.graph
	if graph == nil {
		session.mu.RUnlock()
		return
	}
	head, ok := graph.Head(sessiongraph.MainBranchID)
	if !ok {
		session.mu.RUnlock()
		return
	}
	session.mu.RUnlock()
	if ctx.Metadata == nil {
		ctx.Metadata = map[string]string{}
	}
	ctx.Metadata[sessionGraphBranchMetadataKey] = string(sessiongraph.MainBranchID)
	if head.Head != "" {
		ctx.Metadata[sessionGraphNodeMetadataKey] = string(head.Head)
	}
}

func (s *Service) effectiveSecurityProfile() string {
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.Security.Profile)
	}
	return ""
}

func (s *Service) effectiveAgentProfile(locator SessionLocator, envelope message.Envelope) string {
	if profile := strings.TrimSpace(envelope.Metadata["agent_profile"]); profile != "" {
		return config.NormalizeAgentProfile(profile)
	}
	if profile := strings.TrimSpace(locator.Metadata["agent_profile"]); profile != "" {
		return config.NormalizeAgentProfile(profile)
	}
	if s != nil && s.cfg != nil {
		return s.cfg.DefaultAgentProfileForChannel(firstNonEmpty(string(envelope.Source), locator.Channel))
	}
	return config.AgentProfileGeneral
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultDeliveryTarget(sessionID string, locator SessionLocator, envelope message.Envelope) automation.DeliveryTarget {
	switch envelope.Source {
	case message.SourceFeishu:
		return automation.DeliveryTarget{
			Kind:       "channel",
			Channel:    "feishu",
			SessionKey: locator.Key,
			Recipient:  envelope.Sender,
			Metadata:   cloneStringMap(envelope.Metadata),
		}
	case message.SourceWeixin:
		return automation.DeliveryTarget{
			Kind:       "channel",
			Channel:    "weixin",
			SessionKey: locator.Key,
			Recipient:  envelope.Sender,
			Metadata:   cloneStringMap(envelope.Metadata),
		}
	default:
		return automation.DeliveryTarget{
			Kind:       "session",
			SessionID:  sessionID,
			Channel:    locator.Channel,
			SessionKey: locator.Key,
			Recipient:  envelope.Sender,
			Metadata:   cloneStringMap(envelope.Metadata),
		}
	}
}

func assistantTextSince(messages []protocol.Message, start int) string {
	if start < 0 {
		start = 0
	}
	if start > len(messages) {
		start = len(messages)
	}
	for i := len(messages) - 1; i >= start; i-- {
		if messages[i].Role != protocol.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(messages[i])); text != "" {
			return text
		}
	}
	return ""
}

func sanitizeAttachmentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\n", "-", "\r", "-", "\t", " ")
	name = replacer.Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "attachment"
	}
	return name
}

func deriveSessionTitle(state agent.SessionState) string {
	for _, msg := range state.Messages {
		if msg.Role != protocol.RoleUser {
			continue
		}
		if text := strings.TrimSpace(protocol.MessageText(msg)); text != "" {
			return summarizeTitle(text)
		}
		if msg.Metadata == nil || len(msg.Metadata.Attachments) == 0 {
			continue
		}
		for _, attachment := range msg.Metadata.Attachments {
			if strings.TrimSpace(attachment.Name) != "" {
				return summarizeTitle(attachment.Name)
			}
		}
	}
	return "New chat"
}

func sessionTitleFromEnvelope(envelope message.Envelope) string {
	normalized := envelope.Normalized()
	if text := strings.TrimSpace(normalized.BodyText()); text != "" {
		return summarizeTitle(text)
	}
	for _, attachment := range normalized.Attachments {
		if strings.TrimSpace(attachment.Name) != "" {
			return summarizeTitle(attachment.Name)
		}
	}
	return ""
}

func summarizeTitle(raw string) string {
	raw = strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if raw == "" {
		return "New chat"
	}
	// Use up to 50 chars for better distinguishability.
	runes := []rune(raw)
	maxLen := 50
	if len(runes) <= maxLen {
		return raw
	}
	// Find a natural break point: prefer sentence-ending punctuation or whitespace.
	cut := maxLen
	for i := maxLen - 1; i >= maxLen*2/3 && i >= 0; i-- {
		switch runes[i] {
		case '.', '!', '?', '。', '！', '？', '\n', ';', '；':
			cut = i + 1
			goto done
		case ' ', '\t', ',', '，':
			cut = i
			goto done
		}
	}
done:
	truncated := strings.TrimRight(string(runes[:cut]), " \t\r\n,.;:!?，。！？、；：")
	if truncated == "" {
		truncated = string(runes[:maxLen])
	}
	return truncated + "…"
}

func newAttachmentID() (string, error) {
	var data [8]byte
	if _, err := crand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate attachment id: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

func stateDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
