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
		finalErr = formatAPIError(httpResp.StatusCode, data)
		return nil, finalErr
	}
	finalResp, finalErr = parseOpenAIStream(httpResp.Body, handler)
	return finalResp, finalErr
}

func (c *OpenAIClient) buildRequest(req protocol.Request, stream bool) ([]byte, error) {
	wire := openAIRequest{
		Model:                req.Model,
		MaxTokens:            req.MaxTokens,
		Stream:               stream,
		Messages:             openAIMessagesFromProtocol(req),
		Tools:                openAIToolsFromProtocol(req.Tools),
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
	}
	if strings.TrimSpace(req.System) != "" {
		wire.Messages = append([]openAIMessage{{Role: "system", Content: req.System}}, wire.Messages...)
	}
	return json.Marshal(wire)
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
	Model                string          `json:"model"`
	Messages             []openAIMessage `json:"messages"`
	MaxTokens            int             `json:"max_tokens,omitempty"`
	Tools                []openAITool    `json:"tools,omitempty"`
	Stream               bool            `json:"stream,omitempty"`
	PromptCacheKey       string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string          `json:"prompt_cache_retention,omitempty"`
}

type openAIMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
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
			out = append(out, openAIMessage{Role: "tool", ToolCallID: block.ToolUseID, Content: block.Content})
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
	input := map[string]interface{}{}
	if strings.TrimSpace(call.Function.Arguments) != "" {
		_ = json.Unmarshal([]byte(call.Function.Arguments), &input)
	}
	return protocol.ToolUseBlock(call.ID, call.Function.Name, input)
}

func parseOpenAIStream(reader io.Reader, handler StreamHandler) (*protocol.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var text strings.Builder
	calls := map[int]*openAIToolCall{}
	order := make([]int, 0)
	finishReason := ""
	var reasoning strings.Builder
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
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	blocks := make([]protocol.Block, 0, 1+len(order))
	if strings.TrimSpace(text.String()) != "" {
		blocks = append(blocks, protocol.TextBlock(text.String()))
	}
	for _, idx := range order {
		if calls[idx] != nil {
			blocks = append(blocks, openAIToolCallToBlock(*calls[idx]))
		}
	}
	return &protocol.Response{Content: blocks, StopReason: finishReason, ReasoningContent: reasoning.String()}, nil
}
