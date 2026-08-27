package taskboard

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/tools"
)

// Ledger is the read/write face the agent tools use (subset of *Ledger, kept
// as an interface for tests).
type toolLedger interface {
	ListCards(filter CardFilter) []Card
	GetCard(id string) (Card, error)
	CreateCard(input CreateCardInput) (Card, error)
	UpdateCard(id string, ifVersion int, actor string, input UpdateCardInput) (Card, error)
	MoveCard(id string, ifVersion int, to, actor string) (Card, error)
	CompleteCard(id string, ifVersion int, actor string, force bool) (Card, error)
	AddComment(id string, ifVersion int, author, text string) (Card, error)
	ChecklistAdd(id string, ifVersion int, actor, text string) (Card, error)
	ChecklistCheck(id string, ifVersion int, actor string, index int, evidence string) (Card, error)
	ChecklistUncheck(id string, ifVersion int, actor string, index int) (Card, error)
	SoftDeleteCard(id string, ifVersion int, actor string) (Card, error)
	ListProjects() []Project
}

// compactCard is the terse per-card view used by taskboard_list.
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

// NewTaskboardTools builds the taskboard_* agent toolset against a ledger.
// Protocol gates live in the ledger (done human-only, held cards unstealable,
// no delete during execution) — the tools are thin input adapters.
func NewTaskboardTools(ledger toolLedger) []tools.Tool {
	return []tools.Tool{
		newTaskboardListTool(ledger),
		newTaskboardGetTool(ledger),
		newTaskboardCreateTool(ledger),
		newTaskboardUpdateTool(ledger),
		newTaskboardMoveTool(ledger),
		newTaskboardCommentTool(ledger),
		newTaskboardDeleteTool(ledger),
		newTaskboardChecklistTool(ledger),
	}
}

type listArgs struct {
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Urgency   string `json:"urgency"`
}

func newTaskboardListTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_list", "List task board cards (cross-session project board). Compact summaries with id/status/urgency/holder.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id": map[string]string{"type": "string", "description": "Optional project filter"},
			"status":     map[string]string{"type": "string", "description": "Optional status filter: backlog|todo|in_progress|in_review|done"},
			"urgency":    map[string]string{"type": "string", "description": "Optional urgency filter: urgent|normal|low"},
		},
	}, nil), func(ctx context.Context, args listArgs) (tools.ToolResult, error) {
		_ = ctx
		cards := ledger.ListCards(CardFilter{ProjectID: args.ProjectID, Status: strings.TrimSpace(args.Status), Urgency: strings.TrimSpace(args.Urgency)})
		out := make([]compactCard, 0, len(cards))
		for _, card := range cards {
			out = append(out, compact(card))
		}
		return tools.ToolResult{Structured: map[string]any{"cards": out, "count": len(out)}}, nil
	})
}

type getArgs struct {
	CardID string `json:"card_id"`
}

func newTaskboardGetTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_get", "Read one task card in full: description, prompt, comments (latest requirements — read before acting), checklist, execution records.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"card_id": map[string]string{"type": "string"},
		},
		"required": []string{"card_id"},
	}, nil), func(ctx context.Context, args getArgs) (tools.ToolResult, error) {
		_ = ctx
		card, err := ledger.GetCard(strings.TrimSpace(args.CardID))
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": card}}, nil
	})
}

type createArgs struct {
	ProjectID   string   `json:"project_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Urgency     string   `json:"urgency"`
	Checklist   []string `json:"checklist"`
}

func newTaskboardCreateTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_create", "Create a task card on the project board (starts in backlog).", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id":  map[string]string{"type": "string", "description": "Optional; defaults to the built-in project"},
			"title":       map[string]string{"type": "string"},
			"description": map[string]string{"type": "string"},
			"prompt":      map[string]string{"type": "string", "description": "Execution prompt for the agent session that will run this task"},
			"urgency":     map[string]string{"type": "string", "description": "urgent|normal|low (default normal)"},
			"checklist":   map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "Acceptance criteria (DoD) lines"},
		},
		"required": []string{"title"},
	}, nil), func(ctx context.Context, args createArgs) (tools.ToolResult, error) {
		_ = ctx
		card, err := ledger.CreateCard(CreateCardInput{
			ProjectID:   strings.TrimSpace(args.ProjectID),
			Title:       args.Title,
			Description: args.Description,
			Prompt:      args.Prompt,
			Urgency:     args.Urgency,
			Checklist:   args.Checklist,
			CreatedBy:   agentActor,
		})
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil
	})
}

type updateArgs struct {
	CardID      string  `json:"card_id"`
	Version     int     `json:"version"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Prompt      *string `json:"prompt"`
	Urgency     *string `json:"urgency"`
	Blocked     *bool   `json:"blocked"`
}

func newTaskboardUpdateTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_update", "Edit a card's title/description/prompt/urgency/blocked flag. Requires the version you read (optimistic concurrency).", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"card_id":     map[string]string{"type": "string"},
			"version":     map[string]any{"type": "integer", "description": "Version from your last read"},
			"title":       map[string]string{"type": "string"},
			"description": map[string]string{"type": "string"},
			"prompt":      map[string]string{"type": "string"},
			"urgency":     map[string]string{"type": "string"},
			"blocked":     map[string]any{"type": "boolean"},
		},
		"required": []string{"card_id", "version"},
	}, nil), func(ctx context.Context, args updateArgs) (tools.ToolResult, error) {
		_ = ctx
		card, err := ledger.UpdateCard(strings.TrimSpace(args.CardID), args.Version, agentActor, UpdateCardInput{
			Title: args.Title, Description: args.Description, Prompt: args.Prompt, Urgency: args.Urgency, Blocked: args.Blocked,
		})
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil
	})
}

type moveArgs struct {
	CardID  string `json:"card_id"`
	Version int    `json:"version"`
	To      string `json:"to"`
}

func newTaskboardMoveTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_move", "Move a card: backlog→todo→in_progress→in_review. Gates: done is human-only (submit to in_review instead); a card held by another owner cannot be claimed.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"card_id": map[string]string{"type": "string"},
			"version": map[string]any{"type": "integer"},
			"to":      map[string]string{"type": "string", "description": "todo|in_progress|in_review (done requires human acceptance)"},
		},
		"required": []string{"card_id", "version", "to"},
	}, nil), func(ctx context.Context, args moveArgs) (tools.ToolResult, error) {
		_ = ctx
		card, err := ledger.MoveCard(strings.TrimSpace(args.CardID), args.Version, strings.TrimSpace(args.To), agentActor)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil
	})
}

type commentArgs struct {
	CardID  string `json:"card_id"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}

func newTaskboardCommentTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_comment_add", "Append a comment to a card (handover notes, risks, progress). Humans and other agents read comments as the latest requirements.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"card_id": map[string]string{"type": "string"},
			"version": map[string]any{"type": "integer"},
			"text":    map[string]string{"type": "string"},
		},
		"required": []string{"card_id", "version", "text"},
	}, nil), func(ctx context.Context, args commentArgs) (tools.ToolResult, error) {
		_ = ctx
		card, err := ledger.AddComment(strings.TrimSpace(args.CardID), args.Version, agentActor, args.Text)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version}}, nil
	})
}

type deleteArgs struct {
	CardID  string `json:"card_id"`
	Version int    `json:"version"`
}

func newTaskboardDeleteTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_delete", "Soft-delete a card (refused while an execution is running).", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"card_id": map[string]string{"type": "string"},
			"version": map[string]any{"type": "integer"},
		},
		"required": []string{"card_id", "version"},
	}, nil), func(ctx context.Context, args deleteArgs) (tools.ToolResult, error) {
		_ = ctx
		card, err := ledger.SoftDeleteCard(strings.TrimSpace(args.CardID), args.Version, agentActor)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: map[string]any{"card_id": card.ID, "deleted": true}}, nil
	})
}

type checklistArgs struct {
	CardID   string `json:"card_id"`
	Version  int    `json:"version"`
	Action   string `json:"action"`
	Text     string `json:"text"`
	Index    *int   `json:"index"`
	Evidence string `json:"evidence"`
}

func newTaskboardChecklistTool(ledger toolLedger) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("taskboard_checklist", "Manage a card's acceptance checklist: action=add (text), check (index + evidence note), uncheck (index).", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"card_id":  map[string]string{"type": "string"},
			"version":  map[string]any{"type": "integer"},
			"action":   map[string]string{"type": "string", "description": "add|check|uncheck"},
			"text":     map[string]string{"type": "string", "description": "Item text (add)"},
			"index":    map[string]any{"type": "integer", "description": "Item index (check/uncheck)"},
			"evidence": map[string]string{"type": "string", "description": "Proof note attached when checking (check)"},
		},
		"required": []string{"card_id", "version", "action"},
	}, nil), func(ctx context.Context, args checklistArgs) (tools.ToolResult, error) {
		_ = ctx
		id := strings.TrimSpace(args.CardID)
		var card Card
		var err error
		switch strings.TrimSpace(args.Action) {
		case "add":
			card, err = ledger.ChecklistAdd(id, args.Version, agentActor, args.Text)
		case "check":
			if args.Index == nil {
				return tools.ToolResult{}, fmt.Errorf("taskboard: check requires item index")
			}
			card, err = ledger.ChecklistCheck(id, args.Version, agentActor, *args.Index, args.Evidence)
		case "uncheck":
			if args.Index == nil {
				return tools.ToolResult{}, fmt.Errorf("taskboard: uncheck requires item index")
			}
			card, err = ledger.ChecklistUncheck(id, args.Version, agentActor, *args.Index)
		default:
			return tools.ToolResult{}, fmt.Errorf("taskboard: unknown checklist action %q", args.Action)
		}
		if err != nil {
			return tools.ToolResult{}, err
		}
		done, total := card.ChecklistProgress()
		return tools.ToolResult{Structured: map[string]any{"card": compact(card), "version": card.Version, "checklist_done": done, "checklist_total": total}}, nil
	})
}
