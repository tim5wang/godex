package usage

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/protocol"
)

// Service provides business logic for managing proxy keys, models, and usage recording.
type Service struct {
	store Store

	// bizPinAttempts tracks consecutive wrong-pin attempts per key id so a
	// shared-screen viewer can't brute-force the reveal endpoint. It resets on
	// a correct pin or when the server restarts. ponytail: in-memory only;
	// per-key persistent throttling if this ever runs multi-instance.
	bizPinAttempts map[string]int
	bizPinMu       sync.Mutex
}

// NewService creates a usage service backed by the given store.
func NewService(store Store) *Service {
	return &Service{store: store, bizPinAttempts: map[string]int{}}
}

// generateKey creates a cryptographically random key with the gdx_ prefix.
func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(b), nil
}

// generateBizKey returns a new random secret with the biz_ prefix.
func generateBizKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate biz key: %w", err)
	}
	return BizKeyPrefix + hex.EncodeToString(b), nil
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

// ResetKey rotates the secret for an existing key. The previous secret stops
// authenticating immediately; the new plaintext secret is returned exactly
// once, in the same shape as CreateKey, and is not stored on the key.
func (s *Service) ResetKey(id string) (*KeyCreateResponse, error) {
	key, err := s.store.GetKey(id)
	if err != nil {
		return nil, err
	}
	secret, err := generateKey()
	if err != nil {
		return nil, err
	}
	key.KeyHash = sha256Hex(secret)
	key.KeyPrefix = maskKey(secret)
	key.UpdatedAt = time.Now()
	if err := s.store.UpdateKey(key); err != nil {
		return nil, err
	}
	return &KeyCreateResponse{
		Key:    *key,
		Secret: secret,
	}, nil
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

// ---- biz keys (Agent Step Platform) ----

// CreateBizKey generates a new business-system API key bound to an MCP server
// allowlist, recall providers, sandbox tools and models. The plaintext secret
// is returned exactly once; only its hash is stored.
func (s *Service) CreateBizKey(req BizKeyCreateRequest) (*BizKeyCreateResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("biz key name is required")
	}
	if err := validateBizPin(req.Pin); err != nil {
		return nil, err
	}
	if req.BudgetCredits < 0 {
		return nil, fmt.Errorf("budget_credits must be non-negative")
	}
	if req.WarningThreshold < 0 {
		return nil, fmt.Errorf("warning_threshold must be non-negative")
	}
	secret, err := generateBizKey()
	if err != nil {
		return nil, err
	}
	encrypted, err := s.store.EncryptBizSecret(secret)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	key := &BizAPIKey{
		ID:               NewID("biz"),
		Name:             strings.TrimSpace(req.Name),
		Description:      req.Description,
		DefaultPrompt:    req.DefaultPrompt,
		KeyHash:          sha256Hex(secret),
		KeyPrefix:        maskKey(secret),
		Enabled:          true,
		MCPServers:       req.MCPServers,
		Providers:        req.Providers,
		SandboxTools:     req.SandboxTools,
		Skills:           req.Skills,
		Packages:         req.Packages,
		Models:           req.Models,
		ProjectDir:       req.ProjectDir,
		BudgetCredits:    req.BudgetCredits,
		WarningThreshold: req.WarningThreshold,
		SecretEncrypted:  encrypted,
		PinHash:          s.store.HashBizPin(req.Pin),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if key.MCPServers == nil {
		key.MCPServers = []string{}
	}
	if key.Providers == nil {
		key.Providers = []ProviderRef{}
	}
	if key.SandboxTools == nil {
		key.SandboxTools = []string{}
	}
	if key.Skills == nil {
		key.Skills = []string{}
	}
	if key.Packages == nil {
		key.Packages = []string{}
	}
	if key.Models == nil {
		key.Models = []string{}
	}

	if err := s.store.CreateBizKey(key); err != nil {
		return nil, err
	}
	return &BizKeyCreateResponse{Key: *key, Secret: secret}, nil
}

// ListBizKeys returns all business keys (safe view, no hash/secret).
func (s *Service) ListBizKeys() ([]BizAPIKey, error) {
	return s.store.ListBizKeys()
}

// GetBizKey returns one business key by id (safe view, no hash).
func (s *Service) GetBizKey(id string) (*BizAPIKey, error) {
	return s.store.GetBizKey(id)
}

// UpdateBizKey updates fields of an existing business key.
func (s *Service) UpdateBizKey(id string, req BizKeyUpdateRequest) (*BizAPIKey, error) {
	key, err := s.store.GetBizKey(id)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			return nil, fmt.Errorf("biz key name is required")
		}
		key.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		key.Description = *req.Description
	}
	if req.DefaultPrompt != nil {
		key.DefaultPrompt = *req.DefaultPrompt
	}
	if req.Enabled != nil {
		key.Enabled = *req.Enabled
	}
	if req.MCPServers != nil {
		key.MCPServers = *req.MCPServers
	}
	if req.Providers != nil {
		key.Providers = *req.Providers
	}
	if req.SandboxTools != nil {
		key.SandboxTools = *req.SandboxTools
	}
	if req.Skills != nil {
		key.Skills = *req.Skills
	}
	if req.Packages != nil {
		key.Packages = *req.Packages
	}
	if req.Models != nil {
		key.Models = *req.Models
	}
	if req.ProjectDir != nil {
		key.ProjectDir = *req.ProjectDir
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
	if req.Pin != nil {
		if err := validateBizPin(*req.Pin); err != nil {
			return nil, err
		}
		key.PinHash = s.store.HashBizPin(*req.Pin)
	}
	key.UpdatedAt = time.Now()
	if err := s.store.UpdateBizKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

// DeleteBizKey removes a business key.
func (s *Service) DeleteBizKey(id string) error {
	return s.store.DeleteBizKey(id)
}

// ResetBizKey rotates the business key secret and returns the new plaintext
// exactly once (mirrors ResetKey). The previous secret stops authenticating
// immediately; the new secret is not stored on the key, so callers must copy
// it now or rotate again.
func (s *Service) ResetBizKey(id string) (*BizKeyCreateResponse, error) {
	key, err := s.store.GetBizKey(id)
	if err != nil {
		return nil, err
	}
	secret, err := generateBizKey()
	if err != nil {
		return nil, err
	}
	encrypted, err := s.store.EncryptBizSecret(secret)
	if err != nil {
		return nil, err
	}
	key.KeyHash = sha256Hex(secret)
	key.KeyPrefix = maskKey(secret)
	key.SecretEncrypted = encrypted
	key.UpdatedAt = time.Now()
	if err := s.store.UpdateBizKey(key); err != nil {
		return nil, err
	}
	return &BizKeyCreateResponse{
		Key:    *key,
		Secret: secret,
	}, nil
}

// MaxBizPinAttempts bounds wrong-pin tries before a key's reveal locks for the
// process lifetime (per design: 5 consecutive misses → locked until reset).
const MaxBizPinAttempts = 5

// RevealBizKey returns the plaintext secret after verifying the pin, and the
// masked prefix for reference. Wrong pins count toward a per-key lockout.
func (s *Service) RevealBizKey(id string, req BizKeyRevealRequest) (*BizKeyCreateResponse, error) {
	key, err := s.store.GetBizKey(id)
	if err != nil {
		return nil, err
	}
	if key.SecretEncrypted == "" {
		return nil, fmt.Errorf("biz key has no retrievable secret")
	}
	if !s.bizPinLocked(id) {
		if s.store.VerifyBizPin(req.Pin, key.PinHash) {
			s.bizPinMu.Lock()
			delete(s.bizPinAttempts, id)
			s.bizPinMu.Unlock()
			plain, err := s.store.DecryptBizSecret(key.SecretEncrypted)
			if err != nil {
				return nil, fmt.Errorf("decrypt secret: %w", err)
			}
			return &BizKeyCreateResponse{Key: *key, Secret: plain}, nil
		}
		s.bizPinMu.Lock()
		s.bizPinAttempts[id]++
		remaining := MaxBizPinAttempts - s.bizPinAttempts[id]
		s.bizPinMu.Unlock()
		if remaining <= 0 {
			return nil, fmt.Errorf("pin attempts exhausted; reset the key to recover")
		}
		return nil, fmt.Errorf("invalid pin (%d attempt(s) left)", remaining)
	}
	return nil, fmt.Errorf("pin locked after repeated failures; reset the key to recover")
}

// bizPinLocked reports whether a key has exhausted its wrong-pin budget.
func (s *Service) bizPinLocked(id string) bool {
	s.bizPinMu.Lock()
	defer s.bizPinMu.Unlock()
	return s.bizPinAttempts[id] >= MaxBizPinAttempts
}

// validateBizPin enforces the 6-digit numeric pin policy.
func validateBizPin(pin string) error {
	if len(pin) != 6 {
		return fmt.Errorf("pin must be 6 digits")
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return fmt.Errorf("pin must be 6 digits")
		}
	}
	return nil
}

// AuthenticateBizKey verifies a presented business key and returns its record.
func (s *Service) AuthenticateBizKey(secret string) (*BizAPIKey, error) {
	if !strings.HasPrefix(secret, BizKeyPrefix) {
		return nil, fmt.Errorf("invalid biz key format")
	}
	key, err := s.store.GetBizKeyByHash(sha256Hex(secret))
	if err != nil {
		return nil, fmt.Errorf("invalid biz key")
	}
	if !key.Enabled {
		return nil, fmt.Errorf("biz key is disabled")
	}
	return key, nil
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

// GetTimeSeries returns time-bucketed usage data for trend charts.
func (s *Service) GetTimeSeries(query TimeSeriesQuery) ([]TimeSeriesPoint, error) {
	return s.store.GetTimeSeries(query)
}

// GetSessionUsage returns aggregated usage for a single session.
func (s *Service) GetSessionUsage(sessionID string) (*SessionUsageSummary, error) {
	return s.store.GetSessionUsage(sessionID)
}

// ListSessions returns a paginated list of session usage summaries.
func (s *Service) ListSessions(apiKeyID string, limit, offset int) ([]SessionUsageSummary, error) {
	return s.store.ListSessions(apiKeyID, limit, offset)
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
