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
	// store must stay false: the ChatGPT codex backend rejects store=true with
	// HTTP 400 (measured live), like prompt_cache_retention.
	if got := body["store"]; got != false {
		t.Fatalf("expected store=false (backend rejects true), got %#v", got)
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatalf("codex backend rejects max_output_tokens, got body %#v", body)
	}
	if got := protocol.BlocksText(resp.Content); got != "hello" {
		t.Fatalf("expected response text hello, got %q", got)
	}
}

// The ChatGPT codex backend streams reasoning only as encrypted content, so
// the client signals OnStreamStarted once and frontends show a "thinking…"
// placeholder instead of a blank wait.
func TestOpenAICodexClientSignalsStreamStarted(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning.encrypted_content\",\"encrypted_content\":\"...\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	started := 0
	if _, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.6-sol"}, StreamHandler{
		OnStreamStarted: func() { started++ },
	}); err != nil {
		t.Fatalf("codex stream: %v", err)
	}
	if started != 1 {
		t.Fatalf("expected OnStreamStarted exactly once, got %d", started)
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

// The official api.openai.com endpoint must NOT receive the codex-specific
// headers, must NOT receive prompt_cache_key (automatic prefix caching
// instead of deterministic all-or-nothing caching), and must receive
// prompt_cache_retention (the official API supports it for the codex family).
func TestOpenAICodexClientOfficialEndpointDropsCodexHeadersAndUsesAutomaticCache(t *testing.T) {
	var path string
	var beta string
	var originator string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		beta = r.Header.Get("OpenAI-Beta")
		originator = r.Header.Get("originator")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":80}}}}\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICodexClientForEndpoint(server.URL, "sk-test", 5*time.Second, true)
	resp, err := client.Call(context.Background(), protocol.Request{
		Model:                "gpt-5.1-codex",
		MaxTokens:            4096,
		PromptCacheKey:       "session-abc",
		PromptCacheRetention: "24h",
		Messages: []protocol.APIMessage{{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{protocol.TextBlock("hi")},
		}},
	})
	if err != nil {
		t.Fatalf("official codex call: %v", err)
	}
	if path != "/responses" {
		t.Fatalf("expected /responses endpoint, got %q", path)
	}
	if beta != "" {
		t.Fatalf("did not expect OpenAI-Beta header on official endpoint, got %q", beta)
	}
	if originator != "" {
		t.Fatalf("did not expect originator header on official endpoint, got %q", originator)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("did not expect prompt_cache_key on official endpoint (automatic prefix caching), got %#v", body)
	}
	if got := body["prompt_cache_retention"]; got != "24h" {
		t.Fatalf("expected prompt_cache_retention=24h forwarded on official endpoint, got %#v", body["prompt_cache_retention"])
	}
	if got := body["store"]; got != false {
		t.Fatalf("expected store=false, got %#v", got)
	}
	if resp.Usage == nil || resp.Usage.CacheReadTokens != 80 || resp.Usage.InputTokens != 20 {
		t.Fatalf("expected usage {input:20, cache_read:80}, got %#v", resp.Usage)
	}
}

// A plain OpenAI API key must not produce a chatgpt-account-id header, and an
// api.openai.com base URL is detected as official automatically.
func TestOpenAICodexClientDetectsOfficialEndpointFromBaseURL(t *testing.T) {
	if !isOfficialResponsesBaseURL("https://api.openai.com/v1") {
		t.Fatal("expected api.openai.com base URL to be detected as official")
	}
	if isOfficialResponsesBaseURL("https://chatgpt.com/backend-api/codex") {
		t.Fatal("expected chatgpt.com base URL to NOT be detected as official")
	}
	if isOfficialResponsesBaseURL("") {
		t.Fatal("expected empty base URL to NOT be detected as official")
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
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
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
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "gpt-5.4", ReasoningEffort: "medium"}, StreamHandler{
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
	// reasoning.summary:"auto" is what makes the backend emit the reasoning
	// summary deltas in the first place (pi's codex client sends the same).
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "medium" || reasoning["summary"] != "auto" {
		t.Fatalf("expected reasoning {effort,summary:auto} in request, got %#v", body["reasoning"])
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
