package conversation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/logger"
)

// Caller sends protocol requests to the model provider.
type Caller interface {
	Call(ctx context.Context, req protocol.Request) (*protocol.Response, error)
}

// StreamCaller is implemented by providers that can stream assistant text
// deltas while still returning the final structured response for tool handling.
type StreamCaller interface {
	Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error)
}

// StreamHandler receives model-provider deltas during one streamed call.
type StreamHandler struct {
	OnTextDelta func(string)
	// OnStreamStarted is invoked once when the first stream event arrives. It
	// lets callers show a "thinking…" placeholder for providers (e.g. the
	// ChatGPT codex backend) that deliver reasoning only as encrypted content
	// with no plaintext reasoning deltas.
	OnStreamStarted func()
	// OnToolUse is invoked for every Anthropic tool_use block. It is
	// called three times per block:
	//   1. on content_block_start  (the block's id, name, type are set,
	//      partialJSON is empty),
	//   2. on each content_block_delta of type input_json_delta
	//      (partialJSON carries the accumulated fragment so far),
	//   3. on content_block_stop   (partialJSON carries the complete
	//      reassembled JSON arguments string).
	// The Anthropic-Messages usage gateway (routes_usage.go) translates
	// these callbacks back into the canonical
	// content_block_start / content_block_delta / content_block_stop
	// SSE events so that strict consumers (Pi's anthropic.ts:568-660
	// toolcall_* paths) can execute the requested tool.
	OnToolUse func(block protocol.Block, partialJSON string)
	// OnThinkingDelta is invoked for every Anthropic extended-
	// thinking content block delta. The parser calls it with
	// the chain-of-thought text fragment the upstream just
	// emitted (NOT the cumulative string — callers must
	// concatenate across deltas themselves, mirroring how
	// OnTextDelta is called per text chunk). The signature
	// argument is non-empty only on the trailing
	// signature_delta frame. Without this hook the gateway
	// can't tell a text_delta from a thinking_delta, and the
	// thinking chain-of-thought would be silently dropped.
	OnThinkingDelta func(thinking string, signature string)
	// OnMessageStart is invoked when parseMessageStream receives the
	// upstream's message_start SSE event. The usage parameter carries
	// the initial token counts (input_tokens, cache tokens) the upstream
	// provider reported. The Anthropic streaming gateway uses this to
	// emit a properly-initialised message_start event to the downstream
	// client (pi), without which the client sees input_tokens=0 and
	// displays 0% context usage indefinitely.
	OnMessageStart func(usage protocol.Usage)
}

// Client sends Anthropic-compatible message requests.
type Client struct {
	baseURL        string
	apiKey         string
	httpClient     *http.Client
	maxRetries     int
	retryBaseDelay time.Duration
	maxRetryDelay  time.Duration
	sleep          func(context.Context, time.Duration) error
}

// NewClient creates a shared conversation client.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	httpClient := &http.Client{Transport: newDefaultTransport()}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	return &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		httpClient:     httpClient,
		maxRetries:     2,
		retryBaseDelay: 250 * time.Millisecond,
		maxRetryDelay:  2 * time.Second,
		sleep:          sleepContext,
	}
}

// Call executes the provider request.
func (c *Client) isAnthropicNative() bool {
	baseURL := strings.TrimSpace(c.baseURL)
	return strings.Contains(baseURL, "api.anthropic.com") || strings.Contains(baseURL, "api.anthropic.eu")
}

func (c *Client) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start)})
	}()
	req.AnthropicNative = c.isAnthropicNative()
	var lastErr error
	visionFallbackUsed := false
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		reqBody, err := marshalAnthropicBody(req)
		if err != nil {
			return nil, err
		}
		apiResp, retryAfter, err := c.callOnce(ctx, reqBody)
		if err == nil {
			if logger.LevelEnabled(logger.LevelDebug) {
				logger.Debugf("LLM Call: %s", reqBody)
				jsonResp, _ := json.Marshal(apiResp)
				logger.Debugf("LLM Response: %s", jsonResp)
			}
			finalResp = apiResp
			return apiResp, nil
		}
		lastErr = err
		if !visionFallbackUsed && requestHasImageInputs(req) && shouldFallbackVision(err) {
			req = downgradeVisionRequest(req)
			visionFallbackUsed = true
			logger.Warnf("LLM endpoint rejected image input, retrying once with metadata fallback: %v", err)
			continue
		}
		if attempt == c.maxRetries || !shouldRetryError(err) {
			finalErr = err
			return nil, err
		}
		if err := c.sleep(ctx, c.retryDelay(attempt, retryAfter)); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				finalErr = err
				return nil, err
			}
			finalErr = lastErr
			return nil, lastErr
		}
	}
	finalErr = lastErr
	return nil, lastErr
}

// Stream executes the provider request with Anthropic-compatible SSE streaming.
func (c *Client) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start), Stream: true})
	}()
	req.AnthropicNative = c.isAnthropicNative()
	var lastErr error
	visionFallbackUsed := false
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req.Stream = true
		reqBody, err := marshalAnthropicBody(req)
		if err != nil {
			return nil, err
		}
		apiResp, retryAfter, streamed, err := c.streamOnce(ctx, reqBody, handler)
		if err == nil {
			if logger.LevelEnabled(logger.LevelDebug) {
				logger.Debugf("LLM Stream Call: %s", reqBody)
				jsonResp, _ := json.Marshal(apiResp)
				logger.Debugf("LLM Stream Response: %s", jsonResp)
			}
			finalResp = apiResp
			return apiResp, nil
		}
		lastErr = err
		if streamed {
			finalErr = err
			return nil, err
		}
		if !visionFallbackUsed && requestHasImageInputs(req) && shouldFallbackVision(err) {
			req = downgradeVisionRequest(req)
			visionFallbackUsed = true
			logger.Warnf("LLM endpoint rejected image input during stream setup, retrying once with metadata fallback: %v", err)
			continue
		}
		if attempt == c.maxRetries || !shouldRetryError(err) {
			finalErr = err
			return nil, err
		}
		if err := c.sleep(ctx, c.retryDelay(attempt, retryAfter)); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				finalErr = err
				return nil, err
			}
			finalErr = lastErr
			return nil, lastErr
		}
	}
	finalErr = lastErr
	return nil, lastErr
}

func (c *Client) callOnce(ctx context.Context, reqBody []byte) (*protocol.Response, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/messages", c.baseURL), bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	// Session affinity for cache-aware routing.
	if usage, ok := UsageContextFromContext(ctx); ok && strings.TrimSpace(usage.SessionID) != "" {
		sid := strings.TrimSpace(usage.SessionID)
		httpReq.Header.Set("session_id", sid)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, resp.Header.Get("Retry-After"), formatAPIError(resp.StatusCode, body)
	}

	var apiResp protocol.Response
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, "", err
	}
	if apiResp.Usage != nil {
		apiResp.Usage.Normalize()
	}
	return &apiResp, "", nil
}

func (c *Client) streamOnce(ctx context.Context, reqBody []byte, handler StreamHandler) (*protocol.Response, string, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/messages", c.baseURL), bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", false, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	// Session affinity for cache-aware routing.
	if usage, ok := UsageContextFromContext(ctx); ok && strings.TrimSpace(usage.SessionID) != "" {
		sid := strings.TrimSpace(usage.SessionID)
		httpReq.Header.Set("session_id", sid)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, resp.Header.Get("Retry-After"), false, formatAPIError(resp.StatusCode, body)
	}

	apiResp, streamed, err := parseMessageStream(resp.Body, handler)
	return apiResp, "", streamed, err
}

func newDefaultTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.MaxConnsPerHost = 0
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return transport
}

func formatAPIError(statusCode int, body []byte) error {
	return &apiStatusError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("API error: %d: %s", statusCode, strings.TrimSpace(string(body))),
	}
}

func shouldRetryError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return shouldRetryError(urlErr.Err)
	}

	var statusErr *apiStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		if statusErr.StatusCode >= 500 {
			return true
		}
	}

	return false
}

func (c *Client) retryDelay(attempt int, retryAfter string) time.Duration {
	if retryAfterDelay, ok := parseRetryAfter(retryAfter); ok {
		return retryAfterDelay
	}

	delay := c.retryBaseDelay << attempt
	if delay <= 0 {
		delay = c.retryBaseDelay
	}
	if c.maxRetryDelay > 0 && delay > c.maxRetryDelay {
		return c.maxRetryDelay
	}
	return delay
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		return maxDuration(time.Until(when), 0), true
	}
	return 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

type streamWireEvent struct {
	Type         string              `json:"type"`
	Index        int                 `json:"index,omitempty"`
	Message      *protocol.Response  `json:"message,omitempty"`
	ContentBlock *streamContentBlock `json:"content_block,omitempty"`
	Delta        streamWireDelta     `json:"delta,omitempty"`
	Error        *streamWireError    `json:"error,omitempty"`
	// Usage is the Anthropic streaming `message_delta.usage` field, which
	// the spec places at the TOP level of the frame alongside `type` and
	// `delta` (not nested inside `delta`). We keep Delta.Usage for legacy
	// providers that nest it, and prefer the top-level field when set.
	Usage *protocol.Usage `json:"usage,omitempty"`
	// OpenAI chat.completion.chunk fields. The standard
	// ProviderOpenAICompatible path uses parseOpenAIStream instead of
	// parseMessageStream, so these fields are only reached when a
	// provider configured as `anthropic_compatible` (or the default
	// type) upstreams an OpenAI-shape SSE service — e.g., a reverse
	// proxy or OpenRouter routing to a non-Anthropic model. In that
	// niche case parseMessageStream receives chat.completion.chunk
	// frames (no top-level "type" field, with
	// choices[].delta.tool_calls[]) and must forward the tool_calls
	// deltas to the handler.
	Choices []openAIWireChoice `json:"choices,omitempty"`
}

type openAIWireChoice struct {
	Index        int             `json:"index"`
	Delta        openAIWireDelta `json:"delta"`
	FinishReason string          `json:"finish_reason"`
}

type openAIWireDelta struct {
	Role      string               `json:"role,omitempty"`
	Content   string               `json:"content,omitempty"`
	ToolCalls []openAIWireToolCall `json:"tool_calls,omitempty"`
}

type openAIWireToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openAIWireFunction `json:"function"`
}

type openAIWireFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type streamWireDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	// Thinking is the partial chain-of-thought text from an
	// extended-thinking content_block_delta. The wire shape is
	// `{type:"thinking_delta", thinking:"..."}` per the Anthropic
	// streaming spec; the client concatenates across chunks.
	Thinking string `json:"thinking,omitempty"`
	// Signature is the partial thinking signature from a
	// `{type:"signature_delta", signature:"..."}` content_block_delta.
	// The signature is opaque and only valid when the model run that
	// minted it is resumed on the same upstream; the gateway
	// forwards it verbatim so Pi can keep multi-turn reasoning
	// context intact.
	Signature  string          `json:"signature,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *protocol.Usage `json:"usage,omitempty"`
}

type streamWireError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

// streamContentBlock captures the wire-side content_block fields
// for non-text, non-tool blocks. Anthropic emits
// `content_block_start { content_block: { type: "thinking",
// thinking: "...", signature: "..." } }` for thinking blocks
// (with the chain-of-thought already populated, not a delta), and
// `{ type: "redacted_thinking", data: "..." }` for redacted ones.
// We surface both shapes into the parser so the final response
// can round-trip them.
type streamContentBlock struct {
	Type      string                 `json:"type,omitempty"`
	Text      string                 `json:"text,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	Thinking  string                 `json:"thinking,omitempty"`
	Signature string                 `json:"signature,omitempty"`
	Data      string                 `json:"data,omitempty"`
}

type streamBlockState struct {
	block       protocol.Block
	partialJSON strings.Builder
	// partialThinking accumulates the visible chain-of-thought text
	// across successive `{type:"thinking_delta"}` content_block_delta
	// frames. The final text is what we keep on the response's
	// BlockThinking entry (along with the signature we accumulated
	// in partialSignature).
	partialThinking  strings.Builder
	partialSignature strings.Builder
}

// recoverPartialToolInput tries to decode the accumulated partial_json from
// an upstream Anthropic-style streamed tool_use input frame. When the stream
// is truncated mid-way (the connection drops, a content_block_stop frame is
// lost, or the closing brace is missing) json.Unmarshal fails. Rather than
// aborting the whole turn with a hard error, we attempt a structural
// best-effort close: open string and open `{` / `[` brackets are closed in
// order from the deepest scope outwards. If recovery succeeds the second
// bool is false and the returned map is the parsed input. If recovery still
// fails we surface the fragment to the caller via the reserved
// `__error__` / `__partial__` keys on the returned map and return true so
// the caller can distinguish a degraded result.
func recoverPartialToolInput(raw string) (map[string]interface{}, bool) {
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &input); err == nil {
		return input, false
	}
	// Best-effort structural close: walk the bytes, track whether we are
	// inside a string literal (skipping backslash-escaped characters), and
	// count the open / close depth of `{` and `[`. Anything left open at
	// the end is closed in reverse order so the JSON decoder sees a
	// balanced object.
	var out strings.Builder
	out.Grow(len(raw) + 8)
	out.WriteString(raw)
	inString := false
	var stack []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			if c == '\\' && i+1 < len(raw) {
				i++
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if inString {
		out.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i] {
		case '{':
			out.WriteByte('}')
		case '[':
			out.WriteByte(']')
		}
	}
	if err := json.Unmarshal([]byte(out.String()), &input); err == nil {
		return input, false
	}
	return map[string]interface{}{
		protocol.ToolInputErrorKey:   "streamed_tool_input_truncated",
		protocol.ToolInputPartialKey: raw,
	}, true
}

// mergeUsageDelta folds a non-message_start usage payload (Anthropic's
// message_delta.usage) into the baseline accumulated earlier. The delta
// typically only contains the fields that have grown since message_start
// (output_tokens, cache_creation_input_tokens, cache_read_input_tokens),
// so we only ever raise a counter — never shrink it — to keep the
// message_stop view monotonic.
func mergeUsageDelta(base, delta *protocol.Usage) {
	if base == nil || delta == nil {
		return
	}
	if delta.OutputTokens > base.OutputTokens {
		base.OutputTokens = delta.OutputTokens
	}
	if delta.InputTokens > base.InputTokens {
		base.InputTokens = delta.InputTokens
	}
	if delta.CacheReadTokens > base.CacheReadTokens {
		base.CacheReadTokens = delta.CacheReadTokens
	}
	if delta.CacheWriteTokens > base.CacheWriteTokens {
		base.CacheWriteTokens = delta.CacheWriteTokens
	}
}

func parseMessageStream(reader io.Reader, handler StreamHandler) (*protocol.Response, bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var dataLines []string
	states := make(map[int]*streamBlockState)
	order := make([]int, 0)
	streamed := false
	response := &protocol.Response{}

	ensureState := func(index int, block protocol.Block) *streamBlockState {
		if state, ok := states[index]; ok {
			if state.block.Type == "" && block.Type != "" {
				state.block = block
			}
			return state
		}
		state := &streamBlockState{block: block}
		states[index] = state
		order = append(order, index)
		return state
	}

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		streamed = true
		var event streamWireEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return err
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				response.StopReason = event.Message.StopReason
				if event.Message.Usage != nil {
					usageCopy := *event.Message.Usage
					usageCopy.Normalize()
					response.Usage = &usageCopy
					// Notify the gateway so it can emit a properly-
					// initialised message_start to the downstream
					// client (pi). Without this hook, the gateway
					// emits message_start with zero usage before
					// the upstream stream starts, and the
					// downstream Anthropic SDK never learns the
					// real input token count — pi displays 0%
					// context usage forever.
					if handler.OnMessageStart != nil {
						handler.OnMessageStart(usageCopy)
					}
				}
			}
		case "content_block_start":
			block := protocol.Block{}
			if event.ContentBlock != nil {
				switch event.ContentBlock.Type {
				case "thinking":
					// Anthropic emits a thinking content_block_start
					// with the chain-of-thought already attached in
					// the same frame. We seed the partialThinking
					// buffer with the seed text so subsequent
					// thinking_delta frames concatenate onto it.
					block = protocol.Block{
						Type:      protocol.BlockThinking,
						Text:      event.ContentBlock.Thinking,
						Signature: event.ContentBlock.Signature,
					}
				case "redacted_thinking":
					// Safety filter replaced the chain-of-thought
					// with an opaque payload. The block is final on
					// creation (no deltas), so we mark it redacted
					// and store the opaque data in the Signature
					// field so the client can echo it back on the
					// next turn.
					block = protocol.Block{
						Type:      protocol.BlockThinking,
						Text:      "[Reasoning redacted]",
						Signature: event.ContentBlock.Data,
						Redacted:  true,
					}
				default:
					block = protocol.Block{
						Type:  protocol.BlockType(event.ContentBlock.Type),
						Text:  event.ContentBlock.Text,
						ID:    event.ContentBlock.ID,
						Name:  event.ContentBlock.Name,
						Input: event.ContentBlock.Input,
					}
				}
			}
			state := ensureState(event.Index, block)
			if block.Type == protocol.BlockThinking {
				if block.Text != "" {
					state.partialThinking.WriteString(block.Text)
					if handler.OnThinkingDelta != nil {
						handler.OnThinkingDelta(block.Text, "")
					}
				}
				if block.Signature != "" {
					state.partialSignature.WriteString(block.Signature)
					if handler.OnThinkingDelta != nil {
						handler.OnThinkingDelta("", block.Signature)
					}
				}
				state.block.Signature = state.partialSignature.String()
				state.block.Text = state.partialThinking.String()
			}
			if block.Type == protocol.BlockText && block.Text != "" && handler.OnTextDelta != nil {
				handler.OnTextDelta(block.Text)
			}
			if block.Type == protocol.BlockToolUse && handler.OnToolUse != nil {
				handler.OnToolUse(block, "")
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				state := ensureState(event.Index, protocol.TextBlock(""))
				state.block.Type = protocol.BlockText
				state.block.Text += event.Delta.Text
				if event.Delta.Text != "" && handler.OnTextDelta != nil {
					handler.OnTextDelta(event.Delta.Text)
				}
			case "input_json_delta":
				state := ensureState(event.Index, protocol.ToolUseBlock("", "", nil))
				if state.block.Type == "" {
					state.block.Type = protocol.BlockToolUse
				}
				state.partialJSON.WriteString(event.Delta.PartialJSON)
				if handler.OnToolUse != nil {
					handler.OnToolUse(state.block, state.partialJSON.String())
				}
			case "thinking_delta":
				// Extended-thinking chain-of-thought text. The
				// client concatenates successive deltas into the
				// final reasoning text. We forward each delta to
				// OnThinkingDelta so the gateway can emit a
				// thinking_delta content_block_delta on the wire
				// (NOT a text_delta — Anthropic's SDK uses the
				// delta type to route the fragment into the
				// chain-of-thought buffer rather than the visible
				// text buffer). The running buffer on the parser
				// side lets the final response carry the full
				// reasoning text.
				state := ensureState(event.Index, protocol.Block{Type: protocol.BlockThinking})
				state.block.Type = protocol.BlockThinking
				state.partialThinking.WriteString(event.Delta.Thinking)
				state.block.Text = state.partialThinking.String()
				if event.Delta.Thinking != "" && handler.OnThinkingDelta != nil {
					handler.OnThinkingDelta(event.Delta.Thinking, "")
				}
			case "signature_delta":
				// The opaque signature the client must echo back.
				// We forward it through OnThinkingDelta so the
				// gateway can emit a signature_delta on the
				// wire; the parser's running buffer lets the
				// final response carry the full signature even
				// when the upstream splits it across multiple
				// frames.
				state := ensureState(event.Index, protocol.Block{Type: protocol.BlockThinking})
				state.block.Type = protocol.BlockThinking
				state.partialSignature.WriteString(event.Delta.Signature)
				state.block.Signature = state.partialSignature.String()
				if event.Delta.Signature != "" && handler.OnThinkingDelta != nil {
					handler.OnThinkingDelta("", event.Delta.Signature)
				}
			}
		case "content_block_stop":
			if state, ok := states[event.Index]; ok {
				switch state.block.Type {
				case protocol.BlockToolUse:
					raw := strings.TrimSpace(state.partialJSON.String())
					if raw != "" {
						input, _ := recoverPartialToolInput(raw)
						state.block.Input = input
					}
					if state.block.Input == nil {
						state.block.Input = map[string]interface{}{}
					}
					if handler.OnToolUse != nil {
						handler.OnToolUse(state.block, state.partialJSON.String())
					}
				case protocol.BlockThinking:
					// Lock in the final accumulated text and
					// signature. The upstream Anthropic server
					// sends the signature as the trailing frame
					// (sometimes inside a signature_delta, sometimes
					// inline on content_block_start), so we read
					// whichever the upstream provided.
					state.block.Text = state.partialThinking.String()
					state.block.Signature = state.partialSignature.String()
				}
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				response.StopReason = event.Delta.StopReason
			}
			// Anthropic streaming spec puts `usage` at the top level of
			// `message_delta` (alongside `type` and `delta`), not inside
			// `delta`. We prefer the top-level field when set and fall
			// back to Delta.Usage for legacy providers that nest it.
			usageDelta := event.Usage
			if usageDelta == nil {
				usageDelta = event.Delta.Usage
			}
			if usageDelta != nil {
				if response.Usage == nil {
					usage := *usageDelta
					usage.Normalize()
					response.Usage = &usage
				} else {
					// Anthropic sends deltas with only the fields that
					// changed. Merge: cache / output token deltas override
					// the message_start baseline, but we never shrink a
					// non-zero value because that would be lossy.
					mergeUsageDelta(response.Usage, usageDelta)
				}
			}
		case "message_stop":
			// Soft-close any blocks the upstream did not send a
			// content_block_stop for (e.g. the connection dropped
			// between the last input_json_delta and the stop frame).
			// Without this, those tool_use blocks would land on the
			// response with an empty Input even though their partial
			// JSON accumulated fine. We only re-run the recovery path
			// on blocks whose Input is empty (or nil) — a populated
			// map means the upstream already gave us a complete
			// object and we should not overwrite it.
			for _, st := range states {
				if st == nil {
					continue
				}
				if st.block.Type != protocol.BlockToolUse {
					continue
				}
				if len(st.block.Input) > 0 {
					continue
				}
				raw := strings.TrimSpace(st.partialJSON.String())
				if raw == "" {
					if st.block.Input == nil {
						st.block.Input = map[string]interface{}{}
					}
					continue
				}
				input, _ := recoverPartialToolInput(raw)
				st.block.Input = input
			}
			return nil
		case "ping":
			return nil
		case "error":
			if event.Error != nil && event.Error.Message != "" {
				return fmt.Errorf("stream error: %s", event.Error.Message)
			}
			return fmt.Errorf("stream error")
		}
		// OpenAI compatibility branch for providers that emit
		// chat.completion.chunk frames (the standard
		// ProviderOpenAICompatible path uses parseOpenAIStream, so this
		// branch only runs for providers configured as
		// `anthropic_compatible` (or the default type) whose upstream
		// is actually an OpenAI-shape service). Each frame carries one
		// tool_calls array entry with the per-chunk fragment in
		// call.Function.Arguments — the receiving OpenAI SDK
		// concatenates arguments across chunks, so we forward the
		// fragment verbatim without re-accumulating. The branch runs
		// unconditionally (rather than gated on event.Type == "")
		// because Anthropic-shape frames may also have an empty type
		// for some optional events, and the empty Choices slice is a
		// no-op. We also forward the upstream's tool call index so
		// multiple parallel tool calls in the same turn keep their
		// identity through the gateway.
		if handler.OnToolUse != nil {
			for _, choice := range event.Choices {
				for _, call := range choice.Delta.ToolCalls {
					block := protocol.Block{
						Type:  protocol.BlockToolUse,
						ID:    call.ID,
						Name:  call.Function.Name,
						Index: call.Index,
					}
					handler.OnToolUse(block, call.Function.Arguments)
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return nil, streamed, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, streamed, err
	}
	if err := flush(); err != nil {
		return nil, streamed, err
	}

	response.Content = make([]protocol.Block, 0, len(order))
	for _, index := range order {
		block := states[index].block
		switch block.Type {
		case protocol.BlockText:
			response.Content = append(response.Content, protocol.TextBlock(block.Text))
		case protocol.BlockToolUse:
			response.Content = append(response.Content, protocol.ToolUseBlock(block.ID, block.Name, block.Input))
		case protocol.BlockThinking:
			// Preserve the visible chain-of-thought text and
			// signature so the gateway can return a non-streamed
			// response containing the full reasoning context.
			response.Content = append(response.Content, protocol.ThinkingBlock(block.Text, block.Signature, block.Redacted))
		}
	}
	return response, streamed, nil
}

type apiStatusError struct {
	StatusCode int
	Message    string
}

func (e *apiStatusError) Error() string {
	return e.Message
}

func requestHasImageInputs(req protocol.Request) bool {
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.Type == protocol.BlockImage {
				return true
			}
		}
	}
	return false
}

func shouldFallbackVision(err error) bool {
	var statusErr *apiStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	case http.StatusBadRequest:
		lower := strings.ToLower(statusErr.Message)
		for _, needle := range []string{"image", "vision", "media_type", "base64", "unsupported"} {
			if strings.Contains(lower, needle) {
				return true
			}
		}
	}
	return false
}

func downgradeVisionRequest(req protocol.Request) protocol.Request {
	downgraded := req
	downgraded.Messages = make([]protocol.APIMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		containsImage := false
		content := make([]protocol.Block, 0, len(msg.Content))
		for _, block := range msg.Content {
			if block.Type == protocol.BlockImage {
				containsImage = true
				continue
			}
			content = append(content, cloneAPIBlock(block))
		}
		if containsImage {
			content = append([]protocol.Block{protocol.TextBlock(visionFallbackPromptNote())}, content...)
		}
		if len(content) == 0 {
			continue
		}
		downgraded.Messages = append(downgraded.Messages, protocol.APIMessage{
			Role:    msg.Role,
			Content: content,
		})
	}
	return downgraded
}

func visionFallbackPromptNote() string {
	return "System note: native image understanding is not enabled in the current model endpoint for this request. The image attachment is still part of the session, but you must not claim to have visually inspected it. Explain clearly that image understanding is unavailable in the current environment if the user asks about visual details."
}

func cloneAPIBlock(block protocol.Block) protocol.Block {
	cloned := protocol.Block{
		Type:      block.Type,
		Text:      block.Text,
		ID:        block.ID,
		Name:      block.Name,
		Input:     cloneInput(block.Input),
		ToolUseID: block.ToolUseID,
		Content:   block.Content,
	}
	if block.Source != nil {
		source := *block.Source
		cloned.Source = &source
	}
	return cloned
}

func cloneInput(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

// marshalAnthropicBody builds the Anthropic /v1/messages wire format with
// prompt-cache breakpoints on the system prompt, last tool, and last message.
func marshalAnthropicBody(req protocol.Request) ([]byte, error) {
	payload := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"stream":     req.Stream,
	}

	// Build cache_control value with optional TTL based on PromptCacheRetention.
	// - "short" / "" → {type: "ephemeral"} (no TTL, default)
	// - "24h"        → {type: "ephemeral", ttl: "24h"}
	cacheCtrl := cacheControlValue(req.PromptCacheRetention)

	// System prompt: use content-block array with cache_control for native
	// Anthropic API calls. For compatible providers, keep as plain string
	// since some reject the array format.
	if strings.TrimSpace(req.System) != "" {
		if req.AnthropicNative && cacheCtrl != nil {
			payload["system"] = []map[string]interface{}{
				{
					"type":          "text",
					"text":          req.System,
					"cache_control": cacheCtrl,
				},
			}
		} else {
			payload["system"] = req.System
		}
	}

	// Tools: mark the last tool definition with cache_control.
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for i, tool := range req.Tools {
			t := map[string]interface{}{
				"name":         tool.Name,
				"description":  tool.Description,
				"input_schema": tool.InputSchema,
			}
			if i == len(req.Tools)-1 && cacheCtrl != nil {
				t["cache_control"] = cacheCtrl
			}
			tools = append(tools, t)
		}
		payload["tools"] = tools
	}

	// Messages: mark the last text block in the last message with cache_control.
	msgs := make([]map[string]interface{}, 0, len(req.Messages))
	for mi, apiMsg := range req.Messages {
		isLast := mi == len(req.Messages)-1
		content, lastTextIdx := anthropicContentBlocks(apiMsg, isLast)
		m := map[string]interface{}{
			"role":    apiMsg.Role,
			"content": content,
		}
		if isLast && lastTextIdx >= 0 && cacheCtrl != nil {
			if block, ok := content[lastTextIdx].(map[string]interface{}); ok {
				block["cache_control"] = cacheCtrl
			}
		}
		msgs = append(msgs, m)
	}
	payload["messages"] = msgs

	return json.Marshal(payload)
}

// cacheControlValue returns a cache_control map based on the retention setting.
// Returns nil when there is no cache key (caching disabled).
func cacheControlValue(retention string) map[string]interface{} {
	switch strings.TrimSpace(retention) {
	case "":
		return nil
	case "short":
		return map[string]interface{}{"type": "ephemeral"}
	case "24h", "long":
		return map[string]interface{}{"type": "ephemeral", "ttl": "24h"}
	default:
		// Custom TTL: use the retention value as TTL (e.g. "1h", "30m")
		return map[string]interface{}{"type": "ephemeral", "ttl": strings.TrimSpace(retention)}
	}
}

// anthropicContentBlocks converts APIMessage content blocks into the
// Anthropic wire format and returns the index of the last text block.
func anthropicContentBlocks(msg protocol.APIMessage, trackLastText bool) ([]interface{}, int) {
	blocks := make([]interface{}, 0, len(msg.Content))
	lastTextIdx := -1
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockText:
			if strings.TrimSpace(block.Text) != "" {
				if trackLastText {
					lastTextIdx = len(blocks)
				}
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": block.Text,
				})
			}
		case protocol.BlockImage:
			if block.Source != nil {
				blocks = append(blocks, map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type":       block.Source.Type,
						"media_type": block.Source.MediaType,
						"data":       block.Source.Data,
					},
				})
			}
		case protocol.BlockToolUse:
			input := block.Input
			if input == nil {
				input = map[string]interface{}{}
			}
			blocks = append(blocks, map[string]interface{}{
				"type":  "tool_use",
				"id":    block.ID,
				"name":  block.Name,
				"input": input,
			})
		case protocol.BlockToolResult:
			toolResult := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": block.ToolUseID,
				"content":     block.Content,
			}
			if block.IsError {
				toolResult["is_error"] = true
			}
			blocks = append(blocks, toolResult)
		}
	}
	return blocks, lastTextIdx
}
