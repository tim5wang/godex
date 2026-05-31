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
func (c *Client) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start)})
	}()
	var lastErr error
	visionFallbackUsed := false
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		reqBody, err := json.Marshal(req)
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
	var lastErr error
	visionFallbackUsed := false
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req.Stream = true
		reqBody, err := json.Marshal(req)
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
	Type         string             `json:"type"`
	Index        int                `json:"index"`
	Message      *protocol.Response `json:"message,omitempty"`
	ContentBlock *protocol.Block    `json:"content_block,omitempty"`
	Delta        streamWireDelta    `json:"delta,omitempty"`
	Error        *streamWireError   `json:"error,omitempty"`
}

type streamWireDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type streamWireError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

type streamBlockState struct {
	block       protocol.Block
	partialJSON strings.Builder
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
			}
		case "content_block_start":
			block := protocol.Block{}
			if event.ContentBlock != nil {
				block = *event.ContentBlock
			}
			ensureState(event.Index, block)
			if block.Type == protocol.BlockText && block.Text != "" && handler.OnTextDelta != nil {
				handler.OnTextDelta(block.Text)
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
			}
		case "content_block_stop":
			if state, ok := states[event.Index]; ok && state.block.Type == protocol.BlockToolUse {
				raw := strings.TrimSpace(state.partialJSON.String())
				if raw != "" {
					var input map[string]interface{}
					if err := json.Unmarshal([]byte(raw), &input); err != nil {
						return fmt.Errorf("decode streamed tool input: %w", err)
					}
					state.block.Input = input
				}
				if state.block.Input == nil {
					state.block.Input = map[string]interface{}{}
				}
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				response.StopReason = event.Delta.StopReason
			}
		case "message_stop":
			return nil
		case "ping":
			return nil
		case "error":
			if event.Error != nil && event.Error.Message != "" {
				return fmt.Errorf("stream error: %s", event.Error.Message)
			}
			return fmt.Errorf("stream error")
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
