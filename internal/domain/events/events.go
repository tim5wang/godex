package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

// EventType identifies one runtime event category.
type EventType string

const (
	EventUserMessageAccepted      EventType = "user_message_accepted"
	EventAssistantTextDelta       EventType = "assistant_text_delta"
	EventAssistantMessageComplete EventType = "assistant_message_completed"
	EventToolCallStarted          EventType = "tool_call_started"
	EventToolCallFinished         EventType = "tool_call_finished"
	EventTodoListUpdated          EventType = "todo_list_updated"
	EventWarningRaised            EventType = "warning_raised"
	EventErrorRaised              EventType = "error_raised"
	EventCommandCompleted         EventType = "command_completed"
	EventSkillStateChanged        EventType = "skill_state_changed"
	EventHistoryRecallDecision    EventType = "history_recall_decision"
	EventSubagentJobUpdated       EventType = "subagent_job_updated"
	EventRunnerPhaseChanged       EventType = "runner_phase_changed"
	EventMessageInjected          EventType = "message_injected"
	EventAgentIdentityUpdated     EventType = "agent_identity_updated"
	EventSessionRepairStarted     EventType = "session_repair_started"
	EventSessionRepairCompleted   EventType = "session_repair_completed"
	EventSessionRepairFailed      EventType = "session_repair_failed"
	EventSnapshotReady            EventType = "snapshot_ready"
	EventTurnCompleted            EventType = "turn_completed"
)

// Event is the shared runtime event envelope for all frontends.
type Event struct {
	SessionID string    `json:"session_id,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"`
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload,omitempty"`
}

// MessagePayload carries one accepted inbound message.
type MessagePayload struct {
	Source      string                `json:"source,omitempty"`
	Sender      string                `json:"sender,omitempty"`
	Text        string                `json:"text,omitempty"`
	Attachments []protocol.Attachment `json:"attachments,omitempty"`
	Metadata    map[string]string     `json:"metadata,omitempty"`
}

// Sink receives runtime events.
type Sink interface {
	Emit(Event)
}

// SinkFunc adapts a function into an event sink.
type SinkFunc func(Event)

// Emit dispatches the event to the wrapped function.
func (f SinkFunc) Emit(event Event) {
	if f != nil {
		f(event)
	}
}

// NopSink discards all events.
var NopSink Sink = SinkFunc(func(Event) {})

// TextPayload carries assistant text output.
type TextPayload struct {
	Role string `json:"role,omitempty"`
	Text string `json:"text"`
}

// ToolCallPayload carries one tool invocation lifecycle event.
type ToolCallPayload struct {
	ID            string                 `json:"id,omitempty"`
	Name          string                 `json:"name"`
	Input         map[string]interface{} `json:"input,omitempty"`
	Output        string                 `json:"output,omitempty"`
	Error         string                 `json:"error,omitempty"`
	ArtifactPaths []string               `json:"artifact_paths,omitempty"`
	Code          string                 `json:"code,omitempty"`
	RecoveryHint  string                 `json:"recovery_hint,omitempty"`
	TimedOut      bool                   `json:"timed_out,omitempty"`
	DurationMS    int64                  `json:"duration_ms,omitempty"`
}

// TodoItemPayload is one item in a runtime todo-list update.
type TodoItemPayload struct {
	ID         int    `json:"id,omitempty"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"`
}

// TodoListPayload carries a structured todo-list update for frontends.
type TodoListPayload struct {
	Items            []TodoItemPayload `json:"items,omitempty"`
	Total            int               `json:"total"`
	Completed        int               `json:"completed"`
	InProgress       int               `json:"in_progress"`
	Pending          int               `json:"pending"`
	SourceToolCallID string            `json:"source_tool_call_id,omitempty"`
	SourceToolName   string            `json:"source_tool_name,omitempty"`
}

// NoticePayload carries warning or error text.
type NoticePayload struct {
	Message      string `json:"message"`
	Code         string `json:"code,omitempty"`
	ActorKind    string `json:"actor_kind,omitempty"`
	ActorID      string `json:"actor_id,omitempty"`
	Iteration    int    `json:"iteration,omitempty"`
	MaxTurns     int    `json:"max_turns,omitempty"`
	RecoveryHint string `json:"recovery_hint,omitempty"`
}

// SessionRepairPayload summarizes one deterministic session state repair pass.
type SessionRepairPayload struct {
	Status    string `json:"status,omitempty"`
	Code      string `json:"code,omitempty"`
	Findings  int    `json:"findings,omitempty"`
	Actions   int    `json:"actions,omitempty"`
	BackupDir string `json:"backup_dir,omitempty"`
	Message   string `json:"message,omitempty"`
}

// CommandPayload carries a completed command result.
type CommandPayload struct {
	Name               string `json:"name"`
	Output             string `json:"output,omitempty"`
	ArtifactPath       string `json:"artifact_path,omitempty"`
	RefreshSnapshot    bool   `json:"refresh_snapshot,omitempty"`
	DispatchMode       string `json:"dispatch_mode,omitempty"`
	DispatchInvocation string `json:"dispatch_invocation,omitempty"`
	DispatchStatus     string `json:"dispatch_status,omitempty"`
	DispatchError      string `json:"dispatch_error,omitempty"`
	DispatchedTurnID   string `json:"dispatched_turn_id,omitempty"`
	DispatchedJobID    string `json:"dispatched_job_id,omitempty"`
	Error              string `json:"error,omitempty"`
}

// SkillPayload carries one skill lifecycle change.
type SkillPayload struct {
	Action             string   `json:"action"`
	ID                 string   `json:"id,omitempty"`
	Name               string   `json:"name,omitempty"`
	Source             string   `json:"source,omitempty"`
	Sections           []string `json:"sections,omitempty"`
	RecommendedBundles []string `json:"recommended_bundles,omitempty"`
}

// HistoryRecallPayload carries one policy evaluation for history_search exposure.
type HistoryRecallPayload struct {
	AllowTool        bool     `json:"allow_tool"`
	Automatic        bool     `json:"automatic"`
	ExplicitRequest  bool     `json:"explicit_request"`
	RecommendedScope string   `json:"recommended_scope,omitempty"`
	Score            int      `json:"score"`
	Reasons          []string `json:"reasons,omitempty"`
}

// SubagentJobPayload carries one durable subagent progress update.
type SubagentJobPayload struct {
	JobID             string    `json:"job_id"`
	ParentTurnID      string    `json:"parent_turn_id,omitempty"`
	Sequence          int       `json:"sequence,omitempty"`
	Objective         string    `json:"objective,omitempty"`
	DisplayTitle      string    `json:"display_title,omitempty"`
	IdentityID        string    `json:"identity_id,omitempty"`
	AgentType         string    `json:"agent_type,omitempty"`
	RoleID            string    `json:"role_id,omitempty"`
	RoleName          string    `json:"role_name,omitempty"`
	PackageName       string    `json:"package_name,omitempty"`
	Status            string    `json:"status,omitempty"`
	Phase             string    `json:"phase,omitempty"`
	Message           string    `json:"message,omitempty"`
	ToolID            string    `json:"tool_id,omitempty"`
	ToolName          string    `json:"tool_name,omitempty"`
	Error             string    `json:"error,omitempty"`
	Result            string    `json:"result,omitempty"`
	ToolNames         []string  `json:"tool_names,omitempty"`
	CapabilitySummary []string  `json:"capability_summary,omitempty"`
	ModelHint         string    `json:"model_hint,omitempty"`
	BudgetHint        string    `json:"budget_hint,omitempty"`
	MaxTurns          int       `json:"max_turns,omitempty"`
	ModelRequestCount int       `json:"model_request_count,omitempty"`
	ToolCallCount     int       `json:"tool_call_count,omitempty"`
	LastRunnerPhase   string    `json:"last_runner_phase,omitempty"`
	LastIteration     int       `json:"last_iteration,omitempty"`
	LastRecoveryHint  string    `json:"last_recovery_hint,omitempty"`
	WriteScope        []string  `json:"write_scope,omitempty"`
	SandboxID         string    `json:"sandbox_id,omitempty"`
	WorktreeDir       string    `json:"worktree_dir,omitempty"`
	Isolation         string    `json:"isolation,omitempty"`
	WorkspaceOrigin   string    `json:"workspace_origin,omitempty"`
	GitBranch         string    `json:"git_branch,omitempty"`
	CleanupState      string    `json:"cleanup_state,omitempty"`
	MergeStatus       string    `json:"merge_status,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

// RunnerPhasePayload carries shared runner phase state for main agents,
// subagents, and workflow nodes.
type RunnerPhasePayload struct {
	RunnerID     string `json:"runner_id,omitempty"`
	ActorKind    string `json:"actor_kind,omitempty"`
	ActorID      string `json:"actor_id,omitempty"`
	Objective    string `json:"objective,omitempty"`
	DisplayTitle string `json:"display_title,omitempty"`
	Phase        string `json:"phase"`
	Iteration    int    `json:"iteration,omitempty"`
	MaxTurns     int    `json:"max_turns,omitempty"`
	Model        string `json:"model,omitempty"`
	Message      string `json:"message,omitempty"`
	ToolID       string `json:"tool_id,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	RecoveryHint string `json:"recovery_hint,omitempty"`
}

// MessageInjectedPayload records user follow-up messages that were folded into
// an already-running turn.
type MessageInjectedPayload struct {
	Count          int    `json:"count"`
	Mode           string `json:"mode,omitempty"`
	Remaining      int    `json:"remaining,omitempty"`
	InjectionCycle int    `json:"injection_cycle,omitempty"`
	Summary        string `json:"summary,omitempty"`
}

// AgentIdentityPayload announces one agent identity/manifest update.
type AgentIdentityPayload struct {
	ID                string            `json:"id"`
	Name              string            `json:"name,omitempty"`
	Kind              string            `json:"kind,omitempty"`
	Role              string            `json:"role,omitempty"`
	ParentID          string            `json:"parent_id,omitempty"`
	SessionID         string            `json:"session_id,omitempty"`
	Source            string            `json:"source,omitempty"`
	CapabilitySummary []string          `json:"capability_summary,omitempty"`
	ModelHint         string            `json:"model_hint,omitempty"`
	BudgetHint        string            `json:"budget_hint,omitempty"`
	Display           map[string]string `json:"display,omitempty"`
	LastActivityAt    time.Time         `json:"last_activity_at,omitempty"`
}

// SnapshotPayload announces a fresh snapshot.
type SnapshotPayload struct {
	UpdatedAt           time.Time `json:"updated_at"`
	Running             bool      `json:"running"`
	Compacted           bool      `json:"compacted,omitempty"`
	CompressionReasons  []string  `json:"compression_reasons,omitempty"`
	TokenEstimateBefore int       `json:"token_estimate_before,omitempty"`
	TokenEstimateAfter  int       `json:"token_estimate_after,omitempty"`
}

// TurnPayload summarizes one completed turn.
type TurnPayload struct {
	Status string `json:"status"`
}

// Broadcaster fans runtime events out to multiple sinks.
type Broadcaster struct {
	mu     sync.RWMutex
	nextID atomic.Uint64
	sinks  map[uint64]Sink
}

// NewBroadcaster creates a new broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{sinks: make(map[uint64]Sink)}
}

// Emit delivers the event to all current sinks.
func (b *Broadcaster) Emit(event Event) {
	if b == nil {
		return
	}

	b.mu.RLock()
	sinks := make([]Sink, 0, len(b.sinks))
	for _, sink := range b.sinks {
		sinks = append(sinks, sink)
	}
	b.mu.RUnlock()

	for _, sink := range sinks {
		if sink != nil {
			sink.Emit(event)
		}
	}
}

// Attach registers one sink and returns an unsubscribe function.
func (b *Broadcaster) Attach(sink Sink) func() {
	if b == nil || sink == nil {
		return func() {}
	}

	id := b.nextID.Add(1)
	b.mu.Lock()
	b.sinks[id] = sink
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.sinks, id)
		b.mu.Unlock()
	}
}

// Subscribe keeps the sink attached until ctx is canceled.
func (b *Broadcaster) Subscribe(ctx context.Context, sink Sink) error {
	unsubscribe := b.Attach(sink)
	defer unsubscribe()

	<-ctx.Done()
	return ctx.Err()
}
