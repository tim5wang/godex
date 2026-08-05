package backend

import (
	"context"
	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/sessiongraph"
	"github.com/tim5wang/godex/internal/sessionstore"
	"github.com/tim5wang/godex/internal/tools"
	"io"
	"sync"
	"time"
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
