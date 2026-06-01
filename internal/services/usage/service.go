package usage

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
)

// Service provides business logic for managing proxy keys, models, and usage recording.
type Service struct {
	store Store
}

// NewService creates a usage service backed by the given store.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// generateKey creates a cryptographically random key with the gdx_ prefix.
func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(b), nil
}

// maskKey returns a masked version like "gdx_ab12****" from a full key.
func maskKey(full string) string {
	if len(full) <= 8 {
		return full[:int(math.Min(float64(len(full)), 4))] + "****"
	}
	return full[:8] + "****"
}

// CreateKey generates a new proxy API key.
func (s *Service) CreateKey(req KeyCreateRequest) (*KeyCreateResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("key name is required")
	}
	if req.BudgetCredits < 0 {
		return nil, fmt.Errorf("budget_credits must be non-negative")
	}
	if req.WarningThreshold < 0 {
		return nil, fmt.Errorf("warning_threshold must be non-negative")
	}
	secret, err := generateKey()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	hash := sha256Hex(secret)
	key := &ProxyAPIKey{
		ID:               NewID("key"),
		Name:             strings.TrimSpace(req.Name),
		KeyHash:          hash,
		KeyPrefix:        maskKey(secret),
		Enabled:          true,
		BudgetCredits:    req.BudgetCredits,
		WarningThreshold: req.WarningThreshold,
		AllowedModels:    req.AllowedModels,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if key.AllowedModels == nil {
		key.AllowedModels = []string{}
	}

	if err := s.store.CreateKey(key); err != nil {
		return nil, err
	}

	return &KeyCreateResponse{
		Key:    *key,
		Secret: secret,
	}, nil
}

// ListKeys returns all proxy keys (safe view, no hash/secret).
func (s *Service) ListKeys() ([]ProxyAPIKey, error) {
	return s.store.ListKeys()
}

// ListKeysWithSystemEntries returns proxy keys plus virtual system entries for reporting filters.
func (s *Service) ListKeysWithSystemEntries() ([]ProxyAPIKey, error) {
	keys, err := s.ListKeys()
	if err != nil {
		return nil, err
	}
	keys = append(keys, systemUsageKeys()...)
	return keys, nil
}

// GetKey returns a single key by ID.
func (s *Service) GetKey(id string) (*ProxyAPIKey, error) {
	return s.store.GetKey(id)
}

// UpdateKey updates fields of an existing key.
func (s *Service) UpdateKey(id string, req KeyUpdateRequest) (*ProxyAPIKey, error) {
	key, err := s.store.GetKey(id)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			return nil, fmt.Errorf("key name is required")
		}
		key.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		key.Enabled = *req.Enabled
	}
	if req.BudgetCredits != nil {
		if *req.BudgetCredits < 0 {
			return nil, fmt.Errorf("budget_credits must be non-negative")
		}
		key.BudgetCredits = *req.BudgetCredits
	}
	if req.WarningThreshold != nil {
		if *req.WarningThreshold < 0 {
			return nil, fmt.Errorf("warning_threshold must be non-negative")
		}
		key.WarningThreshold = *req.WarningThreshold
	}
	if req.AllowedModels != nil {
		key.AllowedModels = *req.AllowedModels
	}
	key.UpdatedAt = time.Now()

	if err := s.store.UpdateKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

// AuthenticateKey verifies a presented proxy key and returns the matching key record.
func (s *Service) AuthenticateKey(secret string) (*ProxyAPIKey, error) {
	if !strings.HasPrefix(secret, KeyPrefix) {
		return nil, fmt.Errorf("invalid key format")
	}
	hash := sha256Hex(secret)
	key, err := s.store.GetKeyByHash(hash)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy key")
	}
	if !key.Enabled {
		return nil, fmt.Errorf("proxy key is disabled")
	}
	return key, nil
}

// CheckBudget returns true if the key still has remaining credits.
func (s *Service) CheckBudget(keyID string, estimatedCredits float64) (bool, error) {
	key, err := s.store.GetKey(keyID)
	if err != nil {
		return false, err
	}
	if key.BudgetCredits <= 0 {
		return true, nil // unlimited (0 = unlimited)
	}

	summaries, err := s.store.GetSummary("", keyID)
	if err != nil {
		return false, err
	}
	var used float64
	for _, s := range summaries {
		used += s.Credits
	}
	return (used + estimatedCredits) <= key.BudgetCredits, nil
}

// CreateModel creates a new model mapping.
func (s *Service) CreateModel(req ModelCreateRequest) (*ProxyModel, error) {
	publicModel := strings.TrimSpace(req.PublicModel)
	targetProfileID := strings.TrimSpace(req.TargetProfileID)
	targetModel := strings.TrimSpace(req.TargetModel)
	if publicModel == "" {
		return nil, fmt.Errorf("public_model is required")
	}
	if targetProfileID == "" {
		return nil, fmt.Errorf("target_profile_id is required")
	}
	if req.CreditWeight < 0 {
		return nil, fmt.Errorf("credit_weight must be positive")
	}
	if _, err := s.store.GetModelByPublicName(publicModel); err == nil {
		return nil, fmt.Errorf("public model already exists: %s", publicModel)
	}
	now := time.Now()
	m := &ProxyModel{
		ID:              NewID("model"),
		PublicModel:     publicModel,
		TargetProfileID: targetProfileID,
		TargetModel:     targetModel,
		CreditWeight:    req.CreditWeight,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if m.CreditWeight == 0 {
		m.CreditWeight = 1.0
	}
	if err := s.store.CreateModel(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ListModels returns all model mappings.
func (s *Service) ListModels() ([]ProxyModel, error) {
	return s.store.ListModels()
}

// GetModel returns a single model mapping by ID.
func (s *Service) GetModel(id string) (*ProxyModel, error) {
	return s.store.GetModel(id)
}

// UpdateModel updates an existing model mapping.
func (s *Service) UpdateModel(id string, req ModelUpdateRequest) (*ProxyModel, error) {
	m, err := s.store.GetModel(id)
	if err != nil {
		return nil, err
	}
	if req.PublicModel != nil {
		publicModel := strings.TrimSpace(*req.PublicModel)
		if publicModel == "" {
			return nil, fmt.Errorf("public_model is required")
		}
		if existing, err := s.store.GetModelByPublicName(publicModel); err == nil && existing.ID != m.ID {
			return nil, fmt.Errorf("public model already exists: %s", publicModel)
		}
		m.PublicModel = publicModel
	}
	if req.TargetProfileID != nil {
		if strings.TrimSpace(*req.TargetProfileID) == "" {
			return nil, fmt.Errorf("target_profile_id is required")
		}
		m.TargetProfileID = strings.TrimSpace(*req.TargetProfileID)
	}
	if req.TargetModel != nil {
		m.TargetModel = strings.TrimSpace(*req.TargetModel)
	}
	if req.CreditWeight != nil {
		if *req.CreditWeight <= 0 {
			return nil, fmt.Errorf("credit_weight must be positive")
		}
		m.CreditWeight = *req.CreditWeight
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	m.UpdatedAt = time.Now()
	if err := s.store.UpdateModel(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ResolveModel finds the model mapping for a public model name and checks it is enabled.
func (s *Service) ResolveModel(publicModel string) (*ProxyModel, error) {
	m, err := s.store.GetModelByPublicName(publicModel)
	if err != nil {
		return nil, fmt.Errorf("model mapping not found: %s", publicModel)
	}
	if !m.Enabled {
		return nil, fmt.Errorf("model mapping is disabled: %s", publicModel)
	}
	return m, nil
}

// RecordCall records a usage ledger entry.
func (s *Service) RecordCall(call *UsageCall) error {
	if call.ID == "" {
		call.ID = NewID("call")
	}
	if call.Timestamp.IsZero() {
		call.Timestamp = time.Now()
	}
	// Compute billable tokens and credits
	billable := call.InputTokens + call.OutputTokens + call.CacheWriteTokens
	billable += int(math.Ceil(float64(call.CacheReadTokens) * 0.25))
	call.BillableTokens = billable
	call.Credits = math.Round(float64(billable)*call.CreditWeight*1e6) / 1e6

	return s.store.RecordCall(call)
}

// GetCalls returns usage calls for the given date and optional key filter.
func (s *Service) GetCalls(date string, apiKeyID string) ([]UsageCall, error) {
	return s.store.GetCalls(date, apiKeyID)
}

// GetSummary returns aggregated usage summaries.
func (s *Service) GetSummary(rangeType, apiKeyID string) ([]UsageSummary, error) {
	return s.store.GetSummary(rangeType, apiKeyID)
}

// GetCacheStats returns cache performance statistics grouped by model and period.
func (s *Service) GetCacheStats(query CacheStatsQuery) ([]CacheStats, error) {
	return s.store.GetCacheStats(query)
}

// RecordLLMUsage records one observed model-provider call.
func (s *Service) RecordLLMUsage(event conversation.UsageEvent) error {
	ctx := event.Context
	apiKeyID := strings.TrimSpace(ctx.APIKeyID)
	if apiKeyID == "" {
		apiKeyID = SystemKeyID(ctx.SourceChannel)
	}
	call := &UsageCall{
		APIKeyID:        apiKeyID,
		PublicModel:     event.Request.Model,
		TargetProfileID: ctx.TargetProfileID,
		TargetModel:     firstNonEmpty(ctx.TargetModel, event.Request.Model),
		CreditWeight:    ctx.CreditWeight,
		LatencyMs:       event.Latency.Milliseconds(),
		SourceChannel:   ctx.SourceChannel,
		SessionID:       ctx.SessionID,
		TurnID:          ctx.TurnID,
		JobID:           ctx.JobID,
		Status:          "success",
	}
	if call.CreditWeight <= 0 {
		call.CreditWeight = 1
	}
	if event.Error != nil {
		call.Status = "error"
		call.Error = event.Error.Error()
		call.ErrorCode = "provider_error"
	}
	applyUsage(call, event.Request, event.Response)
	return s.RecordCall(call)
}

func SystemKeyID(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "unknown"
	}
	return "system:" + source
}

func systemUsageKeys() []ProxyAPIKey {
	sources := []string{"web", "tui", "acp", "cli", "cron", "heartbeat", "openai_api", "weixin", "feishu", "unknown"}
	keys := make([]ProxyAPIKey, 0, len(sources))
	for _, source := range sources {
		id := SystemKeyID(source)
		keys = append(keys, ProxyAPIKey{
			ID:        id,
			Name:      "System " + source,
			KeyPrefix: "system",
			Enabled:   true,
		})
	}
	return keys
}

func applyUsage(call *UsageCall, req protocol.Request, resp *protocol.Response) {
	if resp != nil && resp.Usage != nil {
		call.InputTokens = resp.Usage.InputTokens
		call.OutputTokens = resp.Usage.OutputTokens
		call.CacheReadTokens = resp.Usage.CacheReadTokens
		call.CacheWriteTokens = resp.Usage.CacheWriteTokens
		call.Estimated = resp.Usage.Estimated
		return
	}
	call.InputTokens = estimateRequestTokens(req)
	if resp != nil {
		call.OutputTokens = compress.CountTokens(protocol.BlocksText(resp.Content))
	}
	call.Estimated = true
}

func estimateRequestTokens(req protocol.Request) int {
	total := compress.CountTokens(req.System)
	for _, msg := range req.Messages {
		total += compress.CountTokens(protocol.BlocksText(msg.Content))
	}
	if total <= 0 {
		return 1
	}
	return total
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
