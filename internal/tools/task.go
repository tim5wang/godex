package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/domain/task"
)

// NewTaskTool creates a unified task board tool (create / get / list / update / claim).
type taskToolArgs struct {
	Action          string `json:"action"`
	TaskID          int    `json:"task_id,omitempty"`
	Subject         string `json:"subject,omitempty"`
	Description     string `json:"description,omitempty"`
	Status          string `json:"status,omitempty"`
	AddBlockedBy    []int  `json:"add_blocked_by,omitempty"`
	RemoveBlockedBy []int  `json:"remove_blocked_by,omitempty"`
}

func NewTaskTool(mgr *task.Manager) Tool {
	return NewTypedTool(NewToolSpec("task", "Manage persistent task board. action=create: create task. action=get: get by id. action=list: list all. action=update: change status/deps. action=claim: claim and start work.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":            map[string]interface{}{"type": "string", "enum": []string{"create", "get", "list", "update", "claim"}},
			"task_id":           map[string]string{"type": "integer"},
			"subject":           map[string]string{"type": "string"},
			"description":       map[string]string{"type": "string"},
			"status":            map[string]interface{}{"type": "string", "enum": []string{string(task.StatusPending), string(task.StatusInProgress), string(task.StatusCompleted)}},
			"add_blocked_by":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}},
			"remove_blocked_by": map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}},
		},
		"required": []string{"action"},
	}, nil), func(ctx context.Context, args taskToolArgs) (ToolResult, error) {
		_ = ctx
		switch args.Action {
		case "create":
			if strings.TrimSpace(args.Subject) == "" {
				return ToolResult{}, fmt.Errorf("missing subject for create action")
			}
			item, err := mgr.Create(args.Subject, args.Description)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"task_id": item.ID,
				"subject": item.Subject,
				"status":  item.Status,
			}}, nil

		case "get":
			if args.TaskID <= 0 {
				return ToolResult{}, fmt.Errorf("missing task_id for get action")
			}
			item, err := mgr.Get(args.TaskID)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: item}, nil

		case "list":
			return ToolResult{Structured: mgr.List()}, nil

		case "update":
			if args.TaskID <= 0 {
				return ToolResult{}, fmt.Errorf("missing task_id for update action")
			}
			status, err := task.ParseStatus(args.Status)
			if err != nil {
				return ToolResult{}, err
			}
			if err := mgr.Update(args.TaskID, status, args.AddBlockedBy, args.RemoveBlockedBy); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Text: "OK"}, nil

		case "claim":
			if args.TaskID <= 0 {
				return ToolResult{}, fmt.Errorf("missing task_id for claim action")
			}
			item, err := mgr.ClaimPending(args.TaskID)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Structured: map[string]interface{}{
				"task_id":     item.ID,
				"subject":     item.Subject,
				"description": item.Description,
				"status":      task.StatusInProgress,
			}}, nil

		default:
			return ToolResult{}, fmt.Errorf("unknown action: %s. Valid actions: create, get, list, update, claim", args.Action)
		}
	})
}
