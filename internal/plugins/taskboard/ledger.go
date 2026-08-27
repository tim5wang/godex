package taskboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Protocol-gate and validation errors (code-level gates from the design doc:
// agents never reach done; held cards cannot be claimed; cards with running
// executions cannot be deleted).
var (
	ErrCardNotFound      = errors.New("taskboard: card not found")
	ErrProjectNotFound   = errors.New("taskboard: project not found")
	ErrVersionConflict   = errors.New("taskboard: version conflict")
	ErrDoneIsHumanOnly   = errors.New("taskboard: only a human can accept a card (done)")
	ErrCardHeld          = errors.New("taskboard: card is held by another owner")
	ErrRunningExecution  = errors.New("taskboard: card has a running execution")
	ErrProjectHasCards   = errors.New("taskboard: project still has cards")
	ErrBuiltInProject    = errors.New("taskboard: built-in project cannot be deleted")
	ErrInvalidTransition = errors.New("taskboard: invalid status transition")
)

// transition rules: linear flow forward plus in_review -> todo (rejection
// bounce-back). done is reachable only via human acceptance (CompleteCard).
var allowedTransitions = map[string][]string{
	StatusBacklog:    {StatusTodo},
	StatusTodo:       {StatusInProgress},
	StatusInProgress: {StatusInReview},
	StatusInReview:   {StatusTodo},
	StatusDone:       {},
}

// CardFilter narrows ListCards (empty field = no filter).
type CardFilter struct {
	ProjectID string
	Status    string
	Urgency   string
	// IncludeDeleted includes soft-deleted cards (admin views).
	IncludeDeleted bool
}

// ledgerData is the persisted host-authoritative document.
type ledgerData struct {
	Projects []Project `json:"projects"`
	Cards    []Card    `json:"cards"`
}

// Ledger is the host-authoritative task ledger. All mutations are serialized
// through one mutex and persisted atomically (tmp + rename).
type Ledger struct {
	mu    sync.Mutex
	path  string
	data  ledgerData
	now   func() time.Time
	idSeq int
}

// OpenLedger loads (or initializes) the ledger at path, creating the
// parent directory as needed. workspaceDir roots the built-in default
// project (created once when the ledger is empty; design Q3=B).
func OpenLedger(path, workspaceDir string) (*Ledger, error) {
	l := &Ledger{path: path, now: time.Now}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, &l.data); err != nil {
				return nil, fmt.Errorf("taskboard: parse ledger: %w", err)
			}
		}
	case os.IsNotExist(err):
		// first run
	default:
		return nil, err
	}
	l.ensureDefaultProjectLocked(workspaceDir)
	if err := l.saveLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// ensureDefaultProjectLocked seeds the built-in default project exactly once
// (idempotent across restarts). Callers must hold l.mu (open path only).
func (l *Ledger) ensureDefaultProjectLocked(workspaceDir string) {
	for _, p := range l.data.Projects {
		if p.BuiltIn {
			return
		}
	}
	if workspaceDir == "" {
		workspaceDir = "."
	}
	l.data.Projects = append(l.data.Projects, Project{
		ID:      "p-default",
		Name:    "Default",
		RootDir: workspaceDir,
		BuiltIn: true,
	})
}

// saveLocked persists the ledger atomically. Callers must hold l.mu.
func (l *Ledger) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func (l *Ledger) nextID(prefix string) string {
	l.idSeq++
	return fmt.Sprintf("%s-%d-%d", prefix, l.now().UnixMilli(), l.idSeq)
}

func normalizeUrgency(u string) string {
	switch strings.TrimSpace(u) {
	case UrgencyUrgent, UrgencyLow:
		return strings.TrimSpace(u)
	default:
		return UrgencyNormal
	}
}

// ---- Projects ----

// ListProjects returns all projects (default project included).
func (l *Ledger) ListProjects() []Project {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Project, len(l.data.Projects))
	copy(out, l.data.Projects)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CreateProject registers one board project.
func (l *Ledger) CreateProject(name, rootDir string) (Project, error) {
	name = strings.TrimSpace(name)
	rootDir = strings.TrimSpace(rootDir)
	if name == "" || rootDir == "" {
		return Project{}, fmt.Errorf("taskboard: project name and root_dir are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.data.Projects {
		if p.Name == name {
			return Project{}, fmt.Errorf("taskboard: project %q already exists", name)
		}
	}
	project := Project{
		ID:      l.nextID("p"),
		Name:    name,
		RootDir: rootDir,
	}
	l.data.Projects = append(l.data.Projects, project)
	if err := l.saveLocked(); err != nil {
		return Project{}, err
	}
	return project, nil
}

// DeleteProject removes a project. Built-in projects and projects that still
// have cards (including soft-deleted ones) are refused.
func (l *Ledger) DeleteProject(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	idx := -1
	for i := range l.data.Projects {
		if l.data.Projects[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrProjectNotFound
	}
	if l.data.Projects[idx].BuiltIn {
		return ErrBuiltInProject
	}
	for _, card := range l.data.Cards {
		if card.ProjectID == id {
			return ErrProjectHasCards
		}
	}
	l.data.Projects = append(l.data.Projects[:idx], l.data.Projects[idx+1:]...)
	return l.saveLocked()
}

// ---- Cards ----

// ListCards returns cards matching the filter (soft-deleted excluded unless
// requested), newest-created first within the same status order.
func (l *Ledger) ListCards(filter CardFilter) []Card {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Card, 0, len(l.data.Cards))
	for _, card := range l.data.Cards {
		if card.Deleted && !filter.IncludeDeleted {
			continue
		}
		if filter.ProjectID != "" && card.ProjectID != filter.ProjectID {
			continue
		}
		if filter.Status != "" && card.Status != filter.Status {
			continue
		}
		if filter.Urgency != "" && card.Urgency != filter.Urgency {
			continue
		}
		out = append(out, card)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := urgencyRank(out[i].Urgency), urgencyRank(out[j].Urgency)
		if pi != pj {
			return pi < pj
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func urgencyRank(u string) int {
	switch u {
	case UrgencyUrgent:
		return 0
	case UrgencyLow:
		return 2
	default:
		return 1
	}
}

// GetCard returns one card by id (including soft-deleted, for detail views).
func (l *Ledger) GetCard(id string) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, card := range l.data.Cards {
		if card.ID == id {
			return card, nil
		}
	}
	return Card{}, ErrCardNotFound
}

// CreateCardInput seeds a new card.
type CreateCardInput struct {
	ProjectID   string
	Title       string
	Description string
	Prompt      string
	Urgency     string
	Checklist   []string
	CreatedBy   string
	// StartStatus allows human creation directly into todo (agents always
	// start at backlog).
	StartStatus string
}

// CreateCard adds a card to the ledger.
func (l *Ledger) CreateCard(input CreateCardInput) (Card, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Card{}, fmt.Errorf("taskboard: card title is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		if len(l.data.Projects) == 0 {
			return Card{}, ErrProjectNotFound
		}
		// Default to the built-in project when unset.
		for _, p := range l.data.Projects {
			if p.BuiltIn {
				projectID = p.ID
				break
			}
		}
		if projectID == "" {
			projectID = l.data.Projects[0].ID
		}
	}
	found := false
	for _, p := range l.data.Projects {
		if p.ID == projectID {
			found = true
			break
		}
	}
	if !found {
		return Card{}, ErrProjectNotFound
	}
	now := l.now()
	status := StatusBacklog
	if input.StartStatus == StatusTodo {
		status = StatusTodo
	}
	card := Card{
		ID:          l.nextID("t"),
		ProjectID:   projectID,
		Title:       title,
		Description: strings.TrimSpace(input.Description),
		Prompt:      strings.TrimSpace(input.Prompt),
		Urgency:     normalizeUrgency(input.Urgency),
		Status:      status,
		Version:     1,
		CreatedBy:   input.CreatedBy,
		UpdatedBy:   input.CreatedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	for _, text := range input.Checklist {
		if text = strings.TrimSpace(text); text != "" {
			card.Checklist = append(card.Checklist, ChecklistItem{Text: text})
		}
	}
	l.data.Cards = append(l.data.Cards, card)
	if err := l.saveLocked(); err != nil {
		return Card{}, err
	}
	return card, nil
}

// findCardLocked locates a card by id. Callers must hold l.mu.
func (l *Ledger) findCardLocked(id string) (*Card, error) {
	for i := range l.data.Cards {
		if l.data.Cards[i].ID == id {
			return &l.data.Cards[i], nil
		}
	}
	return nil, ErrCardNotFound
}

// mutateCard is the optimistic-concurrency mutation wrapper: it checks
// ifVersion, applies mutate, bumps version/updatedAt, and persists. Callers
// must hold l.mu.
func (l *Ledger) mutateCard(id string, ifVersion int, actor string, mutate func(*Card) error) (Card, error) {
	card, err := l.findCardLocked(id)
	if err != nil {
		return Card{}, err
	}
	if card.Version != ifVersion {
		return Card{}, fmt.Errorf("%w: card %s at v%d, caller had v%d", ErrVersionConflict, id, card.Version, ifVersion)
	}
	if err := mutate(card); err != nil {
		return Card{}, err
	}
	card.Version++
	card.UpdatedBy = actor
	card.UpdatedAt = l.now()
	if err := l.saveLocked(); err != nil {
		return Card{}, err
	}
	return *card, nil
}

// UpdateCardInput carries the editable text fields (agents and humans share
// this; model/execution fields are not agent-writable by design — the M1
// ledger has no such fields yet, M3 adds them read-only to tools).
type UpdateCardInput struct {
	Title       *string
	Description *string
	Prompt      *string
	Urgency     *string
	Blocked     *bool
}

// UpdateCard edits a card's text fields under optimistic concurrency.
func (l *Ledger) UpdateCard(id string, ifVersion int, actor string, input UpdateCardInput) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		if input.Title != nil {
			if title := strings.TrimSpace(*input.Title); title != "" {
				card.Title = title
			}
		}
		if input.Description != nil {
			card.Description = strings.TrimSpace(*input.Description)
		}
		if input.Prompt != nil {
			card.Prompt = strings.TrimSpace(*input.Prompt)
		}
		if input.Urgency != nil {
			card.Urgency = normalizeUrgency(*input.Urgency)
		}
		if input.Blocked != nil {
			card.Blocked = *input.Blocked
		}
		return nil
	})
}

// MoveCard transitions a card along the kanban. Protocol gates: done is
// human-only; linear flow only (plus in_review -> todo rejection bounce);
// a held in_progress card cannot be claimed by another owner.
func (l *Ledger) MoveCard(id string, ifVersion int, to, actor string) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		to = strings.TrimSpace(to)
		if to == StatusDone {
			return ErrDoneIsHumanOnly
		}
		allowed := false
		for _, next := range allowedTransitions[card.Status] {
			if next == to {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, card.Status, to)
		}
		if card.Status == StatusInProgress && card.Holder != "" && card.Holder != actor {
			return fmt.Errorf("%w: %s held by %q", ErrCardHeld, id, card.Holder)
		}
		card.Status = to
		if to == StatusInProgress {
			card.Holder = actor
		} else {
			card.Holder = ""
		}
		return nil
	})
}

// CompleteCard is the human-only acceptance gate: in_review -> done. When the
// checklist has unchecked items, force=true acknowledges them (the UI asks
// for confirmation and shows the outstanding count).
func (l *Ledger) CompleteCard(id string, ifVersion int, actor string, force bool) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		if card.Status != StatusInReview {
			return fmt.Errorf("%w: %s -> %s (accept from in_review)", ErrInvalidTransition, card.Status, StatusDone)
		}
		done, total := card.ChecklistProgress()
		if done < total && !force {
			return fmt.Errorf("taskboard: %d/%d checklist items done; pass force to accept anyway", done, total)
		}
		card.Status = StatusDone
		card.Holder = ""
		return nil
	})
}

// RejectCard bounces an in_review card back to todo with a reason comment.
func (l *Ledger) RejectCard(id string, ifVersion int, actor, reason string) (Card, error) {
	card, err := l.MoveCard(id, ifVersion, StatusTodo, actor)
	if err != nil {
		return card, err
	}
	if strings.TrimSpace(reason) != "" {
		card, err = l.AddComment(id, card.Version, actor, reason)
		if err != nil {
			return card, err
		}
	}
	return card, nil
}

// AddComment appends a discussion entry (agents treat comments as the latest
// requirements).
func (l *Ledger) AddComment(id string, ifVersion int, author, text string) (Card, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Card{}, fmt.Errorf("taskboard: comment text is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, author, func(card *Card) error {
		card.Comments = append(card.Comments, Comment{Author: author, Text: text, CreatedAt: l.now()})
		return nil
	})
}

// ChecklistAdd appends one acceptance criterion.
func (l *Ledger) ChecklistAdd(id string, ifVersion int, actor, text string) (Card, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Card{}, fmt.Errorf("taskboard: checklist item text is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		if len(card.Checklist) >= 30 {
			return fmt.Errorf("taskboard: checklist capped at 30 items")
		}
		card.Checklist = append(card.Checklist, ChecklistItem{Text: text})
		return nil
	})
}

// ChecklistCheck marks item at index done with an evidence note.
func (l *Ledger) ChecklistCheck(id string, ifVersion int, actor string, index int, evidence string) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		if index < 0 || index >= len(card.Checklist) {
			return fmt.Errorf("taskboard: checklist index %d out of range (%d items)", index, len(card.Checklist))
		}
		card.Checklist[index].Done = true
		card.Checklist[index].Evidence = strings.TrimSpace(evidence)
		return nil
	})
}

// ChecklistUncheck clears the done state (and evidence) of item at index.
func (l *Ledger) ChecklistUncheck(id string, ifVersion int, actor string, index int) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		if index < 0 || index >= len(card.Checklist) {
			return fmt.Errorf("taskboard: checklist index %d out of range (%d items)", index, len(card.Checklist))
		}
		card.Checklist[index].Done = false
		card.Checklist[index].Evidence = ""
		return nil
	})
}

// SoftDeleteCard hides a card. Protocol gate: a card with a running execution
// cannot be deleted.
func (l *Ledger) SoftDeleteCard(id string, ifVersion int, actor string) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(id, ifVersion, actor, func(card *Card) error {
		if card.HasRunningExecution() {
			return ErrRunningExecution
		}
		card.Deleted = true
		card.Holder = ""
		return nil
	})
}

// ---- Execution records (written by the executor, M1-d) ----

// StartExecution appends a running execution and claims the card
// (in_progress + holder) on behalf of the executor session. host (optional)
// records the hosting session for UI jump-to-progress.
func (l *Ledger) StartExecution(cardID, executionID, sessionID, actor string, host *HostRef) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mutateCard(cardID, versionForExecution(l, cardID), actor, func(card *Card) error {
		if card.HasRunningExecution() {
			return ErrRunningExecution
		}
		card.Executions = append(card.Executions, Execution{
			ID:        executionID,
			SessionID: sessionID,
			Status:    ExecutionRunning,
			StartedAt: l.now(),
			Host:      host,
		})
		card.Holder = sessionID
		return nil
	})
}

// versionForExecution reads the current version for an execution start (the
// executor does not do read-modify-write races with editors; M1 keeps it
// simple by reading under the lock). Callers must hold l.mu.
func versionForExecution(l *Ledger, cardID string) int {
	if card, err := l.findCardLocked(cardID); err == nil {
		return card.Version
	}
	return -1
}

// FinishExecution closes a running execution with a status and summary.
func (l *Ledger) FinishExecution(cardID, executionID, status, summary string) (Card, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	card, err := l.findCardLocked(cardID)
	if err != nil {
		return Card{}, err
	}
	version := card.Version
	switch status {
	case ExecutionCompleted, ExecutionFailed, ExecutionCancelled:
	default:
		return Card{}, fmt.Errorf("taskboard: invalid execution status %q", status)
	}
	return l.mutateCard(cardID, version, "taskboard", func(card *Card) error {
		for i := range card.Executions {
			if card.Executions[i].ID == executionID && card.Executions[i].Status == ExecutionRunning {
				card.Executions[i].Status = status
				card.Executions[i].Summary = strings.TrimSpace(summary)
				card.Executions[i].EndedAt = l.now()
				if card.Holder == card.Executions[i].SessionID {
					card.Holder = ""
				}
				return nil
			}
		}
		return fmt.Errorf("taskboard: running execution %s not found on card %s", executionID, cardID)
	})
}
