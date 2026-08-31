package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/contracts/protocol"
)

func TestCallerForConfigProfileKeepsCustomPrimaryAheadOfStrategy(t *testing.T) {
	var seenModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		seenModels = append(seenModels, body.Model)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "ok",
					},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		APIKey:            "sk-default",
		Model:             "claude-sonnet-test",
		BaseURL:           server.URL,
		MaxTokens:         4096,
		APITimeoutSeconds: 60,
		DefaultProfileID:  "anthropic.sonnet",
		LLMProviders: map[string]llm.ProviderConfig{
			"anthropic": {
				ID:      "anthropic",
				Name:    "Anthropic",
				Type:    config.ProviderAnthropicCompatible,
				BaseURL: server.URL,
				APIKey:  "sk-ant",
				Models: map[string]llm.ModelConfig{
					"sonnet": {
						ID:        "sonnet",
						Name:      "Claude Sonnet",
						Model:     "claude-sonnet-test",
						MaxTokens: 4096,
					},
				},
			},
		},
		LLMStrategy: llm.StrategyConfig{
			Type: llm.StrategyFallback,
			Candidates: []llm.ModelRef{
				{Provider: "anthropic", Model: "sonnet"},
			},
		},
		ModelProfiles: map[string]config.ModelProfileConfig{
			"anthropic.sonnet": {
				ID:       "anthropic.sonnet",
				Provider: config.ProviderOpenAICompatible,
				Model:    "claude-sonnet-test",
				BaseURL:  server.URL,
				APIKey:   "sk-ant",
			},
			"openai-codex": {
				ID:       "openai-codex",
				Name:     "OpenAI Codex / Codex",
				Provider: config.ProviderOpenAICompatible,
				Model:    "gpt-5.5",
				BaseURL:  server.URL,
				APIKey:   "sk-openai",
			},
		},
	}
	selected, ok := cfg.ModelProfileByID("openai-codex")
	if !ok {
		t.Fatal("selected profile not found")
	}

	caller := callerForConfigProfile(cfg, selected)
	if _, err := caller.Call(context.Background(), protocol.Request{Model: "stale-model", MaxTokens: 10}); err != nil {
		t.Fatalf("call selected profile: %v", err)
	}
	if len(seenModels) != 1 || seenModels[0] != "gpt-5.5" {
		t.Fatalf("expected selected model request, got %#v", seenModels)
	}
}
