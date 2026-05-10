package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
)

func TestDiscoverModelsReadsOpenAICompatibleModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-5.5"},
				{"id": "gpt-5.4-mini", "name": "GPT-5.4 Mini"},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{LLMProviders: map[string]llm.ProviderConfig{
		"openai": {
			ID:      "openai",
			Type:    config.ProviderOpenAICompatible,
			BaseURL: server.URL,
			APIKey:  "sk-test",
		},
	}}

	result := DiscoverModels(context.Background(), cfg, "openai")
	if !result.OK {
		t.Fatalf("discover models: %s", result.Error)
	}
	if len(result.Models) != 2 || result.Models[0].ID != "gpt-5.4-mini" || result.Models[0].Name != "GPT-5.4 Mini" || result.Models[1].ID != "gpt-5.5" {
		t.Fatalf("unexpected models: %#v", result.Models)
	}
}

func TestDiscoverModelsUsesAnthropicHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "sk-ant" {
			t.Fatalf("unexpected x-api-key header: %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("expected anthropic-version header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-sonnet-4-20250514", "display_name": "Claude Sonnet 4"},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{LLMProviders: map[string]llm.ProviderConfig{
		"anthropic": {
			ID:      "anthropic",
			Type:    config.ProviderAnthropicCompatible,
			BaseURL: server.URL,
			APIKey:  "sk-ant",
		},
	}}

	result := DiscoverModels(context.Background(), cfg, "anthropic")
	if !result.OK {
		t.Fatalf("discover models: %s", result.Error)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "claude-sonnet-4-20250514" || result.Models[0].Name != "Claude Sonnet 4" {
		t.Fatalf("unexpected models: %#v", result.Models)
	}
}

func TestDiscoverModelsReturnsStaticCodexOAuthModels(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "codex oauth should not use /v1/models", http.StatusTeapot)
	}))
	defer server.Close()

	cfg := &config.Config{LLMProviders: map[string]llm.ProviderConfig{
		"codex": {
			ID:             "codex",
			Type:           config.ProviderOpenAICodex,
			BaseURL:        server.URL,
			APIKey:         "codex-oauth-token",
			CredentialKind: "codex-oauth",
		},
	}}

	result := DiscoverModels(context.Background(), cfg, "codex")
	if !result.OK {
		t.Fatalf("discover codex models: %s", result.Error)
	}
	if called {
		t.Fatal("codex oauth model discovery should not call /v1/models")
	}
	assertHasModel(t, result.Models, "gpt-5.3-codex")
	assertHasModel(t, result.Models, "gpt-5.3-codex-spark")
	assertHasModel(t, result.Models, "gpt-5.4-mini")
	assertHasModel(t, result.Models, "gpt-5.5")
}

func assertHasModel(t *testing.T, models []ModelInfo, id string) {
	t.Helper()
	for _, model := range models {
		if model.ID == id {
			return
		}
	}
	t.Fatalf("expected model %q in %#v", id, models)
}
