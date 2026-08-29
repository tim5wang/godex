// Package taskboard implements the cross-session project task board plugin
// (需求池 #1): a host-authoritative task ledger with taskboard_* agent tools,
// project claim boundaries, per-task isolated session execution, and a kanban
// surface. Design: docs/taskboard-plugin-design.md.
package taskboard

import "time"

// Task statuses follow the five-column kanban. done is human-only: agent
// tools can never move a card to done (code-level protocol gate).
const (
	StatusBacklog    = "backlog"
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusInReview   = "in_review"
	StatusDone       = "done"
)

// Urgency levels (three-color triage).
const (
	UrgencyUrgent = "urgent"
	UrgencyNormal = "normal"
	UrgencyLow    = "low"
)

// Execution statuses.
const (
	ExecutionRunning   = "running"
	ExecutionCompleted = "completed"
	ExecutionFailed    = "failed"
	ExecutionCancelled = "cancelled"
)

// Execution stages (where a run is stuck / what it is currently doing), surfaced
// in the ledger and UI so a PJM can tell "thinking" from "tool call" from
// "waiting approval" without opening the execution session.
const (
	StageThinking        = "thinking"
	StageToolCall        = "tool_call"
	StageFinalResponse   = "final_response"
	StageWaitingApproval = "waiting_approval"
	StageError           = "error"
	StageInterrupted     = "interrupted"
	StageIdle            = "idle"
)

// Execution error types (coarse buckets for the "how did it fail" insight).
const (
	ErrTypeProvider    = "provider"
	ErrTypeTool        = "tool"
	ErrTypeCancelled   = "cancelled"
	ErrTypeInterrupted = "interrupted"
	ErrTypeUnknown     = "unknown"
)

// Project is one board's workspace boundary: only sessions whose workspace
// belongs to the project may claim or execute its tasks.
type Project struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	RootDir string `json:"root_dir"`
	// BuiltIn marks the default project (rooted at the godex workspace);
	// it cannot be deleted.
	BuiltIn bool `json:"built_in,omitempty"`
}

// ChecklistItem is one acceptance-criterion (DoD) line; checking requires an
// evidence note.
type ChecklistItem struct {
	Text     string `json:"text"`
	Done     bool   `json:"done"`
	Evidence string `json:"evidence,omitempty"`
}

// Comment is one discussion entry; agents treat comments as the latest
// requirements (read before acting).
type Comment struct {
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// HostRef identifies the session hosting an execution, so the UI can link
// to its live progress (subagent timeline in the host chat session).
// ProjectDir is required for identity reconstruction: session ids hash
// channel+key+user_id+project_dir, and the hash is applied AFTER the
// platform fills a default project dir at OpenSession — so a HostRef
// without it hashes to a different session.
type HostRef struct {
	SessionID  string `json:"session_id"`
	Channel    string `json:"channel,omitempty"`
	Key        string `json:"key,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// Execution records one isolated session run of the task.
type Execution struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	// Host is the session hosting this execution (UI jump-to-progress).
	Host *HostRef `json:"host,omitempty"`
	// JobSessionID is the isolated execution session's own id (recorded
	// asynchronously once the durable subagent materializes it). The run's
	// messages, tool calls and timeline live here — this is the primary
	// jump-to-progress target; Host is only a fallback.
	JobSessionID string `json:"job_session_id,omitempty"`
	// Execution observability: where the run is stuck, how it failed, and what
	// it last did — written by the executor's observe/reconcile path so the
	// ledger reflects the real session state without opening the conversation.
	Stage     string    `json:"stage,omitempty"`
	ErrorType string    `json:"error_type,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	LastTool  string    `json:"last_tool,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// ExecutionObservation is a snapshot of one execution's current live state. The
// executor writes it back into the ledger (via UpdateExecutionObservation) so
// the board reflects reality without opening the session.
type ExecutionObservation struct {
	Stage     string `json:"stage,omitempty"`
	ErrorType string `json:"error_type,omitempty"`
	LastError string `json:"last_error,omitempty"`
	LastTool  string `json:"last_tool,omitempty"`
}

// ReconcileReport summarizes one reconciliation pass over running executions.
type ReconcileReport struct {
	Scanned   int `json:"scanned"`
	Observed  int `json:"observed"`
	Finalized int `json:"finalized"`
}

// Research carries the structured investigation/verification asset produced by
// a planner/PJM card and reused by a coder card so the调研 is done once (方案A:
// 上下文传递). It is injected into the execution prompt in two clearly split
// sections — verified facts (trust, don't re-investigate) vs open questions
// (must verify yourself).
type Research struct {
	// Facts is the list of already-verified facts (结论文本), the trust layer.
	Facts []string `json:"facts,omitempty"`
	// Locations are the key landing points as "file:line" (关键落点), so the
	// coder can jump straight to the relevant code without re-grepping.
	Locations []string `json:"locations,omitempty"`
	// ExcludedPaths are the paths already ruled out — do not investigate them.
	ExcludedPaths []string `json:"excluded_paths,omitempty"`
	// OpenQuestions are the points the executor must still verify itself.
	OpenQuestions []string `json:"open_questions,omitempty"`
}

// IsEmpty reports whether the research asset carries nothing useful.
func (r Research) IsEmpty() bool {
	return len(r.Facts) == 0 && len(r.Locations) == 0 &&
		len(r.ExcludedPaths) == 0 && len(r.OpenQuestions) == 0
}

// Card is one task on the board. Version drives optimistic concurrency:
// mutations must pass the version they read (ifVersion) or fail.
type Card struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Urgency     string `json:"urgency"`
	Status      string `json:"status"`
	// TemplateID pins the agent template used to initialize the card's
	// execution session (M3). Empty = project default / builtin default.
	TemplateID string `json:"template_id,omitempty"`
	// TouchedPaths declares the package-level impact surface affected by this
	// card (gate 1 static declaration, e.g. ["internal/platform/tooling"]). It
	// is the basis for cross-card parallel-conflict detection (gates 2/4) and
	// is merged with paths an execution session actually reports (gate 3
	// dynamic observation). Package granularity: coarser than file, finer than
	// directory.
	TouchedPaths []string `json:"touched_paths,omitempty"`
	// Research is the structured investigation asset produced by a
	// planner/PJM card and referenced by a coder card (方案A: 上下文传递). It
	// splits verified-facts (trust) from open-questions (verify yourself) so
	// the调研 is done once and the executor does not re-investigate.
	Research *Research `json:"research,omitempty"`
	// ObservedPaths is the paths an execution session actually reported touching
	// at runtime (gate 3 dynamic observation). It is unioned with TouchedPaths
	// for conflict detection so an accurate report is never lost to a stale
	// declaration.
	ObservedPaths []string `json:"observed_paths,omitempty"`
	// MergeReport is the gate-4 merge-precheck result computed when the card
	// moves to in_review: it lists any cross-card package overlaps against other
	// active cards so a reviewer/PJM can adjudicate before acceptance. Nil means
	// no conflict was detected.
	MergeReport *ConflictReport `json:"merge_report,omitempty"`
	// Holder identifies who currently owns an in_progress card (execution
	// session or claiming agent). A held card cannot be claimed by others.
	Holder string `json:"holder,omitempty"`
	// Blocked marks a card paused for external reasons.
	Blocked    bool            `json:"blocked,omitempty"`
	Checklist  []ChecklistItem `json:"checklist,omitempty"`
	Comments   []Comment       `json:"comments,omitempty"`
	Executions []Execution     `json:"executions,omitempty"`
	Version    int             `json:"version"`
	CreatedBy  string          `json:"created_by"`
	UpdatedBy  string          `json:"updated_by"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Deleted    bool            `json:"deleted,omitempty"`
}

// HasRunningExecution reports whether the card currently has a running
// execution (deleted cards with running executions are refused).
func (c *Card) HasRunningExecution() bool {
	for _, ex := range c.Executions {
		if ex.Status == ExecutionRunning {
			return true
		}
	}
	return false
}

// ChecklistProgress returns (done, total) counts for DoD display.
func (c *Card) ChecklistProgress() (done, total int) {
	for _, item := range c.Checklist {
		if item.Done {
			done++
		}
		total++
	}
	return done, total
}
