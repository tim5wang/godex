package conversation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/logger"
)

// OpenAIClient sends OpenAI-compatible Chat Completions requests.
type OpenAIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewOpenAIClient creates an OpenAI-compatible conversation client.
func NewOpenAIClient(baseURL, apiKey string, timeout time.Duration) *OpenAIClient {
	httpClient := &http.Client{Transport: newDefaultTransport()}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	return &OpenAIClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

func (c *OpenAIClient) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start)})
	}()
	body, err := c.buildRequest(req, false)
	if err != nil {
		finalErr = err
		return nil, err
	}
	httpResp, err := c.do(ctx, body, false)
	if err != nil {
		finalErr = err
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(httpResp.Body)
		finalErr = formatAPIError(httpResp.StatusCode, data)
		return nil, finalErr
	}
	var decoded openAIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		finalErr = err
		return nil, err
	}
	if logger.LevelEnabled(logger.LevelDebug) {
		logger.Debugf("OpenAI-compatible LLM Call: %s", string(body))
	}
	response := openAIResponseToProtocol(decoded)
	finalResp = response
	if logger.LevelEnabled(logger.LevelDebug) {
		debug, _ := json.Marshal(response)
		logger.Debugf("OpenAI-compatible LLM Response: %s", string(debug))
	}
	return response, nil
}

func (c *OpenAIClient) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start), Stream: true})
	}()
	body, err := c.buildRequest(req, true)
	if err != nil {
		finalErr = err
		return nil, err
	}
	httpResp, err := c.do(ctx, body, true)
	if err != nil {
		finalErr = err
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(httpResp.Body)
		// Some OpenAI-compatible providers reject stream_options with HTTP
		// 400. Retry once without it so they keep working; the only loss is
		// usage observability (the previous behavior) for such providers.
		if httpResp.StatusCode == http.StatusBadRequest && bodyHasStreamOptions(body) {
			httpResp.Body.Close()
			if plainBody, buildErr := c.buildRequestBody(req, true, false); buildErr == nil {
				if retried, doErr := c.do(ctx, plainBody, true); doErr == nil {
					if retried.StatusCode == http.StatusOK {
						defer retried.Body.Close()
						finalResp, finalErr = parseOpenAIStream(retried.Body, handler)
						return finalResp, finalErr
					}
					data, _ = io.ReadAll(retried.Body)
					retried.Body.Close()
				}
			}
		}
		finalErr = formatAPIError(httpResp.StatusCode, data)
		return nil, finalErr
	}
	finalResp, finalErr = parseOpenAIStream(httpResp.Body, handler)
	return finalResp, finalErr
}

func (c *OpenAIClient) buildRequest(req protocol.Request, stream bool) ([]byte, error) {
	return c.buildRequestBody(req, stream, true)
}

// buildRequestBody builds the wire payload. includeUsage controls whether a
// streaming request asks the provider to emit a final usage chunk via
// stream_options; the 400-fallback in Stream passes false to retry without it
// for providers that reject the field.
func (c *OpenAIClient) buildRequestBody(req protocol.Request, stream, includeUsage bool) ([]byte, error) {
	msgs := openAIMessagesFromProtocol(req)
	tools := openAIToolsFromProtocol(req.Tools)

	// Anthropic-style cache_control breakpoints for OpenRouter / Anthropic routing.
	// Only apply when:
	// 1. We have a PromptCacheKey (caching is desired)
	// 2. The model name starts with "anthropic/" (meaning OpenRouter routing to Anthropic)
	// 3. We have tools or messages to annotate
	//
	// Other OpenAI-compatible providers (DeepSeek, etc.) don't understand
	// Anthropic-style content array format, so they are safely skipped.
	useAnthropicCache := req.PromptCacheKey != "" && strings.HasPrefix(req.Model, "anthropic/")
	if useAnthropicCache {
		cacheCtrl := cacheControlValueOpenAI(req.PromptCacheRetention)

		// Mark the last tool definition with cache_control.
		if cacheCtrl != nil && len(tools) > 0 {
			lastTool := tools[len(tools)-1]
			lastTool.CacheControl = cacheCtrl
			tools[len(tools)-1] = lastTool
		}

		// Convert the last message's string content to array format with cache_control.
		if cacheCtrl != nil && len(msgs) > 0 {
			lastMsg := msgs[len(msgs)-1]
			if lastMsg.Content != "" {
				lastMsg.ContentParts = []openAIContentPart{
					{Type: "text", Text: lastMsg.Content, CacheControl: cacheCtrl},
				}
				lastMsg.Content = ""
				msgs[len(msgs)-1] = lastMsg
			}
		}
	}

	wire := openAIRequest{
		Model:                req.Model,
		MaxTokens:            req.MaxTokens,
		Stream:               stream,
		Messages:             msgs,
		Tools:                tools,
		StreamOptions:        streamOptionsFor(stream, includeUsage),
		ReasoningEffort:      normalizeOpenAIReasoningEffort(req.ReasoningEffort),
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
	}
	if strings.TrimSpace(req.System) != "" {
		sysMsg := openAIMessage{Role: "system", Content: req.System}
		// For OpenRouter + Anthropic models, add cache_control to system prompt.
		if useAnthropicCache {
			cacheCtrl := cacheControlValueOpenAI(req.PromptCacheRetention)
			if cacheCtrl != nil {
				sysMsg.ContentParts = []openAIContentPart{
					{Type: "text", Text: req.System, CacheControl: cacheCtrl},
				}
				sysMsg.Content = ""
			}
		}
		wire.Messages = append([]openAIMessage{sysMsg}, wire.Messages...)
	}
	return json.Marshal(wire)
}

// cacheControlValueOpenAI returns a cache_control object based on retention.
// Returns nil when retention is empty (caching disabled).
func cacheControlValueOpenAI(retention string) *openAICacheControl {
	switch strings.TrimSpace(retention) {
	case "":
		return nil
	case "short":
		return &openAICacheControl{Type: "ephemeral"}
	case "24h", "long":
		return &openAICacheControl{Type: "ephemeral", TTL: "24h"}
	default:
		return &openAICacheControl{Type: "ephemeral", TTL: strings.TrimSpace(retention)}
	}
}

func (c *OpenAIClient) do(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	endpoint := c.baseURL
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/v1/chat/completions"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if strings.TrimSpace(c.apiKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	// Session affinity headers for cache-aware routing.
	if usage, ok := UsageContextFromContext(ctx); ok && strings.TrimSpace(usage.SessionID) != "" {
		sid := strings.TrimSpace(usage.SessionID)
		httpReq.Header.Set("session_id", sid)
		httpReq.Header.Set("x-client-request-id", sid)
		httpReq.Header.Set("x-session-affinity", sid)
	}
	return c.httpClient.Do(httpReq)
}

type openAIRequest struct {
	Model                string               `json:"model"`
	Messages             []openAIMessage      `json:"messages"`
	MaxTokens            int                  `json:"max_tokens,omitempty"`
	Tools                []openAITool         `json:"tools,omitempty"`
	Stream               bool                 `json:"stream,omitempty"`
	StreamOptions        *openAIStreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort      string               `json:"reasoning_effort,omitempty"`
	PromptCacheKey       string               `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string               `json:"prompt_cache_retention,omitempty"`
}

// openAIStreamOptions is the OpenAI-standard stream_options payload. Setting
// include_usage asks the provider to emit a final usage chunk (input/output
// and cached_tokens) at the end of the stream. Without it, endpoints like
// Volcengine ARK / GLM coding APIs omit usage from streaming responses
// entirely, so godex cannot observe cache hits and reports a 0% hit rate even
// though the server caches well.
type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamOptionsFor returns the stream_options payload for streaming requests
// that ask for usage. Non-streaming requests already carry usage in the
// response body, and the 400-fallback in Stream requests without it, so no
// stream_options is set in either case.
func streamOptionsFor(stream, includeUsage bool) *openAIStreamOptions {
	if !stream || !includeUsage {
		return nil
	}
	return &openAIStreamOptions{IncludeUsage: true}
}

// bodyHasStreamOptions reports whether a request body asked for stream usage.
func bodyHasStreamOptions(body []byte) bool {
	return bytes.Contains(body, []byte(`"stream_options"`))
}

type openAIContentPart struct {
	Type         string              `json:"type"`
	Text         string              `json:"text,omitempty"`
	CacheControl *openAICacheControl `json:"cache_control,omitempty"`
}

type openAICacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type openAIMessage struct {
	Role             string              `json:"role"`
	Content          string              `json:"content,omitempty"`
	ContentParts     []openAIContentPart `json:"-"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall    `json:"tool_calls,omitempty"`
}

// MarshalJSON supports both string content and array content with cache_control.
// The content field is ALWAYS emitted, even when empty: strict OpenAI-compatible
// gateways (Volcengine ARK, AIS/company gateways) reject messages whose content
// key is missing entirely with a 400 ("missing field `content`"). Pure tool-call
// assistant turns and empty tool results would otherwise omit it.
func (m openAIMessage) MarshalJSON() ([]byte, error) {
	payload := map[string]interface{}{
		"role":    m.Role,
		"content": m.Content,
	}
	if len(m.ContentParts) > 0 {
		payload["content"] = m.ContentParts
	}
	if m.ReasoningContent != "" {
		payload["reasoning_content"] = m.ReasoningContent
	}
	if m.ToolCallID != "" {
		payload["tool_call_id"] = m.ToolCallID
	}
	if len(m.ToolCalls) > 0 {
		payload["tool_calls"] = m.ToolCalls
	}
	return json.Marshal(payload)
}

type openAITool struct {
	Type         string              `json:"type"`
	Function     openAIFunction      `json:"function"`
	CacheControl *openAICacheControl `json:"cache_control,omitempty"`
}

type openAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Strict      bool                   `json:"strict,omitempty"`
}

type openAIToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function openAIToolCallFn `json:"function"`
	Index    int              `json:"index,omitempty"`
}

type openAIToolCallFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage,omitempty"`
}

type openAIUsage struct {
	PromptTokens            int                     `json:"prompt_tokens,omitempty"`
	CompletionTokens        int                     `json:"completion_tokens,omitempty"`
	PromptTokensDetails     openAITokenUsageDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails openAITokenUsageDetails `json:"completion_tokens_details,omitempty"`
	InputTokens             int                     `json:"input_tokens,omitempty"`
	OutputTokens            int                     `json:"output_tokens,omitempty"`
	InputTokensDetails      openAITokenUsageDetails `json:"input_tokens_details,omitempty"`
	OutputTokensDetails     openAITokenUsageDetails `json:"output_tokens_details,omitempty"`
	// DeepSeek-style providers report cache hit/miss as top-level fields
	// instead of the OpenAI *_details sub-object.
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
}

type openAITokenUsageDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	Delta        openAIMessage `json:"delta"`
	FinishReason string        `json:"finish_reason"`
}

func openAIMessagesFromProtocol(req protocol.Request) []openAIMessage {
	out := make([]openAIMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		switch msg.Role {
		case protocol.RoleAssistant:
			out = append(out, openAIAssistantMessage(msg))
		default:
			out = append(out, openAIUserMessages(msg)...)
		}
	}
	return out
}

func openAIUserMessages(msg protocol.APIMessage) []openAIMessage {
	out := make([]openAIMessage, 0, 1)
	var text strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockToolResult:
			if strings.TrimSpace(text.String()) != "" {
				out = append(out, openAIMessage{Role: "user", Content: strings.TrimSpace(text.String())})
				text.Reset()
			}
			out = append(out, openAIMessage{Role: "tool", ToolCallID: block.ToolUseID, Content: toolResultWireContent(block.Content)})
		case protocol.BlockText:
			if block.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			}
		case protocol.BlockImage:
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			mediaType := ""
			if block.Source != nil {
				mediaType = block.Source.MediaType
			}
			text.WriteString("[image attachment")
			if mediaType != "" {
				text.WriteString(": ")
				text.WriteString(mediaType)
			}
			text.WriteString("]")
		}
	}
	if strings.TrimSpace(text.String()) != "" || len(out) == 0 {
		out = append(out, openAIMessage{Role: "user", Content: strings.TrimSpace(text.String())})
	}
	return out
}

// toolResultWireContent returns the tool-result content to place on the wire.
// Empty tool output still needs non-empty content on the wire for strict
// OpenAI-compatible gateways (same guard DSH applies): a tool message whose
// content is an empty string can be rejected by serde that treats "" as
// "not provided".
func toolResultWireContent(content string) string {
	if strings.TrimSpace(content) == "" {
		return "(no output)"
	}
	return content
}

// normalizeOpenAIReasoningEffort validates a reasoning_effort hint before it
// goes on the openai_compatible wire. The OpenAI-compatible reasoning field
// is a plain string in the standard vocabulary (none|minimal|low|medium|high|
// xhigh); unknown values are dropped so a typo cannot be forwarded to the
// provider and rejected.
func normalizeOpenAIReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func openAIAssistantMessage(msg protocol.APIMessage) openAIMessage {
	var text strings.Builder
	var calls []openAIToolCall
	for _, block := range msg.Content {
		switch block.Type {
		case protocol.BlockText:
			if block.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			}
		case protocol.BlockToolUse:
			input := block.Input
			if input == nil {
				input = map[string]interface{}{}
			}
			args, _ := json.Marshal(input)
			calls = append(calls, openAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: openAIToolCallFn{
					Name:      block.Name,
					Arguments: string(args),
				},
			})
		}
	}
	return openAIMessage{Role: "assistant", Content: strings.TrimSpace(text.String()), ReasoningContent: msg.ReasoningContent, ToolCalls: calls}
}

func openAIToolsFromProtocol(tools []protocol.ToolSchema) []openAITool {
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		params := openAIFunctionParameters(tool.InputSchema)
		out = append(out, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  params,
				Strict:      openAIJSONSchemaStrictCompatible(params),
			},
		})
	}
	return out
}

func openAIFunctionParameters(schema map[string]interface{}) map[string]interface{} {
	params := map[string]interface{}{}
	for key, item := range schema {
		params[key] = normalizeOpenAIJSONSchemaValue(item)
	}
	if _, ok := params["type"]; !ok {
		params["type"] = "object"
	}
	return normalizeOpenAIJSONSchemaObject(params)
}

func normalizeOpenAIJSONSchemaObject(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	if schema["type"] == "object" {
		properties, ok := schema["properties"].(map[string]interface{})
		if !ok || properties == nil {
			schema["properties"] = map[string]interface{}{}
		} else {
			cloned := make(map[string]interface{}, len(properties))
			for name, property := range properties {
				cloned[name] = normalizeOpenAIJSONSchemaValue(property)
			}
			schema["properties"] = cloned
		}
		if _, ok := schema["additionalProperties"]; !ok {
			schema["additionalProperties"] = false
		}
	}
	if items, ok := schema["items"]; ok {
		schema["items"] = normalizeOpenAIJSONSchemaValue(items)
	}
	return schema
}

func openAIJSONSchemaStrictCompatible(schema map[string]interface{}) bool {
	if schema == nil {
		return true
	}
	if schema["type"] == "object" {
		if schema["additionalProperties"] != false {
			return false
		}
		properties, ok := schema["properties"].(map[string]interface{})
		if !ok {
			return false
		}
		required := openAIRequiredSet(schema["required"])
		for name, property := range properties {
			if _, ok := required[name]; !ok {
				return false
			}
			if !openAIJSONSchemaValueStrictCompatible(property) {
				return false
			}
		}
	}
	if items, ok := schema["items"]; ok {
		return openAIJSONSchemaValueStrictCompatible(items)
	}
	return true
}

func openAIJSONSchemaValueStrictCompatible(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		return openAIJSONSchemaStrictCompatible(typed)
	case []interface{}:
		for _, item := range typed {
			if !openAIJSONSchemaValueStrictCompatible(item) {
				return false
			}
		}
	}
	return true
}

func openAIRequiredSet(value interface{}) map[string]struct{} {
	required := map[string]struct{}{}
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			required[item] = struct{}{}
		}
	case []interface{}:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				required[name] = struct{}{}
			}
		}
	}
	return required
}

func normalizeOpenAIJSONSchemaValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			cloned[key] = normalizeOpenAIJSONSchemaValue(item)
		}
		return normalizeOpenAIJSONSchemaObject(cloned)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for i, item := range typed {
			cloned[i] = normalizeOpenAIJSONSchemaValue(item)
		}
		return cloned
	default:
		return value
	}
}

func openAIResponseToProtocol(resp openAIResponse) *protocol.Response {
	if len(resp.Choices) == 0 {
		return &protocol.Response{Usage: openAIUsageToProtocol(resp.Usage)}
	}
	choice := resp.Choices[0]
	blocks := make([]protocol.Block, 0, 1+len(choice.Message.ToolCalls))
	if text := strings.TrimSpace(choice.Message.Content); text != "" {
		blocks = append(blocks, protocol.TextBlock(text))
	}
	for _, call := range choice.Message.ToolCalls {
		blocks = append(blocks, openAIToolCallToBlock(call))
	}
	return &protocol.Response{Content: blocks, StopReason: choice.FinishReason, ReasoningContent: choice.Message.ReasoningContent, Usage: openAIUsageToProtocol(resp.Usage)}
}

func openAIUsageToProtocol(usage openAIUsage) *protocol.Usage {
	input := usage.PromptTokens
	if input == 0 {
		input = usage.InputTokens
	}
	output := usage.CompletionTokens
	if output == 0 {
		output = usage.OutputTokens
	}
	cacheRead := usage.PromptTokensDetails.CachedTokens + usage.PromptTokensDetails.CacheReadTokens + usage.InputTokensDetails.CachedTokens + usage.InputTokensDetails.CacheReadTokens
	cacheWrite := usage.PromptTokensDetails.CacheWriteTokens + usage.InputTokensDetails.CacheWriteTokens
	if usage.PromptCacheHitTokens > 0 {
		// DeepSeek reports cache hits only via the top-level field.
		cacheRead += usage.PromptCacheHitTokens
	}
	// Normalize InputTokens to the Anthropic convention: it counts only the
	// UNCACHED prompt tokens. OpenAI-style prompt_tokens / input_tokens
	// include the cached portion, so subtract it to keep the canonical
	// protocol.Usage comparable across providers (cache hit rate =
	// cache_read / (input + cache_read)). DeepSeek goes further and reports
	// the miss count directly, which is exactly the uncached input.
	if usage.PromptCacheMissTokens > 0 {
		input = usage.PromptCacheMissTokens
	} else if cacheRead > 0 && input >= cacheRead {
		input -= cacheRead
	}
	if input == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		return nil
	}
	return &protocol.Usage{
		InputTokens:      input,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}
}

func openAIToolCallToBlock(call openAIToolCall) protocol.Block {
	input := parseToolArguments(call.Function.Arguments)
	block := protocol.ToolUseBlock(call.ID, call.Function.Name, input)
	// Forward the OpenAI tool_calls[].index so the gateway can emit
	// the same index on the wire and the OpenAI SDK's per-chunk
	// dedup (which keys on index) works for multiple tool calls in
	// a single assistant turn. Anthropic callers ignore this field.
	block.Index = call.Index
	return block
}

func parseOpenAIStream(reader io.Reader, handler StreamHandler) (*protocol.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var text strings.Builder
	calls := map[int]*openAIToolCall{}
	order := make([]int, 0)
	finishReason := ""
	var reasoning strings.Builder
	// Track the latest usage from the stream. OpenAI providers emit
	// usage in the final chunk (the one with finish_reason), and some
	// providers also include it in every chunk. We capture the last
	// non-empty usage so the gateway can surface input/output/cache
	// token counts to the downstream client (e.g. pi's context-usage
	// display). Without this, pi always shows 0% because the upstream
	// provider's token counts never reach the client.
	var lastUsage *protocol.Usage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event openAIResponse
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, err
		}
		// Capture usage from the stream. OpenAI providers emit usage in
		// the final chunk (the one with finish_reason) and optionally in
		// every chunk. We convert the last non-empty usage payload to
		// protocol.Usage so the gateway can relay it to downstream
		// clients (e.g. pi for context usage display).
		if usage := openAIUsageToProtocol(event.Usage); usage != nil {
			lastUsage = usage
		}
		if len(event.Choices) == 0 {
			continue
		}
		choice := event.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		if choice.Delta.Content != "" {
			text.WriteString(choice.Delta.Content)
			if handler.OnTextDelta != nil {
				handler.OnTextDelta(choice.Delta.Content)
			}
		}
		if choice.Delta.ReasoningContent != "" {
			reasoning.WriteString(choice.Delta.ReasoningContent)
		}
		for _, deltaCall := range choice.Delta.ToolCalls {
			idx := deltaCall.Index
			call := calls[idx]
			if call == nil {
				call = &openAIToolCall{Index: idx, Type: "function"}
				calls[idx] = call
				order = append(order, idx)
			}
			if deltaCall.ID != "" {
				call.ID = deltaCall.ID
			}
			if deltaCall.Type != "" {
				call.Type = deltaCall.Type
			}
			if deltaCall.Function.Name != "" {
				call.Function.Name = deltaCall.Function.Name
			}
			if deltaCall.Function.Arguments != "" {
				call.Function.Arguments += deltaCall.Function.Arguments
			}
			// Notify the StreamHandler that a tool_call delta arrived so the
			// OpenAI usage-gateway (routes_usage.go streamUsageGatewayChatCompletions)
			// can forward the delta.tool_calls chunk to the wire. OpenAI SDKs and
			// Codex-style clients depend on these deltas to assemble JSON arguments
			// in their tool loops. We pass deltaCall.Function.Arguments (the new
			// fragment that just arrived in this SSE frame) rather than
			// call.Function.Arguments (the cumulative string we accumulate
			// locally). OpenAI SDKs concatenate arguments across chunks, so
			// sending the cumulative string on every chunk would corrupt the
			// final JSON. Mirror of parseMessageStream's OnToolUse hook for the
			// Anthropic path (see internal/core/conversation/client.go).
			if handler.OnToolUse != nil {
				handler.OnToolUse(openAIToolCallToBlock(*call), deltaCall.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	blocks := make([]protocol.Block, 0, 1+len(order))
	// Surface OpenAI `reasoning_content` deltas as a thinking block
	// (alongside the consolidated `ReasoningContent` string the
	// OpenAI response shape expects). Pi's OpenAI provider does the
	// same — reasoning is just a thinking block with no signature —
	// so the gateway can round-trip to either OpenAI or Anthropic
	// shape without losing the model's chain-of-thought.
	if strings.TrimSpace(reasoning.String()) != "" {
		blocks = append(blocks, protocol.ThinkingBlock(reasoning.String(), "", false))
	}
	if strings.TrimSpace(text.String()) != "" {
		blocks = append(blocks, protocol.TextBlock(text.String()))
	}
	for _, idx := range order {
		if calls[idx] != nil {
			blocks = append(blocks, openAIToolCallToBlock(*calls[idx]))
		}
	}
	return &protocol.Response{Content: blocks, StopReason: finishReason, ReasoningContent: reasoning.String(), Usage: lastUsage}, nil
}
