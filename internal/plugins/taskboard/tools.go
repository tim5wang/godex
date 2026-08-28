package taskboard

import (
	"context"
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
	ChecklistAdd(id string, ifVersion int, actor, text string) (Card, error)
	ChecklistCheck(id string, ifVersion int, actor string, index int, evidence string) (Card, error)
	ChecklistUncheck(id string, ifVersion int, actor string, index int) (Card, error)
	SoftDeleteCard(id string, ifVersion int, actor string) (Card, error)
	ListProjects() []Project
}

// compactCard is the terse per-card view used by list/get results.
type compactCard struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Urgency  string `json:"urgency"`
	Project  string `json:"project"`
	Holder   string `json:"holder,omitempty"`
	Blocked  bool   `json:"blocked,omitempty"`
	CheckDn  int    `json:"checklist_done,omitempty"`
	CheckAll int    `json:"checklist_total,omitempty"`
}

func compact(c Card) compactCard {
	done, total := c.ChecklistProgress()
	return compactCard{
		ID: c.ID, Title: c.Title, Status: c.Status, Urgency: c.Urgency,
		Project: c.ProjectID, Holder: c.Holder, Blocked: c.Blocked,
		CheckDn: done, CheckAll: total,
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
	actionList       = "list"
	actionGet        = "get"
	actionCreate     = "create"
	actionUpdate     = "update"
	actionMove       = "move"
	actionCommentAdd = "comment_add"
	actionDelete     = "delete"
	actionChecklist  = "checklist"
)

// NewTaskboardTool builds the single taskboard agent tool. All board
// operations are dispatched through one action parameter (same style as the
// background tool and the human PATCH API), keeping the agent tool list
// compact. Protocol gates live in the ledger (done human-only, held cards
// unstealable, no delete during execution).
func NewTaskboardTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard", "Cross-session project task board. Actions: list (query board), get (read one card in full — read comments before acting), create, update, move (backlog→todo→in_progress→in_review; done is human-only), comment_add, delete (soft; refused while an execution is running), checklist (add / check with evidence / uncheck).", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "list|get|create|update|move|comment_add|delete|checklist",
				"enum":        []string{actionList, actionGet, actionCreate, actionUpdate, actionMove, actionCommentAdd, actionDelete, actionChecklist},
			},
			"card_id":      map[string]string{"type": "string", "description": "Card id (get/update/move/comment_add/delete/checklist)"},
			"version":      map[string]any{"type": "integer", "description": "Optimistic-concurrency version from your last read (all writes)"},
			"project_id":   map[string]string{"type": "string", "description": "Project filter (list) or target project (create; defaults to the built-in project)"},
			"status":       map[string]string{"type": "string", "description": "Status filter (list): backlog|todo|in_progress|in_review|done"},
			"urgency":      map[string]string{"type": "string", "description": "Urgency (list filter / create): urgent|normal|low"},
			"title":        map[string]string{"type": "string", "description": "Card title (create / update)"},
			"description":  map[string]string{"type": "string", "description": "Card description (create / update)"},
			"prompt":       map[string]string{"type": "string", "description": "Execution prompt for the isolated session that will run this task (create / update)"},
			"blocked":      map[string]any{"type": "boolean", "description": "Blocked flag (update)"},
			"to":           map[string]string{"type": "string", "description": "Target status (move): todo|in_progress|in_review"},
			"text":         map[string]string{"type": "string", "description": "Comment text (comment_add) or checklist item text (checklist add)"},
			"check_action": map[string]string{"type": "string", "description": "checklist sub-action: add|check|uncheck"},
			"index":        map[string]any{"type": "integer", "description": "Checklist item index (checklist check/uncheck)"},
			"evidence":     map[string]string{"type": "string", "description": "Proof note attached when checking a checklist item"},
			"checklist":    map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Acceptance criteria lines (create)"},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args taskboardArgs) (tools.ToolResult, error) {
		return dispatchTaskboard(ctx, ledger, args)
	})
}

// taskboardArgs carries the union of parameters across actions; only the
// fields relevant to the requested action are read.
type taskboardArgs struct {
	Action      string   `json:"action"`
	CardID      string   `json:"card_id"`
	Version     *int     `json:"version"`
	ProjectID   string   `json:"project_id"`
	Status      string   `json:"status"`
	Urgency     string   `json:"urgency"`
	Title       *string  `json:"title"`
	Description *string  `json:"description"`
	Prompt      *string  `json:"prompt"`
	Blocked     *bool    `json:"blocked"`
	To          string   `json:"to"`
	Text        string   `json:"text"`
	CheckAction string   `json:"check_action"`
	Index       *int     `json:"index"`
	Evidence    string   `json:"evidence"`
	Checklist   []string `json:"checklist"`
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
func dispatchTaskboard(ctx context.Context, ledger toolLedger, args taskboardArgs) (tools.ToolResult, error) {
	actor := actorName(ctx)
	switch strings.TrimSpace(args.Action) {
	case actionList:
		cards := ledger.ListCards(CardFilter{
			ProjectID: strings.TrimSpace(args.ProjectID),
			Status:    strings.TrimSpace(args.Status),
			Urgency:   strings.TrimSpace(args.Urgency),
		})
		out := make([]compactCard, 0, len(cards))
		for _, card := range cards {
			out = append(out, compact(card))
		}
		return tools.ToolResult{Structured: map[string]any{"cards": out, "count": len(out)}}, nil

	case actionGet:
		id, err := requireCardID(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		card, err := ledger.GetCard(id)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": card}}, nil

	case actionCreate:
		title := strings.TrimSpace(derefString(args.Title))
		if title == "" {
			return tools.ToolResult{}, fmt.Errorf("taskboard: title is required for create")
		}
		card, err := ledger.CreateCard(CreateCardInput{
			ProjectID:   strings.TrimSpace(args.ProjectID),
			Title:       title,
			Description: derefString(args.Description),
			Prompt:      derefString(args.Prompt),
			Urgency:     args.Urgency,
			Checklist:   args.Checklist,
			CreatedBy:   agentActor,
		})
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil

	case actionUpdate:
		id, err := requireCardID(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		version, err := requireVersion(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		var urgencyPtr *string
		if u := strings.TrimSpace(args.Urgency); u != "" {
			urgencyPtr = &u
		}
		card, err := ledger.UpdateCard(id, version, agentActor, UpdateCardInput{
			Title: args.Title, Description: args.Description, Prompt: args.Prompt, Urgency: urgencyPtr, Blocked: args.Blocked,
		})
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil

	case actionMove:
		id, err := requireCardID(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		version, err := requireVersion(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		card, err := ledger.MoveCard(id, version, strings.TrimSpace(args.To), actor)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil

	case actionCommentAdd:
		id, err := requireCardID(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		version, err := requireVersion(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		card, err := ledger.AddComment(id, version, agentActor, args.Text)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil

	case actionDelete:
		id, err := requireCardID(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		version, err := requireVersion(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		card, err := ledger.SoftDeleteCard(id, version, agentActor)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card_id": card.ID, "deleted": true}}, nil

	case actionChecklist:
		id, err := requireCardID(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		version, err := requireVersion(args)
		if err != nil {
			return tools.ToolResult{}, err
		}
		var card Card
		switch strings.TrimSpace(args.CheckAction) {
		case "add":
			card, err = ledger.ChecklistAdd(id, version, agentActor, args.Text)
		case "check":
			if args.Index == nil {
				return tools.ToolResult{}, fmt.Errorf("taskboard: check requires item index")
			}
			card, err = ledger.ChecklistCheck(id, version, agentActor, *args.Index, args.Evidence)
		case "uncheck":
			if args.Index == nil {
				return tools.ToolResult{}, fmt.Errorf("taskboard: uncheck requires item index")
			}
			card, err = ledger.ChecklistUncheck(id, version, agentActor, *args.Index)
		default:
			return tools.ToolResult{}, fmt.Errorf("taskboard: unknown checklist action %q", args.CheckAction)
		}
		if err != nil {
			return tools.ToolResult{}, err
		}
		done, total := card.ChecklistProgress()
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version, "checklist_done": done, "checklist_total": total}}, nil

	default:
		return tools.ToolResult{}, fmt.Errorf("taskboard: unknown action %q", args.Action)
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// NewTaskboardTools keeps the historical multi-tool entry point for callers
// expecting a slice; it now contributes exactly one tool.
func NewTaskboardTools(ledger toolLedger) []tools.Tool {
	return []tools.Tool{NewTaskboardTool(ledger)}
}
