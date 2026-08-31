package conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestOpenAIResponsesClientUsesResponsesEndpointNoCodexHeaders(t *testing.T) {
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
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "sk-test", 5*time.Second)
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
		t.Fatalf("responses call: %v", err)
	}
	if path != "/responses" {
		t.Fatalf("expected /responses endpoint, got %q", path)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("unexpected authorization header %q", auth)
	}
	// The generic Responses client must NOT send codex-only headers.
	if beta != "" {
		t.Fatalf("expected no OpenAI-Beta header on generic responses client, got %q", beta)
	}
	if originator != "" {
		t.Fatalf("expected no originator header on generic responses client, got %q", originator)
	}
	if body["max_output_tokens"] != float64(4096) {
		t.Fatalf("expected max_output_tokens=4096, got %#v", body["max_output_tokens"])
	}
	if got := protocol.BlocksText(resp.Content); got != "hi" {
		t.Fatalf("expected response text hi, got %q", got)
	}
}

func TestOpenAIResponsesClientStreamSurfacesTextDeltas(t *testing.T) {
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

	client := NewOpenAIResponsesClient(server.URL, "sk-test", 5*time.Second)
	var deltas []string
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4"}, StreamHandler{
		OnTextDelta: func(delta string) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("responses stream: %v", err)
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

func TestOpenAIResponsesClientStreamSurfacesReasoningDeltas(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"plan\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"- inspect\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "sk-test", 5*time.Second)
	var thinking []string
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4", ReasoningEffort: "medium"}, StreamHandler{
		OnThinkingDelta: func(delta, signature string) {
			thinking = append(thinking, delta)
		},
	})
	if err != nil {
		t.Fatalf("responses stream: %v", err)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "medium" || reasoning["summary"] != "auto" {
		t.Fatalf("expected reasoning {effort,summary:auto} in request, got %#v", body["reasoning"])
	}
	if got := strings.Join(thinking, ""); got != "plan- inspect" {
		t.Fatalf("expected live thinking deltas, got %q", got)
	}
	if got := resp.ReasoningContent; got != "plan- inspect" {
		t.Fatalf("expected accumulated reasoning content, got %q", got)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != protocol.BlockThinking {
		t.Fatalf("expected thinking block first, got %#v", resp.Content)
	}
	if got := protocol.BlocksText(resp.Content); got != "done" {
		t.Fatalf("expected answer text intact, got %q", got)
	}
}

func TestOpenAIResponsesClientStreamParsesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"command\\\"\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\":\\\"pwd\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "sk-test", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4-mini"}, StreamHandler{})
	if err != nil {
		t.Fatalf("responses stream: %v", err)
	}
	tools := protocol.ToolUses(resp.Content)
	if len(tools) != 1 || tools[0].ID != "call_1" || tools[0].Name != "bash" || tools[0].Input["command"] != "pwd" {
		t.Fatalf("unexpected tool call: %#v", resp.Content)
	}
}

func TestOpenAIResponsesClientToolParametersAreObjects(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "sk-test", 5*time.Second)
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
		t.Fatalf("responses stream: %v", err)
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
		t.Fatalf("expected parameters object, got %#v", tool["parameters"])
	}
	if parameters["type"] != "object" {
		t.Fatalf("expected object parameters, got %#v", parameters)
	}
	if _, ok := parameters["properties"].(map[string]any); !ok {
		t.Fatalf("expected properties object, got %#v", parameters["properties"])
	}
}

func TestOpenAIResponsesClientForwardsCacheRetention(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "sk-test", 5*time.Second)
	_, err := client.Stream(context.Background(), protocol.Request{
		Model:                "gpt-5.4",
		PromptCacheRetention: protocol.CacheRetentionLong,
	}, StreamHandler{})
	if err != nil {
		t.Fatalf("responses stream: %v", err)
	}
	if body["prompt_cache_retention"] != "24h" {
		t.Fatalf("expected prompt_cache_retention=24h, got %#v", body["prompt_cache_retention"])
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("expected no prompt_cache_key (automatic prefix caching), got %#v", body["prompt_cache_key"])
	}
}
