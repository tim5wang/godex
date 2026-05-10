package events

import (
	"fmt"
	"strings"
)

// Summary returns a compact progress label for a todo-list update.
func (p TodoListPayload) Summary() string {
	return fmt.Sprintf("%d/%d completed", p.Completed, p.Total)
}

// RenderPlain renders a todo-list update as a compact checklist.
func (p TodoListPayload) RenderPlain() string {
	lines := make([]string, 0, len(p.Items)+1)
	lines = append(lines, "Todo list ("+p.Summary()+")")
	for _, item := range p.Items {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		marker := "[?]"
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "completed":
			marker = "[x]"
		case "in_progress":
			marker = "[>]"
		case "pending":
			marker = "[ ]"
		}
		suffix := ""
		if strings.EqualFold(strings.TrimSpace(item.Status), "in_progress") && strings.TrimSpace(item.ActiveForm) != "" {
			suffix = " <- " + strings.TrimSpace(item.ActiveForm)
		}
		lines = append(lines, fmt.Sprintf("%s %s%s", marker, content, suffix))
	}
	return strings.Join(lines, "\n")
}
