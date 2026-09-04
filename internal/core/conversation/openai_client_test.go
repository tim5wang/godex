package conversation

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
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

// TestOpenAIClientStreamIncludesUsageCovers the Volcengine ARK / GLM-style
// endpoints that omit usage from streaming chunks by default: godex must send
// stream_options.include_usage=true so the provider emits a final usage chunk
// (with cached_tokens) that the stream parser can surface. Without it godex
// would report a 0% cache hit rate even though the server caches well.
func TestOpenAIClientStreamIncludesUsage(t *testing.T) {
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		requestBody = buf.String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hello!\",\"role\":\"assistant\"},\"finish_reason\":null,\"index\":0}],\"usage\":null}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"\",\"role\":\"assistant\"},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":null}\n\n" +
				"data: {\"usage\":{\"prompt_tokens\":6956,\"completion_tokens\":16,\"total_tokens\":6972,\"prompt_tokens_details\":{\"cached_tokens\":6912},\"completion_tokens_details\":{\"reasoning_tokens\":0}}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "glm-5.3"}, StreamHandler{})
	if err != nil {
		t.Fatalf("openai-compatible stream: %v", err)
	}
	// The request must ask the provider to include usage in the stream tail.
	if !strings.Contains(requestBody, `"stream_options":{"include_usage":true}`) {
		t.Fatalf("expected stream_options.include_usage in request body, got: %s", requestBody)
	}
	if resp.Usage == nil {
		t.Fatal("expected usage captured from the stream usage chunk")
	}
	// prompt_tokens (6956) includes cached_tokens (6912); protocol usage
	// normalizes InputTokens to the uncached portion.
	if resp.Usage.CacheReadTokens != 6912 || resp.Usage.InputTokens != 44 || resp.Usage.OutputTokens != 16 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "Hello!" {
		t.Fatalf("unexpected content: %+v", resp.Content)
	}
}

// TestOpenAIClientStreamFallbackWithoutStreamOptions covers providers that
// reject stream_options with HTTP 400: the client must retry once without it
// so requests keep working (only usage observability is lost).
func TestOpenAIClientStreamFallbackWithoutStreamOptions(t *testing.T) {
	var attempts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		body := buf.String()
		attempts = append(attempts, body)
		if strings.Contains(body, `"stream_options"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown field stream_options","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\",\"role\":\"assistant\"},\"finish_reason\":null,\"index\":0}],\"usage\":null}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"\",\"role\":\"assistant\"},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":null}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "strict-provider"}, StreamHandler{})
	if err != nil {
		t.Fatalf("openai-compatible stream with fallback: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts (with + without stream_options), got %d", len(attempts))
	}
	if !strings.Contains(attempts[0], `"stream_options"`) {
		t.Fatalf("first attempt should include stream_options")
	}
	if strings.Contains(attempts[1], `"stream_options"`) {
		t.Fatalf("retry attempt should drop stream_options, got: %s", attempts[1])
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "ok" {
		t.Fatalf("unexpected content after fallback: %+v", resp.Content)
	}
}

// TestOpenAIClientAlwaysEmitsContentField guards against a 400 class seen on
// strict OpenAI-compatible gateways (Volcengine ARK, AIS gateway): messages
// whose content is omitted entirely (pure tool-call assistant turns, empty
// tool results) fail deserialization with "missing field `content`". The wire
// format must always carry content, even when empty (DSH does the same).
func TestOpenAIClientAlwaysEmitsContentField(t *testing.T) {
	assistantToolOnly := protocol.NewMessage(protocol.RoleAssistant,
		protocol.ToolUseBlock("call_1", "read_file", map[string]interface{}{"path": "x"}),
	)
	toolEmptyResult := protocol.NewMessage(protocol.RoleUser,
		protocol.ToolResultBlock("call_1", ""),
	)
	client := NewOpenAIClient("http://127.0.0.1:9", "test-key", 5*time.Second)
	body, err := client.buildRequest(protocol.Request{
		Model:    "deepseek-chat",
		Messages: SanitizeMessagesForProvider(protocol.ToAPIMessages([]protocol.Message{assistantToolOnly, toolEmptyResult})),
	}, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(decoded.Messages) != 2 {
		t.Fatalf("expected 2 wire messages, got %d: %s", len(decoded.Messages), string(body))
	}
	for i, msg := range decoded.Messages {
		if _, ok := msg["content"]; !ok {
			t.Fatalf("wire message %d missing content field: %s", i, string(body))
		}
	}
	// Empty tool results get a non-empty placeholder (gateways reject empty
	// tool content on the wire).
	toolMsg := decoded.Messages[1]
	if got := toolMsg["content"]; got != "(no output)" {
		t.Fatalf("expected empty tool result placeholder, got %q (body: %s)", got, string(body))
	}
}

// TestOpenAIClientForwardsReasoningEffort guards that the openai_compatible
// wire carries the configured reasoning_effort hint (mirroring the codex
// client), so providers like the AIS gateway can tune the model's thinking
// budget instead of defaulting to max reasoning on every turn.
func TestOpenAIClientForwardsReasoningEffort(t *testing.T) {
	client := NewOpenAIClient("http://127.0.0.1:9", "test-key", 5*time.Second)
	body, err := client.buildRequest(protocol.Request{
		Model:           "deepseek-chat",
		ReasoningEffort: "high",
		Messages:        protocol.ToAPIMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}),
	}, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got := decoded["reasoning_effort"]; got != "high" {
		t.Fatalf("expected reasoning_effort=high on wire, got %v (body: %s)", got, string(body))
	}

	// Unknown efforts are dropped rather than forwarded and rejected.
	body, err = client.buildRequest(protocol.Request{
		Model:           "deepseek-chat",
		ReasoningEffort: "turbo-max",
		Messages:        protocol.ToAPIMessages([]protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}),
	}, false)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var decoded2 map[string]any
	if err := json.Unmarshal(body, &decoded2); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, ok := decoded2["reasoning_effort"]; ok {
		t.Fatalf("expected unknown reasoning_effort dropped, got %v (body: %s)", decoded2["reasoning_effort"], string(body))
	}
}

func TestOpenAIClientRequestGzip(t *testing.T) {
	var gotEncoding string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Content-Encoding")
		// Server-side gunzip, exactly what a godex usage gateway does.
		var reader io.Reader = r.Body
		if strings.EqualFold(strings.TrimSpace(gotEncoding), "gzip") {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Fatalf("gunzip request: %v", err)
			}
			defer zr.Close()
			reader = zr
		}
		if err := json.NewDecoder(reader).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	req := protocol.Request{
		Model: "deepseek-chat",
		Messages: protocol.ToAPIMessages([]protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, strings.Repeat("large-context-", 200)),
		}),
	}

	// Default: plain body, no Content-Encoding.
	plain := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	if _, err := plain.Call(context.Background(), req); err != nil {
		t.Fatalf("plain call: %v", err)
	}
	if gotEncoding != "" {
		t.Fatalf("expected no content-encoding by default, got %q", gotEncoding)
	}

	// With request_gzip enabled: body must be gzipped and still decode.
	gzipped := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	gzipped.SetRequestGzip(true)
	if _, err := gzipped.Call(context.Background(), req); err != nil {
		t.Fatalf("gzip call: %v", err)
	}
	if !strings.EqualFold(gotEncoding, "gzip") {
		t.Fatalf("expected gzip content-encoding, got %q", gotEncoding)
	}
	if msg := gotBody["messages"]; msg == nil {
		t.Fatalf("expected decoded messages after gunzip, got %#v", gotBody)
	}
}

// TestOpenAIClientStreamForwardsReasoningDeltas verifies that openai_compatible
// providers' `reasoning_content` stream deltas are forwarded to
// OnThinkingDelta (mirroring the Responses client). Without this forwarding
// the runtime's OnAssistantThinkingDelta never fires, so thinking is lost from
// both the live feed and the persisted timeline.
func TestOpenAIClientStreamForwardsReasoningDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Let me check\",\"role\":\"assistant\"},\"index\":0}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" the plan first.\",\"role\":\"assistant\"},\"index\":0}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"Done.\",\"role\":\"assistant\"},\"finish_reason\":\"stop\",\"index\":0}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "test-key", 5*time.Second)
	var gotThinking []string
	_, err := client.Stream(context.Background(), protocol.Request{Model: "deepseek-test"}, StreamHandler{
		OnThinkingDelta: func(thinking, _ string) {
			if thinking != "" {
				gotThinking = append(gotThinking, thinking)
			}
		},
	})
	if err != nil {
		t.Fatalf("openai-compatible stream: %v", err)
	}
	if len(gotThinking) != 2 {
		t.Fatalf("expected 2 forwarded reasoning deltas, got %d: %+v", len(gotThinking), gotThinking)
	}
	if gotThinking[0] != "Let me check" || gotThinking[1] != " the plan first." {
		t.Fatalf("unexpected reasoning deltas: %+v", gotThinking)
	}
}
