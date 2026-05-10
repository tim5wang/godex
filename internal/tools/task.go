package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/tim5wang/godex/internal/domain/task"
)

type taskCreateArgs struct {
	Subject     string `json:"subject"`
	Description string `json:"description,omitempty"`
}

type taskIDArgs struct {
	TaskID int `json:"task_id"`
}

type taskListArgs struct{}

type taskUpdateArgs struct {
	TaskID          int    `json:"task_id"`
	Status          string `json:"status,omitempty"`
	AddBlockedBy    []int  `json:"add_blocked_by,omitempty"`
	RemoveBlockedBy []int  `json:"remove_blocked_by,omitempty"`
}

// NewTaskCreateTool creates a new task create tool.
func NewTaskCreateTool(mgr *task.Manager) Tool {
	return NewTypedTool(NewToolSpec("task_create", "Create a persistent file-based task on the board", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"subject":     map[string]string{"type": "string"},
			"description": map[string]string{"type": "string"},
		},
		"required": []string{"subject"},
	}, nil), func(ctx context.Context, args taskCreateArgs) (ToolResult, error) {
		_ = ctx
		if strings.TrimSpace(args.Subject) == "" {
			return ToolResult{}, fmt.Errorf("missing subject argument")
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
	})
}

// NewTaskGetTool creates a new task get tool.
func NewTaskGetTool(mgr *task.Manager) Tool {
	return NewTypedTool(NewToolSpec("task_get", "Get task details by ID", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]string{"type": "integer"},
		},
		"required": []string{"task_id"},
	}, nil), func(ctx context.Context, args taskIDArgs) (ToolResult, error) {
		_ = ctx
		if args.TaskID <= 0 {
			return ToolResult{}, fmt.Errorf("missing task_id argument")
		}
		item, err := mgr.Get(args.TaskID)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Structured: item}, nil
	})
}

// NewTaskListTool creates a new task list tool.
func NewTaskListTool(mgr *task.Manager) Tool {
	return NewTypedTool(NewToolSpec("task_list", "List all tasks on the board", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args taskListArgs) (ToolResult, error) {
		_ = ctx
		return ToolResult{Structured: mgr.List()}, nil
	})
}

// NewTaskUpdateTool creates a new task update tool.
func NewTaskUpdateTool(mgr *task.Manager) Tool {
	return NewTypedTool(NewToolSpec("task_update", "Update task status or dependencies", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id":           map[string]string{"type": "integer"},
			"status":            map[string]interface{}{"type": "string", "enum": []string{string(task.StatusPending), string(task.StatusInProgress), string(task.StatusCompleted)}},
			"add_blocked_by":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}},
			"remove_blocked_by": map[string]interface{}{"type": "array", "items": map[string]string{"type": "integer"}},
		},
		"required": []string{"task_id"},
	}, nil), func(ctx context.Context, args taskUpdateArgs) (ToolResult, error) {
		_ = ctx
		if args.TaskID <= 0 {
			return ToolResult{}, fmt.Errorf("missing task_id argument")
		}
		status, err := task.ParseStatus(args.Status)
		if err != nil {
			return ToolResult{}, err
		}
		if err := mgr.Update(args.TaskID, status, args.AddBlockedBy, args.RemoveBlockedBy); err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Text: "OK"}, nil
	})
}

// NewClaimTaskTool creates a new claim task tool.
func NewClaimTaskTool(mgr *task.Manager) Tool {
	return NewTypedTool(NewToolSpec("claim_task", "Claim a task from the board to work on", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]string{"type": "integer"},
		},
		"required": []string{"task_id"},
	}, nil), func(ctx context.Context, args taskIDArgs) (ToolResult, error) {
		_ = ctx
		if args.TaskID <= 0 {
			return ToolResult{}, fmt.Errorf("missing task_id argument")
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
	})
}
