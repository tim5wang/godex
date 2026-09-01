package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/usage"
)

func registerUsageRoutes(mux *http.ServeMux, protected func(http.Handler) http.Handler, usageService *usage.Service, manager *config.Manager) {
	if usageService == nil {
		return
	}
	mux.Handle("GET /usage/keys", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys, err := usageService.ListKeysWithSystemEntries()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})))
	mux.Handle("POST /usage/keys", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req usage.KeyCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := usageService.CreateKey(req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, resp)
	})))
	mux.Handle("PATCH /usage/keys/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req usage.KeyUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		key, err := usageService.UpdateKey(r.PathValue("id"), req)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, key)
	})))
	mux.Handle("POST /usage/keys/{id}/reset", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reset rotates the secret and returns the new plaintext exactly once.
		// The previous secret stops authenticating immediately; the new secret is
		// not stored on the key, so callers must copy it now or rotate again.
		resp, err := usageService.ResetKey(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})))
	mux.Handle("GET /usage/models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		models, err := usageService.ListModels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, models)
	})))
	mux.Handle("POST /usage/models", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req usage.ModelCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		m, err := usageService.CreateModel(req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, m)
	})))
	mux.Handle("PATCH /usage/models/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req usage.ModelUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		m, err := usageService.UpdateModel(r.PathValue("id"), req)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, m)
	})))
	mux.Handle("GET /usage/summary", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeType := r.URL.Query().Get("range")
		apiKeyID := r.URL.Query().Get("api_key_id")
		if rangeType == "" {
			rangeType = "day"
		}
		summary, err := usageService.GetSummary(rangeType, apiKeyID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})))
	mux.Handle("GET /usage/calls", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("date")
		apiKeyID := r.URL.Query().Get("api_key_id")
		calls, err := usageService.GetCalls(date, apiKeyID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, calls)
	})))
	mux.Handle("GET /usage/cache-stats", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := usage.CacheStatsQuery{
			RangeType: r.URL.Query().Get("range"),
			Model:     r.URL.Query().Get("model"),
			// The web-token admin path can request another
			// tenant's cache stats by passing api_key_id
			// explicitly. The proxy-key path (see
			// /v1/usage/cache-stats below) ignores this and
			// always pins the query to the authenticated
			// key, so a proxy key user can't escalate to
			// the all-tenants view.
			APIKeyID: r.URL.Query().Get("api_key_id"),
		}
		if query.RangeType == "" {
			query.RangeType = "day"
		}
		stats, err := usageService.GetCacheStats(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})))

	// /v1/usage/cache-stats is the proxy-key-facing variant
	// of the admin /usage/cache-stats endpoint. It accepts
	// the same gdx_ auth shape the other /v1/ routes do, and
	// always pins the query to the authenticated key so a
	// proxy key holder can never see another tenant's cache
	// stats. We expose it under the same /v1/ namespace so
	// an external Anthropic-SDK-style client that wants to
	// observe its own cache performance doesn't need to know
	// the godex admin route. The response shape mirrors the
	// admin endpoint so a single dashboard widget can render
	// both.
	mux.Handle("GET /v1/usage/cache-stats", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Authenticate the proxy key from the standard
		// Authorization: Bearer gdx_xxx or x-api-key header.
		// extractProxyKeySecret is shared with the other
		// /v1/ routes so the auth contract is uniform.
		secret := extractProxyKeySecret(r)
		if secret == "" || !strings.HasPrefix(secret, usage.KeyPrefix) {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("Invalid API Key. Please provide a valid proxy key with gdx_ prefix."))
			return
		}
		key, err := usageService.AuthenticateKey(secret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("Invalid API Key."))
			return
		}
		// Pin to the authenticated key. We deliberately
		// ignore any api_key_id the caller may have
		// passed in the query string; otherwise a proxy
		// key holder could enumerate other tenants by
		// guessing their key IDs.
		query := usage.CacheStatsQuery{
			RangeType: r.URL.Query().Get("range"),
			Model:     r.URL.Query().Get("model"),
			APIKeyID:  key.ID,
		}
		if query.RangeType == "" {
			query.RangeType = "day"
		}
		stats, err := usageService.GetCacheStats(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	}))

	// ---- Time-series (trend charts) ----
	mux.Handle("GET /usage/time-series", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := usage.TimeSeriesQuery{
			Granularity: q.Get("granularity"),
			StartTime:   q.Get("start_time"),
			EndTime:     q.Get("end_time"),
			APIKeyID:    q.Get("api_key_id"),
			SessionID:   q.Get("session_id"),
			Model:       q.Get("model"),
		}
		if query.Granularity == "" {
			query.Granularity = "day"
		}
		points, err := usageService.GetTimeSeries(query)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, points)
	})))

	// ---- Session usage ----
	mux.Handle("GET /usage/sessions", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		apiKeyID := q.Get("api_key_id")
		limit := parseIntQuery(q.Get("limit"), 20)
		offset := parseIntQuery(q.Get("offset"), 0)
		sessions, err := usageService.ListSessions(apiKeyID, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, sessions)
	})))

	mux.Handle("GET /usage/sessions/{id}", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		summary, err := usageService.GetSessionUsage(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})))
}

func handleUsageGatewayChatCompletions(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager) {
	start := time.Now()

	// Authenticate proxy key
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	secret := strings.TrimPrefix(auth, "Bearer ")
	secret = strings.TrimSpace(secret)

	key, err := usageService.AuthenticateKey(secret)
	if err != nil {
		writeUsageGatewayError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid API Key. Please provide a valid proxy key.")
		return
	}

	var req openAIChatCompletionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeUsageGatewayError(w, http.StatusBadRequest, "invalid_request", "Invalid request body.")
		return
	}

	// Resolve model
	if req.Model == "" {
		writeUsageGatewayError(w, http.StatusBadRequest, "invalid_model", "Model is required.")
		return
	}

	// Check allowed models
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

	// Resolve model mapping
	modelMapping, err := usageService.ResolveModel(req.Model)
	if err != nil {
		writeUsageGatewayError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("Model '%s' not found or disabled.", req.Model))
		return
	}

	// Usage gateway supports streaming as of this version: we type-assert the
	// resolved caller to a StreamCaller (every provider factory now returns
	// one) and forward deltas directly to an OpenAI-compatible SSE response.
	// If a particular provider cannot stream, we fall through to the
	// non-streaming path below so the client still receives a valid JSON body.
	// (The actual stream branch lives after the profile + budget checks below.)
	if req.Stream {
		// Defer to the post-budget block; fall through to non-stream if no
		// StreamCaller is available (e.g. legacy custom caller).
		_ = req.Stream
	}
	providerReq, err := usageGatewayProtocolRequest(req)
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
	if ok, err := usageService.CheckBudget(key.ID, float64(usageGatewayEstimateInputTokens(providerReq))*modelMapping.CreditWeight); err != nil {
		writeUsageGatewayError(w, http.StatusInternalServerError, "budget_check_failed", "Failed to check usage budget.")
		return
	} else if !ok {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "budget_exceeded", "Usage budget exceeded.")
		writeUsageGatewayError(w, http.StatusPaymentRequired, "budget_exceeded", "Usage budget exceeded.")
		return
	}

	// Streaming branch: route to the provider's Stream path if available, so
	// SSE clients receive real chat.completion.chunk deltas. The non-stream
	// call path remains the fallback for callers that cannot stream.
	if req.Stream {
		caller := conversation.NewCallerForProfile(profile)
		if streamer, ok := caller.(conversation.StreamCaller); ok {
			streamUsageGatewayChatCompletions(w, r, usageService, streamer, key, req, modelMapping, providerReq, profile, start)
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
	content := ""
	var toolCalls []openAIToolCallWire
	if resp != nil {
		content = strings.TrimSpace(protocol.BlocksText(resp.Content))
		toolCalls = protocolToolUsesToOpenAIToolCalls(resp.Content)
	}
	call := usageGatewaySuccessCall(start, key.ID, req.Model, modelMapping, providerReq, resp)
	if err := usageService.RecordCall(call); err != nil {
		writeUsageGatewayError(w, http.StatusInternalServerError, "usage_record_failed", "Failed to record usage.")
		return
	}
	writeJSON(w, http.StatusOK, openAIChatCompletionResponse{
		ID:      openAICompletionID(call.ID),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []openAIChatChoice{{
			Index: 0,
			Message: &openAIChatMessage{
				Role:      "assistant",
				Content:   content,
				ToolCalls: toolCalls,
			},
			FinishReason: firstNonEmptyString(respStopReason(resp), "stop"),
		}},
		Usage: map[string]interface{}{
			// call.InputTokens is the canonical UNCACHED input (see
			// openAIUsageToProtocol); OpenAI wire format expects
			// prompt_tokens to include the cached portion.
			"prompt_tokens":     call.InputTokens + call.CacheReadTokens,
			"completion_tokens": call.OutputTokens,
			"total_tokens":      call.InputTokens + call.OutputTokens + call.CacheReadTokens + call.CacheWriteTokens,
			"estimated":         call.Estimated,
		},
	})
}

func usageGatewayProtocolRequest(req openAIChatCompletionRequest) (protocol.Request, error) {
	var systemParts []string
	messages := make([]protocol.APIMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))

		// System / developer messages are flattened into a
		// single system prompt; the protocol model has no
		// notion of "developer" vs "system" and Anthropic
		// uses the same field for both.
		if role == "system" || role == "developer" {
			text := strings.TrimSpace(openAIContentText(msg.Content))
			if text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}

		// `role: "tool"` messages carry the tool result of
		// an earlier assistant tool_call. The OpenAI wire
		// shape places the result in `content` and the
		// originating tool_call's id in `tool_call_id`; we
		// translate to a single BlockToolResult so the
		// upstream sees a content block of type
		// "tool_result" with the matching tool_use_id. The
		// previous implementation dropped messages with
		// empty `content` (which is the common case for
		// tool result messages whose `content` may be empty
		// but whose `tool_call_id` is the load-bearing
		// field) and also dropped messages with `role:
		// "tool"` because the role mapping had no special
		// case for it. That broke the agent loop on the
		// second turn of any tool-using conversation: the
		// model never saw its own tool result, so it could
		// never produce a follow-up reply. This block
		// restores both fields.
		if role == "tool" {
			toolUseID := strings.TrimSpace(msg.ToolCallID)
			if toolUseID == "" {
				// Defensive: an OpenAI tool message
				// without a tool_call_id is
				// malformed (the SDK always sets it),
				// but we surface the error rather
				// than silently dropping the
				// message so the caller can see the
				// problem.
				return protocol.Request{}, fmt.Errorf("role: \"tool\" message is missing tool_call_id")
			}
			content := openAIContentText(msg.Content)
			messages = append(messages, protocol.APIMessage{
				Role: protocol.RoleUser, // tool_result lives under role=user in Anthropic
				Content: []protocol.Block{{
					Type:      protocol.BlockToolResult,
					ToolUseID: toolUseID,
					Content:   content,
				}},
			})
			continue
		}

		protoRole := role
		switch protoRole {
		case protocol.RoleAssistant, protocol.RoleUser:
		default:
			protoRole = protocol.RoleUser
		}

		// Assistant messages can carry (a) text content,
		// (b) tool_calls, or (c) both. The text goes
		// into a BlockText block; each tool_call goes
		// into a BlockToolUse block with the
		// arguments already-JSON-marshaled. The previous
		// implementation only looked at `content` and
		// dropped the tool_calls array, which meant the
		// upstream model saw no record of its own prior
		// tool calls on the second turn (the OpenAI
		// client reconstructed them from the tool result
		// message, but the upstream model lost the
		// symmetry). The fix here makes the assistant
		// message carry both shapes so the upstream can
		// match each tool_use_id with its
		// corresponding tool_result.
		protoMsg := protocol.APIMessage{Role: protoRole}
		// Only emit a text block when the assistant message
		// actually carries text content. Pi sends `content:
		// null` on tool_calls turns (and the OpenAI SDK
		// round-trips the same shape on subsequent turns), and
		// an empty text block would either serialize as the
		// string "<nil>" (which Anthropic rejects) or as a
		// zero-length content array entry (which Anthropic
		// also rejects with 2013 "messages must have
		// non-empty content"). Without this guard, the second
		// turn of any tool-using conversation would 400.
		if msg.Content != nil {
			if text := strings.TrimSpace(openAIContentText(msg.Content)); text != "" {
				protoMsg.Content = append(protoMsg.Content, protocol.TextBlock(text))
			}
		}
		for _, call := range msg.ToolCalls {
			if strings.TrimSpace(call.Function.Name) == "" {
				// A tool_call fragment with no name
				// is a partial OpenAI streaming
				// delta (Pi sends the function name
				// on the first frame and arguments
				// on subsequent frames). The
				// non-streaming path should never
				// see those because Pi only sends
				// assembled tool_calls on the
				// non-streaming /v1/chat/completions
				// path, but be defensive: skip
				// nameless fragments so we don't
				// emit a broken tool_use block.
				continue
			}
			var input map[string]interface{}
			if strings.TrimSpace(call.Function.Arguments) != "" {
				if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
					return protocol.Request{}, fmt.Errorf("assistant tool_call %q has invalid JSON arguments: %w", call.Function.Name, err)
				}
			}
			if input == nil {
				input = map[string]interface{}{}
			}
			protoMsg.Content = append(protoMsg.Content, protocol.Block{
				Type:  protocol.BlockToolUse,
				ID:    call.ID,
				Name:  call.Function.Name,
				Input: input,
				Index: call.Index,
			})
		}
		// Don't drop the message if it's an empty
		// assistant turn (text="" + no tool_calls); the
		// model sometimes sends these as separator
		// markers and the upstream handles them as a
		// valid no-op. Only skip when the message is
		// truly empty AND has no tool_calls — a
		// tool_call-only message is meaningful.
		if len(protoMsg.Content) == 0 {
			continue
		}
		messages = append(messages, protoMsg)
	}
	if len(messages) == 0 {
		return protocol.Request{}, fmt.Errorf("at least one non-empty user or assistant message is required")
	}
	// Forward the OpenAI tools catalog to the upstream LLM. Without
	// this, the model has no schema to ground its tool_calls deltas
	// in, so it falls back to emitting the call as a literal
	// `<bash>...</bash>` block in the content stream. The OpenAI
	// SDK does not parse that as a tool call, so the client tool
	// loop never runs. The LLM client (OpenAIClient) translates
	// each protocol.ToolSchema into the wire shape
	// {type:"function", function:{name, description, parameters,
	// strict}} before sending to the upstream.
	tools := make([]protocol.ToolSchema, 0, len(req.Tools))
	for _, tool := range req.Tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			continue
		}
		tools = append(tools, protocol.ToolSchema{
			Name:        name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}
	return protocol.Request{
		MaxTokens: req.MaxTokens,
		System:    strings.Join(systemParts, "\n\n"),
		Messages:  messages,
		Tools:     tools,
	}, nil
}

func usageGatewaySuccessCall(start time.Time, keyID, publicModel string, modelMapping *usage.ProxyModel, req protocol.Request, resp *protocol.Response) *usage.UsageCall {
	call := &usage.UsageCall{
		Timestamp:       time.Now(),
		APIKeyID:        keyID,
		PublicModel:     publicModel,
		TargetProfileID: modelMapping.TargetProfileID,
		TargetModel:     modelMapping.TargetModel,
		CreditWeight:    modelMapping.CreditWeight,
		Status:          "success",
		LatencyMs:       time.Since(start).Milliseconds(),
	}
	if resp != nil && resp.Usage != nil {
		call.InputTokens = resp.Usage.InputTokens
		call.OutputTokens = resp.Usage.OutputTokens
		call.CacheReadTokens = resp.Usage.CacheReadTokens
		call.CacheWriteTokens = resp.Usage.CacheWriteTokens
		call.Estimated = resp.Usage.Estimated
	} else {
		call.InputTokens = usageGatewayEstimateInputTokens(req)
		if resp != nil {
			call.OutputTokens = compress.CountTokens(protocol.BlocksText(resp.Content))
		}
		call.Estimated = true
	}
	return call
}

func usageGatewayEstimateInputTokens(req protocol.Request) int {
	total := compress.CountTokens(req.System)
	for _, msg := range req.Messages {
		total += compress.CountTokens(protocol.BlocksText(msg.Content))
	}
	if total <= 0 {
		return 1
	}
	return total
}

func recordUsageGatewayError(usageService *usage.Service, start time.Time, keyID, publicModel string, modelMapping *usage.ProxyModel, code, message string) {
	if usageService == nil {
		return
	}
	call := &usage.UsageCall{
		Timestamp:    time.Now(),
		APIKeyID:     keyID,
		PublicModel:  publicModel,
		CreditWeight: 1,
		Status:       "error",
		Error:        message,
		ErrorCode:    code,
		LatencyMs:    time.Since(start).Milliseconds(),
		Estimated:    true,
	}
	if modelMapping != nil {
		call.TargetProfileID = modelMapping.TargetProfileID
		call.TargetModel = modelMapping.TargetModel
		call.CreditWeight = modelMapping.CreditWeight
	}
	_ = usageService.RecordCall(call)
}

func respStopReason(resp *protocol.Response) string {
	if resp == nil {
		return ""
	}
	raw := strings.TrimSpace(resp.StopReason)
	// OpenAI-compatible clients (including the Pi coding agent) only
	// recognize the canonical finish_reason values: "stop", "length",
	// "tool_calls", "content_filter", and "function_call". Anthropic
	// providers emit "end_turn", "stop_sequence", "tool_use", and
	// "max_tokens" instead. Map the common cases so the chat.completion
	// response can be consumed by the OpenAI SDK family without the
	// client surfacing "Provider finish_reason: end_turn" as an error.
	switch strings.ToLower(raw) {
	case "", "stop", "stop_sequence", "end_turn":
		return "stop"
	case "max_tokens", "length":
		return "length"
	case "tool_use", "tool_calls":
		return "tool_calls"
	default:
		return raw
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseIntQuery(value string, defaultVal int) int {
	if value == "" {
		return defaultVal
	}
	n := 0
	for _, c := range value {
		if c < '0' || c > '9' {
			return defaultVal
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func writeUsageGatewayError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    code,
			"code":    fmt.Sprintf("%d", status),
		},
	})
}

// streamUsageGatewayChatCompletions forwards an SSE streaming response to
// the client. It mirrors the web-token path in streamOpenAIChatCompletion
// (routes_openai.go) but authenticates and budgets the call through the
// usage gateway. Each text delta from the provider is converted to an
// OpenAI chat.completion.chunk via emitOpenAIChunk, terminated by a final
// stop chunk and the data: [DONE] sentinel. Usage is recorded once the
// stream returns the final response so we can read the provider's usage
// payload.
//
// Cross-protocol contract (Pi + OpenAI protocol + anthropic_compatible
// provider): the upstream Anthropic parser (parseMessageStream) calls
// OnToolUse with the CUMULATIVE tool_use JSON, but Pi's OpenAI SDK
// expects each tool_calls chunk to carry a per-chunk delta that the SDK
// concatenates itself. Forwarding the cumulative string would make Pi
// see the same bytes repeated and accumulate the JSON by string
// concatenation, producing invalid JSON like
// `{"command":"ls"}{"command":"ls","timeout":30}` — exactly the
// pattern that made the agent loop hang after the first tool call. We
// track the previous cumulative per tool index and forward only the
// delta suffix, mirroring the toolJSONDeltaSuffix fix the
// Anthropic-streaming path already had.
func streamUsageGatewayChatCompletions(
	w http.ResponseWriter,
	r *http.Request,
	usageService *usage.Service,
	streamer conversation.StreamCaller,
	key *usage.ProxyAPIKey,
	req openAIChatCompletionRequest,
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

	completionID := openAICompletionID("stream-" + key.ID)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	created := time.Now().Unix()

	// First chunk announces the assistant role (OpenAI streaming convention).
	emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{Role: "assistant"}, "", nil)

	// toolCallsSeen flips to true the moment the provider forwards an
	// OnToolUse callback. We use it to set the OpenAI finish_reason to
	// "tool_calls" instead of "stop" on the terminal chunk — the SDKs
	// dedupe tool calls by id+function.name across the streaming chunks,
	// but they still expect the wire frame to declare finish_reason.
	toolCallsSeen := false

	// lastPartialArgsPerIndex tracks the previous cumulative
	// arguments string per tool call index. We need this
	// because the upstream Anthropic parser passes the full
	// reassembled JSON to OnToolUse on every callback, but
	// the OpenAI SDK expects per-chunk deltas that it
	// concatenates itself. The map is keyed on the
	// block.Index field the upstream surfaces; for OpenAI-
	// shape upstreams (which use 0-based indices that match
	// chat.completion.chunk tool_calls[].index) and for the
	// anthropic_compatible→OpenAI path, this is a faithful
	// cross-walk. We allocate lazily on first encounter.
	lastPartialArgsPerIndex := map[int]string{}

	resp, err := streamer.Stream(r.Context(), providerReq, conversation.StreamHandler{
		OnTextDelta: func(delta string) {
			if delta == "" {
				return
			}
			emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{Content: delta}, "", nil)
		},
		OnToolUse: func(block protocol.Block, partialJSON string) {
			toolCallsSeen = true
			// Compute the per-chunk arguments fragment to
			// forward. When the upstream is OpenAI-shape
			// (parseOpenAIStream), partialJSON is already
			// the per-chunk delta, so the diff is a no-op.
			// When the upstream is Anthropic-shape
			// (parseMessageStream), partialJSON is the
			// cumulative reassembled JSON; we must diff
			// against the last cumulative we saw to
			// recover the delta suffix.
			idx := block.Index
			prev, seen := lastPartialArgsPerIndex[idx]
			fragment := toolJSONDeltaSuffix(prev, partialJSON)
			// Suppress the trailing content_block_stop
			// callback when it carries the same cumulative
			// string we already forwarded. The OpenAI SDK
			// would concatenate it again and corrupt the
			// JSON; emitting nothing here is the same
			// behaviour the Anthropic streaming path uses.
			if seen && partialJSON == prev {
				return
			}
			lastPartialArgsPerIndex[idx] = partialJSON
			// Always forward at least the id+name on the
			// first emission for this index, even if the
			// fragment is empty. OpenAI SDKs use the
			// presence of an id+name to register the tool
			// call in the accumulator; emitting the
			// subsequent fragments without a name would
			// leave the call unnamed. The empty-fragment
			// case is the first emission (cumulative is
			// "" because the upstream just sent the
			// content_block_start frame with no
			// input_json_delta yet), so we send the full
			// block shape with arguments="".
			calls := []openAIToolCallWire{{
				Index: idx,
				ID:    block.ID,
				Type:  "function",
				Function: openAIFunctionCall{
					Name:      block.Name,
					Arguments: fragment,
				},
			}}
			emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{ToolCalls: calls}, "", nil)
		},
	})
	if err != nil {
		emitOpenAIError(w, flusher, err)
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "provider_error", err.Error())
		return
	}

	// Final chunk with finish_reason and the data: [DONE] sentinel. The
	// provider may surface stop_reason="tool_use" (Anthropic) or
	// "tool_calls" (OpenAI); both must be translated to the OpenAI SDK's
	// canonical "tool_calls" finish reason. We also fall back to
	// "tool_calls" when OnToolUse fired during the stream, even if the
	// provider never sent an explicit stop_reason (some streaming proxies
	// drop the terminal frame).
	//
	// IMPORTANT: if the upstream stop_reason was "length" (max_tokens hit),
	// we MUST NOT override it to "tool_calls" even if toolCallsSeen is
	// true. A truncated tool call with finish_reason="tool_calls" makes
	// the downstream client (Pi) believe the tool call is complete, so it
	// tries to execute the truncated arguments and fails with "parameter
	// error" or "source text not found". Preserving "length" lets Pi's
	// agent loop know the response was truncated and handle recovery.
	finishReason := firstNonEmptyString(respStopReason(resp), "stop")
	if finishReason == "tool_use" {
		finishReason = "tool_calls"
	} else if toolCallsSeen && finishReason != "length" {
		finishReason = "tool_calls"
	}
	// Build the usage payload for the final chunk. OpenAI-compatible
	// clients (including pi) read the usage from the last chunk before
	// [DONE] to compute context-usage percentages. Without this, they
	// see all-zero token counts and display 0% context usage forever.
	var usagePayload map[string]interface{}
	if resp != nil && resp.Usage != nil {
		// resp.Usage.InputTokens counts only UNCACHED input tokens (see
		// openAIUsageToProtocol); OpenAI clients expect prompt_tokens to
		// include the cached portion, so add it back on the wire.
		promptTokens := resp.Usage.InputTokens + resp.Usage.CacheReadTokens
		usagePayload = map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      promptTokens + resp.Usage.OutputTokens,
		}
		if resp.Usage.CacheReadTokens > 0 {
			usagePayload["prompt_tokens_details"] = map[string]interface{}{
				"cached_tokens": resp.Usage.CacheReadTokens,
			}
		}
	}
	emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{}, finishReason, usagePayload)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	// Record usage once the stream has completed; resp.Usage carries the
	// authoritative input/output/cache token counts from the provider.
	_ = profile // profile is referenced via providerReq already; silence unused warning on future edits
	call := usageGatewaySuccessCall(start, key.ID, req.Model, modelMapping, providerReq, resp)
	if err := usageService.RecordCall(call); err != nil {
		logger.Warnf("usage gateway: failed to record streamed call %s: %v", call.ID, err)
	}
}

// =============================================================================
// Anthropic Messages API (Usage Gateway)
// =============================================================================

// dispatchAnthropicMessages runs the LLM gateway dispatch for an already-
// authenticated POST /v1/messages request. It is shared between the proxy-
// key path (handleAnthropicGatewayMessages) and the web-token path
// (handleAnthropicWebTokenMessages) so both Pi and other Anthropic-SDK
// clients see the same wire format. The caller passes the resolved
// ProxyAPIKey (which may be a virtual web-token identity) and is
// responsible for the auth check.
func protocolToolUsesToOpenAIToolCalls(blocks []protocol.Block) []openAIToolCallWire {
	var calls []openAIToolCallWire
	callIdx := 0
	for _, block := range blocks {
		if block.Type != protocol.BlockToolUse {
			continue
		}
		args := ""
		if block.Input != nil {
			if raw, err := json.Marshal(block.Input); err == nil {
				args = string(raw)
			}
		}
		calls = append(calls, openAIToolCallWire{
			Index: callIdx,
			ID:    block.ID,
			Type:  "function",
			Function: openAIFunctionCall{
				Name:      block.Name,
				Arguments: args,
			},
		})
		callIdx++
	}
	return calls
}

// toolJSONDeltaSuffix returns the per-chunk partial_json fragment that
// must be forwarded to a downstream Anthropic SDK so the SDK's
// per-chunk concatenation recovers `next`. The semantics are:
//
//   - prev is empty: returns next (first emission).
//   - next is empty: returns "" (no new content; caller should skip).
//   - next is a strict extension of prev: returns the new suffix.
//   - next shrank or changed shape relative to prev: returns next
//     (best-effort resync; the SDK's accumulated args may become
//     invalid, but this is the only way to surface a mid-stream
//     shape change).
//
// Pi's streamAnthropic (anthropic.ts:612) calls parseStreamingJson on
// the concatenated partial_json, so resending the cumulative string
// on every callback would corrupt the JSON. The upstream
// parseMessageStream passes the CUMULATIVE partialJSON to OnToolUse;
// this helper translates it to the per-chunk suffix the wire expects.
func toolJSONDeltaSuffix(prev, next string) string {
	if next == "" {
		return ""
	}
	if prev == "" {
		return next
	}
	if strings.HasPrefix(next, prev) {
		return next[len(prev):]
	}
	return next
}
