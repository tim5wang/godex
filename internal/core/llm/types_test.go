package llm

import "testing"

func TestRegistryResolvesProviderModelsAndStrategyCandidates(t *testing.T) {
	registry := NewRegistry(map[string]ProviderConfig{
		"anthropic": {
			Type:    "anthropic",
			BaseURL: "https://api.anthropic.com",
			APIKey:  "sk-a",
			Models: map[string]ModelConfig{
				"sonnet": {Model: "claude-sonnet-4-20250514", MaxTokens: 8192},
			},
		},
		"openai": {
			Type:    "openai",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-o",
			Models: map[string]ModelConfig{
				"gpt": {Model: "gpt-4o"},
			},
		},
	}, StrategyConfig{
		Type: StrategyFallback,
		Candidates: []ModelRef{
			{Provider: "anthropic", Model: "sonnet"},
			{Provider: "openai", Model: "gpt"},
		},
	})

	candidates := registry.StrategyCandidates(ProfileID("anthropic", "sonnet"))
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %#v", candidates)
	}
	if candidates[0].ProfileID != "anthropic.sonnet" || candidates[0].ProviderType != ProviderAnthropicCompatible {
		t.Fatalf("unexpected first candidate: %#v", candidates[0])
	}
	if candidates[1].ProfileID != "openai.gpt" || candidates[1].ProviderType != ProviderOpenAICompatible {
		t.Fatalf("unexpected second candidate: %#v", candidates[1])
	}
}
