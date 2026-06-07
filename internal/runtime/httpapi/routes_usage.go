package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
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
	if resp != nil {
		content = strings.TrimSpace(protocol.BlocksText(resp.Content))
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
				Role:    "assistant",
				Content: content,
			},
			FinishReason: firstNonEmptyString(respStopReason(resp), "stop"),
		}},
		Usage: map[string]interface{}{
			"prompt_tokens":     call.InputTokens,
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
	emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{Role: "assistant"}, "")

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
			emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{Content: delta}, "")
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
			emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{ToolCalls: calls}, "")
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
	finishReason := firstNonEmptyString(respStopReason(resp), "stop")
	if finishReason == "tool_use" || toolCallsSeen {
		finishReason = "tool_calls"
	}
	emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{}, finishReason)
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

	// Check budget. The virtual web-token identity (`ID == "system:web_token"`)
	// is not stored in the usage store, so a budget lookup would
	// 500. We treat it as an admin override with unlimited budget
	// and skip the check entirely. Real proxy keys still go through
	// the normal path so the dashboard can enforce per-key budgets.
	if !strings.HasPrefix(key.ID, "system:") {
		if ok, err := usageService.CheckBudget(key.ID, float64(usageGatewayEstimateInputTokens(
			protocol.Request{Model: providerModel, Messages: []protocol.APIMessage{{Role: "user", Content: []protocol.Block{{Type: "text", Text: "test"}}}}},
		))*modelMapping.CreditWeight); err != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "budget_check_failed", "Failed to check usage budget.")
			return
		} else if !ok {
			recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "budget_exceeded", "Usage budget exceeded.")
			writeAnthropicError(w, http.StatusPaymentRequired, "budget_exceeded", "Usage budget exceeded.")
			return
		}
	}

	// Convert Anthropic request to internal protocol
	providerReq, err := anthropicToProtocolRequest(req, providerModel)
	if err != nil {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "invalid_request", err.Error())
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
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

		// Set stop reason
		switch resp.StopReason {
		case "end_turn", "stop_sequence", "stop", "":
			anthropicResp.StopReason = "end_turn"
		case "max_tokens":
			anthropicResp.StopReason = "max_tokens"
		case "tool_use", "tool_calls":
			anthropicResp.StopReason = "tool_use"
		case "pause_turn", "refusal":
			// Pi's mapStopReason at anthropic.ts:1210-1230 maps
			// these to local stop reasons. Forward them as-is
			// so the SDK can do the translation; the
			// chat.completion path's respStopReason helper
			// already handles the cross-protocol mapping.
			anthropicResp.StopReason = resp.StopReason
		default:
			anthropicResp.StopReason = resp.StopReason
		}

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

	// 1) message_start — must be the first event on the wire so consumers
	// (e.g. the Pi Anthropic SDK at anthropic.ts:528-539) can populate
	// output.responseId and the initial usage snapshot.
	flushSSE("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":          fmt.Sprintf("msg_%d", start.UnixNano()),
			"type":        "message",
			"role":        "assistant",
			"content":     []interface{}{},
			"model":       req.Model,
			"stop_reason": nil,
			"usage": map[string]interface{}{
				"input_tokens":                0,
				"output_tokens":               0,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			},
		},
	})

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
		OnTextDelta: func(delta string) {
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
		stopReason := "end_turn"
		if finalResp != nil && strings.TrimSpace(finalResp.StopReason) != "" {
			stopReason = finalResp.StopReason
		}
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

// writeAnthropicError writes an Anthropic-format error response.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Anthropic uses a specific error format
	errorResp := map[string]interface{}{
		"type":    "error",
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
