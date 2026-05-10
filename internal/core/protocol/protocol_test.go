package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageFromResponsePreservesTextAndToolUseBlocks(t *testing.T) {
	resp := Response{Content: []Block{
		TextBlock("hello "),
		{Type: BlockType("thinking"), Text: "skip"},
		ToolUseBlock("tool-1", "bash", map[string]interface{}{"command": "pwd"}),
	}}

	msg := MessageFromResponse(resp)
	if msg.Role != RoleAssistant {
		t.Fatalf("expected assistant role, got %s", msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 supported blocks, got %d", len(msg.Content))
	}
	if msg.Content[0].Type != BlockText || msg.Content[1].Type != BlockToolUse {
		t.Fatalf("unexpected block sequence: %+v", msg.Content)
	}
	if MessageText(msg) != "hello " {
		t.Fatalf("expected text extraction to preserve text blocks, got %q", MessageText(msg))
	}
}

func TestToAPIMessagesPreservesToolResults(t *testing.T) {
	messages := []Message{
		NewTextMessage(RoleUser, "start"),
		NewMessage(RoleUser, TextBlock("result:"), ToolResultBlock("tool-1", "done")),
	}

	apiMessages := ToAPIMessages(messages)
	if len(apiMessages) != 2 {
		t.Fatalf("expected 2 api messages, got %d", len(apiMessages))
	}
	if apiMessages[1].Content[1].Type != BlockToolResult {
		t.Fatalf("expected second api message to keep tool_result block, got %+v", apiMessages[1].Content)
	}
	if apiMessages[1].Content[1].ToolUseID != "tool-1" || apiMessages[1].Content[1].Content != "done" {
		t.Fatalf("unexpected tool_result payload: %+v", apiMessages[1].Content[1])
	}
}

func TestToAPIMessagesDropsUnsupportedBlocks(t *testing.T) {
	messages := []Message{
		NewMessage(RoleAssistant,
			Block{Type: BlockType("thinking"), Text: "skip"},
			TextBlock("keep"),
		),
	}

	apiMessages := ToAPIMessages(messages)
	if len(apiMessages) != 1 {
		t.Fatalf("expected 1 api message, got %d", len(apiMessages))
	}
	if len(apiMessages[0].Content) != 1 || apiMessages[0].Content[0].Type != BlockText {
		t.Fatalf("expected unsupported blocks to be filtered, got %+v", apiMessages[0].Content)
	}
}

func TestToolUseWithEmptyInputKeepsEmptyObjectInJSON(t *testing.T) {
	req := Request{
		Model:     "test-model",
		MaxTokens: 16,
		System:    "system",
		Messages: ToAPIMessages([]Message{
			NewMessage(RoleAssistant, ToolUseBlock("tool-1", "list_mcp_resources", nil)),
			NewMessage(RoleUser, ToolResultBlock("tool-1", `{"resources":[]}`)),
		}),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"name":"list_mcp_resources"`) || !strings.Contains(got, `"input":{}`) {
		t.Fatalf("expected tool_use input to marshal as empty object, got %s", got)
	}
}

func TestMessageCloneDeepCopiesNestedToolInput(t *testing.T) {
	original := NewMessage(RoleAssistant, ToolUseBlock("tool-1", "todo_write", map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"content": "first",
				"labels":  []string{"a", "b"},
			},
		},
	}))

	cloned := original.Clone()
	items := cloned.Content[0].Input["items"].([]interface{})
	entry := items[0].(map[string]interface{})
	entry["content"] = "mutated"
	entry["labels"].([]string)[0] = "changed"

	originalItems := original.Content[0].Input["items"].([]interface{})
	originalEntry := originalItems[0].(map[string]interface{})
	if got := originalEntry["content"]; got != "first" {
		t.Fatalf("expected original nested content to stay unchanged, got %#v", got)
	}
	labels := originalEntry["labels"].([]string)
	if labels[0] != "a" {
		t.Fatalf("expected original nested slice to stay unchanged, got %#v", labels)
	}
}
