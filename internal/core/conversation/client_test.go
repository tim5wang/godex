package conversation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestClientCallRetriesTransientStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/v1/messages" {
			t.Fatalf("expected request path /v1/messages, got %s", got)
		}
		current := attempts.Add(1)
		if current == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("try again"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	client.retryBaseDelay = time.Millisecond
	client.maxRetryDelay = time.Millisecond

	resp, err := client.Call(context.Background(), protocol.Request{
		Model:     "test-model",
		MaxTokens: 32,
		System:    "system",
	})
	if err != nil {
		t.Fatalf("client call: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if got := protocol.BlocksText(resp.Content); got != "ok" {
		t.Fatalf("expected response text %q, got %q", "ok", got)
	}
}

func TestClientStreamEmitsTextDeltasAndReturnsResponse(t *testing.T) {
	var requestBody struct {
		Stream bool `json:"stream"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
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
	var deltas []string
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{
		OnTextDelta: func(delta string) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("client stream: %v", err)
	}
	if !requestBody.Stream {
		t.Fatal("expected stream flag in request")
	}
	if got := strings.Join(deltas, ""); got != "hello" {
		t.Fatalf("expected streamed deltas %q, got %q", "hello", got)
	}
	if got := protocol.BlocksText(resp.Content); got != "hello" {
		t.Fatalf("expected final response text %q, got %q", "hello", got)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("expected stop reason end_turn, got %q", resp.StopReason)
	}
}

// TestClientStreamAssemblesThinkingBlock covers the extended-thinking
// case where the upstream Anthropic API emits a thinking content
// block (chain-of-thought text + opaque signature). The previous
// parser did not know about the `thinking` content block type and
// silently dropped the block from the final response, so multi-turn
// Pi sessions lost their reasoning context after the first turn.
// The fix surfaces the block on the response (alongside the
// consolidated reasoning text and signature) and forwards each
// per-chunk delta to the OnThinkingDelta callback so the gateway
// can emit the canonical `thinking_delta` / `signature_delta`
// frames on the wire.
func TestClientStreamAssemblesThinkingBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think..."}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)

	var thinkingDeltas []string
	var signatures []string
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{
		OnThinkingDelta: func(thinking string, signature string) {
			if thinking != "" {
				thinkingDeltas = append(thinkingDeltas, thinking)
			}
			if signature != "" {
				signatures = append(signatures, signature)
			}
		},
	})
	if err != nil {
		t.Fatalf("client stream: %v", err)
	}

	// The parser must surface the consolidated thinking block
	// on the final response so multi-turn sessions can keep the
	// chain-of-thought. Without the fix the block was missing
	// and reasoningContent was empty.
	if got := strings.Join(thinkingDeltas, ""); got != "let me think..." {
		t.Errorf("expected per-chunk thinking %q, got %q", "let me think...", got)
	}
	if len(signatures) != 1 || signatures[0] != "sig-abc" {
		t.Errorf("expected exactly one signature 'sig-abc', got %v", signatures)
	}
	if got := protocol.BlocksText(resp.Content); got != "answer" {
		t.Errorf("expected final text 'answer', got %q", got)
	}
	// The thinking block must be present on the response so the
	// gateway can return it on the non-streaming path and so
	// multi-turn sessions can echo the signature back. We use
	// the type-filtering ToolUses helper for tool calls; for
	// thinking we walk the slice ourselves.
	var foundThinking *protocol.Block
	for i := range resp.Content {
		if resp.Content[i].Type == protocol.BlockThinking {
			foundThinking = &resp.Content[i]
			break
		}
	}
	if foundThinking == nil {
		t.Fatalf("expected a thinking block in the response, got %+v", resp.Content)
	}
	if foundThinking.Text != "let me think..." {
		t.Errorf("expected thinking text %q, got %q", "let me think...", foundThinking.Text)
	}
	if foundThinking.Signature != "sig-abc" {
		t.Errorf("expected thinking signature 'sig-abc', got %q", foundThinking.Signature)
	}
	if foundThinking.Redacted {
		t.Errorf("expected non-redacted thinking block, got Redacted=true")
	}
}

// TestClientStreamAssemblesRedactedThinkingBlock covers the safety-
// filter case where Anthropic emits a `redacted_thinking` content
// block (opaque data payload, no visible text). The parser must
// surface the block with the redacted marker set so the gateway
// forwards the correct wire shape on the non-streaming path.
func TestClientStreamAssemblesRedactedThinkingBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque-data"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("client stream: %v", err)
	}
	var found *protocol.Block
	for i := range resp.Content {
		if resp.Content[i].Type == protocol.BlockThinking {
			found = &resp.Content[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a redacted thinking block in the response, got %+v", resp.Content)
	}
	if !found.Redacted {
		t.Errorf("expected Redacted=true on the redacted thinking block")
	}
	if found.Signature != "opaque-data" {
		t.Errorf("expected signature to carry the opaque data, got %q", found.Signature)
	}
}

func TestClientStreamAssemblesToolUseInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"bash","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"pwd\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("client stream: %v", err)
	}
	tools := protocol.ToolUses(resp.Content)
	if len(tools) != 1 || tools[0].Name != "bash" || tools[0].Input["command"] != "pwd" {
		t.Fatalf("unexpected streamed tool use: %+v", resp.Content)
	}
}

func TestClientCallDoesNotRetryPermanentStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	client.retryBaseDelay = time.Millisecond
	client.maxRetryDelay = time.Millisecond

	_, err := client.Call(context.Background(), protocol.Request{Model: "test-model"})
	if err == nil {
		t.Fatal("expected client call to fail")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts.Load())
	}
	if !strings.Contains(err.Error(), "API error: 400: bad request") {
		t.Fatalf("expected API error message, got %v", err)
	}
}

func TestClientCallFallsBackWhenImageInputIsRejected(t *testing.T) {
	var attempts atomic.Int32
	var seenBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBodies = append(seenBodies, string(body))
		current := attempts.Add(1)
		if current == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("image content is not supported by this endpoint"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"fallback ok"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Call(context.Background(), protocol.Request{
		Model:     "test-model",
		MaxTokens: 32,
		System:    "system",
		Messages: []protocol.APIMessage{{
			Role: "user",
			Content: []protocol.Block{
				protocol.TextBlock("请描述这张图片"),
				protocol.ImageBlock("image/jpeg", "ZmFrZQ=="),
			},
		}},
	})
	if err != nil {
		t.Fatalf("client call: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if got := protocol.BlocksText(resp.Content); got != "fallback ok" {
		t.Fatalf("unexpected fallback response text %q", got)
	}
	if len(seenBodies) != 2 {
		t.Fatalf("expected two request bodies, got %d", len(seenBodies))
	}
	if !strings.Contains(seenBodies[0], `"type":"image"`) {
		t.Fatalf("expected first request to contain image input, got %s", seenBodies[0])
	}
	if strings.Contains(seenBodies[1], `"type":"image"`) {
		t.Fatalf("expected downgraded request to remove image input, got %s", seenBodies[1])
	}
	if !strings.Contains(seenBodies[1], "native image understanding is not enabled") {
		t.Fatalf("expected downgraded request to inject fallback note, got %s", seenBodies[1])
	}
}

func TestNewClientConfiguresSharedTransport(t *testing.T) {
	client := NewClient("http://example.com", "test-key", 5*time.Second)

	if client.httpClient.Timeout != 5*time.Second {
		t.Fatalf("expected client timeout %s, got %s", 5*time.Second, client.httpClient.Timeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.httpClient.Transport)
	}
	if transport.MaxIdleConns < 50 {
		t.Fatalf("expected configured connection pool, got MaxIdleConns=%d", transport.MaxIdleConns)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("expected IdleConnTimeout to be configured, got %s", transport.IdleConnTimeout)
	}
}

func TestClientCallHonorsRetryAfterHeader(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := attempts.Add(1)
		if current == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	waited := make([]time.Duration, 0, 1)
	client.sleep = func(ctx context.Context, delay time.Duration) error {
		waited = append(waited, delay)
		return nil
	}

	_, err := client.Call(context.Background(), protocol.Request{Model: "test-model"})
	if err != nil {
		t.Fatalf("client call: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if len(waited) != 1 {
		t.Fatalf("expected one retry sleep, got %d", len(waited))
	}
	if waited[0] != 0 {
		t.Fatalf("expected Retry-After header to control retry delay, got %s", waited[0])
	}
}

func TestClientCallRetriesTemporaryNetworkError(t *testing.T) {
	client := NewClient("http://example.com", "test-key", 5*time.Second)
	client.retryBaseDelay = time.Millisecond
	client.maxRetryDelay = time.Millisecond
	var attempts atomic.Int32
	client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, temporaryTestError{msg: "temporary"}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)),
		}, nil
	})

	resp, err := client.Call(context.Background(), protocol.Request{Model: "test-model"})
	if err != nil {
		t.Fatalf("client call: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
	if got := protocol.BlocksText(resp.Content); got != "ok" {
		t.Fatalf("expected response text %q, got %q", "ok", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type temporaryTestError struct {
	msg string
}

func (e temporaryTestError) Error() string   { return e.msg }
func (e temporaryTestError) Timeout() bool   { return false }
func (e temporaryTestError) Temporary() bool { return true }

// TestClientStreamToleratesTruncatedToolInputJSON covers the case where the
// upstream closes the content_block_stop frame with partial_json left in an
// invalid state that even the structural recovery path cannot salvage
// (e.g. a fragment with mismatched delimiters or a trailing colon that
// would parse as an unterminated key/value). The parser must NOT abort the
// whole stream — it should surface the fragment to the caller so the runner
// can decide whether to retry or skip the tool call.
func TestClientStreamToleratesTruncatedToolInputJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"bash","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":[\"a\","}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("client stream should not error on truncated tool input, got: %v", err)
	}
	tools := protocol.ToolUses(resp.Content)
	if len(tools) != 1 || tools[0].Name != "bash" {
		t.Fatalf("expected one bash tool_use even on truncation, got: %+v", resp.Content)
	}
	if reason, ok := tools[0].Input["__error__"].(string); !ok || reason != "streamed_tool_input_truncated" {
		t.Fatalf("expected __error__=streamed_tool_input_truncated, got input=%#v", tools[0].Input)
	}
	if partial, ok := tools[0].Input["__partial__"].(string); !ok || !strings.Contains(partial, `"command"`) {
		t.Fatalf("expected __partial__ to carry the raw fragment, got input=%#v", tools[0].Input)
	}
}

// TestClientStreamToleratesMessageStopBeforeContentBlockStop covers the case
// where the upstream sends message_stop without ever emitting the
// content_block_stop for a tool_use block (e.g. the connection drops between
// the last input_json_delta and the stop frame). The parser must treat
// message_stop as a soft close for any blocks still missing a stop frame.
func TestClientStreamToleratesMessageStopBeforeContentBlockStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"bash","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\"}"}}`,
			// NOTE: no content_block_stop for index 0
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("client stream should not error when content_block_stop is missing, got: %v", err)
	}
	tools := protocol.ToolUses(resp.Content)
	if len(tools) != 1 || tools[0].Name != "bash" {
		t.Fatalf("expected one bash tool_use, got: %+v", resp.Content)
	}
	if tools[0].Input["command"] != "pwd" {
		t.Fatalf("expected parsed command=pwd, got input=%#v", tools[0].Input)
	}
	if _, hasError := tools[0].Input["__error__"]; hasError {
		t.Fatalf("a complete JSON should not carry __error__, got input=%#v", tools[0].Input)
	}
}

// TestClientStreamToleratesToolInputMissingClosingBrace covers the case where
// partial_json is missing the closing brace (the upstream cuts the frame
// after the last value but before the structural close). The parser should
// close the JSON before decoding so the call does not abort.
func TestClientStreamToleratesToolInputMissingClosingBrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, payload := range []string{
			`{"type":"message_start","message":{"content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"bash","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\""}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 5*time.Second)
	resp, err := client.Stream(context.Background(), protocol.Request{Model: "test-model"}, StreamHandler{})
	if err != nil {
		t.Fatalf("client stream should not error on missing closing brace, got: %v", err)
	}
	tools := protocol.ToolUses(resp.Content)
	if len(tools) != 1 || tools[0].Name != "bash" {
		t.Fatalf("expected one bash tool_use, got: %+v", resp.Content)
	}
	if tools[0].Input["command"] != "pwd" {
		t.Fatalf("expected parsed command=pwd, got input=%#v", tools[0].Input)
	}
	if _, hasError := tools[0].Input["__error__"]; hasError {
		t.Fatalf("a brace-recovered JSON should not carry __error__, got input=%#v", tools[0].Input)
	}
}
