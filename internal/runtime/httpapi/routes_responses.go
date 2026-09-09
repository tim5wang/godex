package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/usage"
)

// =============================================================================
// OpenAI Responses API (Usage Gateway) — POST /v1/responses
// =============================================================================
//
// The Responses protocol is the modern OpenAI wire format: the request carries
// an `input` array of items (messages, function calls, function call outputs,
// reasoning) plus `instructions`, and the response returns a structured
// `output` array with separate message / reasoning / function_call items.
//
// This gateway translates Responses requests into the internal protocol.Request,
// dispatches through the same LLM gateway the chat.completions path uses, and
// translates the provider response back to Responses JSON (non-streaming) or
// Responses SSE events (streaming).

// responsesTool is the Responses API tool shape. Unlike chat.completions
// (which nests the function under {type,function}), the Responses wire format
// is flat: {type:"function", name, description, parameters}.
type responsesTool struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// responsesGatewayRequest is the lightweight wire shape of POST /v1/responses.
// Only the fields the gateway understands are captured; everything else is
// ignored (previous_response_id / conversation are intentionally not forwarded
// because the gateway translates a stateless request each time).
type responsesGatewayRequest struct {
	Model                string              `json:"model"`
	Input                json.RawMessage     `json:"input"`
	Instructions         string              `json:"instructions"`
	Stream               bool                `json:"stream,omitempty"`
	MaxOutputTokens      int                 `json:"max_output_tokens,omitempty"`
	PromptCacheRetention string              `json:"prompt_cache_retention,omitempty"`
	Tools                []responsesTool     `json:"tools,omitempty"`
	Reasoning            *responsesReasoning `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// responsesInputItem is one element of the `input` array. We parse it loosely
// so unknown item types (file_search_call, web_search_call, compaction, …) are
// skipped instead of failing the whole request.
type responsesInputItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// function_call / function_call_output
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
	// message content parts
	Text string `json:"text"`
}

// responsesContentPart is one element of a message item's content array.
type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesRequestToProtocol converts a Responses request into the internal
// protocol.Request. Mapping rules (mirror usageGatewayProtocolRequest):
//   - instructions → System;
//   - message items with role system/developer → flattened into System;
//   - message items with role user/assistant → APIMessage;
//   - function_call items → assistant tool_use blocks;
//   - function_call_output items → user tool_result blocks;
//   - reasoning / other item types → skipped.
func responsesRequestToProtocol(req responsesGatewayRequest) (protocol.Request, error) {
	var systemParts []string
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		systemParts = append(systemParts, instructions)
	}

	messages := make([]protocol.APIMessage, 0, 8)
	var pendingToolUse []protocol.Block // function_call items accumulate here
	var pendingToolResult []protocol.Block

	flushAssistant := func() {
		if len(pendingToolUse) == 0 {
			return
		}
		messages = append(messages, protocol.APIMessage{
			Role:    protocol.RoleAssistant,
			Content: pendingToolUse,
		})
		pendingToolUse = nil
	}
	flushUser := func() {
		if len(pendingToolResult) == 0 {
			return
		}
		messages = append(messages, protocol.APIMessage{
			Role:    protocol.RoleUser,
			Content: pendingToolResult,
		})
		pendingToolResult = nil
	}

	items, err := parseResponsesInputItems(req.Input)
	if err != nil {
		return protocol.Request{}, err
	}
	for _, item := range items {
		// Easy-input message form: the Responses spec (and the openai-go
		// SDK used by codex-style clients) let callers omit the "type"
		// field on plain messages ({"role":"user","content":"..."}),
		// so an item that carries a role and no explicit type is a message.
		// Without this, such requests parse to zero messages and fail with
		// "at least one non-empty user or assistant message is required".
		itemType := item.Type
		if itemType == "" && strings.TrimSpace(item.Role) != "" {
			itemType = "message"
		}
		switch itemType {
		case "message":
			role := strings.ToLower(strings.TrimSpace(item.Role))
			if role == "system" || role == "developer" {
				if text := strings.TrimSpace(responsesItemText(item)); text != "" {
					systemParts = append(systemParts, text)
				}
				continue
			}
			text := strings.TrimSpace(responsesItemText(item))
			if text == "" {
				continue
			}
			if role == "assistant" || role == "model" {
				flushUser()
				messages = append(messages, protocol.APIMessage{
					Role:    protocol.RoleAssistant,
					Content: []protocol.Block{protocol.TextBlock(text)},
				})
			} else {
				flushAssistant()
				messages = append(messages, protocol.APIMessage{
					Role:    protocol.RoleUser,
					Content: []protocol.Block{protocol.TextBlock(text)},
				})
			}
		case "function_call":
			flushUser()
			if strings.TrimSpace(item.CallID) == "" {
				continue
			}
			var input map[string]interface{}
			if strings.TrimSpace(item.Arguments) != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil {
					return protocol.Request{}, fmt.Errorf("function_call %q has invalid JSON arguments: %w", item.Name, err)
				}
			}
			pendingToolUse = append(pendingToolUse, protocol.Block{
				Type:  protocol.BlockToolUse,
				ID:    item.CallID,
				Name:  item.Name,
				Input: input,
			})
		case "function_call_output":
			flushAssistant()
			if strings.TrimSpace(item.CallID) == "" {
				continue
			}
			pendingToolResult = append(pendingToolResult, protocol.Block{
				Type:      protocol.BlockToolResult,
				ToolUseID: item.CallID,
				Content:   item.Output,
			})
		default:
			// file_search_call / web_search_call / reasoning / compaction / …
			// have no protocol.Request analogue; skip them.
		}
	}
	flushAssistant()
	flushUser()

	if len(messages) == 0 {
		return protocol.Request{}, fmt.Errorf("at least one non-empty user or assistant message is required")
	}

	tools := make([]protocol.ToolSchema, 0, len(req.Tools))
	for _, tool := range req.Tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		tools = append(tools, protocol.ToolSchema{
			Name:        name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}

	proto := protocol.Request{
		MaxTokens:            req.MaxOutputTokens,
		System:               strings.Join(systemParts, "\n\n"),
		Messages:             messages,
		Tools:                tools,
		PromptCacheRetention: responsesCacheRetentionToProtocol(req.PromptCacheRetention),
	}
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		proto.ReasoningEffort = strings.TrimSpace(req.Reasoning.Effort)
	}
	return proto, nil
}

func responsesCacheRetentionToProtocol(value string) string {
	switch strings.TrimSpace(value) {
	case "24h":
		return protocol.CacheRetentionLong
	case "":
		return ""
	default:
		return protocol.CacheRetentionShort
	}
}

// parseResponsesInputItems decodes the `input` array (or a plain string form)
// into a loose item list.
func parseResponsesInputItems(raw json.RawMessage) ([]responsesInputItem, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var items []responsesInputItem
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("invalid responses input array: %w", err)
		}
		return items, nil
	}
	// Plain-string input form: a single user message.
	text, _ := strings.CutPrefix(trimmed, "\"")
	text, _ = strings.CutSuffix(text, "\"")
	return []responsesInputItem{{Type: "message", Role: "user", Text: text}}, nil
}

// responsesItemText extracts the plain text of a message item from either the
// string content form or the content-part array form.
func responsesItemText(item responsesInputItem) string {
	if strings.TrimSpace(item.Text) != "" {
		return item.Text
	}
	if len(item.Content) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(item.Content))
	if strings.HasPrefix(trimmed, "\"") {
		var text string
		if json.Unmarshal(item.Content, &text) == nil {
			return text
		}
		return ""
	}
	var parts []responsesContentPart
	if err := json.Unmarshal(item.Content, &parts); err != nil {
		return ""
	}
	var out strings.Builder
	for _, part := range parts {
		if part.Type == "input_text" || part.Type == "output_text" {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

// handleUsageGatewayResponses authenticates a gdx_ proxy key and dispatches a
// POST /v1/responses request through the LLM gateway. It mirrors
// handleUsageGatewayChatCompletions: allowed-models check, model mapping
// resolution, budget check, then streaming or non-streaming dispatch.
func handleUsageGatewayResponses(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	secret := strings.TrimPrefix(auth, "Bearer ")
	secret = strings.TrimSpace(secret)

	key, err := usageService.AuthenticateKey(secret)
	if err != nil {
		writeUsageGatewayError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API Key. Please provide a valid proxy key.")
		return
	}
	dispatchUsageGatewayResponses(w, r, usageService, manager, key)
}

// dispatchUsageGatewayResponses runs the LLM gateway dispatch for an already-
// authenticated POST /v1/responses request. It is shared between the proxy-key
// path (handleUsageGatewayResponses) and the web-token path
// (handleResponsesWebToken) so both auth modes see the same wire format.
func dispatchUsageGatewayResponses(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager, key *usage.ProxyAPIKey) {
	start := time.Now()

	var req responsesGatewayRequest
	if err := decodeJSON(r, &req); err != nil {
		writeUsageGatewayError(w, http.StatusBadRequest, "invalid_request", "Invalid request body.")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeUsageGatewayError(w, http.StatusBadRequest, "invalid_model", "Model is required.")
		return
	}
	if len(req.Input) == 0 {
		writeUsageGatewayError(w, http.StatusBadRequest, "invalid_request", "input is required.")
		return
	}

	if len(key.AllowedModels) > 0 {
		allowed := false
		for _, m := range key.AllowedModels {
			if m == req.Model {
				allowed = true
				break
			}
		}
		if !allowed {
			writeUsageGatewayError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("Model '%s' is not allowed for this API key.", req.Model))
			return
		}
	}

	modelMapping, err := usageService.ResolveModel(req.Model)
	if err != nil {
		writeUsageGatewayError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("Model '%s' not found or disabled.", req.Model))
		return
	}

	providerReq, err := responsesRequestToProtocol(req)
	if err != nil {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "invalid_request", err.Error())
		writeUsageGatewayError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	current := manager.Current()
	profile, ok := current.ModelProfileByID(modelMapping.TargetProfileID)
	if !ok {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "profile_not_found", "Target model profile not found.")
		writeUsageGatewayError(w, http.StatusNotFound, "profile_not_found", "Target model profile not found.")
		return
	}
	if strings.TrimSpace(modelMapping.TargetModel) != "" {
		profile.Model = strings.TrimSpace(modelMapping.TargetModel)
	}
	providerReq.Model = profile.Model
	if providerReq.MaxTokens <= 0 {
		providerReq.MaxTokens = profile.MaxTokens
	}
	// Check budget. The virtual web-token identity (ID == "system:web_token")
	// is not stored in the usage store, so a budget lookup would 500. Treat it
	// as an admin override with unlimited budget and skip the check. Real proxy
	// keys still go through the normal path (mirrors the Anthropic gateway).
	if !strings.HasPrefix(key.ID, "system:") {
		if ok, err := usageService.CheckBudget(key.ID, float64(usageGatewayEstimateInputTokens(providerReq))*modelMapping.CreditWeight); err != nil {
			writeUsageGatewayError(w, http.StatusInternalServerError, "budget_check_failed", "Failed to check usage budget.")
			return
		} else if !ok {
			recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "budget_exceeded", "Usage budget exceeded.")
			writeUsageGatewayError(w, http.StatusPaymentRequired, "budget_exceeded", "Usage budget exceeded.")
			return
		}
	}

	if req.Stream {
		caller := conversation.NewCallerForProfile(profile)
		if streamer, ok := caller.(conversation.StreamCaller); ok {
			streamUsageGatewayResponses(w, r, usageService, streamer, key, req, modelMapping, providerReq, profile, start)
			return
		}
		req.Stream = false
	}

	resp, err := conversation.NewCallerForProfile(profile).Call(r.Context(), providerReq)
	if err != nil {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "provider_error", err.Error())
		writeUsageGatewayError(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	call := usageGatewaySuccessCall(start, key.ID, req.Model, modelMapping, providerReq, resp)
	if err := usageService.RecordCall(call); err != nil {
		writeUsageGatewayError(w, http.StatusInternalServerError, "usage_record_failed", "Failed to record usage.")
		return
	}
	writeJSON(w, http.StatusOK, protocolToResponsesResponse(resp, req.Model))
}

// protocolToResponsesResponse serializes a completed protocol.Response into the
// Responses wire shape: {id, object:"response", status, model, output[], usage}.
func protocolToResponsesResponse(resp *protocol.Response, model string) map[string]interface{} {
	output := make([]map[string]interface{}, 0, len(resp.Content)+1)
	for _, block := range resp.Content {
		switch block.Type {
		case protocol.BlockThinking:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			output = append(output, map[string]interface{}{
				"type": "reasoning",
				"id":   "rs_" + randHex(8),
				"summary": []map[string]interface{}{{
					"type": "summary_text",
					"text": block.Text,
				}},
			})
		case protocol.BlockToolUse:
			args, _ := json.Marshal(block.Input)
			output = append(output, map[string]interface{}{
				"type":      "function_call",
				"id":        "fc_" + randHex(8),
				"call_id":   block.ID,
				"name":      block.Name,
				"arguments": string(args),
				"status":    "completed",
			})
		case protocol.BlockText:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			output = append(output, map[string]interface{}{
				"type":    "message",
				"id":      "msg_" + randHex(8),
				"role":    "assistant",
				"status":  "completed",
				"content": []map[string]interface{}{{"type": "output_text", "text": block.Text}},
			})
		}
	}
	response := map[string]interface{}{
		"id":         "resp_" + randHex(16),
		"object":     "response",
		"created_at": float64(time.Now().Unix()),
		"status":     responsesStatus(resp),
		"model":      model,
		"output":     output,
	}
	if resp != nil && resp.Usage != nil {
		response["usage"] = protocolUsageToResponses(resp.Usage)
	}
	return response
}

// responsesStatus maps the internal stop reason to the Responses status field.
func responsesStatus(resp *protocol.Response) string {
	if resp == nil {
		return "completed"
	}
	switch strings.ToLower(strings.TrimSpace(resp.StopReason)) {
	case "length", "max_tokens":
		return "incomplete"
	case "tool_use", "tool_calls":
		return "completed"
	default:
		return "completed"
	}
}

// protocolUsageToResponses converts protocol.Usage to the Responses usage
// shape. The Responses API input_tokens INCLUDES cached tokens (the SDK
// subtracts them internally), so we add the cached portion back — mirroring
// how the chat.completions gateway restores prompt_tokens.
func protocolUsageToResponses(usage *protocol.Usage) map[string]interface{} {
	if usage == nil {
		return nil
	}
	return map[string]interface{}{
		"input_tokens":  usage.InputTokens + usage.CacheReadTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.InputTokens + usage.CacheReadTokens + usage.OutputTokens,
		"input_tokens_details": map[string]interface{}{
			"cached_tokens": usage.CacheReadTokens,
		},
	}
}

// randHex returns n random hex chars (used for response/item ids).
func randHex(n int) string {
	const hexDigits = "0123456789abcdef"
	b := make([]byte, n)
	seed := time.Now().UnixNano()
	for i := range b {
		seed = seed*6364136223846793005 + 1442695040888963407
		b[i] = hexDigits[(seed>>33)&0xf]
	}
	return string(b)
}

// streamUsageGatewayResponses forwards a streaming provider response back to
// the client as Responses SSE events. The wire shape mirrors the official
// Responses API so standard Responses SDK clients (OpenAI SDK, litellm,
// codex-style consumers) can consume the stream:
//
//  1. response.created (response in_progress, empty output)
//  2. response.output_item.added for each output item (message / reasoning /
//     function_call) the moment it first appears;
//  3. response.content_part.added for the text item's output_text part;
//  4. response.output_text.delta / response.reasoning_summary_text.delta /
//     response.function_call_arguments.delta for incremental content;
//  5. response.output_text.done / response.reasoning_summary_text.done /
//     response.function_call_arguments.done / response.content_part.done /
//     response.output_item.done as each item and part completes;
//  6. response.completed carrying the fully-assembled output array (identical
//     shape to the non-streaming response), then the [DONE] sentinel.
//
// Item ids stay stable across deltas: each message/reasoning/function_call
// output item is allocated once and referenced by every subsequent delta/done
// frame for that item, matching what Responses SDKs expect (they aggregate
// deltas by item_id). output_index increments across items in emission order.

// responsesStreamToolCall records one function_call output item announced
// during a streaming Responses response, keyed by the upstream protocol block
// index so deltas can reference the stable ids allocated at announcement.
type responsesStreamToolCall struct {
	ItemID      string
	CallID      string
	Name        string
	OutputIndex int
}

func streamUsageGatewayResponses(
	w http.ResponseWriter,
	r *http.Request,
	usageService *usage.Service,
	streamer conversation.StreamCaller,
	key *usage.ProxyAPIKey,
	req responsesGatewayRequest,
	modelMapping *usage.ProxyModel,
	providerReq protocol.Request,
	profile config.ModelProfileConfig,
	start time.Time,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "streaming_unsupported", "response writer does not support flushing")
		writeUsageGatewayError(w, http.StatusInternalServerError, "streaming_unsupported", "Streaming is not supported by this response writer.")
		return
	}

	responseID := "resp_" + randHex(16)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// sequenceNumber increments on every SSE frame, mirroring the official
	// Responses API (clients use it to detect gaps and ordering).
	sequenceNumber := 0
	// emit stamps sequence_number onto every frame before writing it.
	emit := func(payload map[string]interface{}) {
		payload["sequence_number"] = sequenceNumber
		sequenceNumber++
		emitResponsesSSE(w, flusher, payload)
	}

	// baseResponse is the response object carried by created/completed frames.
	baseResponse := func(status string, output []interface{}) map[string]interface{} {
		return map[string]interface{}{
			"id":     responseID,
			"object": "response",
			"status": status,
			"model":  req.Model,
			"output": output,
		}
	}

	// Emit response.created before the upstream stream starts. Some providers
	// (e.g. the Anthropic gateway path) never invoke OnStreamStarted, so
	// relying on that callback would drop the opening frame entirely.
	emit(map[string]interface{}{
		"type":     "response.created",
		"response": baseResponse("in_progress", []interface{}{}),
	})

	// nextOutputIndex is the output_index assigned to the next item that is
	// announced via response.output_item.added. The official API numbers
	// output items sequentially, and Responses SDKs rely on it to correlate
	// deltas with their owning item.
	nextOutputIndex := 0

	// The gateway emits at most one assistant text message and one reasoning
	// item per response; deltas for them reference the ids allocated here on
	// first appearance. The previous implementation minted a new id per delta,
	// which made SDK clients lose every text stream because deltas never
	// matched a known item.
	var messageItemID, messagePartID string
	messageOutputIndex := 0
	messageAnnounced := false

	var reasoningItemID string
	reasoningOutputIndex := 0
	reasoningAnnounced := false

	// toolCallsPerIndex tracks function_call items announced via
	// output_item.added, keyed by the upstream protocol block index. The
	// protocol block carries the upstream call id; we surface it as both
	// call_id and item id. The record remembers the item's output_index
	// (deltas must reference the item index, not the upstream block index).
	toolCallsPerIndex := map[int]*responsesStreamToolCall{}
	// lastPartialArgsPerIndex tracks the previous cumulative arguments string
	// per output index so we can emit differential deltas (same as the
	// chat.completions streaming path).
	lastPartialArgsPerIndex := map[int]string{}
	// completedOutputItems accumulates the final assembled output items so the
	// terminal response.completed frame can carry the full output array
	// (previously it always sent an empty array). Items are appended in
	// announcement order, so their index equals their output_index.
	completedOutputItems := []interface{}{}
	// Accumulated full text / reasoning, used to fill the done frames and the
	// completed output array.
	var textAccumulated, reasoningAccumulated strings.Builder

	announceTextItem := func() {
		if messageAnnounced {
			return
		}
		messageAnnounced = true
		messageItemID = "msg_" + randHex(8)
		messagePartID = "cp_" + randHex(8)
		messageOutputIndex = nextOutputIndex
		nextOutputIndex++
		item := map[string]interface{}{
			"type":    "message",
			"id":      messageItemID,
			"role":    "assistant",
			"status":  "in_progress",
			"content": []interface{}{},
		}
		completedOutputItems = append(completedOutputItems, item)
		emit(map[string]interface{}{
			"type":         "response.output_item.added",
			"output_index": messageOutputIndex,
			"item":         item,
		})
		emit(map[string]interface{}{
			"type":          "response.content_part.added",
			"item_id":       messageItemID,
			"output_index":  messageOutputIndex,
			"content_index": 0,
			"part":          map[string]interface{}{"type": "output_text", "id": messagePartID, "text": "", "annotations": []interface{}{}},
		})
	}

	announceReasoningItem := func() {
		if reasoningAnnounced {
			return
		}
		reasoningAnnounced = true
		reasoningItemID = "rs_" + randHex(8)
		reasoningOutputIndex = nextOutputIndex
		nextOutputIndex++
		item := map[string]interface{}{
			"type":    "reasoning",
			"id":      reasoningItemID,
			"summary": []interface{}{},
		}
		completedOutputItems = append(completedOutputItems, item)
		emit(map[string]interface{}{
			"type":         "response.output_item.added",
			"output_index": reasoningOutputIndex,
			"item":         item,
		})
	}

	resp, err := streamer.Stream(r.Context(), providerReq, conversation.StreamHandler{
		OnStreamStarted: func() {
			// response.created is emitted above so providers that never fire
			// this callback still produce a valid stream; nothing to do here.
		},
		OnTextDelta: func(delta string) {
			if delta == "" {
				return
			}
			textAccumulated.WriteString(delta)
			announceTextItem()
			emit(map[string]interface{}{
				"type":          "response.output_text.delta",
				"item_id":       messageItemID,
				"output_index":  messageOutputIndex,
				"content_index": 0,
				"delta":         delta,
			})
		},
		OnThinkingDelta: func(delta, signature string) {
			if delta == "" {
				return
			}
			reasoningAccumulated.WriteString(delta)
			announceReasoningItem()
			emit(map[string]interface{}{
				"type":          "response.reasoning_summary_text.delta",
				"item_id":       reasoningItemID,
				"output_index":  reasoningOutputIndex,
				"content_index": 0,
				"delta":         delta,
			})
		},
		OnToolUse: func(block protocol.Block, partialJSON string) {
			idx := block.Index
			tc, seen := toolCallsPerIndex[idx]
			if !seen {
				callID := strings.TrimSpace(block.ID)
				if callID == "" {
					callID = "call_" + randHex(8)
				}
				itemID := "fc_" + randHex(8)
				outputIndex := nextOutputIndex
				nextOutputIndex++
				tc = &responsesStreamToolCall{
					ItemID:      itemID,
					CallID:      callID,
					Name:        block.Name,
					OutputIndex: outputIndex,
				}
				toolCallsPerIndex[idx] = tc
				item := map[string]interface{}{
					"type":      "function_call",
					"id":        itemID,
					"call_id":   callID,
					"name":      block.Name,
					"arguments": "",
					"status":    "in_progress",
				}
				completedOutputItems = append(completedOutputItems, item)
				emit(map[string]interface{}{
					"type":         "response.output_item.added",
					"output_index": outputIndex,
					"item":         item,
				})
			}
			// Forward the per-chunk arguments fragment (differential suffix
			// against the last cumulative, same as the chat.completions path).
			prev, prevSeen := lastPartialArgsPerIndex[tc.OutputIndex]
			fragment := toolJSONDeltaSuffix(prev, partialJSON)
			if prevSeen && partialJSON == prev {
				return
			}
			lastPartialArgsPerIndex[tc.OutputIndex] = partialJSON
			if fragment == "" {
				return
			}
			emit(map[string]interface{}{
				"type":         "response.function_call_arguments.delta",
				"item_id":      tc.ItemID,
				"output_index": tc.OutputIndex,
				"delta":        fragment,
			})
		},
	})
	if err != nil {
		emit(map[string]interface{}{
			"type":    "response.error",
			"message": err.Error(),
		})
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "provider_error", err.Error())
		return
	}

	// Finish the text item: output_text.done, content_part.done and
	// output_item.done carry the fully accumulated text.
	if messageAnnounced {
		text := textAccumulated.String()
		part := map[string]interface{}{"type": "output_text", "id": messagePartID, "text": text, "annotations": []interface{}{}}
		emit(map[string]interface{}{
			"type":          "response.output_text.done",
			"item_id":       messageItemID,
			"output_index":  messageOutputIndex,
			"content_index": 0,
			"text":          text,
		})
		emit(map[string]interface{}{
			"type":          "response.content_part.done",
			"item_id":       messageItemID,
			"output_index":  messageOutputIndex,
			"content_index": 0,
			"part":          part,
		})
		item := map[string]interface{}{
			"type":    "message",
			"id":      messageItemID,
			"role":    "assistant",
			"status":  "completed",
			"content": []interface{}{part},
		}
		completedOutputItems[messageOutputIndex] = item
		emit(map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": messageOutputIndex,
			"item":         item,
		})
	}

	// Finish the reasoning item.
	if reasoningAnnounced {
		reasoningText := reasoningAccumulated.String()
		summary := map[string]interface{}{"type": "summary_text", "text": reasoningText}
		emit(map[string]interface{}{
			"type":          "response.reasoning_summary_text.done",
			"item_id":       reasoningItemID,
			"output_index":  reasoningOutputIndex,
			"content_index": 0,
			"text":          reasoningText,
		})
		item := map[string]interface{}{
			"type":    "reasoning",
			"id":      reasoningItemID,
			"summary": []interface{}{summary},
		}
		completedOutputItems[reasoningOutputIndex] = item
		emit(map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": reasoningOutputIndex,
			"item":         item,
		})
	}

	// Finish tool call items using the final assembled arguments from the
	// completed response: the stream callbacks do not carry an end-of-tool
	// marker for every provider, so the assembled resp is the source of truth.
	// Iterate in output_index order (map iteration is random, and the done
	// frames must arrive in the same order the items were announced).
	toolOrder := make([]*responsesStreamToolCall, 0, len(toolCallsPerIndex))
	for _, tc := range toolCallsPerIndex {
		toolOrder = append(toolOrder, tc)
	}
	sort.Slice(toolOrder, func(a, b int) bool {
		return toolOrder[a].OutputIndex < toolOrder[b].OutputIndex
	})
	for _, tc := range toolOrder {
		finalArgs := ""
		if resp != nil {
			for _, blk := range resp.Content {
				if blk.Type == protocol.BlockToolUse && strings.TrimSpace(blk.ID) == tc.CallID {
					if b, err := json.Marshal(blk.Input); err == nil {
						finalArgs = string(b)
					}
					break
				}
			}
		}
		emit(map[string]interface{}{
			"type":         "response.function_call_arguments.done",
			"item_id":      tc.ItemID,
			"output_index": tc.OutputIndex,
			"arguments":    finalArgs,
		})
		item := map[string]interface{}{
			"type":      "function_call",
			"id":        tc.ItemID,
			"call_id":   tc.CallID,
			"name":      tc.Name,
			"arguments": finalArgs,
			"status":    "completed",
		}
		completedOutputItems[tc.OutputIndex] = item
		emit(map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": tc.OutputIndex,
			"item":         item,
		})
	}

	// Terminal frame. Mirror responsesStatus() for the status field.
	status := "completed"
	if resp != nil && strings.EqualFold(resp.StopReason, "length") {
		status = "incomplete"
	}
	usagePayload := map[string]interface{}{}
	if resp != nil && resp.Usage != nil {
		usagePayload = protocolUsageToResponses(resp.Usage)
	}
	response := baseResponse(status, completedOutputItems)
	response["usage"] = usagePayload
	emit(map[string]interface{}{
		"type":     "response.completed",
		"response": response,
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	_ = profile // referenced via providerReq already
	call := usageGatewaySuccessCall(start, key.ID, req.Model, modelMapping, providerReq, resp)
	if err := usageService.RecordCall(call); err != nil {
		logger.Warnf("usage gateway: failed to record streamed responses call %s: %v", call.ID, err)
	}
}

// emitResponsesSSE writes one Responses SSE frame and flushes it.
func emitResponsesSSE(w http.ResponseWriter, flusher http.Flusher, payload map[string]interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// handleResponsesWebToken dispatches a POST /v1/responses request through the
// LLM gateway for an admin (web-token) caller. It synthesises a virtual
// "admin" key with no model restrictions and no budget (mirroring
// handleAnthropicWebTokenMessages), so Responses SDK clients configured with
// the web token see the same gateway surface as chat.completions.
func handleResponsesWebToken(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager) {
	if usageService == nil {
		writeUsageGatewayError(w, http.StatusServiceUnavailable, "gateway_unavailable", "Usage gateway not configured.")
		return
	}
	adminKey := &usage.ProxyAPIKey{
		ID:        "system:web_token",
		Name:      "Web Token Admin",
		KeyPrefix: "web_token",
		Enabled:   true,
	}
	dispatchUsageGatewayResponses(w, r, usageService, manager, adminKey)
}
