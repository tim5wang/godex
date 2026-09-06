package taskboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/tools"
)

// Ledger is the read/write face the agent tool uses (kept as an interface
// for tests).
type toolLedger interface {
	ListCards(filter CardFilter) []Card
	GetCard(id string) (Card, error)
	CreateCard(input CreateCardInput) (Card, error)
	UpdateCard(id string, ifVersion int, actor string, input UpdateCardInput) (Card, error)
	MoveCard(id string, ifVersion int, to, actor string) (Card, error)
	AddComment(id string, ifVersion int, author, text string) (Card, error)
	ChecklistAdd(id string, ifVersion int, actor string, texts []string) (Card, error)
	ChecklistCheck(id string, ifVersion int, actor string, index int, evidence string) (Card, error)
	ChecklistUncheck(id string, ifVersion int, actor string, index int) (Card, error)
	SoftDeleteCard(id string, ifVersion int, actor string) (Card, error)
	ListProjects() []Project
	// Conflict-gate helpers (gate 2 dispatch intercept, gate 3 report,
	// gate 4 merge precheck).
	CheckCardPathConflicts(card Card) ConflictReport
	PrecheckDispatchConflicts(card Card) error
	ReportObservedPaths(cardID string, ifVersion int, actor string, paths []string) (Card, error)
	MergePrecheck(cardID string, ifVersion int, actor string) (Card, ConflictReport, error)
	// StatusCounts returns a read-only snapshot of the board grouped by card /
	// execution status (used by the status action and the cron watchdog
	// directive evaluator).
	StatusCounts(projectID string) StatusCounts
}

// compactCard is the terse per-card view used by list/get results.
type compactCard struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Urgency    string `json:"urgency"`
	Project    string `json:"project"`
	Holder     string `json:"holder,omitempty"`
	Blocked    bool   `json:"blocked,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
	CheckDn    int    `json:"checklist_done,omitempty"`
	CheckAll   int    `json:"checklist_total,omitempty"`
}

func compact(c Card) compactCard {
	done, total := c.ChecklistProgress()
	return compactCard{
		ID: c.ID, Title: c.Title, Status: c.Status, Urgency: c.Urgency,
		Project: c.ProjectID, Holder: c.Holder, Blocked: c.Blocked,
		TemplateID: c.TemplateID,
		CheckDn:    done, CheckAll: total,
	}
}

const agentActor = "agent"

// actorName returns the actor identity for a card mutation. A card holder is
// recorded as the hosting/execution session id (see Ledger.StartExecution), so
// the agent running that session must present the same id to advance its own
// held card. Falls back to agentActor for session-less callers (tests,
// background/durable agents with no tool session context).
func actorName(ctx context.Context) string {
	if id := tools.SessionIDFromContext(ctx); id != "" {
		return id
	}
	return agentActor
}

// tool actions
const (
	actionList          = "list"
	actionGet           = "get"
	actionCreate        = "create"
	actionUpdate        = "update"
	actionMove          = "move"
	actionCommentAdd    = "comment_add"
	actionDelete        = "delete"
	actionChecklist     = "checklist"
	actionDispatch      = "dispatch"
	actionObserve       = "observe"
	actionReconcile     = "reconcile"
	actionRecover       = "recover"
	actionRetry         = "retry"
	actionReportTouched = "report_touched"
	actionMergePrecheck = "merge_precheck"
	actionStatus        = "status"
)

// NewTaskboardTool builds the single taskboard agent tool. All board
// operations are dispatched through one action parameter (same style as the
// background tool and the human PATCH API), keeping the agent tool list
// compact. Protocol gates live in the ledger (done human-only, held cards
// unstealable, no delete during execution).
func NewTaskboardTool(ledger toolLedger) tools.Tool {
	return newTaskboardTool(ledger, nil)
}

// NewTaskboardToolWithExecutor is NewTaskboardTool plus the card executor for
// the dispatch action (M5 PJM): dispatch starts (or reuses) the card's
// execution session. executor may be nil in tool-only tests; dispatch then
// returns a clear "unavailable" error.
func NewTaskboardToolWithExecutor(ledger toolLedger, executor Executor) tools.Tool {
	return newTaskboardTool(ledger, executor)
}

func newTaskboardTool(ledger toolLedger, executor Executor) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard", "Cross-session project task board. Actions: list (query board), get (read one card in full — read comments before acting), create, update, move (backlog→todo→in_progress→in_review; done is human-only), comment_add, delete (soft; refused while an execution is running), checklist (add / check with evidence / uncheck), dispatch (start or reuse the card's execution session).", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "list|get|create|update|move|comment_add|delete|checklist|dispatch|observe|reconcile|recover|retry|report_touched|merge_precheck|status",
				"enum":        []string{actionList, actionGet, actionCreate, actionUpdate, actionMove, actionCommentAdd, actionDelete, actionChecklist, actionDispatch, actionObserve, actionReconcile, actionRecover, actionRetry, actionReportTouched, actionMergePrecheck, actionStatus},
			},
			"card_id":        map[string]string{"type": "string", "description": "Card id (get/update/move/comment_add/delete/checklist/dispatch/observe/reconcile/recover/retry)"},
			"version":        map[string]any{"type": "integer", "description": "Optimistic-concurrency version: pass the CURRENT version returned by create/get (not the next one). On conflict the tool auto-re-reads and retries once."},
			"project_id":     map[string]string{"type": "string", "description": "Project filter (list) or target project (create; defaults to the built-in project)"},
			"status":         map[string]string{"type": "string", "description": "Status filter (list): backlog|todo|in_progress|in_review|done"},
			"urgency":        map[string]string{"type": "string", "description": "Urgency (list filter / create): urgent|normal|low"},
			"title":          map[string]string{"type": "string", "description": "Card title (create / update)"},
			"description":    map[string]string{"type": "string", "description": "Card description (create / update)"},
			"prompt":         map[string]string{"type": "string", "description": "Execution prompt for the isolated session that will run this task (create / update)"},
			"template_id":    map[string]string{"type": "string", "description": "Agent template id for the execution session (create / update / dispatch; dispatch persists the choice on the card before opening the session; empty = card/default)"},
			"blocked":        map[string]any{"type": "boolean", "description": "Blocked flag (update)"},
			"to":             map[string]string{"type": "string", "description": "Target status (move): todo|in_progress|in_review"},
			"text":           map[string]string{"type": "string", "description": "Comment text (comment_add), single checklist item (checklist add), recovery message (recover), or batch via checklist array"},
			"check_action":   map[string]string{"type": "string", "description": "checklist sub-action: add|check|uncheck"},
			"index":          map[string]any{"type": "integer", "description": "Checklist item index (checklist check/uncheck)"},
			"evidence":       map[string]string{"type": "string", "description": "Proof note attached when checking a checklist item"},
			"checklist":      map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Acceptance criteria lines (create), or batch items for checklist add"},
			"touched_paths":  map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Package-level impact surface (create/update): e.g. [\"internal/platform/tooling\"]. Used for cross-card parallel-conflict detection (dispatch gate + merge precheck)."},
			"observed_paths": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Paths an execution session actually touched at runtime (report_touched); unioned with touched_paths for conflict detection."},
			"research": map[string]any{"type": "object", "description": "Structured investigation asset (方案A: 上下文传递; create/update): facts + locations(file:line) + excluded_paths + open_questions. Split into verified vs open-points in the execution prompt so the调研 is done once.", "properties": map[string]any{
				"facts":          map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Already-verified facts (trust; don't re-investigate)."},
				"locations":      map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Key landing points as file:line."},
				"excluded_paths": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Paths already ruled out — do not investigate."},
				"open_questions": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Points the executor must verify itself."},
			}},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args taskboardArgs) (tools.ToolResult, error) {
		return dispatchTaskboard(ctx, ledger, executor, args)
	})
}

// taskboardArgs carries the union of parameters across actions; only the
// fields relevant to the requested action are read.
type taskboardArgs struct {
	Action        string    `json:"action"`
	CardID        string    `json:"card_id"`
	ExecutionID   string    `json:"execution_id"`
	Version       *int      `json:"version"`
	ProjectID     string    `json:"project_id"`
	Status        string    `json:"status"`
	Urgency       string    `json:"urgency"`
	Title         *string   `json:"title"`
	Description   *string   `json:"description"`
	Prompt        *string   `json:"prompt"`
	Blocked       *bool     `json:"blocked"`
	TemplateID    string    `json:"template_id"`
	WorkDir       string    `json:"work_dir"`
	To            string    `json:"to"`
	Text          string    `json:"text"`
	CheckAction   string    `json:"check_action"`
	Index         *int      `json:"index"`
	Evidence      string    `json:"evidence"`
	Checklist     []string  `json:"checklist"`
	TouchedPaths  []string  `json:"touched_paths"`
	ObservedPaths []string  `json:"observed_paths"`
	Research      *Research `json:"research"`
}

// requireVersion extracts the mandatory optimistic-concurrency version.
func requireVersion(args taskboardArgs) (int, error) {
	if args.Version == nil {
		return 0, fmt.Errorf("taskboard: version is required for %s (read the card first)", args.Action)
	}
	return *args.Version, nil
}

func requireCardID(args taskboardArgs) (string, error) {
	id := strings.TrimSpace(args.CardID)
	if id == "" {
		return "", fmt.Errorf("taskboard: card_id is required for %s", args.Action)
	}
	return id, nil
}

// dispatchTaskboard routes one action to the corresponding ledger call.
// executor is optional; only the dispatch action requires it.
func dispatchTaskboard(ctx context.Context, ledger toolLedger, executor Executor, args taskboardArgs) (tools.ToolResult, error) {
	actor := actorName(ctx)
	switch strings.TrimSpace(args.Action) {
	case actionList:
		return listTaskboardCards(ledger, args)
	case actionGet:
		return getTaskboardCard(ledger, args)
	case actionCreate:
		return createTaskboardCard(ledger, args)
	case actionUpdate:
		return updateTaskboardCard(ledger, args)
	case actionMove, actionCommentAdd, actionDelete:
		return mutateTaskboardCard(ledger, actor, args)
	case actionChecklist:
		return updateTaskboardChecklist(ledger, args)
	case actionDispatch:
		return dispatchTaskboardCard(ctx, ledger, executor, args)
	case actionObserve, actionReconcile, actionRecover, actionRetry:
		return observeTaskboardExecution(ctx, executor, args)
	case actionReportTouched:
		return reportTaskboardTouchedPaths(ledger, actor, args)
	case actionMergePrecheck:
		return precheckTaskboardMerge(ledger, actor, args)
	case actionStatus:
		return statusTaskboard(ledger, args)
	default:
		return tools.ToolResult{}, fmt.Errorf("taskboard: unknown action %q", args.Action)
	}
}

// ChecklistItems returns the checklist texts for a checklist add. The batch
// (checklist array) wins when present; otherwise a single text is honored, so
// callers can add several items in one call and still use the legacy text.
func (a taskboardArgs) ChecklistItems() []string {
	if len(a.Checklist) > 0 {
		return a.Checklist
	}
	if strings.TrimSpace(a.Text) != "" {
		return []string{a.Text}
	}
	return nil
}

// writeWithRetry runs a card mutation, retrying once on an optimistic-concurrency
// version conflict (a concurrent write landed since our last read). On retry the
// fresh version is read from the ledger, so parallel writers to the same card no
// longer wedge on a stale version. Version semantics: callers pass the version
// they last read (create returns it), not the next one.
func writeWithRetry(ledger toolLedger, cardID string, initialVersion int, mut func(version int) (Card, error)) (Card, error) {
	card, err := mut(initialVersion)
	if err == nil {
		return card, nil
	}
	if !errors.Is(err, ErrVersionConflict) {
		return card, err
	}
	cur, gerr := ledger.GetCard(cardID)
	if gerr != nil {
		return card, err
	}
	return mut(cur.Version)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// writeWithRetryMerge runs a merge-style mutation (returns a card plus a
// separate report) with one optimistic-concurrency retry, mirroring
// writeWithRetry. It is used by the merge_precheck action so a concurrent
// write to the same card cannot wedge the gate-4 precheck on a stale version.
func writeWithRetryMerge(ledger toolLedger, cardID string, initialVersion int, mut func(version int) (Card, ConflictReport, error)) (Card, ConflictReport, error) {
	card, report, err := mut(initialVersion)
	if err == nil {
		return card, report, nil
	}
	if !errors.Is(err, ErrVersionConflict) {
		return card, report, err
	}
	cur, gerr := ledger.GetCard(cardID)
	if gerr != nil {
		return card, report, err
	}
	return mut(cur.Version)
}

// NewTaskboardTools keeps the historical multi-tool entry point for callers
// expecting a slice; it now contributes exactly one tool.
func NewTaskboardTools(ledger toolLedger) []tools.Tool {
	return []tools.Tool{NewTaskboardTool(ledger)}
}
