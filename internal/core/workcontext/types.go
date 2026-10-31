// Package workcontext stores long-running work streams, independently from
// sessions and the taskboard execution boundary. SQLite is the initial store.
package workcontext

import "errors"

var (
	ErrNotFound = errors.New("work context: not found")
	ErrConflict = errors.New("work context: version conflict; reload before saving")
	ErrInvalid  = errors.New("work context: invalid input")
	ErrArchived = errors.New("work context: archived; restore before editing")
)

type Scope struct {
	TenantID string
	OwnerID  string
}

// ScopeKey renders a Scope as one stable string. Push subscriptions are keyed by
// this so a background nudge for one user can never be delivered to another
// user's browser — and the tenant is part of the key because two tenants may
// legitimately have a user with the same id.
func ScopeKey(scope Scope) string {
	tenantID, ownerID := scopeArgs(scope)
	if tenantID == "" && ownerID == "" {
		return ""
	}
	return tenantID + "\x00" + ownerID
}

type Checkpoint struct {
	Summary       string   `json:"summary"`
	Completed     []string `json:"completed"`
	InProgress    []string `json:"in_progress"`
	NextActions   []string `json:"next_actions"`
	Blockers      []string `json:"blockers"`
	OpenQuestions []string `json:"open_questions"`
	EvidenceRefs  []string `json:"evidence_refs"`
	Revision      int      `json:"revision"`
	CreatedAt     string   `json:"created_at"`
}
type Context struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Background string     `json:"background"`
	Goal       string     `json:"goal"`
	Status     string     `json:"status"`
	Version    int        `json:"version"`
	Checkpoint Checkpoint `json:"checkpoint"`
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
}
type ContextInput struct {
	Title      string `json:"title"`
	Background string `json:"background"`
	Goal       string `json:"goal"`
}
type ContextPatch struct {
	Title      *string `json:"title,omitempty"`
	Background *string `json:"background,omitempty"`
	Goal       *string `json:"goal,omitempty"`
	Status     *string `json:"status,omitempty"`
	IfVersion  int     `json:"if_version"`
}
type Resource struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Locator    string   `json:"locator"`
	NodeID     string   `json:"node_id"`
	Purpose    string   `json:"purpose"`
	ContextIDs []string `json:"context_ids"`
	CreatedAt  string   `json:"created_at"`
}
type ResourceInput struct {
	ID      string `json:"id,omitempty"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Locator string `json:"locator"`
	NodeID  string `json:"node_id"`
	Purpose string `json:"purpose"`
}
type SessionLink struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Topic     string `json:"topic"`
	CreatedAt string `json:"created_at"`
}

// Todo is a first-class plan item on a stream: unlike the free-text checkpoint
// lists it carries status, an owner and a due time, so "what is overdue / who
// owes it / what is blocked" is answerable without re-reading prose.
type Todo struct {
	ID        string `json:"id"`
	ContextID string `json:"context_id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	Owner     string `json:"owner"`
	DueAt     string `json:"due_at"`
	Source    string `json:"source"`
	Position  int    `json:"position"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
type TodoInput struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Owner  string `json:"owner"`
	DueAt  string `json:"due_at"`
	Source string `json:"source"`
}
type TodoPatch struct {
	Text      *string `json:"text,omitempty"`
	Status    *string `json:"status,omitempty"`
	Owner     *string `json:"owner,omitempty"`
	DueAt     *string `json:"due_at,omitempty"`
	Position  *int    `json:"position,omitempty"`
	IfVersion int     `json:"if_version"`
}
type Delegation struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	CardID    string `json:"card_id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Pack      string `json:"pack"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
}
type Fact struct {
	ID                string `json:"id"`
	ContextID         string `json:"context_id"`
	Title             string `json:"title"`
	Content           string `json:"content"`
	Kind              string `json:"kind"`
	Status            string `json:"status"`
	Scope             string `json:"scope"`
	Subject           string `json:"subject"`
	Predicate         string `json:"predicate"`
	Source            string `json:"source"`
	SourceExcerpt     string `json:"source_excerpt"`
	SourceRevision    string `json:"source_revision"`
	ObservedAt        string `json:"observed_at"`
	VerifiedAt        string `json:"verified_at"`
	Provenance        string `json:"provenance"`
	SupersedesID      string `json:"supersedes_id"`
	Version           int    `json:"version"`
	CreatedAt         string `json:"created_at"`
	MemoryCandidateID string `json:"memory_candidate_id"`
}
type FactInput struct {
	Title          string `json:"title"`
	Content        string `json:"content"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope"`
	Subject        string `json:"subject"`
	Predicate      string `json:"predicate"`
	Source         string `json:"source"`
	SourceExcerpt  string `json:"source_excerpt"`
	SourceRevision string `json:"source_revision"`
	ObservedAt     string `json:"observed_at"`
	Provenance     string `json:"provenance"`
	SupersedesID   string `json:"supersedes_id"`
}
type FactReview struct {
	Status    string `json:"status"`
	IfVersion int    `json:"if_version"`
}
type Relation struct {
	ID        string `json:"id"`
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	Kind      string `json:"kind"`
	Condition string `json:"condition"`
	CreatedAt string `json:"created_at"`
}
type RelationInput struct {
	ToID      string `json:"to_id"`
	Kind      string `json:"kind"`
	Condition string `json:"condition"`
}
type Event struct {
	ID        string `json:"id"`
	ContextID string `json:"context_id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
}
type Detail struct {
	Context     Context       `json:"context"`
	Resources   []Resource    `json:"resources"`
	Sessions    []SessionLink `json:"sessions"`
	Delegations []Delegation  `json:"delegations"`
	Facts       []Fact        `json:"facts"`
	Relations   []Relation    `json:"relations"`
	Events      []Event       `json:"events"`
	Todos       []Todo        `json:"todos"`
}
