package conversation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestSDKStreamErrorsAreClassifiedFromStructuredData(t *testing.T) {
	for _, tc := range []struct {
		code, message string
		want          TurnErrorClass
	}{
		{"stream_error", "unexpected EOF", TurnErrorRetryable},
		{"rate_limit_exceeded", "slow down", TurnErrorTransient},
		{"invalid_request_error", "EOF is not a valid parameter", TurnErrorNonRetryable},
		{"unknown", "EOF", TurnErrorUnknown},
	} {
		data := []byte(fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, tc.code, tc.message))
		err := fmt.Errorf("wrapped: %w", &ssestream.StreamError{Message: "stream error", Event: ssestream.Event{Data: data}})
		if got := ClassifyTurnError(err); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.code, got, tc.want)
		}
	}
}

func TestStreamRetryPreservesCompletedToolsAndDiscardsFailedText(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"write_file\"}}\n\n")
			io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"name\":\"write_file\",\"arguments\":\"{}\"}\n\n")
			io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n")
		case 2:
			io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"failed fragment\"}\n\n")
			io.WriteString(w, "data: {\"error\":{\"code\":\"stream_error\",\"message\":\"unexpected EOF\"}}\n\n")
		default:
			io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"verified result\"}\n\n")
			io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":5}}}\n\n")
		}
	}))
	defer server.Close()
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "implement")}
	var text strings.Builder
	var tools, resets int
	var reported protocol.TurnMetrics
	result, err := (Runner{
		Caller: NewOpenAIResponsesClient(server.URL, "test-key", time.Second),
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return NewRequest("model", 1024, "", "", messages, nil), nil
		},
		AppendAssistant:   func(msg protocol.Message) { messages = append(messages, msg) },
		AppendToolResults: func(msg protocol.Message) { messages = append(messages, msg) },
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			tools++
			return ToolExecutionResult{Output: "saved"}, nil
		},
		OnAssistantTextDelta: func(delta string) { text.WriteString(delta) },
		OnStreamReset:        func() { resets++; text.Reset() },
		OnMetrics:            func(metrics protocol.TurnMetrics) { reported = metrics },
		ModelRetryDelay:      10 * time.Millisecond,
		MaxTurns:             4,
	}).Run(context.Background())
	if err != nil || result == nil || !result.Completed {
		t.Fatalf("retry failed: %+v, %v", result, err)
	}
	if calls.Load() != 3 || tools != 1 || resets != 1 || text.String() != "verified result" {
		t.Fatalf("duplicate work/output: calls=%d tools=%d resets=%d text=%q", calls.Load(), tools, resets, text.String())
	}
	if reported.ModelCalls != 3 || reported.ModelRetries != 1 || reported.ModelErrors != 1 || reported.ToolCalls != 1 || reported.OutputTokens != 7 || reported.FirstTextMS == nil || *reported.FirstTextMS < 5 {
		t.Fatalf("incorrect metrics: %+v", reported)
	}
	for _, msg := range messages {
		if strings.Contains(protocol.MessageText(msg), "failed fragment") {
			t.Fatal("failed stream entered persistent history")
		}
	}
}

func TestIncompleteResponsesStreamDoesNotYieldPartialTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"write_file\"}}\n\n")
	}))
	defer server.Close()
	resp, err := NewOpenAIResponsesClient(server.URL, "test-key", time.Second).Call(context.Background(), protocol.Request{Model: "model"})
	if resp != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial stream accepted: %+v, %v", resp, err)
	}
}

type retryUntilCanceledCaller struct{ calls int }

func (c *retryUntilCanceledCaller) Call(context.Context, protocol.Request) (*protocol.Response, error) {
	c.calls++
	return nil, NewTransientTurnError("temporary outage")
}

func TestCancellationDuringBackoffDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller := &retryUntilCanceledCaller{}
	result, err := (Runner{
		Caller:       caller,
		BuildRequest: func(context.Context) (protocol.Request, error) { return protocol.Request{}, nil },
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			t.Fatal("no tools expected")
			return ToolExecutionResult{}, nil
		},
		OnStreamReset:   cancel,
		ModelRetryDelay: time.Hour,
	}).Run(ctx)
	if !errors.Is(err, context.Canceled) || caller.calls != 1 || result == nil || !result.Stopped || result.Metrics.ModelRetries != 0 {
		t.Fatalf("cancellation failed: result=%+v calls=%d err=%v", result, caller.calls, err)
	}
}

func TestIncompleteChatAndAnthropicStreamsAreNotCommitted(t *testing.T) {
	if response, err := parseOpenAIStream(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"), StreamHandler{}); response != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial chat stream accepted: %+v, %v", response, err)
	}
	if response, _, err := parseMessageStream(strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"role\":\"assistant\"}}\n\n"), StreamHandler{}); response != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial Anthropic stream accepted: %+v, %v", response, err)
	}
}
