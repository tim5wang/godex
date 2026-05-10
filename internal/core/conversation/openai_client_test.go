package conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestOpenAIClientToolParametersAreObjects(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	_, err := client.Call(context.Background(), protocol.Request{
		Model: "deepseek-chat",
		Tools: []protocol.ToolSchema{{
			Name:        "list_memory_candidates",
			Description: "List pending durable-memory candidates",
			InputSchema: nil,
		}},
	})
	if err != nil {
		t.Fatalf("openai-compatible call: %v", err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool in body, got %#v", body["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected tool object, got %#v", tools[0])
	}
	function, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected function object, got %#v", tool["function"])
	}
	parameters, ok := function["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters object, got %#v in body %#v", function["parameters"], body)
	}
	if parameters["type"] != "object" {
		t.Fatalf("expected object parameters, got %#v", parameters)
	}
	if _, ok := parameters["properties"].(map[string]any); !ok {
		t.Fatalf("expected properties object, got %#v", parameters["properties"])
	}
}

func TestOpenAIClientPreservesReasoningContentForToolFollowUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","reasoning_content":"checked the tool plan","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_memory_candidates","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{Model: "deepseek-reasoner"})
	if err != nil {
		t.Fatalf("openai-compatible call: %v", err)
	}
	if resp.ReasoningContent != "checked the tool plan" {
		t.Fatalf("expected reasoning content on response, got %q", resp.ReasoningContent)
	}
	assistant := protocol.MessageFromResponse(*resp)
	if assistant.Metadata == nil || assistant.Metadata.ReasoningContent != "checked the tool plan" {
		t.Fatalf("expected reasoning content on assistant history message, got %+v", assistant.Metadata)
	}

	body, err := client.buildRequest(protocol.Request{
		Model:    "deepseek-reasoner",
		Messages: SanitizeMessagesForProvider(protocol.ToAPIMessages([]protocol.Message{assistant})),
	}, false)
	if err != nil {
		t.Fatalf("build follow-up request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode follow-up request: %v", err)
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) < 1 {
		t.Fatalf("expected messages, got %#v", decoded["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected message object, got %#v", messages[0])
	}
	if got := message["reasoning_content"]; got != "checked the tool plan" {
		t.Fatalf("expected reasoning_content to be passed back, got %#v in body %s", got, string(body))
	}
}
