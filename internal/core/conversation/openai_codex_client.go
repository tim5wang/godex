package conversation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// OpenAICodexClient talks to the ChatGPT Codex OAuth backend, or to the
// official OpenAI Responses API when baseURL points at api.openai.com. This
// mirrors temp/pi-go's Codex path: openai-go Responses streaming against
// https://chatgpt.com/backend-api/codex with Codex-specific headers.
type OpenAICodexClient struct {
	client   openai.Client
	official bool // true when speaking the official api.openai.com Responses API
}

// isOfficialResponsesBaseURL reports whether the base URL is the official
// OpenAI platform Responses API rather than the ChatGPT Codex OAuth endpoint.
func isOfficialResponsesBaseURL(baseURL string) bool {
	return strings.Contains(baseURL, "api.openai.com")
}

// NewOpenAICodexClient derives the endpoint flavor from baseURL and delegates
// to NewOpenAICodexClientForEndpoint.
func NewOpenAICodexClient(baseURL, token string, timeout time.Duration) *OpenAICodexClient {
	return NewOpenAICodexClientForEndpoint(baseURL, token, timeout, isOfficialResponsesBaseURL(baseURL))
}

// NewOpenAICodexClientForEndpoint is NewOpenAICodexClient with an explicit
// official-endpoint flag (test hook; production callers use the detection in
// NewOpenAICodexClient).
//
// Differences on the official endpoint (base_url https://api.openai.com/v1):
//   - codex-specific headers (originator, OpenAI-Beta responses=experimental,
//     chatgpt-account-id) are omitted — they belong to the chatgpt.com OAuth
//     backend only;
//   - prompt_cache_retention IS forwarded (the official API supports it for
//     the gpt-5.x-codex family; the OAuth endpoint rejects it with HTTP 400);
//   - prompt_cache_key is NOT forwarded, so the request uses the official
//     automatic prefix caching (longest-byte-prefix match, cached input billed
//     at 0.1×) instead of deterministic prompt_cache_key caching, whose
//     all-or-nothing semantics are unsafe for a growing agent prefix.
func NewOpenAICodexClientForEndpoint(baseURL, token string, timeout time.Duration, official bool) *OpenAICodexClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	httpClient := &http.Client{Transport: newDefaultTransport()}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	opts := []option.RequestOption{
		option.WithAPIKey(token),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(httpClient),
	}
	if !official {
		opts = append(opts,
			option.WithHeader("originator", "godex"),
			option.WithHeader("OpenAI-Beta", "responses=experimental"),
		)
		if accountID := codexAccountIDFromToken(token); accountID != "" {
			opts = append(opts, option.WithHeader("chatgpt-account-id", accountID))
		}
	}
	return &OpenAICodexClient{client: openai.NewClient(opts...), official: official}
}

func (c *OpenAICodexClient) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return c.Stream(ctx, req, StreamHandler{})
}

func (c *OpenAICodexClient) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	start := time.Now()
	var finalResp *protocol.Response
	var finalErr error
	defer func() {
		notifyUsage(ctx, UsageEvent{Request: req, Response: finalResp, Error: finalErr, Latency: time.Since(start), Stream: true})
	}()
	params := c.codexResponsesParams(req)

	stream := c.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	state := &codexResponsesStreamState{
		toolCalls: make(map[int64]codexToolCallAcc),
	}
	started := false
	for stream.Next() {
		evt := stream.Current()
		if !started {
			started = true
			// Reasoning-started signal: the ChatGPT codex backend delivers
			// reasoning only as encrypted content (no plaintext
			// reasoning_summary_text deltas), so frontends get a "thinking…"
			// placeholder instead of a blank wait.
			if handler.OnStreamStarted != nil {
				handler.OnStreamStarted()
			}
		}
		if err := applyCodexResponsesSDKEvent(state, evt, handler); err != nil {
			finalErr = err
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			finalErr = ctx.Err()
			return nil, ctx.Err()
		}
		finalErr = err
		return nil, err
	}
	response := codexStreamStateToProtocol(state)
	finalResp = response
	return response, nil
}

func (c *OpenAICodexClient) codexResponsesParams(req protocol.Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: codexInputFromProtocol(req),
		},
		// store stays false on both endpoints: the ChatGPT codex OAuth
		// endpoint rejects store=true with HTTP 400 (measured live), and the
		// official API allows it but we keep responses out of server-side
		// storage. Cache behavior is independent of store.
		Store: param.NewOpt(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
	}
	if instructions := strings.TrimSpace(req.System); instructions != "" {
		params.Instructions = param.NewOpt(instructions)
	}
	if len(req.Tools) > 0 {
		params.Tools = codexToolsFromProtocol(req.Tools)
	}
	if effort := normalizeCodexReasoningEffort(req.ReasoningEffort); effort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort: shared.ReasoningEffort(effort),
			// summary:"auto" is required for the backend to emit
			// response.reasoning_summary_text.delta events (the live "thinking"
			// stream). pi's codex client sends the same; without it the backend
			// delivers reasoning only as encrypted content and the frontend
			// gets no intermediate output.
			Summary: shared.ReasoningSummaryAuto,
		}
	}
	// Prompt cache affinity. On the chatgpt.com OAuth endpoint we forward the
	// stable session cache key and nothing else: the backend rejects
	// prompt_cache_retention with HTTP 400, and its caching is capped server
	// side anyway. On the official api.openai.com endpoint we forward
	// prompt_cache_retention (supported for the gpt-5.x-codex family, extends
	// the prefix cache TTL to 24h) and deliberately do NOT forward
	// prompt_cache_key: deterministic prompt_cache_key caching is
	// all-or-nothing on the whole prompt and would never hit for a growing
	// agent prefix, while the official automatic prefix caching does
	// longest-byte-prefix matching and bills cached input at 0.1×.
	if c.official {
		if retention := strings.TrimSpace(req.PromptCacheRetention); retention != "" {
			switch retention {
			case "24h":
				params.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetention24h
			default:
				params.PromptCacheRetention = responses.ResponseNewParamsPromptCacheRetentionInMemory
			}
		}
		return params
	}
	if key := strings.TrimSpace(req.PromptCacheKey); key != "" {
		params.PromptCacheKey = param.NewOpt(key)
	}
	return params
}

func normalizeCodexReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func codexInputFromProtocol(req protocol.Request) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := codexResponsesRole(msg.Role)
		var textParts []string
		flushText := func() {
			text := strings.TrimSpace(strings.Join(textParts, "\n"))
			if text == "" {
				textParts = nil
				return
			}
			items = append(items, responses.ResponseInputItemParamOfMessage(text, role))
			textParts = nil
		}
		for _, block := range msg.Content {
			switch block.Type {
			case protocol.BlockText:
				if block.Text != "" {
					textParts = append(textParts, block.Text)
				}
			case protocol.BlockImage:
				textParts = append(textParts, codexImagePlaceholder(block))
			case protocol.BlockToolUse:
				flushText()
				args, _ := json.Marshal(block.Input)
				if strings.TrimSpace(block.ID) != "" {
					items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(args), block.ID, block.Name))
				}
			case protocol.BlockToolResult:
				flushText()
				if strings.TrimSpace(block.ToolUseID) != "" {
					items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(block.ToolUseID, block.Content))
				}
			}
		}
		flushText()
	}
	return items
}

func codexResponsesRole(role string) responses.EasyInputMessageRole {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "model":
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}

func codexImagePlaceholder(block protocol.Block) string {
	mediaType := ""
	if block.Source != nil {
		mediaType = strings.TrimSpace(block.Source.MediaType)
	}
	if mediaType == "" {
		return "[image attachment]"
	}
	return "[image attachment: " + mediaType + "]"
}

func codexToolsFromProtocol(tools []protocol.ToolSchema) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		out = append(out, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        tool.Name,
				Description: param.NewOpt(tool.Description),
				Parameters:  codexFunctionParameters(tool.InputSchema),
				Strict:      param.NewOpt(false),
			},
		})
	}
	return out
}

func codexFunctionParameters(schema any) shared.FunctionParameters {
	params := make(shared.FunctionParameters)
	switch value := schema.(type) {
	case nil:
	case shared.FunctionParameters:
		for key, item := range value {
			params[key] = item
		}
	case map[string]interface{}:
		for key, item := range value {
			params[key] = item
		}
	default:
		data, err := json.Marshal(schema)
		if err == nil {
			var decoded map[string]interface{}
			if json.Unmarshal(data, &decoded) == nil {
				for key, item := range decoded {
					params[key] = item
				}
			}
		}
	}
	if _, ok := params["type"]; !ok {
		params["type"] = "object"
	}
	return normalizeCodexJSONSchemaObject(params)
}

func normalizeCodexJSONSchemaObject(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	if schema["type"] == "object" {
		properties, ok := schema["properties"].(map[string]interface{})
		if !ok || properties == nil {
			schema["properties"] = map[string]interface{}{}
		} else {
			for name, property := range properties {
				properties[name] = normalizeCodexJSONSchemaValue(property)
			}
		}
	}
	if items, ok := schema["items"]; ok {
		schema["items"] = normalizeCodexJSONSchemaValue(items)
	}
	return schema
}

func normalizeCodexJSONSchemaValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return normalizeCodexJSONSchemaObject(typed)
	case []interface{}:
		for i, item := range typed {
			typed[i] = normalizeCodexJSONSchemaValue(item)
		}
		return typed
	default:
		return value
	}
}

type codexResponsesStreamState struct {
	text         strings.Builder
	reasoning    strings.Builder
	finishReason string
	toolCalls    map[int64]codexToolCallAcc
	usage        *protocol.Usage
}

type codexToolCallAcc struct {
	id, name, arguments string
}

func applyCodexResponsesSDKEvent(state *codexResponsesStreamState, event responses.ResponseStreamEventUnion, handler StreamHandler) error {
	switch event.Type {
	case "error", "response.error":
		if event.Message != "" {
			return fmt.Errorf("Codex API error: %s", event.Message)
		}
		return fmt.Errorf("Codex API error: %s", event.Code)
	case "response.output_text.delta":
		state.text.WriteString(event.Delta)
		if handler.OnTextDelta != nil && event.Delta != "" {
			handler.OnTextDelta(event.Delta)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		// Chain-of-thought deltas: accumulate them into the final response and
		// forward them live so frontends can render the reasoning stream (the
		// Codex backend emits reasoning summaries as plaintext even though the
		// full reasoning content is encrypted).
		if event.Delta != "" {
			state.reasoning.WriteString(event.Delta)
			if handler.OnThinkingDelta != nil {
				handler.OnThinkingDelta(event.Delta, "")
			}
		}
	case "response.completed":
		state.finishReason = string(event.Response.Status)
		state.usage = codexUsageToProtocol(event.Response.Usage)
	case "response.function_call_arguments.delta":
		updateCodexResponsesToolCall(state, event.OutputIndex, "", "", event.Delta, true)
	case "response.function_call_arguments.done":
		updateCodexResponsesToolCall(state, event.OutputIndex, "", event.Name, event.Arguments, false)
	case "response.output_item.added":
		call := event.Item.AsFunctionCall()
		updateCodexResponsesToolCall(state, event.OutputIndex, call.CallID, call.Name, "", false)
	case "response.output_item.done":
		call := event.Item.AsFunctionCall()
		updateCodexResponsesToolCall(state, event.OutputIndex, call.CallID, call.Name, call.Arguments, false)
	}
	return nil
}

func updateCodexResponsesToolCall(state *codexResponsesStreamState, idx int64, id, name, arguments string, appendArguments bool) {
	call := state.toolCalls[idx]
	if id != "" {
		call.id = id
	}
	if name != "" {
		call.name = name
	}
	if arguments != "" {
		if appendArguments {
			call.arguments += arguments
		} else {
			call.arguments = arguments
		}
	}
	state.toolCalls[idx] = call
}

func codexStreamStateToProtocol(state *codexResponsesStreamState) *protocol.Response {
	blocks := make([]protocol.Block, 0, 2+len(state.toolCalls))
	if reasoning := strings.TrimSpace(state.reasoning.String()); reasoning != "" {
		blocks = append(blocks, protocol.ThinkingBlock(reasoning, "", false))
	}
	if text := strings.TrimSpace(state.text.String()); text != "" {
		blocks = append(blocks, protocol.TextBlock(text))
	}
	indices := make([]int64, 0, len(state.toolCalls))
	for idx := range state.toolCalls {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for _, idx := range indices {
		call := state.toolCalls[idx]
		if call.id == "" && call.name == "" {
			continue
		}
		input := parseToolArguments(call.arguments)
		blocks = append(blocks, protocol.ToolUseBlock(call.id, call.name, input))
	}
	if state.usage != nil {
		state.usage.Normalize()
	}
	return &protocol.Response{Content: blocks, StopReason: state.finishReason, ReasoningContent: state.reasoning.String(), Usage: state.usage}
}

// codexUsageToProtocol converts the Codex Responses API usage payload into
// the protocol.Usage struct. The Codex SDK exposes input_tokens_details as a
// struct with a CachedTokens field (OpenAI prompt caching). We also pick up
// output_tokens / total_tokens so the rest of the system can rely on a
// complete usage record. The Responses API input_tokens INCLUDES the cached
// portion; we subtract it so protocol.Usage.InputTokens always counts only
// the uncached input, matching the Anthropic convention.
func codexUsageToProtocol(usage responses.ResponseUsage) *protocol.Usage {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.InputTokensDetails.CachedTokens == 0 {
		return nil
	}
	cacheRead := int(usage.InputTokensDetails.CachedTokens)
	input := int(usage.InputTokens)
	if cacheRead > 0 && input >= cacheRead {
		input -= cacheRead
	}
	return &protocol.Usage{
		InputTokens:     input,
		OutputTokens:    int(usage.OutputTokens),
		CacheReadTokens: cacheRead,
	}
}

func codexAccountIDFromToken(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ""
	}
	authBlob, ok := raw["https://api.openai.com/auth"]
	if !ok {
		return ""
	}
	var claims struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(authBlob, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.ChatGPTAccountID)
}
