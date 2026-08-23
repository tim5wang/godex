package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUICardToolValidKinds(t *testing.T) {
	tool := NewUICardTool()
	if tool.Name() != "ui_card" {
		t.Fatalf("expected snake_case tool name, got %q", tool.Name())
	}
	for _, kind := range []string{"form", "button_group", "card"} {
		result, err := tool.Execute(context.Background(), map[string]interface{}{"kind": kind, "title": "T"})
		if err != nil {
			t.Fatalf("kind %s: unexpected error: %v", kind, err)
		}
		if !strings.Contains(result, kind) {
			t.Fatalf("kind %s: expected structured JSON echo containing kind, got %q", kind, result)
		}
	}
}

func TestUICardToolRejectsUnknownKind(t *testing.T) {
	tool := NewUICardTool()
	_, err := tool.Execute(context.Background(), map[string]interface{}{"kind": "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestUICardToolFormEcho(t *testing.T) {
	tool := NewUICardTool()
	input := map[string]interface{}{
		"kind": "form",
		"fields": []interface{}{
			map[string]interface{}{"name": "repo", "label": "Repository", "type": "text", "required": true},
		},
	}
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var back struct {
		Kind   string `json:"kind"`
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(result), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Kind != "form" || len(back.Fields) != 1 || back.Fields[0].Name != "repo" {
		t.Fatalf("unexpected echo: %s", result)
	}
}
