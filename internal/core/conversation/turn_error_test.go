package conversation

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// --- classification ---

func TestClassifyTurnErrorExplicit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want TurnErrorClass
	}{
		{"nil", nil, TurnErrorUnknown},
		{"retryable", NewRetryableTurnError("boom"), TurnErrorRetryable},
		{"transient", NewTransientTurnError("boom"), TurnErrorTransient},
		{"nonretryable", NewNonRetryableTurnError("boom"), TurnErrorNonRetryable},
		{"wrapped retryable", wrapErr(NewRetryableTurnError("inner")), TurnErrorRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnError(tc.err); got != tc.want {
				t.Fatalf("ClassifyTurnError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyTurnErrorAPICodes(t *testing.T) {
	cases := []struct {
		code int
		want TurnErrorClass
	}{
		{http.StatusBadRequest, TurnErrorNonRetryable},
		{http.StatusUnauthorized, TurnErrorNonRetryable},
		{http.StatusForbidden, TurnErrorNonRetryable},
		{http.StatusNotFound, TurnErrorNonRetryable},
		{http.StatusUnprocessableEntity, TurnErrorNonRetryable},
		{http.StatusRequestTimeout, TurnErrorTransient},
		{http.StatusTooManyRequests, TurnErrorTransient},
		{http.StatusInternalServerError, TurnErrorRetryable},
		{http.StatusBadGateway, TurnErrorRetryable},
		{http.StatusServiceUnavailable, TurnErrorTransient},
		{http.StatusGatewayTimeout, TurnErrorTransient},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			err := formatAPIError(tc.code, []byte("provider said no"))
			if got := ClassifyTurnError(err); got != tc.want {
				t.Fatalf("ClassifyTurnError(%d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

func TestClassifyTurnErrorTransport(t *testing.T) {
	timeoutErr := &net.OpError{Op: "dial", Net: "tcp", Err: fakeTimeoutErr{}}
	cases := []struct {
		name string
		err  error
		want TurnErrorClass
	}{
		{"context canceled", context.Canceled, TurnErrorNonRetryable},
		{"context deadline", context.DeadlineExceeded, TurnErrorTransient},
		{"net timeout", timeoutErr, TurnErrorTransient},
		{"io eof", io.EOF, TurnErrorRetryable},
		{"wrapped url error", &url.Error{Op: "Get", URL: "http://x", Err: io.ErrUnexpectedEOF}, TurnErrorRetryable},
		{"plain error", errors.New("random"), TurnErrorUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnError(tc.err); got != tc.want {
				t.Fatalf("ClassifyTurnError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestTurnFailureMessage(t *testing.T) {
	if got := TurnFailureMessage(NewNonRetryableTurnError("invalid request: nope")); got != "invalid request: nope" {
		t.Fatalf("TurnFailureMessage(nonretryable) = %q, want the message", got)
	}
	generic := TurnFailureMessage(errors.New("weird failure"))
	if generic == "" || strings.Contains(generic, "weird failure") {
		t.Fatalf("TurnFailureMessage(generic) = %q, want generic text without leaking details", generic)
	}
	if got := TurnFailureMessage(NewRetryableTurnError("retry me")); got == "retry me" {
		t.Fatalf("TurnFailureMessage(retryable) = %q, want generic text", got)
	}
}

// --- runner routing ---

// errCaller returns a fixed error from Call.
type errCaller struct {
	err   error
	calls int
}

func (e *errCaller) Call(context.Context, protocol.Request) (*protocol.Response, error) {
	e.calls++
	return nil, e.err
}

// transientThenSuccessCaller fails with a transient error n times then returns a final response.
type transientThenSuccessCaller struct {
	failures int
	calls    int
	err      error
}

func (c *transientThenSuccessCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	c.calls++
	if c.calls <= c.failures {
		return nil, c.err
	}
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock("done")}}, nil
}

func TestRunnerRetriesTransientModelError(t *testing.T) {
	caller := &transientThenSuccessCaller{failures: 2, err: NewTransientTurnError("overloaded")}
	runner := Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return NewRequest("m", 100, "", "sys", []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}, nil), nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{}, errors.New("no tools expected")
		},
		MaxModelRetries: 3,
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if !result.Completed {
		t.Fatalf("Run: expected completed, got %+v", result)
	}
	if caller.calls != 3 {
		t.Fatalf("Run: expected 3 calls (2 failures + 1 success), got %d", caller.calls)
	}
}

func TestRunnerExhaustsTransientRetries(t *testing.T) {
	caller := &errCaller{err: NewTransientTurnError("still down")}
	runner := Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return NewRequest("m", 100, "", "sys", []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}, nil), nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{}, errors.New("no tools expected")
		},
		MaxModelRetries: 2,
	}
	_, err := runner.Run(context.Background())
	if err == nil {
		t.Fatalf("Run: expected error after exhausting retries")
	}
	if !errors.Is(err, caller.err) {
		t.Fatalf("Run: expected wrapped original error, got %v", err)
	}
}

func TestRunnerDoesNotRetryNonRetryableModelError(t *testing.T) {
	caller := &errCaller{err: NewNonRetryableTurnError("bad request shape")}
	runner := Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return NewRequest("m", 100, "", "sys", []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}, nil), nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{}, errors.New("no tools expected")
		},
		MaxModelRetries: 5,
	}
	_, err := runner.Run(context.Background())
	if err == nil {
		t.Fatalf("Run: expected error")
	}
	if caller.calls != 1 {
		t.Fatalf("Run: expected exactly 1 call (non-retryable must not retry), got %d", caller.calls)
	}
}

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func wrapErr(err error) error {
	return &url.Error{Op: "Get", URL: "http://x", Err: err}
}

func TestIsContextLengthError(t *testing.T) {
	if !IsContextLengthError(&apiStatusError{StatusCode: 400, Message: "maximum context length is 128000 tokens"}) {
		t.Fatal("expected 400 context-length error detected")
	}
	if !IsContextLengthError(&apiStatusError{StatusCode: 422, Message: "context_length_exceeded"}) {
		t.Fatal("expected 422 context_length_exceeded detected")
	}
	if IsContextLengthError(&apiStatusError{StatusCode: 400, Message: "rate limited"}) {
		t.Fatal("expected unrelated 400 not classified as context-length")
	}
	if IsContextLengthError(&apiStatusError{StatusCode: 500, Message: "maximum context length"}) {
		t.Fatal("expected 500 not classified as context-length")
	}
	if IsContextLengthError(errors.New("maximum context length")) {
		t.Fatal("expected non-status error not classified as context-length")
	}
}

// overflowCaller fails with a context-length error once, then succeeds.
type overflowCaller struct {
	failures int
	calls    int
}

func (c *overflowCaller) Call(context.Context, protocol.Request) (*protocol.Response, error) {
	c.calls++
	if c.calls <= c.failures {
		return nil, &apiStatusError{StatusCode: 400, Message: "maximum context length exceeded"}
	}
	return &protocol.Response{Content: []protocol.Block{protocol.TextBlock("recovered")}}, nil
}

func TestRunnerContextOverflowCompactsAndRetries(t *testing.T) {
	caller := &overflowCaller{failures: 1}
	builds := 0
	overflowCalls := 0
	runner := Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			builds++
			return NewRequest("m", 100, "", "sys", []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}, nil), nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{}, errors.New("no tools expected")
		},
		OnContextOverflow: func(context.Context) bool {
			overflowCalls++
			return true
		},
		MaxContextOverflowRecoveries: 1,
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if overflowCalls != 1 {
		t.Fatalf("expected overflow recovery invoked once, got %d", overflowCalls)
	}
	if builds != 2 {
		t.Fatalf("expected request rebuilt after compaction, got %d builds", builds)
	}
	if result.LastAssistantText != "recovered" {
		t.Fatalf("expected recovered text, got %q", result.LastAssistantText)
	}
}

func TestRunnerContextOverflowBounded(t *testing.T) {
	// The provider keeps rejecting after compaction: recovery must stop after
	// the bounded budget instead of looping forever.
	caller := &overflowCaller{failures: 100}
	overflowCalls := 0
	runner := Runner{
		Caller: caller,
		BuildRequest: func(context.Context) (protocol.Request, error) {
			return NewRequest("m", 100, "", "sys", []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, "hi")}, nil), nil
		},
		ExecuteTool: func(context.Context, string, map[string]interface{}) (ToolExecutionResult, error) {
			return ToolExecutionResult{}, errors.New("no tools expected")
		},
		OnContextOverflow: func(context.Context) bool {
			overflowCalls++
			return true
		},
		MaxContextOverflowRecoveries: 2,
	}
	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("expected the overflow error to surface after bounded recovery")
	}
	if overflowCalls != 2 {
		t.Fatalf("expected exactly 2 overflow recoveries, got %d", overflowCalls)
	}
}
