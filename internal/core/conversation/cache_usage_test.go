package conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// TestClientCallDecodesAnthropicCacheUsage covers root cause #1:
// Anthropic returns usage.cache_creation_input_tokens /
// usage.cache_read_input_tokens, but the protocol.Usage JSON tags
// previously only knew the OpenAI-style cache_read_tokens /
// cache_write_tokens names. The test asserts that when the upstream
// response includes the Anthropic fields, the decoded protocol.Usage
// exposes the values through CacheReadTokens / CacheWriteTokens.
func TestClientCallDecodesAnthropicCacheUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{
				"input_tokens":100,
				"output_tokens":50,
				"cache_creation_input_tokens":30,
				"cache_read_input_tokens":70
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{
		Model:     "test-model",
		MaxTokens: 32,
	})
	if err != nil {
		t.Fatalf("client call: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected response usage to be decoded")
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 50 {
		t.Fatalf("expected input/output tokens 100/50, got %+v", resp.Usage)
	}
	if resp.Usage.CacheReadTokens != 70 {
		t.Fatalf("expected cache read tokens 70, got %d", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.CacheWriteTokens != 30 {
		t.Fatalf("expected cache write tokens 30, got %d", resp.Usage.CacheWriteTokens)
	}
}

// TestClientStreamDecodesAnthropicCacheUsage covers root cause #2:
// Anthropic streaming emits usage on message_start.message.usage. The
// stream parser previously ignored it, so streaming calls always
// reported zero cache tokens.
func TestClientStreamDecodesAnthropicCacheUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[],"usage":{"input_tokens":200,"output_tokens":0,"cache_creation_input_tokens":10,"cache_read_input_tokens":90}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("client stream: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected response usage from message_start")
	}
	if resp.Usage.InputTokens != 200 {
		t.Fatalf("expected input tokens 200, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadTokens != 90 {
		t.Fatalf("expected cache read tokens 90, got %d", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.CacheWriteTokens != 10 {
		t.Fatalf("expected cache write tokens 10, got %d", resp.Usage.CacheWriteTokens)
	}
}

// TestOpenAICodexStreamFillsCacheUsageFromCompletedEvent covers root cause
// #3: OpenAI Codex's response.completed event carries
// response.usage.input_tokens_details.cached_tokens, but the Codex stream
// state-to-protocol conversion previously dropped it.
func TestOpenAICodexStreamFillsCacheUsageFromCompletedEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		completedEvent := map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":     "resp_1",
				"status": "completed",
				"usage": map[string]any{
					"input_tokens":  500,
					"output_tokens": 200,
					"total_tokens":  700,
					"input_tokens_details": map[string]any{
						"cached_tokens": 400,
					},
				},
			},
		}
		completedJSON, _ := json.Marshal(completedEvent)
		for _, payload := range []string{
			`{"type":"response.output_text.delta","delta":"hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			string(completedJSON),
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	client := NewOpenAICodexClient(server.URL, "test-token", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{
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
	if resp.Usage == nil {
		t.Fatal("expected response usage from response.completed")
	}
	// input_tokens (500) includes cached_tokens (400); the protocol layer
	// normalizes InputTokens to the uncached portion (500-400=100).
	if resp.Usage.InputTokens != 100 {
		t.Fatalf("expected input tokens 100, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 200 {
		t.Fatalf("expected output tokens 200, got %d", resp.Usage.OutputTokens)
	}
	if resp.Usage.CacheReadTokens != 400 {
		t.Fatalf("expected cache read tokens 400, got %d", resp.Usage.CacheReadTokens)
	}
}
