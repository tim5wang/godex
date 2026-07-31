package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/tim5wang/godex/internal/core/llm"
)

// ImportedProvider holds a single provider discovered from another coding agent.
type ImportedProvider struct {
	Source         string                       `json:"source"`
	ProviderID     string                       `json:"provider_id"`
	ProviderConfig llm.ProviderConfig           `json:"provider_config"`
	Models         []llm.ModelConfig            `json:"models"`
}

// CodexTOMLConfig parses the top-level keys we care about in ~/.codex/config.toml.
type codexTOMLConfig struct {
	ModelProvider   string                       `toml:"model_provider"`
	ModelCatalogJSON string                     `toml:"model_catalog_json"`
	ModelProviders  map[string]codexProviderDef `toml:"model_providers"`
}

type codexProviderDef struct {
	Name                   string `toml:"name"`
	WireAPI                string `toml:"wire_api"`
	BaseURL                string `toml:"base_url"`
	RequiresOpenAIAuth     bool   `toml:"requires_openai_auth"`
	ExperimentalBearerToken string `toml:"experimental_bearer_token"`
}

type aisModelEntry struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// DefaultCodexConfigPath returns the default path to the Codex config file.
func DefaultCodexConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// DefaultCodexModelCatalogPath returns the default path to the AIS switch model catalog.
func DefaultCodexModelCatalogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "ais-switch-model-catalog.json")
}

// ImportCodexProviders reads the Codex config.toml and optional AIS model catalog,
// and returns godex-compatible provider configs ready to merge.
func ImportCodexProviders(configPath, catalogPath string) ([]ImportedProvider, error) {
	if configPath == "" {
		configPath = DefaultCodexConfigPath()
	}
	if configPath == "" {
		return nil, fmt.Errorf("cannot determine Codex config path")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read Codex config %s: %w", configPath, err)
	}

	var raw codexTOMLConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse Codex config %s: %w", configPath, err)
	}

	if len(raw.ModelProviders) == 0 {
		return nil, fmt.Errorf("no model_providers found in Codex config %s", configPath)
	}

	// Resolve catalog path: prefer config.toml's model_catalog_json, then the default.
	if catalogPath == "" {
		catalogPath = raw.ModelCatalogJSON
	}
	if catalogPath == "" {
		catalogPath = DefaultCodexModelCatalogPath()
	}

	// Try to read AIS model catalog (non-fatal).
	aisModels := readAISModelCatalog(catalogPath)

	result := make([]ImportedProvider, 0, len(raw.ModelProviders))
	for id, def := range raw.ModelProviders {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		p := codexToGodexProvider(id, def, aisModels)
		models := make([]llm.ModelConfig, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, m)
		}
		result = append(result, ImportedProvider{
			Source:         "codex",
			ProviderID:     id,
			ProviderConfig: p,
			Models:         models,
		})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no parsable providers found in Codex config")
	}
	return result, nil
}

// codexToGodexProvider maps a single Codex model_provider definition to a godex ProviderConfig.
func codexToGodexProvider(id string, def codexProviderDef, aisModels []llm.ModelConfig) llm.ProviderConfig {
	name := strings.TrimSpace(def.Name)
	if name == "" {
		name = id
	}

	// Map Codex wire_api to godex provider type.
	providerType := llm.ProviderOpenAICompatible
	wire := strings.ToLower(strings.TrimSpace(def.WireAPI))
	switch wire {
	case "anthropic", "anthropic_compatible":
		providerType = llm.ProviderAnthropicCompatible
	case "chat_completions", "responses", "":
		providerType = llm.ProviderOpenAICompatible
	default:
		providerType = llm.ProviderOpenAICompatible
	}

	// Strip Codex-specific path suffixes from base_url. Godex auto-appends
	// /v1/chat/completions (or /v1/messages for Anthropic), so we want the
	// root of the API host.
	baseURL := normalizeCodexBaseURL(def.BaseURL)

	models := make(map[string]llm.ModelConfig)
	for _, m := range aisModels {
		models[m.ID] = m
	}
	// If no AIS catalog, create a single placeholder model so the provider
	// can be listed and the user can discover models from the endpoint.
	if len(models) == 0 {
		placeholderID := "default"
		models[placeholderID] = llm.ModelConfig{
			ID:    placeholderID,
			Name:  placeholderID,
			Model: placeholderID,
		}
	}

	return llm.ProviderConfig{
		Name: name,
		Type: providerType,
		BaseURL: baseURL,
		Models: models,
		// Do NOT copy Codex's bearer token or OAuth tokens; the user must
		// configure credentials again through godex.
		APIKeyEnv: credentialsEnvForProvider(id, providerType),
	}
}

// normalizeCodexBaseURL strips Codex-specific path segments so godex can
// append its own protocol paths (/v1/chat/completions etc.).
func normalizeCodexBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Remove trailing slash first.
	raw = strings.TrimRight(raw, "/")
	// Strip known Codex endpoint suffixes.
	knownSuffixes := []string{"/codex/v1", "/v1", "/responses", "/chat/completions"}
	for {
		changed := false
		for _, suffix := range knownSuffixes {
			if cleaned, ok := strings.CutSuffix(raw, suffix); ok {
				raw = cleaned
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return raw
}

// readAISModelCatalog reads the AIS switch model catalog JSON file and
// returns a flat list of godex ModelConfig entries.
func readAISModelCatalog(path string) []llm.ModelConfig {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// The file may be either a bare JSON array or an object with a "models" key.
	var rawModels []json.RawMessage
	if err := json.Unmarshal(data, &rawModels); err != nil {
		// Try object form.
		var wrapper struct {
			Models []json.RawMessage `json:"models"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			return nil
		}
		rawModels = wrapper.Models
	}

	models := make([]llm.ModelConfig, 0, len(rawModels))
	for _, raw := range rawModels {
		var entry aisModelEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		slug := strings.TrimSpace(entry.Slug)
		if slug == "" {
			continue
		}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = slug
		}
		models = append(models, llm.ModelConfig{
			ID:                slug,
			Name:              display,
			Model:             slug,
			SupportsStreaming: true,
		})
	}
	return models
}

// credentialsEnvForProvider returns a conventional env-var name for the given provider.
func credentialsEnvForProvider(id string, providerType string) string {
	upper := strings.ToUpper(strings.TrimSpace(id))
	// Strip common suffixes / prefixes for a clean env name.
	upper = strings.ReplaceAll(upper, "-", "_")
	switch {
	case providerType == llm.ProviderAnthropicCompatible:
		return upper + "_API_KEY"
	default:
		return upper + "_API_KEY"
	}
}
