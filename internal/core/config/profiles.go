package config

import (
	"fmt"
	"github.com/tim5wang/godex/internal/core/llm"
	"gopkg.in/yaml.v3"
	"sort"
	"strings"
)

func modelProfilesFromConfigFile(file ConfigFile) map[string]ModelProfileConfig {
	return modelProfilesFromProviders(file, llmProvidersFromConfigFile(file))
}

func modelProfilesFromProviders(file ConfigFile, providers map[string]llm.ProviderConfig) map[string]ModelProfileConfig {
	registry := llm.NewRegistry(providers, file.API.ModelStrategy)
	candidates := registry.AllCandidates()
	profiles := make(map[string]ModelProfileConfig, len(candidates))
	for _, candidate := range candidates {
		profiles[candidate.ProfileID] = modelProfileFromLLMCandidate(candidate)
	}
	return profiles
}

func llmProvidersFromConfigFile(file ConfigFile) map[string]llm.ProviderConfig {
	registry := llm.NewRegistry(file.API.Providers, file.API.ModelStrategy)
	providers := normalizeOpenAICodexProviders(registry.Providers())
	defaults := defaultConfigFile().API
	defaultRefText := strings.TrimSpace(file.API.DefaultModel)
	if defaultRefText == "" {
		defaultRefText = strings.TrimSpace(file.API.DefaultProfile)
	}
	ref, ok := llm.ParseProfileID(defaultRefText)
	if !ok {
		return providers
	}
	provider, ok := providers[ref.Provider]
	if !ok {
		return providers
	}
	model, ok := provider.Models[ref.Model]
	if !ok {
		return providers
	}
	if file.API.TimeoutSeconds > 0 && file.API.TimeoutSeconds != defaults.TimeoutSeconds {
		provider.TimeoutSeconds = file.API.TimeoutSeconds
	}
	provider.Models[ref.Model] = model
	provider = normalizeOpenAICodexProviderModels(provider)
	providers[ref.Provider] = provider
	return providers
}

func normalizeOpenAICodexProviders(providers map[string]llm.ProviderConfig) map[string]llm.ProviderConfig {
	for id, provider := range providers {
		providers[id] = normalizeOpenAICodexProviderModels(provider)
	}
	return providers
}

func applyProviderCredentialEnv(providers map[string]llm.ProviderConfig, dotenvMap map[string]string) {
	for id, provider := range providers {
		envName := strings.TrimSpace(provider.APIKeyEnv)
		if envName == "" {
			switch provider.Type {
			case ProviderOpenAICodex:
				envName = "GODEX_OPENAI_CODEX_OAUTH_TOKEN"
			case ProviderOpenAICompatible:
				envName = "OPENAI_API_KEY"
			}
		}
		if envName != "" {
			if dot := lookupEnvValue(dotenvMap, envName); dot.Set {
				provider.APIKey = dot.Value
			}
			if env := lookupProcessValue(envName); env.Set {
				provider.APIKey = env.Value
			}
		}
		if strings.TrimSpace(provider.CredentialKind) == "" {
			if provider.Type == ProviderOpenAICodex {
				provider.CredentialKind = "oauth-token"
			} else if strings.TrimSpace(envName) != "" || strings.TrimSpace(provider.APIKey) != "" {
				provider.CredentialKind = "api-key"
			}
		}
		providers[id] = provider
	}
}

func llmStrategyFromConfigFile(file ConfigFile) llm.StrategyConfig {
	strategy := llm.NormalizeStrategy(file.API.ModelStrategy)
	if len(strategy.Candidates) == 0 {
		if ref, ok := llm.ParseProfileID(file.API.DefaultModel); ok {
			strategy.Candidates = append(strategy.Candidates, ref)
		}
	}
	if len(strategy.Candidates) == 0 {
		if ref, ok := llm.ParseProfileID(file.API.DefaultProfile); ok {
			strategy.Candidates = append(strategy.Candidates, ref)
		}
	}
	return strategy
}

func isProviderModelProfileID(file ConfigFile, profileID string) bool {
	ref, ok := llm.ParseProfileID(profileID)
	if !ok {
		return false
	}
	provider, ok := file.API.Providers[ref.Provider]
	if !ok {
		return false
	}
	_, ok = provider.Models[ref.Model]
	return ok
}

func normalizeSecurityProfileName(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "trusted-local", "guarded-local", "sandboxed", "strict", "host-privileged", "dev/repair":
		return strings.ToLower(strings.TrimSpace(profile))
	case "dev-repair", "dev_repair", "repair":
		return "dev/repair"
	case "trusted":
		return "trusted-local"
	case "guarded", "":
		return "guarded-local"
	default:
		return "guarded-local"
	}
}

// normalizeMemoryStrategyKind maps a configured memory strategy name to the
// canonical kind used by internal/core/memory. Unknown or empty values fall
// back to "per-turn" (the default behavior).
func normalizeMemoryStrategyKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "per-turn", "per_turn", "perturn":
		return "per-turn"
	case "agent-only", "agent_only", "agentonly":
		return "agent-only"
	case "consolidated", "consolidation":
		return "consolidated"
	case "":
		return "per-turn"
	default:
		return "per-turn"
	}
}

func normalizeSessionStorageBackend(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "sqlite":
		return "sqlite"
	default:
		return "json"
	}
}

func normalizeSubagentReadOnlyIsolation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "snapshot":
		return "snapshot"
	case "shared_readonly", "":
		return "shared_readonly"
	default:
		return "shared_readonly"
	}
}

func normalizeSubagentGitDirtyIsolation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "snapshot":
		return "snapshot"
	case "dirty_overlay", "":
		return "dirty_overlay"
	default:
		return "dirty_overlay"
	}
}

func normalizeSubagentNonGitWriteIsolation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "shared_with_approval", "deny":
		return strings.ToLower(strings.TrimSpace(value))
	case "copy_snapshot", "":
		return "copy_snapshot"
	default:
		return "copy_snapshot"
	}
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// nonNegativeOrDefault keeps an explicit 0 (meaning "derive from ratio") while
// clamping negative misconfigurations to the fallback.
func nonNegativeOrDefault(value, fallback int) int {
	if value >= 0 {
		return value
	}
	return fallback
}

// ratioOrDefault validates a 0..1 ratio; out-of-range values fall back.
func ratioOrDefault(value, fallback float64) float64 {
	if value > 0 && value < 1 {
		return value
	}
	return fallback
}

// resolveCompactionModelPolicies normalizes per-model compaction policies,
// trimming identifiers and applying ratio defaults per entry.
func resolveCompactionModelPolicies(items []CompactionModelPolicySection) []CompactionModelPolicy {
	out := make([]CompactionModelPolicy, 0, len(items))
	for _, item := range items {
		policy := CompactionModelPolicy{
			Provider:            strings.TrimSpace(item.Provider),
			Model:               strings.TrimSpace(item.Model),
			ContextWindowTokens: positiveOrDefault(item.ContextWindowTokens, 128000),
			TriggerTokens:       positiveOrDefault(item.TriggerTokens, 0),
			RetainTokens:        nonNegativeOrDefault(item.RetainTokens, 0),
			TriggerRatio:        ratioOrDefault(item.TriggerRatio, 0.8),
			RetainRatio:         ratioOrDefault(item.RetainRatio, 0.16),
		}
		out = append(out, policy)
	}
	return out
}

func controlNodesFromConfigFile(file ConfigFile) []ControlNodeConfig {
	nodes := make([]ControlNodeConfig, 0, len(file.Control.Nodes))
	for _, item := range file.Control.Nodes {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		nodes = append(nodes, ControlNodeConfig{
			ID:           id,
			Name:         strings.TrimSpace(item.Name),
			Endpoint:     strings.TrimSpace(item.Endpoint),
			WorkspaceDir: strings.TrimSpace(item.WorkspaceDir),
			GodexHome:    strings.TrimSpace(item.GodexHome),
			Version:      strings.TrimSpace(item.Version),
			Capabilities: uniqueNonEmptyStrings(item.Capabilities),
		})
	}
	return nodes
}

func acpAgentsFromConfigFile(file ConfigFile) map[string]ACPAgentConfig {
	agents := make(map[string]ACPAgentConfig, len(file.ACP.Agents))
	for id, item := range file.ACP.Agents {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		env := make(map[string]string, len(item.Env))
		for key, value := range item.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			env[key] = value
		}
		agents[id] = ACPAgentConfig{
			ID:             id,
			Command:        strings.TrimSpace(item.Command),
			Args:           append([]string{}, item.Args...),
			Env:            env,
			TimeoutSeconds: item.TimeoutSeconds,
			Description:    strings.TrimSpace(item.Description),
		}
	}
	return agents
}

func uniqueNonEmptyStrings(values []string) []string {
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

func defaultProfileIDFromProfiles(profiles map[string]ModelProfileConfig) string {
	if _, ok := profiles[defaultProfileID]; ok {
		return defaultProfileID
	}
	keys := make([]string, 0, len(profiles))
	for key := range profiles {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return defaultProfileID
	}
	return keys[0]
}

func maskProfileSecrets(profiles map[string]ModelProfileConfig) map[string]ModelProfileConfig {
	out := make(map[string]ModelProfileConfig, len(profiles))
	for id, profile := range profiles {
		if strings.TrimSpace(profile.APIKey) != "" {
			profile.APIKey = "********"
		}
		out[id] = profile
	}
	return out
}

func maskACPAgentSecrets(agents map[string]ACPAgentConfig) map[string]ACPAgentConfig {
	out := make(map[string]ACPAgentConfig, len(agents))
	for id, agent := range agents {
		if len(agent.Args) > 0 {
			agent.Args = append([]string{}, agent.Args...)
		}
		if len(agent.Env) > 0 {
			env := make(map[string]string, len(agent.Env))
			for key, value := range agent.Env {
				if looksSecretKey(key) && strings.TrimSpace(value) != "" {
					value = "********"
				}
				env[key] = value
			}
			agent.Env = env
		}
		out[id] = agent
	}
	return out
}

func asLLMProviders(value any) (map[string]llm.ProviderConfig, error) {
	if value == nil {
		return map[string]llm.ProviderConfig{}, nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return map[string]llm.ProviderConfig{}, nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("invalid api.providers: %w", err)
		}
	}
	var providers map[string]llm.ProviderConfig
	if err := yaml.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("invalid api.providers: %w", err)
	}
	return llm.NewRegistry(providers, llm.StrategyConfig{}).Providers(), nil
}

func asLLMStrategy(value any) (llm.StrategyConfig, error) {
	if value == nil {
		return llm.StrategyConfig{}, nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return llm.StrategyConfig{}, nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return llm.StrategyConfig{}, fmt.Errorf("invalid api.model_strategy: %w", err)
		}
	}
	var strategy llm.StrategyConfig
	if err := yaml.Unmarshal(data, &strategy); err != nil {
		return llm.StrategyConfig{}, fmt.Errorf("invalid api.model_strategy: %w", err)
	}
	return llm.NormalizeStrategy(strategy), nil
}

func asACPAgents(value any) (map[string]ACPAgentSection, error) {
	if value == nil {
		return map[string]ACPAgentSection{}, nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return map[string]ACPAgentSection{}, nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("invalid acp.agents: %w", err)
		}
	}
	var agents map[string]ACPAgentSection
	if err := yaml.Unmarshal(data, &agents); err != nil {
		return nil, fmt.Errorf("invalid acp.agents: %w", err)
	}
	cleaned := make(map[string]ACPAgentSection, len(agents))
	for id, agent := range agents {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		agent.Command = strings.TrimSpace(agent.Command)
		if agent.Env == nil {
			agent.Env = map[string]string{}
		}
		if agent.TimeoutSeconds <= 0 {
			agent.TimeoutSeconds = 600
		}
		cleaned[id] = agent
	}
	return cleaned, nil
}

func asControlNodeSections(value any) []ControlNodeSection {
	if value == nil {
		return nil
	}
	var data []byte
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		data = []byte(trimmed)
	default:
		var err error
		data, err = yaml.Marshal(typed)
		if err != nil {
			return nil
		}
	}
	var nodes []ControlNodeSection
	if err := yaml.Unmarshal(data, &nodes); err != nil {
		return nil
	}
	out := make([]ControlNodeSection, 0, len(nodes))
	for _, node := range nodes {
		node.ID = strings.TrimSpace(node.ID)
		if node.ID == "" {
			continue
		}
		node.Name = strings.TrimSpace(node.Name)
		node.Endpoint = strings.TrimSpace(node.Endpoint)
		node.WorkspaceDir = strings.TrimSpace(node.WorkspaceDir)
		node.GodexHome = strings.TrimSpace(node.GodexHome)
		node.Version = strings.TrimSpace(node.Version)
		node.Capabilities = uniqueNonEmptyStrings(node.Capabilities)
		out = append(out, node)
	}
	return out
}

func maskLLMProviders(providers map[string]llm.ProviderConfig) map[string]llm.ProviderConfig {
	out := make(map[string]llm.ProviderConfig, len(providers))
	for id, provider := range providers {
		if strings.TrimSpace(provider.APIKey) != "" {
			provider.APIKey = "********"
		}
		if len(provider.Models) > 0 {
			models := make(map[string]llm.ModelConfig, len(provider.Models))
			for modelID, model := range provider.Models {
				model.Tags = append([]string{}, model.Tags...)
				models[modelID] = model
			}
			provider.Models = models
		}
		out[id] = provider
	}
	return out
}

func maskACPAgentSections(agents map[string]ACPAgentSection) map[string]ACPAgentSection {
	out := make(map[string]ACPAgentSection, len(agents))
	for id, agent := range agents {
		if len(agent.Args) > 0 {
			agent.Args = append([]string{}, agent.Args...)
		}
		if len(agent.Env) > 0 {
			env := make(map[string]string, len(agent.Env))
			for key, value := range agent.Env {
				if looksSecretKey(key) && strings.TrimSpace(value) != "" {
					value = "********"
				}
				env[key] = value
			}
			agent.Env = env
		}
		out[id] = agent
	}
	return out
}

func looksSecretKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password")
}
