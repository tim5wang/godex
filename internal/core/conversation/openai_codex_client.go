package conversation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
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
	client           openai.Client
	official         bool // true when speaking the official api.openai.com Responses API
	origin           string
	cacheUnsupported sync.Map
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
//   - Cache parameters on the official endpoint are selected by model;
//     OAuth only receives the stable session cache key.
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
	return &OpenAICodexClient{client: openai.NewClient(opts...), official: official, origin: responsesOrigin(baseURL, token)}
}

func (c *OpenAICodexClient) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	return c.Stream(ctx, req, StreamHandler{})
}

func (c *OpenAICodexClient) Stream(ctx context.Context, req protocol.Request, handler StreamHandler) (*protocol.Response, error) {
	return streamResponses(ctx, req, handler, c.client, c.codexResponsesParams(req), c.origin, c.official, &c.cacheUnsupported)
}

func (c *OpenAICodexClient) codexResponsesParams(req protocol.Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: req.Model,
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
	// OAuth rejects retention/options. Official legacy models retain their
	// existing retention policy; newer models use explicit 30m boundaries in
	// streamResponses. A cache key is not a whole-prompt equality requirement.
	if c.official {
		if retention := strings.TrimSpace(req.PromptCacheRetention); retention != "" && !supportsResponsesExplicitCache(req.Model) {
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
	var lastOutputCallID string
	var lastOutputIndex int
	var lastOutput string
	for _, msg := range req.Messages {
		role := codexResponsesRole(msg.Role)
		var textParts []string
		var content responses.ResponseInputMessageContentListParam
		flushTextPart := func() {
			text := strings.TrimSpace(strings.Join(textParts, "\n"))
			if text != "" {
				content = append(content, responses.ResponseInputContentParamOfInputText(text))
			}
			textParts = nil
		}
		flushMessage := func() {
			flushTextPart()
			if len(content) == 1 && content[0].OfInputText != nil {
				// Preserve the existing wire shape for text-only messages.
				items = append(items, responses.ResponseInputItemParamOfMessage(content[0].OfInputText.Text, role))
			} else if len(content) > 0 {
				items = append(items, responses.ResponseInputItemParamOfMessage(content, role))
			}
			content = nil
		}
		for _, block := range msg.Content {
			switch block.Type {
			case protocol.BlockText:
				if block.Text != "" {
					textParts = append(textParts, block.Text)
				}
			case protocol.BlockImage:
				if block.Source == nil || block.Source.Data == "" {
					textParts = append(textParts, codexImagePlaceholder(block))
					continue
				}
				flushTextPart()
				content = append(content, responses.ResponseInputContentUnionParam{OfInputImage: &responses.ResponseInputImageParam{
					Detail:   responses.ResponseInputImageDetailAuto,
					ImageURL: param.NewOpt("data:" + block.Source.MediaType + ";base64," + block.Source.Data),
				}})
			case protocol.BlockToolUse:
				flushMessage()
				args, _ := json.Marshal(block.Input)
				if strings.TrimSpace(block.ID) != "" {
					items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(args), block.ID, block.Name))
				}
			case protocol.BlockToolResult:
				flushMessage()
				if strings.TrimSpace(block.ToolUseID) != "" {
					lastOutputCallID = block.ToolUseID
					lastOutput = block.Content
					items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(block.ToolUseID, block.Content))
					lastOutputIndex = len(items) - 1
				}
			}
		}
		flushMessage()
	}
	if tail := strings.TrimSpace(req.RuntimeTail); tail != "" {
		if lastOutputCallID != "" {
			// Ride the runtime tail on the last function_call_output item so it
			// is not interpreted as a fresh user instruction.
			sep := ""
			if strings.TrimSpace(lastOutput) != "" {
				sep = "\n\n"
			}
			items[lastOutputIndex] = responses.ResponseInputItemParamOfFunctionCallOutput(lastOutputCallID, lastOutput+sep+tail)
		} else {
			items = append(items, responses.ResponseInputItemParamOfMessage(tail, responses.EasyInputMessageRoleUser))
		}
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
	outputItems  map[int64]json.RawMessage
}

type codexToolCallAcc struct {
	id, name, arguments string
}

func applyCodexResponsesSDKEvent(state *codexResponsesStreamState, event responses.ResponseStreamEventUnion, handler StreamHandler) error {
	switch event.Type {
	case "error", "response.error":
		return newProviderStreamError(event.Code, event.Message)
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
	case "response.completed", "response.incomplete":
		state.finishReason = string(event.Response.Status)
		if state.finishReason == "" {
			state.finishReason = "completed"
		}
		if event.Type == "response.incomplete" {
			state.finishReason = "length"
		}
		state.usage = codexUsageToProtocol(event.Response.Usage)
		if len(event.Response.Output) > 0 {
			state.outputItems = make(map[int64]json.RawMessage, len(event.Response.Output))
			for i, item := range event.Response.Output {
				captureResponsesOutput(state, int64(i), item)
			}
		}
	case "response.failed":
		return newProviderStreamError(string(event.Response.Error.Code), event.Response.Error.Message)
	case "response.function_call_arguments.delta":
		updateCodexResponsesToolCall(state, event.OutputIndex, "", "", event.Delta, true)
	case "response.function_call_arguments.done":
		updateCodexResponsesToolCall(state, event.OutputIndex, "", event.Name, event.Arguments, false)
	case "response.output_item.added":
		if event.Item.Type == "function_call" {
			call := event.Item.AsFunctionCall()
			updateCodexResponsesToolCall(state, event.OutputIndex, call.CallID, call.Name, "", false)
		}
	case "response.output_item.done":
		captureResponsesOutput(state, event.OutputIndex, event.Item)
	}
	return nil
}

func captureResponsesOutput(state *codexResponsesStreamState, index int64, item responses.ResponseOutputItemUnion) {
	if raw := item.RawJSON(); raw != "" {
		if state.outputItems == nil {
			state.outputItems = make(map[int64]json.RawMessage)
		}
		state.outputItems[index] = json.RawMessage(raw)
	}
	if item.Type == "function_call" {
		call := item.AsFunctionCall()
		updateCodexResponsesToolCall(state, index, call.CallID, call.Name, call.Arguments, false)
	}
}

func sortedResponsesOutputIndices(items map[int64]json.RawMessage) []int64 {
	indices := make([]int64, 0, len(items))
	for index := range items {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	return indices
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
	// Some compatible endpoints only send the final output array. Recover
	// visible content without emitting duplicate deltas or duplicate tool calls.
	if state.text.Len() == 0 {
		for _, index := range sortedResponsesOutputIndices(state.outputItems) {
			var item struct {
				Type    string
				Content []struct{ Type, Text string }
			}
			if json.Unmarshal(state.outputItems[index], &item) == nil && item.Type == "message" {
				for _, part := range item.Content {
					if part.Type == "output_text" {
						state.text.WriteString(part.Text)
					}
				}
			}
		}
	}
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
// and cache-write portions; we subtract both so protocol.Usage.InputTokens
// counts only ordinary uncached input, matching the Anthropic convention.
func codexUsageToProtocol(usage responses.ResponseUsage) *protocol.Usage {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.InputTokensDetails.CachedTokens == 0 {
		return nil
	}
	var details struct {
		CacheWriteTokens int `json:"cache_write_tokens"`
	}
	_ = json.Unmarshal([]byte(usage.InputTokensDetails.RawJSON()), &details)
	input := max(0, int(usage.InputTokens))
	cacheRead := min(input, max(0, int(usage.InputTokensDetails.CachedTokens)))
	cacheWrite := min(input-cacheRead, max(0, details.CacheWriteTokens))
	input -= cacheRead + cacheWrite
	return &protocol.Usage{
		InputTokens:      input,
		OutputTokens:     max(0, int(usage.OutputTokens)),
		ReasoningTokens:  min(max(0, int(usage.OutputTokens)), max(0, int(usage.OutputTokensDetails.ReasoningTokens))),
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
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
