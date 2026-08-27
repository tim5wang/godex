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
