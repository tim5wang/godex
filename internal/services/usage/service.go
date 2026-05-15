package usage

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
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
	secret, err := generateKey()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	hash := sha256Hex(secret)
	key := &ProxyAPIKey{
		ID:               NewID("key"),
		Name:             req.Name,
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
		key.Name = *req.Name
	}
	if req.Enabled != nil {
		key.Enabled = *req.Enabled
	}
	if req.BudgetCredits != nil {
		key.BudgetCredits = *req.BudgetCredits
	}
	if req.WarningThreshold != nil {
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
	now := time.Now()
	m := &ProxyModel{
		ID:              NewID("model"),
		PublicModel:     req.PublicModel,
		TargetProfileID: req.TargetProfileID,
		TargetModel:     req.TargetModel,
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
		m.PublicModel = *req.PublicModel
	}
	if req.TargetProfileID != nil {
		m.TargetProfileID = *req.TargetProfileID
	}
	if req.TargetModel != nil {
		m.TargetModel = *req.TargetModel
	}
	if req.CreditWeight != nil {
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
