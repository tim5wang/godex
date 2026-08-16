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
			Name:        "memory",
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

func TestOpenAIClientStrictToolParametersDisallowExtraFields(t *testing.T) {
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
		Model: "gpt-test",
		Tools: []protocol.ToolSchema{{
			Name:        "write_file",
			Description: "Write a file",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string"},
					"content": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path", "content"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("openai-compatible call: %v", err)
	}

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool in body, got %#v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	function := tool["function"].(map[string]any)
	if function["strict"] != true {
		t.Fatalf("expected strict tool schema, got %#v", function)
	}
	parameters := function["parameters"].(map[string]any)
	if parameters["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %#v", parameters)
	}
}

func TestOpenAIClientPreservesReasoningContentForToolFollowUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","reasoning_content":"checked the tool plan","tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
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

	resultMsg := protocol.NewMessage(protocol.RoleUser, protocol.ToolResultBlock("call_1", `{"ok":true}`))
	body, err := client.buildRequest(protocol.Request{
		Model:    "deepseek-reasoner",
		Messages: SanitizeMessagesForProvider(protocol.ToAPIMessages([]protocol.Message{assistant, resultMsg})),
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

func TestOpenAIClientParsesTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":5,
				"prompt_tokens_details":{"cached_tokens":3},
				"completion_tokens_details":{"reasoning_tokens":2}
			}
		}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("openai-compatible call: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected token usage")
	}
	// prompt_tokens (12) includes cached_tokens (3); the protocol layer
	// normalizes InputTokens to the uncached portion (12-3=9).
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 5 || resp.Usage.CacheReadTokens != 3 || resp.Usage.Estimated {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

// TestOpenAIClientParsesDeepSeekCacheUsage covers DeepSeek-style usage
// payloads that report cache hit/miss as top-level
// prompt_cache_hit_tokens / prompt_cache_miss_tokens instead of the
// OpenAI *_details sub-object. The protocol layer maps hit →
// CacheReadTokens and miss → InputTokens (the uncached input), so the
// session cache hit rate matches the supplier dashboard.
func TestOpenAIClientParsesDeepSeekCacheUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":213756,
				"completion_tokens":120,
				"prompt_cache_hit_tokens":172872,
				"prompt_cache_miss_tokens":40884
			}
		}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{Model: "deepseek-test"})
	if err != nil {
		t.Fatalf("deepseek-style call: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected token usage")
	}
	if resp.Usage.CacheReadTokens != 172872 {
		t.Fatalf("expected cache read 172872, got %d", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.InputTokens != 40884 {
		t.Fatalf("expected uncached input 40884, got %d", resp.Usage.InputTokens)
	}
	// Hit rate must match the supplier dashboard: 172872/(172872+40884).
	hitRate := float64(resp.Usage.CacheReadTokens) / float64(resp.Usage.InputTokens+resp.Usage.CacheReadTokens) * 100
	if hitRate < 80.8 || hitRate > 81.0 {
		t.Fatalf("expected hit rate ~80.9%%, got %.2f", hitRate)
	}
}
