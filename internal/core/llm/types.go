package llm

import (
	"fmt"
	"sort"
	"strings"
)

const (
	ProviderAnthropicCompatible = "anthropic_compatible"
	ProviderOpenAICompatible    = "openai_compatible"
	ProviderOpenAICodex         = "openai_codex"

	StrategyPrimary    = "primary"
	StrategyFallback   = "fallback"
	StrategyRoundRobin = "round_robin"
)

// ProviderConfig describes one LLM supplier account/endpoint.
type ProviderConfig struct {
	ID             string                 `yaml:"-" json:"id,omitempty"`
	Name           string                 `yaml:"name" json:"name,omitempty"`
	Type           string                 `yaml:"type" json:"type"`
	BaseURL        string                 `yaml:"base_url" json:"base_url,omitempty"`
	APIKey         string                 `yaml:"api_key" json:"api_key,omitempty"`
	APIKeyEnv      string                 `yaml:"api_key_env" json:"api_key_env,omitempty"`
	CredentialKind string                 `yaml:"credential_kind" json:"credential_kind,omitempty"`
	OAuth          OAuthConfig            `yaml:"oauth" json:"oauth,omitempty"`
	TimeoutSeconds int                    `yaml:"timeout_seconds" json:"timeout_seconds,omitempty"`
	Models         map[string]ModelConfig `yaml:"models" json:"models,omitempty"`
}

type OAuthConfig struct {
	Provider  string `yaml:"provider,omitempty" json:"provider,omitempty"`
	AccountID string `yaml:"account_id,omitempty" json:"account_id,omitempty"`
	Mode      string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// ModelConfig describes one model exposed by a provider.
type ModelConfig struct {
	ID                  string   `yaml:"-" json:"id,omitempty"`
	Name                string   `yaml:"name" json:"name,omitempty"`
	Model               string   `yaml:"model" json:"model"`
	MaxTokens           int      `yaml:"max_tokens" json:"max_tokens,omitempty"`
	ContextWindowTokens int      `yaml:"context_window_tokens" json:"context_window_tokens,omitempty"`
	SupportsStreaming   bool     `yaml:"supports_streaming" json:"supports_streaming,omitempty"`
	SupportsVision      bool     `yaml:"supports_vision" json:"supports_vision,omitempty"`
	ReasoningEffort     string   `yaml:"reasoning_effort" json:"reasoning_effort,omitempty"`
	Tags                []string `yaml:"tags" json:"tags,omitempty"`
}

// ModelRef points at a provider model by stable ids.
type ModelRef struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`
}

// StrategyConfig controls how candidates are selected at call time.
type StrategyConfig struct {
	Type       string     `yaml:"type" json:"type"`
	Candidates []ModelRef `yaml:"candidates" json:"candidates,omitempty"`
}

// Candidate is a fully resolved provider+model invocation target.
type Candidate struct {
	ProfileID           string
	ProviderID          string
	ProviderName        string
	ProviderType        string
	ModelID             string
	ModelName           string
	Model               string
	BaseURL             string
	APIKey              string
	MaxTokens           int
	ContextWindowTokens int
	TimeoutSeconds      int
	SupportsStreaming   bool
	SupportsVision      bool
	ReasoningEffort     string
	Tags                []string
}

// Registry resolves configured providers and model references.
type Registry struct {
	providers map[string]ProviderConfig
	strategy  StrategyConfig
}

func NewRegistry(providers map[string]ProviderConfig, strategy StrategyConfig) Registry {
	normalized := make(map[string]ProviderConfig, len(providers))
	for id, provider := range providers {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		normalized[id] = NormalizeProvider(id, provider)
	}
	return Registry{
		providers: normalized,
		strategy:  NormalizeStrategy(strategy),
	}
}

func NormalizeProvider(id string, provider ProviderConfig) ProviderConfig {
	provider.ID = strings.TrimSpace(firstNonEmpty(provider.ID, id))
	provider.Name = strings.TrimSpace(provider.Name)
	if provider.Name == "" {
		provider.Name = provider.ID
	}
	provider.Type = NormalizeProviderType(provider.Type)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	provider.APIKey = strings.TrimSpace(provider.APIKey)
	provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
	provider.CredentialKind = strings.TrimSpace(provider.CredentialKind)
	provider.OAuth.Provider = strings.TrimSpace(provider.OAuth.Provider)
	provider.OAuth.AccountID = strings.TrimSpace(provider.OAuth.AccountID)
	provider.OAuth.Mode = strings.TrimSpace(provider.OAuth.Mode)
	if provider.TimeoutSeconds <= 0 {
		provider.TimeoutSeconds = 600
	}
	models := make(map[string]ModelConfig, len(provider.Models))
	for modelID, model := range provider.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		models[modelID] = NormalizeModel(modelID, model)
	}
	provider.Models = models
	return provider
}

func NormalizeModel(id string, model ModelConfig) ModelConfig {
	model.ID = strings.TrimSpace(firstNonEmpty(model.ID, id))
	model.Name = strings.TrimSpace(model.Name)
	if model.Name == "" {
		model.Name = model.ID
	}
	model.Model = strings.TrimSpace(model.Model)
	if model.Model == "" {
		model.Model = model.ID
	}
	if model.MaxTokens <= 0 {
		model.MaxTokens = 4096
	}
	if !model.SupportsStreaming {
		model.SupportsStreaming = true
	}
	if !model.SupportsVision {
		model.SupportsVision = likelyVisionModel(model.Model)
	}
	model.ReasoningEffort = NormalizeReasoningEffort(model.ReasoningEffort)
	model.Tags = uniqueNonEmpty(model.Tags)
	return model
}

func NormalizeReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func NormalizeStrategy(strategy StrategyConfig) StrategyConfig {
	kind := strings.ToLower(strings.TrimSpace(strategy.Type))
	switch kind {
	case "", StrategyFallback:
		kind = StrategyFallback
	case StrategyPrimary, StrategyRoundRobin:
	default:
		kind = StrategyFallback
	}
	out := StrategyConfig{Type: kind}
	seen := map[string]struct{}{}
	for _, ref := range strategy.Candidates {
		ref.Provider = strings.TrimSpace(ref.Provider)
		ref.Model = strings.TrimSpace(ref.Model)
		if ref.Provider == "" || ref.Model == "" {
			continue
		}
		key := ProfileID(ref.Provider, ref.Model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out.Candidates = append(out.Candidates, ref)
	}
	return out
}

func NormalizeProviderType(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "", "anthropic", ProviderAnthropicCompatible:
		return ProviderAnthropicCompatible
	case "openai", ProviderOpenAICompatible:
		return ProviderOpenAICompatible
	case ProviderOpenAICodex:
		return ProviderOpenAICodex
	default:
		return strings.ToLower(strings.TrimSpace(providerType))
	}
}

func ProfileID(providerID, modelID string) string {
	return strings.TrimSpace(providerID) + "." + strings.TrimSpace(modelID)
}

func ParseProfileID(profileID string) (ModelRef, bool) {
	left, right, ok := strings.Cut(strings.TrimSpace(profileID), ".")
	if !ok || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return ModelRef{}, false
	}
	return ModelRef{Provider: strings.TrimSpace(left), Model: strings.TrimSpace(right)}, true
}

func (r Registry) Providers() map[string]ProviderConfig {
	out := make(map[string]ProviderConfig, len(r.providers))
	for id, provider := range r.providers {
		provider.Models = cloneModels(provider.Models)
		out[id] = provider
	}
	return out
}

func (r Registry) Strategy() StrategyConfig {
	return NormalizeStrategy(r.strategy)
}

func (r Registry) Candidate(ref ModelRef) (Candidate, bool) {
	provider, ok := r.providers[strings.TrimSpace(ref.Provider)]
	if !ok {
		return Candidate{}, false
	}
	modelID := strings.TrimSpace(ref.Model)
	model, ok := provider.Models[modelID]
	if !ok {
		return Candidate{}, false
	}
	return Candidate{
		ProfileID:           ProfileID(provider.ID, model.ID),
		ProviderID:          provider.ID,
		ProviderName:        provider.Name,
		ProviderType:        provider.Type,
		ModelID:             model.ID,
		ModelName:           model.Name,
		Model:               model.Model,
		BaseURL:             provider.BaseURL,
		APIKey:              provider.APIKey,
		MaxTokens:           model.MaxTokens,
		ContextWindowTokens: model.ContextWindowTokens,
		TimeoutSeconds:      provider.TimeoutSeconds,
		SupportsStreaming:   model.SupportsStreaming,
		SupportsVision:      model.SupportsVision,
		ReasoningEffort:     model.ReasoningEffort,
		Tags:                append([]string{}, model.Tags...),
	}, true
}

func (r Registry) Candidates(refs []ModelRef) []Candidate {
	out := make([]Candidate, 0, len(refs))
	seen := map[string]struct{}{}
	for _, ref := range refs {
		candidate, ok := r.Candidate(ref)
		if !ok {
			continue
		}
		if _, ok := seen[candidate.ProfileID]; ok {
			continue
		}
		seen[candidate.ProfileID] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func (r Registry) AllCandidates() []Candidate {
	providerIDs := make([]string, 0, len(r.providers))
	for id := range r.providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	out := make([]Candidate, 0)
	for _, providerID := range providerIDs {
		modelIDs := make([]string, 0, len(r.providers[providerID].Models))
		for id := range r.providers[providerID].Models {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			if candidate, ok := r.Candidate(ModelRef{Provider: providerID, Model: modelID}); ok {
				out = append(out, candidate)
			}
		}
	}
	return out
}

func (r Registry) StrategyCandidates(primary string) []Candidate {
	refs := make([]ModelRef, 0, len(r.strategy.Candidates)+1)
	if ref, ok := ParseProfileID(primary); ok {
		refs = append(refs, ref)
	}
	refs = append(refs, r.strategy.Candidates...)
	candidates := r.Candidates(refs)
	if len(candidates) == 0 {
		return r.AllCandidates()
	}
	return candidates
}

func (r Registry) Validate() error {
	if len(r.providers) == 0 {
		return fmt.Errorf("llm registry has no providers")
	}
	for id, provider := range r.providers {
		if strings.TrimSpace(provider.Type) == "" {
			return fmt.Errorf("llm provider %q has no type", id)
		}
		if len(provider.Models) == 0 {
			return fmt.Errorf("llm provider %q has no models", id)
		}
	}
	return nil
}

func cloneModels(in map[string]ModelConfig) map[string]ModelConfig {
	out := make(map[string]ModelConfig, len(in))
	for id, model := range in {
		model.Tags = append([]string{}, model.Tags...)
		out[id] = model
	}
	return out
}

func uniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func likelyVisionModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, token := range []string{"vision", "kimi-k2.5", "gpt-4.1", "gpt-4o", "claude-3", "claude-sonnet", "gemini"} {
		if strings.Contains(model, token) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
