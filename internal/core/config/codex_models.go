package config

import (
	"strings"

	"github.com/tim5wang/godex/internal/core/llm"
)

var openAICodexDefaultModels = map[string]llm.ModelConfig{
	"gpt-5.1":             {Name: "GPT-5.1", Model: "gpt-5.1", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.1-codex-max":   {Name: "GPT-5.1 Codex Max", Model: "gpt-5.1-codex-max", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.1-codex-mini":  {Name: "GPT-5.1 Codex Mini", Model: "gpt-5.1-codex-mini", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.2":             {Name: "GPT-5.2", Model: "gpt-5.2", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.2-codex":       {Name: "GPT-5.2 Codex", Model: "gpt-5.2-codex", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.3-codex":       {Name: "GPT-5.3 Codex", Model: "gpt-5.3-codex", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.3-codex-spark": {Name: "GPT-5.3 Codex Spark", Model: "gpt-5.3-codex-spark", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.4":             {Name: "GPT-5.4", Model: "gpt-5.4", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.4-mini":        {Name: "GPT-5.4 Mini", Model: "gpt-5.4-mini", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
	"gpt-5.5":             {Name: "GPT-5.5", Model: "gpt-5.5", MaxTokens: 4096, SupportsStreaming: true, SupportsVision: true},
}

func normalizeOpenAICodexProviderModels(provider llm.ProviderConfig) llm.ProviderConfig {
	if provider.Type != ProviderOpenAICodex {
		return provider
	}
	models := make(map[string]llm.ModelConfig, len(provider.Models))
	for id, model := range provider.Models {
		actual := strings.TrimSpace(model.Model)
		if actual == "" {
			actual = strings.TrimSpace(id)
		}
		if defaultModel, ok := openAICodexDefaultModels[actual]; ok {
			if strings.TrimSpace(model.Name) == "" {
				model.Name = defaultModel.Name
			}
			if !model.SupportsStreaming {
				model.SupportsStreaming = defaultModel.SupportsStreaming
			}
			if !model.SupportsVision {
				model.SupportsVision = defaultModel.SupportsVision
			}
		}
		model.ID = strings.TrimSpace(id)
		model.Model = actual
		if strings.TrimSpace(model.Name) == "" {
			model.Name = actual
		}
		if model.ID == "" {
			model.ID = actual
		}
		model.MaxTokens = normalizedOpenAICodexMaxTokens(model.MaxTokens)
		model.SupportsStreaming = true
		models[model.ID] = model
	}
	if len(models) == 0 {
		models["gpt-5.4"] = openAICodexDefaultModels["gpt-5.4"]
	}
	provider.Models = models
	return provider
}

func normalizedOpenAICodexMaxTokens(value int) int {
	if value <= 0 || value > 32768 {
		return 4096
	}
	return value
}
