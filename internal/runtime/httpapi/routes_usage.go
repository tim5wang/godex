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
		text := strings.TrimSpace(openAIContentText(msg.Content))
		if text == "" {
			continue
		}
		if role == "system" || role == "developer" {
			systemParts = append(systemParts, text)
			continue
		}
		protoRole := role
		switch protoRole {
		case protocol.RoleAssistant, protocol.RoleUser:
		default:
			protoRole = protocol.RoleUser
		}
		messages = append(messages, protocol.APIMessage{Role: protoRole, Content: []protocol.Block{protocol.TextBlock(text)}})
	}
	if len(messages) == 0 {
		return protocol.Request{}, fmt.Errorf("at least one non-empty user or assistant message is required")
	}
	return protocol.Request{MaxTokens: req.MaxTokens, System: strings.Join(systemParts, "\n\n"), Messages: messages}, nil
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

	resp, err := streamer.Stream(r.Context(), providerReq, conversation.StreamHandler{
		OnTextDelta: func(delta string) {
			if delta == "" {
				return
			}
			emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{Content: delta}, "")
		},
	})
	if err != nil {
		emitOpenAIError(w, flusher, err)
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "provider_error", err.Error())
		return
	}

	// Final chunk with finish_reason=stop and the data: [DONE] sentinel.
	emitOpenAIChunk(w, flusher, completionID, req.Model, created, openAIChatMessage{}, firstNonEmptyString(respStopReason(resp), "stop"))
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

// handleAnthropicGatewayMessages handles POST /v1/messages for the usage gateway.
// It accepts Anthropic's native request format and routes to the appropriate provider.
func handleAnthropicGatewayMessages(w http.ResponseWriter, r *http.Request, usageService *usage.Service, manager *config.Manager, secret string) {
	start := time.Now()

	// secret is extracted by extractProxyKeySecret at the routing layer so
	// the gdx_ prefix can be matched from either Authorization: Bearer or
	// the Anthropic SDK's x-api-key header.

	key, err := usageService.AuthenticateKey(secret)
	if err != nil {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid API Key.")
		return
	}

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
			writeAnthropicError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("Model '%s' is not allowed for this API key.", req.Model))
			return
		}
	}

	// Resolve model mapping
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

	// Check budget
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
							Type:       block.Source.Type,
							MediaType:  block.Source.MediaType,
							Data:       block.Source.Data,
						},
					})
				}
			case "tool_use":
				protoMsg.Content = append(protoMsg.Content, protocol.Block{
					Type:      protocol.BlockToolUse,
					ID:        block.ID,
					Name:      block.Name,
					Input:     block.Input,
				})
			case "tool_result":
				protoMsg.Content = append(protoMsg.Content, protocol.Block{
					Type:      protocol.BlockToolResult,
					ToolUseID: block.ToolUseID,
					Content:   block.Text, // Anthropic uses 'content' field for tool result
				})
			}
		}
		if len(protoMsg.Content) == 0 {
			continue
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

	// Handle thinking config (for providers that support it)
	if req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		protoReq.ReasoningEffort = fmt.Sprintf("%d", req.Thinking.BudgetTokens)
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
		// Convert content blocks
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

	// Verify client accepts event-stream
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	if accept != "" && !strings.Contains(accept, "text/event-stream") {
		// Anthropic clients typically send Accept: text/event-stream, but we'll be lenient
	}

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
			"id":         fmt.Sprintf("msg_%d", start.UnixNano()),
			"type":       "message",
			"role":       "assistant",
			"content":    []interface{}{},
			"model":      req.Model,
			"stop_reason": nil,
			"usage": map[string]interface{}{
				"input_tokens":                0,
				"output_tokens":               0,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			},
		},
	})

	// 2) content_block_start — must precede the first text_delta so consumers
	// can create a content block to associate subsequent deltas with. Without
	// it, Pi's `output.content` stays empty and the deltas are dropped
	// (anthropic.ts:540-547 + 582-594).
	const textBlockIndex = 0
	blockStarted := false
	toolBlockStarted := false
	lastToolPartialJSON := ""
	flushBlockStart := func() {
		if blockStarted {
			return
		}
		blockStarted = true
		flushSSE("content_block_start", map[string]interface{}{
			"type":          "content_block_start",
			"index":         textBlockIndex,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		})
	}
	flushBlockStop := func() {
		if !blockStarted {
			return
		}
		blockStarted = false
		flushSSE("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		})
	}

	// Stream the upstream response. Client.streamOnce calls OnTextDelta for
	// every text_delta SSE event and returns a final *protocol.Response with
	// the consolidated content + usage. Client.parseMessageStream calls
	// OnToolUse three times per tool_use block (content_block_start,
	// each input_json_delta, content_block_stop) so the gateway can
	// forward Anthropic-SSE tool_use frames to the wire and Pi's
	// anthropic.ts:568-660 toolcall_* paths can execute the tool.
	finalResp, streamErr := streamer.Stream(r.Context(), providerReq, conversation.StreamHandler{
		OnTextDelta: func(delta string) {
			if delta == "" {
				return
			}
			// 3) content_block_start + content_block_delta — first delta
			// opens the text block; every delta appends to it.
			flushBlockStart()
			flushSSE("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": textBlockIndex,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": delta,
				},
			})
		},
		OnToolUse: func(block protocol.Block, partialJSON string) {
			// 3') tool_use block: open a content_block_start on the first
			// callback (id+name+empty input), then forward each
			// input_json_delta fragment, then content_block_stop on the
			// final callback. The three phases share a per-block "started"
			// flag so we don't repeat the start event on every delta.
			// We also track lastToolPartialJSON so the trailing
			// content_block_stop callback (which re-delivers the full
			// accumulated partialJSON) doesn't double-emit a delta.
			toolBlockIndex := textBlockIndex
			if block.Type != protocol.BlockToolUse {
				return
			}
			if !toolBlockStarted {
				toolBlockStarted = true
				inputMap := block.Input
				if inputMap == nil {
					inputMap = map[string]interface{}{}
				}
				flushSSE("content_block_start", map[string]interface{}{
					"type":          "content_block_start",
					"index":         toolBlockIndex,
					"content_block": map[string]interface{}{"type": "tool_use", "id": block.ID, "name": block.Name, "input": inputMap},
				})
				if fragment := strings.TrimSpace(partialJSON); fragment != "" && fragment != lastToolPartialJSON {
					lastToolPartialJSON = fragment
					flushSSE("content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": toolBlockIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": fragment,
						},
					})
				}
				return
			}
			if partialJSON != lastToolPartialJSON {
				lastToolPartialJSON = partialJSON
				flushSSE("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": toolBlockIndex,
					"delta": map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": partialJSON,
					},
				})
			}
		},
	})
	// Close the open text block whether or not the upstream returned deltas;
	// consumers expect a content_block_stop per started block.
	flushBlockStop()
	if toolBlockStarted {
		flushSSE("content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		})
		toolBlockStarted = false
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
