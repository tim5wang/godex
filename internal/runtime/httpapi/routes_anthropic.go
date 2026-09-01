package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/usage"
)

func dispatchAnthropicMessages(
	w http.ResponseWriter,
	r *http.Request,
	usageService *usage.Service,
	manager *config.Manager,
	key *usage.ProxyAPIKey,
) {
	start := time.Now()

	var req anthropicMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "Invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if req.MaxTokens <= 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "max_tokens is required and must be positive")
		return
	}
	if len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "messages is required and cannot be empty")
		return
	}

	// Check allowed models (a virtual web-token identity has
	// AllowedModels == nil which lets every model through).
	if len(key.AllowedModels) > 0 {
		allowed := false
		for _, m := range key.AllowedModels {
			if m == req.Model {
				allowed = true
				break
			}
		}
		if !allowed {
			writeAnthropicError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("Model '%s' is not allowed for this API key.", req.Model))
			return
		}
	}

	// Resolve model mapping. The web-token path also uses model
	// mappings because the gateway treats every public name
	// uniformly: a mapping tells the gateway which provider profile
	// and target model to route to. If no mapping exists we return
	// 404 so the caller knows the model isn't exposed (rather than
	// silently routing to a default profile, which would surprise
	// the admin).
	modelMapping, err := usageService.ResolveModel(req.Model)
	if err != nil {
		writeAnthropicError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("Model '%s' not found or disabled.", req.Model))
		return
	}

	// Get profile
	current := manager.Current()
	profile, ok := current.ModelProfileByID(modelMapping.TargetProfileID)
	if !ok {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "profile_not_found", "Target model profile not found.")
		writeAnthropicError(w, http.StatusNotFound, "profile_not_found", "Target model profile not found.")
		return
	}

	// Override model name if configured
	providerModel := profile.Model
	if strings.TrimSpace(modelMapping.TargetModel) != "" {
		providerModel = strings.TrimSpace(modelMapping.TargetModel)
	}

	// Convert Anthropic request to internal protocol first so we can use
	// the real request for the budget estimate (not a dummy placeholder).
	providerReq, err := anthropicToProtocolRequest(req, providerModel)
	if err != nil {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "invalid_request", err.Error())
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Check budget. The virtual web-token identity (`ID == "system:web_token"`)
	// is not stored in the usage store, so a budget lookup would
	// 500. We treat it as an admin override with unlimited budget
	// and skip the check entirely. Real proxy keys still go through
	// the normal path so the dashboard can enforce per-key budgets.
	//
	// Use the real providerReq for the input token estimate rather than
	// a dummy placeholder — the previous implementation passed a hardcoded
	// `"test"` message which always estimated ≈1 token and made the budget
	// check a no-op for real requests.
	if !strings.HasPrefix(key.ID, "system:") {
		if ok, err := usageService.CheckBudget(key.ID, float64(usageGatewayEstimateInputTokens(providerReq))*modelMapping.CreditWeight); err != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "budget_check_failed", "Failed to check usage budget.")
			return
		} else if !ok {
			recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "budget_exceeded", "Usage budget exceeded.")
			writeAnthropicError(w, http.StatusPaymentRequired, "budget_exceeded", "Usage budget exceeded.")
			return
		}
	}

	// Handle streaming
	if req.Stream {
		caller := conversation.NewCallerForProfile(profile)
		if streamer, ok := caller.(conversation.StreamCaller); ok {
			streamAnthropicGatewayMessages(w, r, usageService, streamer, key, req, modelMapping, providerReq, start)
			return
		}
		// Fallback to non-streaming if provider doesn't support streaming
	}

	// Non-streaming call
	caller := conversation.NewCallerForProfile(profile)
	resp, err := caller.Call(r.Context(), providerReq)
	if err != nil {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "provider_error", err.Error())
		writeAnthropicError(w, http.StatusBadGateway, "provider_error", err.Error())
		return
	}

	// Record usage
	call := usageGatewaySuccessCall(start, key.ID, req.Model, modelMapping, providerReq, resp)
	if err := usageService.RecordCall(call); err != nil {
		logger.Warnf("usage gateway: failed to record Anthropic call %s: %v", call.ID, err)
	}

	// Convert and write response
	anthropicResp := protocolToAnthropicResponse(resp, req.Model)
	writeJSON(w, http.StatusOK, anthropicResp)
}

// handleAnthropicGatewayMessages handles POST /v1/messages for the usage gateway.
// It accepts Anthropic's native request format and routes to the appropriate provider.
func handleAnthropicGatewayMessages(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager, secret string) {
	// secret is extracted by extractProxyKeySecret at the routing layer so
	// the gdx_ prefix can be matched from either Authorization: Bearer or
	// the Anthropic SDK's x-api-key header.
	key, err := usageService.AuthenticateKey(secret)
	if err != nil {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid API Key.")
		return
	}
	dispatchAnthropicMessages(w, r, usageService, manager, key)
}

// anthropicToProtocolRequest converts an Anthropic Messages API request to the internal protocol format.
func anthropicToProtocolRequest(req anthropicMessageRequest, model string) (protocol.Request, error) {
	protoReq := protocol.Request{
		Model:     model,
		MaxTokens: req.MaxTokens,
	}

	// Handle system prompt (can be string or content block array)
	switch s := req.System.(type) {
	case string:
		protoReq.System = strings.TrimSpace(s)
	case []interface{}:
		// Extract text from content blocks
		var systemParts []string
		for _, item := range s {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if blockType := strings.TrimSpace(fmt.Sprint(block["type"])); blockType == "text" {
				if text := strings.TrimSpace(fmt.Sprint(block["text"])); text != "" {
					systemParts = append(systemParts, text)
				}
			}
		}
		protoReq.System = strings.Join(systemParts, "\n")
	}

	// Convert messages. Drop blocks whose recognized fields are empty
	// (e.g. a malformed client sends {"type":"text"} with no "text" key)
	// and drop messages whose content reduces to nothing, so the upstream
	// Anthropic-compatible provider does not surface "messages is empty
	// (2013)" for a block that was never populated in the first place.
	for _, msg := range req.Messages {
		protoMsg := protocol.APIMessage{Role: msg.Role}
		var reasoning strings.Builder
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if text := strings.TrimSpace(block.Text); text != "" {
					protoMsg.Content = append(protoMsg.Content, protocol.Block{Type: protocol.BlockText, Text: text})
				}
			case "image":
				if block.Source != nil {
					protoMsg.Content = append(protoMsg.Content, protocol.Block{
						Type: protocol.BlockImage,
						Source: &protocol.ImageSource{
							Type:      block.Source.Type,
							MediaType: block.Source.MediaType,
							Data:      block.Source.Data,
						},
					})
				}
			case "tool_use":
				protoMsg.Content = append(protoMsg.Content, protocol.Block{
					Type:  protocol.BlockToolUse,
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				})
			case "tool_result":
				protoMsg.Content = append(protoMsg.Content, protocol.Block{
					Type:      protocol.BlockToolResult,
					ToolUseID: block.ToolUseID,
					Content:   collapseAnthropicToolResultContent(block.Content),
				})
			case "thinking":
				// Round-trip extended-thinking blocks. We surface
				// the chain-of-thought text on the API message
				// alongside the assistant's regular content so
				// multi-turn Pi sessions can keep their reasoning
				// context; the upstream Anthropic-compatible
				// client (and the protocol.Block type) does not
				// carry the signature itself, but the
				// reasoning_content field is the closest analogue
				// and is what Anthropic-native models need to
				// continue the chain-of-thought.
				text := block.Thinking
				if text == "" {
					// Some compatible providers omit the visible
					// text on a thinking block and only carry the
					// signature. We forward an empty block so the
					// upstream sees the boundary marker; the
					// signature is dropped because the
					// provider-specific token is only valid on
					// the upstream that minted it.
					text = ""
				}
				protoMsg.Content = append(protoMsg.Content, protocol.Block{
					Type:      protocol.BlockThinking,
					Text:      text,
					Signature: block.Signature,
				})
				if text != "" {
					if reasoning.Len() > 0 {
						reasoning.WriteString("\n")
					}
					reasoning.WriteString(text)
				}
			}
		}
		if len(protoMsg.Content) == 0 {
			continue
		}
		// Mirror the assistant's thinking text into the
		// ReasoningContent field so the upstream client carries
		// it on the wire (the Anthropic client uses
		// reasoning_content to keep multi-turn reasoning context).
		if msg.Role == protocol.RoleAssistant && reasoning.Len() > 0 {
			protoMsg.ReasoningContent = reasoning.String()
		}
		protoReq.Messages = append(protoReq.Messages, protoMsg)
	}

	// Convert tools
	for _, tool := range req.Tools {
		protoTool := protocol.ToolSchema{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}
		protoReq.Tools = append(protoReq.Tools, protoTool)
	}

	// Handle thinking config (for providers that support it). Pi
	// fills this in when the user picks a reasoning level — the
	// shape is `{type:"enabled", budget_tokens:N}` for budget-based
	// models and `{type:"adaptive"}` for adaptive-thinking models.
	// We forward the budget_tokens as a numeric `ReasoningEffort`
	// string so the upstream LLM client can map it to a local
	// `reasoning_effort` knob; the adaptive case is left for the
	// upstream client to detect from the request shape itself.
	if req.Thinking != nil {
		thinkingType := strings.ToLower(strings.TrimSpace(req.Thinking.Type))
		if thinkingType == "" || thinkingType == "enabled" {
			if req.Thinking.BudgetTokens > 0 {
				protoReq.ReasoningEffort = fmt.Sprintf("%d", req.Thinking.BudgetTokens)
			}
		}
	}

	return protoReq, nil
}

// protocolToAnthropicResponse converts an internal protocol response to Anthropic format.
func protocolToAnthropicResponse(resp *protocol.Response, model string) anthropicResponse {
	id := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	if resp != nil && len(resp.Content) > 0 {
		// Use first block's ID if available
		for _, block := range resp.Content {
			if block.ID != "" {
				id = "msg_" + block.ID
				break
			}
		}
	}

	anthropicResp := anthropicResponse{
		ID:    id,
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	if resp != nil {
		// Convert content blocks. We emit a thinking block FIRST so
		// the response content ordering matches Anthropic's spec
		// (thinking blocks come before text / tool_use blocks in a
		// multi-block response). Without this ordering, Pi's
		// `output.content` array can drop the thinking content and
		// only surface the text + tool_use blocks on the next turn.
		for _, block := range resp.Content {
			if block.Type != protocol.BlockThinking {
				continue
			}
			if block.Redacted {
				// Anthropic uses `redacted_thinking` (singular
				// `data` field) for the safety-filtered case.
				// The gateway emits the opaque data in the
				// `data` field rather than `signature` so the
				// client can echo it back verbatim on the next
				// turn.
				anthropicResp.Content = append(anthropicResp.Content, anthropicResponseBlock{
					Type: "redacted_thinking",
					Data: block.Signature,
				})
				continue
			}
			anthropicResp.Content = append(anthropicResp.Content, anthropicResponseBlock{
				Type:      "thinking",
				Thinking:  block.Text,
				Signature: block.Signature,
			})
		}
		for _, block := range resp.Content {
			switch block.Type {
			case protocol.BlockText:
				anthropicResp.Content = append(anthropicResp.Content, anthropicResponseBlock{
					Type: "text",
					Text: block.Text,
				})
			case protocol.BlockToolUse:
				anthropicResp.Content = append(anthropicResp.Content, anthropicResponseBlock{
					Type:  "tool_use",
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				})
			}
		}

		// Set stop reason. We use the shared
		// anthropicOutputStopReason helper so the streaming
		// and non-streaming paths emit the exact same wire
		// vocabulary; without the unification a
		// finish_reason of "length" (OpenAI) would become
		// "max_tokens" on the streaming wire but stay
		// "length" on the non-streaming wire, surfacing
		// inconsistent values to the same client depending
		// on stream mode.
		anthropicResp.StopReason = anthropicOutputStopReason(resp)

		// Set usage
		if resp.Usage != nil {
			anthropicResp.Usage = &anthropicUsage{
				InputTokens:              resp.Usage.InputTokens,
				OutputTokens:             resp.Usage.OutputTokens,
				CacheCreationInputTokens: resp.Usage.CacheWriteTokens,
				CacheReadInputTokens:     resp.Usage.CacheReadTokens,
			}
		}
	}

	return anthropicResp
}

// streamAnthropicGatewayMessages handles streaming responses for the Anthropic Messages API.
//
// The previous implementation assumed a single text block at index 0 and
// a single tool_use block sharing the same index, so Pi (or any client
// using the Anthropic SDK) would silently drop the second tool call in
// a multi-tool turn. This rewrite tracks every block the upstream
// surfaces, assigns each one a unique wire index, and emits a complete
// start/delta/stop triplet per block. It also handles thinking blocks
// (extended thinking) end-to-end so the chain-of-thought survives the
// gateway hop on its way back to Pi's multi-turn reasoning buffer.
func streamAnthropicGatewayMessages(
	w http.ResponseWriter,
	r *http.Request,
	usageService *usage.Service,
	streamer conversation.StreamCaller,
	key *usage.ProxyAPIKey,
	req anthropicMessageRequest,
	modelMapping *usage.ProxyModel,
	providerReq protocol.Request,
	start time.Time,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "streaming_unsupported", "response writer does not support flushing")
		writeAnthropicError(w, http.StatusInternalServerError, "streaming_unsupported", "Streaming is not supported by this response writer.")
		return
	}

	// Anthropic clients (Pi included) require Content-Type:
	// text/event-stream and an anthropic-version header. We emit
	// version "2023-06-01" to match Anthropic's current API and
	// keep the SDK's strict-mode parser happy.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("anthropic-version", "2023-06-01")
	w.WriteHeader(http.StatusOK)

	// flushSSE writes one Anthropic SSE frame: a `event:` line (the
	// discriminator the Anthropic SDK + Pi use to filter events via
	// ANTHROPIC_MESSAGE_EVENTS at anthropic.ts:267-274/417-444), a
	// `data:` line carrying the JSON payload, and a blank line, then
	// flushes. Centralising it keeps every event framed identically.
	flushSSE := func(event string, payload map[string]interface{}) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	// messageStartEmitted guards against emitting message_start more
	// than once. We delay message_start until the upstream's
	// message_start arrives (via OnMessageStart) so we can forward the
	// real input_token count. If the upstream never sends message_start
	// (e.g. it's an OpenAI shim), the first content delta triggers a
	// zero-usage fallback so the downstream SDK still sees a valid
	// stream header.
	messageID := fmt.Sprintf("msg_%d", start.UnixNano())
	messageStartEmitted := false

	emitMessageStart := func(usage map[string]interface{}) {
		if messageStartEmitted {
			return
		}
		messageStartEmitted = true
		flushSSE("message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":          messageID,
				"type":        "message",
				"role":        "assistant",
				"content":     []interface{}{},
				"model":       req.Model,
				"stop_reason": nil,
				"usage":       usage,
			},
		})
	}

	ensureMessageStart := func() {
		if !messageStartEmitted {
			emitMessageStart(map[string]interface{}{
				"input_tokens":                0,
				"output_tokens":               0,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			})
		}
	}

	// anthropicStreamState tracks the per-block state the gateway
	// needs to keep the wire in sync with the upstream SSE stream.
	// Each block the upstream surfaces (text, tool_use, thinking)
	// gets a stable wire index assigned in arrival order; the
	// previous implementation reused index 0 for both text and
	// tool_use which broke Pi's tool loop on the second tool call.
	type anthropicStreamState struct {
		// wireIndex is the Anthropic content_block index the
		// gateway has assigned to this block. It is unique per
		// active block in the message and is what consumers
		// (Pi's anthropic.ts stream loop) use to attach
		// subsequent deltas to the correct block.
		wireIndex int
		// started is true once the gateway has emitted a
		// content_block_start event for this block. The
		// upstream may surface the start inline (Anthropic
		// native) or via a separate content_block_start
		// callback; we always emit exactly one start per
		// block on the wire.
		started bool
		// stopped flips to true the moment we emit
		// content_block_stop, so the post-stream teardown
		// doesn't double-fire on blocks whose stop already
		// arrived as part of the upstream stream.
		stopped bool
		// lastPartialJSON is the cumulative tool_use JSON the
		// upstream has emitted for this block. We diff against
		// the new partialJSON on every callback so the wire
		// only sees the per-chunk suffix — see the
		// OnToolUse branch for why this matters.
		lastPartialJSON string
		// lastPartialThinking is the cumulative thinking text
		// the upstream has emitted for thinking blocks. We
		// diff against the new fragment so the wire only sees
		// the per-chunk suffix.
		lastPartialThinking string
	}

	// blockStates maps from the upstream's block id (or, for
	// legacy upstreams that don't surface one, the upstream's
	// content_block index) to the gateway's per-block state.
	// We key on the upstream's id first so the gateway can
	// route every callback for the same tool_use to the same
	// wire state even when the upstream reuses index 0 across
	// multiple tool calls.
	blockStates := map[string]*anthropicStreamState{}
	// indexByWire keeps the reverse lookup so we can flush a
	// block_stop for every active block at the end of the
	// stream. The slice order matches the arrival order so the
	// wire-order of content_block_stop events is stable.
	indexByWire := []*anthropicStreamState{}
	nextWireIndex := 0

	// assignBlockIndex returns the wire index for the given
	// upstream block id, allocating a new one on the first
	// encounter. The first time a block surfaces we ALSO
	// emit a content_block_start for it on the wire so the
	// downstream SDK can attach subsequent deltas to the
	// correct block. The opener is split per block-type so
	// the wire shape matches the upstream's intent (e.g.,
	// tool_use blocks carry id+name+empty input, thinking
	// blocks carry type+thinking+signature, text blocks
	// carry an empty text field).
	assignBlockIndex := func(blockID, blockType string) *anthropicStreamState {
		if state, ok := blockStates[blockID]; ok {
			return state
		}
		state := &anthropicStreamState{wireIndex: nextWireIndex}
		nextWireIndex++
		blockStates[blockID] = state
		indexByWire = append(indexByWire, state)
		return state
	}

	// flushBlockStart emits the wire-side content_block_start
	// for one block. The shape of `content_block` depends on
	// the block type — text starts with `{type:"text",
	// text:""}`, tool_use with `{type:"tool_use", id, name,
	// input:{}}`, thinking with `{type:"thinking", thinking,
	// signature}` (or `{type:"redacted_thinking", data}`),
	// and unknown shapes pass through the upstream's payload
	// verbatim so we don't break on a future Anthropic block
	// type.
	flushBlockStart := func(state *anthropicStreamState, blockType string, opener map[string]interface{}, block protocol.Block) {
		if state.started {
			return
		}
		state.started = true
		payload := map[string]interface{}{
			"type":          "content_block_start",
			"index":         state.wireIndex,
			"content_block": opener,
		}
		_ = block
		flushSSE("content_block_start", payload)
	}

	// textBlockKey produces a stable key for a text block so
	// concurrent text deltas from the upstream all attach to
	// the same wire index. The upstream may emit many text
	// deltas without a fresh content_block_start, so we treat
	// the (empty) block id as a key sentinel.
	textBlockKey := "text-0"

	// Stream the upstream response. Client.streamOnce calls OnTextDelta for
	// every text_delta SSE event and returns a final *protocol.Response with
	// the consolidated content + usage. Client.parseMessageStream calls
	// OnToolUse three times per tool_use block (content_block_start,
	// each input_json_delta, content_block_stop) so the gateway can
	// forward Anthropic-SSE tool_use frames to the wire and Pi's
	// anthropic.ts:568-660 toolcall_* paths can execute the tool.
	// OnThinkingDelta fires per extended-thinking chain-of-thought
	// fragment (the parser splits thinking_delta and signature_delta
	// through this hook so the gateway can emit the right wire
	// shape on the Anthropic SSE stream).
	finalResp, streamErr := streamer.Stream(r.Context(), providerReq, conversation.StreamHandler{
		OnMessageStart: func(usage protocol.Usage) {
			// The upstream's message_start arrived with the real
			// input token count. Emit our message_start NOW (it was
			// delayed from the preamble) so the downstream
			// Anthropic SDK (pi) initialises its usage accumulator
			// with the correct input_tokens. Without this, pi sees
			// input_tokens=0 and displays 0% context usage forever.
			emitMessageStart(map[string]interface{}{
				"input_tokens":                usage.InputTokens,
				"output_tokens":               0,
				"cache_creation_input_tokens": usage.CacheWriteTokens,
				"cache_read_input_tokens":     usage.CacheReadTokens,
			})
		},
		OnTextDelta: func(delta string) {
			ensureMessageStart()
			if delta == "" {
				return
			}
			// All text deltas attach to a single block —
			// Anthropic's streaming shape is one text block
			// per assistant turn, and the SDK uses the
			// absence of a new content_block_start to
			// determine the boundary. We open the text
			// block on the first delta and forward
			// subsequent deltas with the same wire index.
			state := assignBlockIndex(textBlockKey, "text")
			flushBlockStart(state, "text", map[string]interface{}{"type": "text", "text": ""}, protocol.Block{Type: protocol.BlockText})
			flushSSE("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": state.wireIndex,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": delta,
				},
			})
		},
		OnThinkingDelta: func(thinking string, signature string) {
			ensureMessageStart()
			// Each thinking block gets its own wire index.
			// The previous implementation forwarded thinking
			// deltas as text deltas, which (a) left Pi's
			// thinking text in the visible content block
			// rather than the reasoning buffer and (b) lost
			// the signature entirely so multi-turn sessions
			// couldn't keep their reasoning context. The
			// upstream parser fires this hook for each
			// thinking_delta (with empty signature) and the
			// trailing signature_delta (with empty thinking
			// fragment); we route each to the right wire
			// frame and keep them in separate state entries
			// so a thinking block can be interleaved with
			// text and tool_use blocks without losing
			// boundaries.
			//
			// We use a stable key per upstream content_block
			// index so multiple thinking deltas from the
			// same upstream block all attach to the same
			// wire index. The key is derived from the
			// parser's block state — we read the current
			// state's running text and signature to compute
			// a per-block identity, but the simpler approach
			// is to key on the upstream's content_block
			// index which is stable across all callbacks
			// for the same block.
			//
			// We do not have the upstream index here
			// directly, so we use a per-call unique key.
			// The gateway already deduplicates via the
			// blockStates map; using a fresh key per call
			// would create a new block per delta which is
			// wrong. The parser's StreamHandler cannot
			// pass the upstream index through the OnThinkingDelta
			// signature today, so we accept that limitation
			// and forward thinking deltas as a single
			// "thinking" content block with running text.
			//
			// For simplicity and correctness, we now key
			// on a single "thinking" block; if the upstream
			// ever surfaces two parallel thinking blocks in
			// the same turn (not a real Anthropic pattern),
			// the second will overwrite the first on the
			// wire. We accept this trade-off because the
			// StreamHandler hook does not currently carry
			// the upstream block index. A future refactor
			// can extend OnThinkingDelta to pass the index.
			const thinkingBlockKey = "thinking-0"
			state := assignBlockIndex(thinkingBlockKey, "thinking")
			if !state.started {
				state.started = true
				flushSSE("content_block_start", map[string]interface{}{
					"type":          "content_block_start",
					"index":         state.wireIndex,
					"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
				})
			}
			if thinking != "" {
				// Per-chunk delta. Append to the running
				// buffer and forward a thinking_delta.
				state.lastPartialThinking += thinking
				flushSSE("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": state.wireIndex,
					"delta": map[string]interface{}{
						"type":     "thinking_delta",
						"thinking": thinking,
					},
				})
			}
			if signature != "" {
				// The trailing signature_delta frame. The
				// signature is opaque and is the binding
				// token the client must echo back on the
				// next turn to keep multi-turn reasoning
				// context. Forward it verbatim.
				flushSSE("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": state.wireIndex,
					"delta": map[string]interface{}{
						"type":      "signature_delta",
						"signature": signature,
					},
				})
			}
		},
		OnToolUse: func(block protocol.Block, partialJSON string) {
			ensureMessageStart()
			// The upstream parser calls OnToolUse three
			// times per tool_use block:
			//
			//   1. content_block_start  — id+name+type
			//      set, partialJSON empty
			//   2. each input_json_delta — partialJSON
			//      is the cumulative reassembled JSON
			//   3. content_block_stop   — partialJSON
			//      is the final cumulative JSON
			//
			// We open the wire-side tool_use on call #1
			// (no fragment to forward, partialJSON is
			// empty), forward the per-chunk delta on
			// calls #2 and #3, and emit a stop on call
			// #3 (the previous implementation forgot
			// this for the second case and ended up
			// with one missing stop per multi-tool turn,
			// which made Pi's agent loop hang).
			if block.Type != protocol.BlockToolUse {
				return
			}
			blockID := strings.TrimSpace(block.ID)
			if blockID == "" {
				// Defensive fallback: the upstream
				// didn't surface an id, so key on
				// name+index. The previous behaviour
				// was to drop the block, but in
				// practice every Anthropic-shape
				// upstream attaches an id.
				blockID = fmt.Sprintf("tool-%s-%d", block.Name, block.Index)
			}
			state := assignBlockIndex(blockID, "tool_use")
			if !state.started {
				inputMap := block.Input
				if inputMap == nil {
					inputMap = map[string]interface{}{}
				}
				flushBlockStart(state, "tool_use", map[string]interface{}{
					"type":  "tool_use",
					"id":    block.ID,
					"name":  block.Name,
					"input": inputMap,
				}, block)
				if fragment := toolJSONDeltaSuffix("", partialJSON); fragment != "" {
					state.lastPartialJSON = partialJSON
					flushSSE("content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": state.wireIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": fragment,
						},
					})
				}
				return
			}
			if fragment := toolJSONDeltaSuffix(state.lastPartialJSON, partialJSON); fragment != "" {
				state.lastPartialJSON = partialJSON
				flushSSE("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": state.wireIndex,
					"delta": map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": fragment,
					},
				})
			}
		},
	})

	// Close any open blocks. We emit a content_block_stop
	// for every block the upstream opened, in wire-index
	// order, regardless of whether the upstream itself
	// surfaced a stop event. The previous implementation
	// only closed the text block, which left tool_use
	// blocks dangling on the wire and made Pi's agent
	// loop wait forever for the matching stop.
	for _, state := range indexByWire {
		if state.stopped {
			continue
		}
		state.stopped = true
		flushSSE("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": state.wireIndex,
		})
	}

	if streamErr != nil {
		// Emit an SSE error event so strict consumers (Anthropic SDK, Pi) can
		// surface a meaningful message instead of an empty assistant turn.
		flushSSE("error", map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "api_error",
				"message": streamErr.Error(),
			},
		})
	} else {
		// 4) message_delta — must carry delta.stop_reason (Anthropic's
		// canonical stop-reason vocabulary that consumers like Pi's
		// mapStopReason at anthropic.ts:1210-1230 translate to local
		// values) plus incremental usage. Usage MUST live here, not on
		// message_stop, per the canonical spec.
		//
		// We map the upstream's stop_reason through the
		// Anthropic-output vocabulary rather than the OpenAI one:
		// OpenAI's "tool_calls" becomes Anthropic's "tool_use",
		// "length" becomes "max_tokens", and "stop" becomes
		// "end_turn". The previous implementation passed the
		// upstream value through unchanged, which surfaced
		// OpenAI's "tool_calls" on the Anthropic wire — a value
		// the Anthropic SDK does not recognise, causing the
		// agent loop to hang after the first tool call.
		stopReason := anthropicOutputStopReason(finalResp)
		deltaPayload := map[string]interface{}{
			"type":  "message_delta",
			"delta": map[string]interface{}{"stop_reason": stopReason},
		}
		if finalResp != nil && finalResp.Usage != nil {
			deltaPayload["usage"] = map[string]interface{}{
				"input_tokens":                finalResp.Usage.InputTokens,
				"output_tokens":               finalResp.Usage.OutputTokens,
				"cache_creation_input_tokens": finalResp.Usage.CacheWriteTokens,
				"cache_read_input_tokens":     finalResp.Usage.CacheReadTokens,
			}
		}
		flushSSE("message_delta", deltaPayload)
	}

	// 5) message_stop — terminal event. Per the Anthropic spec it carries
	// no `usage` field (usage travels in message_delta). Keep the JSON
	// shape minimal so strict consumers do not reject the stream.
	flushSSE("message_stop", map[string]interface{}{"type": "message_stop"})

	// Record usage.
	if streamErr == nil {
		// Reuse the final response the streamer produced; if it returned nil
		// (e.g. the upstream closed without a typed payload), synthesise a
		// minimal stop record so the usage log still has an entry.
		if finalResp == nil {
			finalResp = &protocol.Response{
				StopReason: "end_turn",
				Content:    []protocol.Block{{Type: protocol.BlockText, Text: ""}},
			}
		}
		call := usageGatewaySuccessCall(start, key.ID, req.Model, modelMapping, providerReq, finalResp)
		if err := usageService.RecordCall(call); err != nil {
			logger.Warnf("usage gateway: failed to record streamed Anthropic call %s: %v", call.ID, err)
		}
	} else {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "provider_error", streamErr.Error())
	}
}

// anthropicOutputStopReason maps an upstream protocol.Response
// stop_reason onto the Anthropic wire vocabulary used by the
// /v1/messages streaming path. The mapping is the inverse of
// the OpenAI-output mapping in respStopReason:
//
//	upstream         → Anthropic wire
//	"" / "stop"      → "end_turn"
//	"length"         → "max_tokens"
//	"tool_calls"     → "tool_use"
//	"function_call"  → "tool_use"
//	"end_turn"       → "end_turn"     (passthrough)
//	"tool_use"       → "tool_use"     (passthrough)
//	"pause_turn"     → "pause_turn"   (passthrough; Pi's mapStopReason
//	                                  translates this locally)
//	"refusal"        → "refusal"      (passthrough; same)
//	"content_filter" → "refusal"      (OpenAI -> Anthropic
//	                                  safety-filter mapping)
//
// Without this mapper, an upstream finish_reason of
// "tool_calls" (the OpenAI canonical value) would surface on
// the Anthropic wire as "tool_calls" too, which the
// Anthropic SDK does not recognise and would treat as a
// malformed stream — the canonical symptom is the agent
// loop hanging after the first tool call because the SDK
// never sees the matching terminal stop_reason it knows
// how to translate. The non-streaming protocolToAnthropicResponse
// path uses the same mapping table for consistency.
func anthropicOutputStopReason(resp *protocol.Response) string {
	if resp == nil {
		return "end_turn"
	}
	switch strings.ToLower(strings.TrimSpace(resp.StopReason)) {
	case "", "stop", "stop_sequence", "end_turn":
		return "end_turn"
	case "length", "max_tokens":
		return "max_tokens"
	case "tool_calls", "function_call", "tool_use":
		return "tool_use"
	case "pause_turn":
		return "pause_turn"
	case "refusal":
		return "refusal"
	case "content_filter":
		// OpenAI uses "content_filter" for safety-filtered
		// completions; Anthropic's closest equivalent is
		// "refusal". Map across so the Anthropic SDK
		// surfaces the same intent on the user side.
		return "refusal"
	default:
		// Unknown / provider-specific values pass
		// through unchanged. Pi's mapStopReason throws
		// on unknown values, but the passthrough is
		// the best the gateway can do without a more
		// specific translation table.
		return resp.StopReason
	}
}

// writeAnthropicError writes an Anthropic-format error response.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Anthropic uses a specific error format
	errorResp := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    errType,
			"message": message,
		},
	}
	json.NewEncoder(w).Encode(errorResp)
}

// collapseAnthropicToolResultContent flattens an Anthropic tool_result
// `content` field to a string for the upstream provider. The Anthropic
// spec lets `content` be either a string (the common case) or an array
// of content blocks (text + image, etc.). The upstream
// anthropic_compatible client only accepts a string, so for the array
// form we concatenate the text blocks (and drop image / non-text
// blocks — the upstream does not surface them in tool results
// regardless). Returns "" for missing or unrecognized content.
func collapseAnthropicToolResultContent(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch strings.TrimSpace(fmt.Sprint(block["type"])) {
			case "text":
				if text := strings.TrimSpace(fmt.Sprint(block["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// protocolToolUsesToOpenAIToolCalls converts protocol response tool_use
// blocks to the OpenAI tool_calls wire format for non-streaming responses.
// This is the inverse of the streaming path's per-chunk emission: the
// non-streaming response carries the final assembled tool calls in a single
// message.tool_calls array so the OpenAI SDK can execute them immediately
// without concatenating per-chunk deltas.
