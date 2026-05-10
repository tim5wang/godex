package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
)

type Status struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Type           string `json:"type"`
	BaseURL        string `json:"base_url,omitempty"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	CredentialKind string `json:"credential_kind,omitempty"`
	OAuthProvider  string `json:"oauth_provider,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	OAuthMode      string `json:"oauth_mode,omitempty"`
	HasCredential  bool   `json:"has_credential"`
	TokenPresent   bool   `json:"token_present"`
	Masked         string `json:"masked_credential,omitempty"`
	LastTestError  string `json:"last_test_error,omitempty"`
}

type ListResponse struct {
	Providers []Status `json:"providers"`
}

type TestResponse struct {
	Status Status `json:"status"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

type ModelInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name,omitempty"`
	Model             string `json:"model"`
	SupportsStreaming bool   `json:"supports_streaming,omitempty"`
}

type ModelsResponse struct {
	ProviderID string      `json:"provider_id"`
	Models     []ModelInfo `json:"models,omitempty"`
	OK         bool        `json:"ok"`
	Error      string      `json:"error,omitempty"`
}

var codexOAuthModels = []ModelInfo{
	{ID: "gpt-5.1", Name: "GPT-5.1", Model: "gpt-5.1", SupportsStreaming: true},
	{ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max", Model: "gpt-5.1-codex-max", SupportsStreaming: true},
	{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1 Codex Mini", Model: "gpt-5.1-codex-mini", SupportsStreaming: true},
	{ID: "gpt-5.2", Name: "GPT-5.2", Model: "gpt-5.2", SupportsStreaming: true},
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", Model: "gpt-5.2-codex", SupportsStreaming: true},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Model: "gpt-5.3-codex", SupportsStreaming: true},
	{ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark", Model: "gpt-5.3-codex-spark", SupportsStreaming: true},
	{ID: "gpt-5.4", Name: "GPT-5.4", Model: "gpt-5.4", SupportsStreaming: true},
	{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", Model: "gpt-5.4-mini", SupportsStreaming: true},
	{ID: "gpt-5.5", Name: "GPT-5.5", Model: "gpt-5.5", SupportsStreaming: true},
}

func List(cfg *config.Config) ListResponse {
	if cfg == nil {
		return ListResponse{}
	}
	out := make([]Status, 0, len(cfg.LLMProviders))
	for id, provider := range cfg.LLMProviders {
		out = append(out, StatusFromProvider(id, provider))
	}
	return ListResponse{Providers: out}
}

func StatusFromProvider(id string, provider llm.ProviderConfig) Status {
	credential := strings.TrimSpace(provider.APIKey)
	kind := strings.TrimSpace(provider.CredentialKind)
	if kind == "" && credential != "" {
		kind = "api-key"
	}
	return Status{
		ID:             strings.TrimSpace(id),
		Name:           provider.Name,
		Type:           provider.Type,
		BaseURL:        provider.BaseURL,
		APIKeyEnv:      provider.APIKeyEnv,
		CredentialKind: kind,
		OAuthProvider:  provider.OAuth.Provider,
		AccountID:      provider.OAuth.AccountID,
		OAuthMode:      provider.OAuth.Mode,
		HasCredential:  credential != "",
		TokenPresent:   credential != "",
		Masked:         maskCredential(credential),
	}
}

func Test(ctx context.Context, cfg *config.Config, id string) TestResponse {
	status, provider, ok := findProvider(cfg, id)
	if !ok {
		err := fmt.Sprintf("provider not found: %s", id)
		return TestResponse{Status: Status{ID: strings.TrimSpace(id), LastTestError: err}, OK: false, Error: err}
	}
	if !status.HasCredential {
		status.LastTestError = "credential not configured"
		return TestResponse{Status: status, OK: false, Error: status.LastTestError}
	}
	if provider.Type == config.ProviderOpenAICodex {
		return TestResponse{Status: status, OK: true}
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		status.LastTestError = "base_url not configured"
		return TestResponse{Status: status, OK: false, Error: status.LastTestError}
	}
	endpoint := providerModelsEndpoint(provider.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.LastTestError = err.Error()
		return TestResponse{Status: status, OK: false, Error: status.LastTestError}
	}
	setProviderModelHeaders(req, provider)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		status.LastTestError = err.Error()
		return TestResponse{Status: status, OK: false, Error: status.LastTestError}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		status.LastTestError = fmt.Sprintf("provider test failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
		return TestResponse{Status: status, OK: false, Error: status.LastTestError}
	}
	return TestResponse{Status: status, OK: true}
}

func DiscoverModels(ctx context.Context, cfg *config.Config, id string) ModelsResponse {
	status, provider, ok := findProvider(cfg, id)
	providerID := strings.TrimSpace(id)
	if ok {
		providerID = status.ID
	}
	if !ok {
		err := fmt.Sprintf("provider not found: %s", id)
		return ModelsResponse{ProviderID: providerID, OK: false, Error: err}
	}
	if !status.HasCredential {
		return ModelsResponse{ProviderID: providerID, OK: false, Error: "credential not configured"}
	}
	if provider.Type == config.ProviderOpenAICodex {
		return ModelsResponse{ProviderID: providerID, Models: cloneModels(codexOAuthModels), OK: true}
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return ModelsResponse{ProviderID: providerID, OK: false, Error: "base_url not configured"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, providerModelsEndpoint(provider.BaseURL), nil)
	if err != nil {
		return ModelsResponse{ProviderID: providerID, OK: false, Error: err.Error()}
	}
	setProviderModelHeaders(req, provider)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ModelsResponse{ProviderID: providerID, OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ModelsResponse{ProviderID: providerID, OK: false, Error: fmt.Sprintf("model discovery failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))}
	}
	models, err := decodeModelsResponse(resp.Body)
	if err != nil {
		return ModelsResponse{ProviderID: providerID, OK: false, Error: err.Error()}
	}
	return ModelsResponse{ProviderID: providerID, Models: models, OK: true}
}

func cloneModels(models []ModelInfo) []ModelInfo {
	cloned := make([]ModelInfo, len(models))
	copy(cloned, models)
	return cloned
}

func findProvider(cfg *config.Config, id string) (Status, llm.ProviderConfig, bool) {
	if cfg == nil {
		return Status{}, llm.ProviderConfig{}, false
	}
	id = strings.TrimSpace(id)
	provider, ok := cfg.LLMProviders[id]
	if !ok {
		return Status{}, llm.ProviderConfig{}, false
	}
	return StatusFromProvider(id, provider), provider, true
}

func providerModelsEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case baseURL == "":
		return ""
	case strings.HasSuffix(baseURL, "/models"):
		return baseURL
	case strings.HasSuffix(baseURL, "/v1"):
		return baseURL + "/models"
	default:
		return baseURL + "/v1/models"
	}
}

func setProviderModelHeaders(req *http.Request, provider llm.ProviderConfig) {
	req.Header.Set("Accept", "application/json")
	switch strings.ToLower(strings.TrimSpace(provider.Type)) {
	case config.ProviderAnthropicCompatible:
		req.Header.Set("x-api-key", provider.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
}

func decodeModelsResponse(reader io.Reader) ([]ModelInfo, error) {
	var payload struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(reader, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	items := append(payload.Data, payload.Models...)
	models := make([]ModelInfo, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		model := modelInfoFromRaw(item)
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		if _, ok := seen[model.ID]; ok {
			continue
		}
		seen[model.ID] = struct{}{}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}

func modelInfoFromRaw(raw json.RawMessage) ModelInfo {
	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		id = strings.TrimSpace(id)
		return ModelInfo{ID: id, Name: id, Model: id, SupportsStreaming: true}
	}
	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ModelInfo{}
	}
	id = firstStringField(object, "id", "model")
	name := firstStringField(object, "name", "display_name")
	if name == "" {
		name = id
	}
	return ModelInfo{ID: id, Name: name, Model: id, SupportsStreaming: true}
}

func firstStringField(object map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func maskCredential(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return "********"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
