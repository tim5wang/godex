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

	"github.com/tim5wang/godex/internal/core/protocol"
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
