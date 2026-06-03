package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
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

	if req.Stream {
		recordUsageGatewayError(usageService, start, key.ID, req.Model, modelMapping, "unsupported_streaming", "streaming is not supported by usage gateway yet")
		writeUsageGatewayError(w, http.StatusBadRequest, "unsupported_streaming", "Streaming is not supported by Usage Gateway yet.")
		return
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
	return strings.TrimSpace(resp.StopReason)
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
