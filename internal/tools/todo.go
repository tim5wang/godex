package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/domain/todo"
)

type todoWriteItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form"`
}

type todoWriteArgs struct {
	Items []todoWriteItem `json:"items"`
}

type todoListArgs struct{}

// NewTodoWriteTool creates a new todo write tool.
func NewTodoWriteTool(mgr *todo.Manager) Tool {
	return NewTypedTool(NewToolSpec("todo_write", "Update the todo list for the current task. Use this tool proactively and often to track progress. The first action of a multi-step task must be a todo_write that lists every step in order. After finishing any sub-step, immediately call todo_write again to mark that item completed and set the next pending item to in_progress before starting the next action. The list must always contain exactly one in_progress item while work is in progress; never leave a finished item as in_progress, and never advance to the next item without first marking the previous one completed. To update, send the full updated list with items: [{content,status,active_form}]. Use the items array directly; do not wrap the list in a JSON string object.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"items": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content":     map[string]string{"type": "string"},
						"status":      map[string]interface{}{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
						"active_form": map[string]string{"type": "string"},
					},
					"required": []string{"content", "status", "active_form"},
				},
			},
		},
		"required": []string{"items"},
	}, map[string]string{"todos": "items"}), func(ctx context.Context, args todoWriteArgs) (ToolResult, error) {
		_ = ctx
		if len(args.Items) == 0 {
			return ToolResult{}, fmt.Errorf("missing items argument")
		}
		if len(args.Items) > 20 {
			return ToolResult{}, fmt.Errorf("max 20 todos allowed")
		}

		now := time.Now()
		items := make([]todo.Item, 0, len(args.Items))
		inProgressCount := 0
		for i, item := range args.Items {
			content := strings.TrimSpace(item.Content)
			if content == "" {
				return ToolResult{}, fmt.Errorf("item %d: content required", i)
			}

			statusStr := item.Status
			if statusStr == "" {
				statusStr = string(todo.StatusPending)
			}
			status := todo.Status(statusStr)
			if status != todo.StatusPending && status != todo.StatusInProgress && status != todo.StatusCompleted {
				return ToolResult{}, fmt.Errorf("item %d: invalid status '%s'", i, statusStr)
			}

			activeText := item.ActiveForm
			if activeText == "" {
				activeText = content
			}
			if status == todo.StatusInProgress {
				inProgressCount++
			}

			items = append(items, todo.Item{
				Content:    content,
				Status:     status,
				ActiveForm: activeText,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}
		if inProgressCount > 1 {
			return ToolResult{}, fmt.Errorf("only one in_progress allowed")
		}

		replaced, err := mgr.Replace(items)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Text: renderTodos(replaced)}, nil
	})
}

// NewTodoListTool creates a new todo list tool.
func NewTodoListTool(mgr *todo.Manager) Tool {
	return NewTypedTool(NewToolSpec("todo_list", "List all todo items", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args todoListArgs) (ToolResult, error) {
		_ = ctx
		return ToolResult{Text: mgr.Render()}, nil
	})
}

func renderTodos(items []todo.Item) string {
	if len(items) == 0 {
		return "No todos."
	}
	lines := make([]string, 0, len(items)+1)
	done := 0
	for _, item := range items {
		status := "[?]"
		switch item.Status {
		case todo.StatusCompleted:
			status = "[x]"
			done++
		case todo.StatusInProgress:
			status = "[>]"
		case todo.StatusPending:
			status = "[ ]"
		}
		suffix := ""
		if item.Status == todo.StatusInProgress {
			suffix = " <- " + item.ActiveForm
		}
		lines = append(lines, fmt.Sprintf("%s %s%s", status, item.Content, suffix))
	}
	lines = append(lines, fmt.Sprintf("\n(%d/%d completed)", done, len(items)))
	return strings.Join(lines, "\n")
}
