package conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
)

func TestOpenAICodexClientUsesStreamingResponsesEndpoint(t *testing.T) {
	var path string
	var auth string
	var beta string
	var originator string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		beta = r.Header.Get("OpenAI-Beta")
		originator = r.Header.Get("originator")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{
		Model:     "gpt-5.5",
		MaxTokens: 4096,
		System:    "be brief",
		Messages: []protocol.APIMessage{{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{protocol.TextBlock("hi")},
		}},
	})
	if err != nil {
		t.Fatalf("codex call: %v", err)
	}
	if path != "/responses" {
		t.Fatalf("expected /responses endpoint, got %q", path)
	}
	if auth != "Bearer test-token" {
		t.Fatalf("unexpected authorization header %q", auth)
	}
	if beta != "responses=experimental" {
		t.Fatalf("unexpected OpenAI-Beta header %q", beta)
	}
	if originator != "godex" {
		t.Fatalf("unexpected originator header %q", originator)
	}
	if got := body["model"]; got != "gpt-5.5" {
		t.Fatalf("expected model gpt-5.5, got %#v", got)
	}
	if got := body["instructions"]; got != "be brief" {
		t.Fatalf("expected system instructions, got %#v", got)
	}
	if got := body["stream"]; got != true {
		t.Fatalf("expected stream=true, got %#v", got)
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatalf("codex backend rejects max_output_tokens, got body %#v", body)
	}
	if got := protocol.BlocksText(resp.Content); got != "hello" {
		t.Fatalf("expected response text hello, got %q", got)
	}
}

func TestOpenAICodexClientForwardsPromptCacheKey(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	_, err := client.Call(context.Background(), protocol.Request{
		Model:                "gpt-5.5",
		MaxTokens:            4096,
		System:               "be brief",
		PromptCacheKey:       "session-abc",
		PromptCacheRetention: "24h",
		Messages: []protocol.APIMessage{{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{protocol.TextBlock("hi")},
		}},
	})
	if err != nil {
		t.Fatalf("codex call: %v", err)
	}
	if got := body["prompt_cache_key"]; got != "session-abc" {
		t.Fatalf("expected prompt_cache_key forwarded, got %#v", got)
	}
	if _, ok := body["prompt_cache_retention"]; ok {
		t.Fatalf("did not expect prompt_cache_retention (unsupported by codex endpoint), got %#v", body)
	}
}

func TestOpenAICodexClientOmitsPromptCacheFieldsWhenUnset(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	_, err := client.Call(context.Background(), protocol.Request{
		Model:     "gpt-5.5",
		MaxTokens: 4096,
		Messages: []protocol.APIMessage{{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{protocol.TextBlock("hi")},
		}},
	})
	if err != nil {
		t.Fatalf("codex call: %v", err)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("did not expect prompt_cache_key when unset, got %#v", body)
	}
	if _, ok := body["prompt_cache_retention"]; ok {
		t.Fatalf("did not expect prompt_cache_retention when unset, got %#v", body)
	}
}

func TestOpenAICodexClientStreamParsesResponsesEvents(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"do\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ne\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	var deltas []string
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4"}, StreamHandler{
		OnTextDelta: func(delta string) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("codex stream: %v", err)
	}
	if got := body["stream"]; got != true {
		t.Fatalf("expected stream=true, got %#v", got)
	}
	if strings.Join(deltas, "") != "done" {
		t.Fatalf("expected final text delta, got %q", strings.Join(deltas, ""))
	}
	if got := protocol.BlocksText(resp.Content); got != "done" {
		t.Fatalf("expected response text done, got %q", got)
	}
}

func TestOpenAICodexClientToolParametersAreObjects(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	_, err := client.Stream(context.Background(), protocol.Request{
		Model: "gpt-5.4-mini",
		Tools: []protocol.ToolSchema{{
			Name:        "skill",
			Description: "List available skills",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": nil,
			},
		}},
	}, StreamHandler{})
	if err != nil {
		t.Fatalf("codex stream: %v", err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool in body, got %#v", body["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected tool object, got %#v", tools[0])
	}
	parameters, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters object, got %#v in body %#v", tool["parameters"], body)
	}
	if parameters["type"] != "object" {
		t.Fatalf("expected object parameters, got %#v", parameters)
	}
	if _, ok := parameters["properties"].(map[string]any); !ok {
		t.Fatalf("expected properties object, got %#v", parameters["properties"])
	}
}

func TestOpenAICodexClientStreamSurfacesReasoningDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"plan\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"- inspect\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"ning\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"do\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ne\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	var thinking []string
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4"}, StreamHandler{
		OnThinkingDelta: func(delta, signature string) {
			if signature != "" {
				t.Fatalf("unexpected signature on reasoning delta: %q", signature)
			}
			thinking = append(thinking, delta)
		},
	})
	if err != nil {
		t.Fatalf("codex stream: %v", err)
	}
	if got := strings.Join(thinking, ""); got != "plan- inspectning" {
		t.Fatalf("expected live thinking deltas in arrival order, got %q", got)
	}
	if got := resp.ReasoningContent; got != "plan- inspectning" {
		t.Fatalf("expected accumulated reasoning content, got %q", got)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != protocol.BlockThinking {
		t.Fatalf("expected thinking block first, got %#v", resp.Content)
	}
	if got := protocol.BlocksText(resp.Content); got != "done" {
		t.Fatalf("expected answer text intact, got %q", got)
	}
}

func TestOpenAICodexClientStreamParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"command\\\"\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\":\\\"pwd\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4-mini"}, StreamHandler{})
	if err != nil {
		t.Fatalf("codex stream: %v", err)
	}
	tools := protocol.ToolUses(resp.Content)
	if len(tools) != 1 || tools[0].ID != "call_1" || tools[0].Name != "bash" || tools[0].Input["command"] != "pwd" {
		t.Fatalf("unexpected tool call: %#v", resp.Content)
	}
}
