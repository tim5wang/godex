package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/domain/todo"
)

func TestTodoWriteToolReplacesEntireList(t *testing.T) {
	manager := todo.NewManager(t.TempDir())
	if _, err := manager.Add("old item", "old item"); err != nil {
		t.Fatalf("seed todo: %v", err)
	}

	tool := NewTodoWriteTool(manager)
	if tool.Name() != "todo_write" {
		t.Fatalf("expected snake_case tool name, got %q", tool.Name())
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"content": "first", "status": "completed", "active_form": "first"},
			map[string]interface{}{"content": "second", "status": "in_progress", "active_form": "working second"},
		},
	})
	if err != nil {
		t.Fatalf("todo write execute: %v", err)
	}

	items := manager.List()
	if len(items) != 2 {
		t.Fatalf("expected 2 todos after replace, got %d", len(items))
	}
	if items[0].ID != 1 || items[1].ID != 2 {
		t.Fatalf("expected sequential ids after replace, got %+v", items)
	}
	if items[0].Status != todo.StatusCompleted || items[1].Status != todo.StatusInProgress {
		t.Fatalf("unexpected statuses after replace: %+v", items)
	}
	if strings.Contains(result, "old item") {
		t.Fatalf("expected old todo to be replaced, got %q", result)
	}
	if !strings.Contains(result, "[x] first") || !strings.Contains(result, "[>] second <- working second") {
		t.Fatalf("expected rendered replacement todos, got %q", result)
	}
}

func TestTodoWriteToolAcceptsTodosAlias(t *testing.T) {
	manager := todo.NewManager(t.TempDir())
	tool := NewTodoWriteTool(manager)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{"content": "alias item", "status": "in_progress", "active_form": "Working alias item"},
		},
	})
	if err != nil {
		t.Fatalf("todo write execute with todos alias: %v", err)
	}
	if !strings.Contains(result, "[>] alias item <- Working alias item") {
		t.Fatalf("expected alias todo to render, got %q", result)
	}
}

func TestTodoWriteToolSpecEnforcesProactiveUsage(t *testing.T) {
	manager := todo.NewManager(t.TempDir())
	tool := NewTodoWriteTool(manager)
	desc := strings.ToLower(tool.Spec().Description)
	musts := []string{
		"proactively",
		"in_progress",
		"completed",
		"before starting",
	}
	for _, phrase := range musts {
		if !strings.Contains(desc, phrase) {
			t.Fatalf("todo_write description must include %q to enforce proactive updates, got: %q", phrase, tool.Spec().Description)
		}
	}
}

func TestTodoWriteToolAcceptsJSONStringArray(t *testing.T) {
	manager := todo.NewManager(t.TempDir())
	tool := NewTodoWriteTool(manager)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"items": `[{"content":"json item","status":"pending","active_form":"Adding json item"}]`,
	})
	if err != nil {
		t.Fatalf("todo write execute with JSON string array: %v", err)
	}
	if !strings.Contains(result, "[ ] json item") {
		t.Fatalf("expected JSON string todo to render, got %q", result)
	}
}
