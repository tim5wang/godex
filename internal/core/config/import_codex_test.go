package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/llm"
)

func TestImportCodexProvidersReadsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a minimal Codex config.toml with one custom provider.
	configTOML := strings.Join([]string{
		`model = "llm-gateway--kimi-k3"`,
		`model_provider = "custom"`,
		`model_catalog_json = "` + filepath.Join(codexDir, "ais-switch-model-catalog.json") + `"`,
		``,
		`[model_providers]`,
		``,
		`[model_providers.custom]`,
		`name = "custom"`,
		`wire_api = "responses"`,
		`base_url = "http://127.0.0.1:15721/codex/v1"`,
		`requires_openai_auth = true`,
	}, "\n")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configTOML), 0600); err != nil {
		t.Fatal(err)
	}

	// Write a minimal AIS model catalog.
	catalog := []map[string]string{
		{"slug": "llm-gateway--kimi-k3", "display_name": "kimi-k3 (LLM Gateway)"},
		{"slug": "llm-gateway--deepseek-v4-flash", "display_name": "deepseek-v4-flash (LLM Gateway)"},
	}
	catalogPath := filepath.Join(codexDir, "ais-switch-model-catalog.json")
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(catalogPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	providers, err := ImportCodexProviders(configPath, catalogPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}

	p := providers[0]
	if p.ProviderID != "custom" {
		t.Fatalf("expected provider id custom, got %s", p.ProviderID)
	}
	if p.ProviderConfig.Type != llm.ProviderOpenAICompatible {
		t.Fatalf("expected openai_compatible type, got %s", p.ProviderConfig.Type)
	}
	if p.ProviderConfig.BaseURL != "http://127.0.0.1:15721" {
		t.Fatalf("expected base_url http://127.0.0.1:15721, got %s", p.ProviderConfig.BaseURL)
	}
	if len(p.ProviderConfig.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(p.ProviderConfig.Models), p.ProviderConfig.Models)
	}
	if _, ok := p.ProviderConfig.Models["llm-gateway--kimi-k3"]; !ok {
		t.Fatalf("expected kimi-k3 model, got %v", p.ProviderConfig.Models)
	}
}

func TestImportCodexProvidersMissingFile(t *testing.T) {
	_, err := ImportCodexProviders("/nonexistent/codex/config.toml", "")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestImportCodexProvidersNoProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	configTOML := `model = "gpt-5.4"`
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configTOML), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := ImportCodexProviders(configPath, "")
	if err == nil {
		t.Fatal("expected error for config with no model_providers")
	}
}

func TestImportCodexProvidersWithoutCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}

	configTOML := strings.Join([]string{
		`model = "custom-model"`,
		`model_provider = "local"`,
		``,
		`[model_providers]`,
		``,
		`[model_providers.local]`,
		`name = "local"`,
		`wire_api = "chat_completions"`,
		`base_url = "http://127.0.0.1:11434/v1"`,
	}, "\n")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configTOML), 0600); err != nil {
		t.Fatal(err)
	}

	providers, err := ImportCodexProviders(configPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}

	p := providers[0]
	if p.ProviderConfig.Type != llm.ProviderOpenAICompatible {
		t.Fatalf("expected openai_compatible for chat_completions wire_api, got %s", p.ProviderConfig.Type)
	}
	if p.ProviderConfig.BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("expected stripped base_url, got %s", p.ProviderConfig.BaseURL)
	}
	// Should have one placeholder model since no catalog was provided.
	if len(p.ProviderConfig.Models) == 0 {
		t.Fatal("expected at least one placeholder model")
	}
}

func TestNormalizeCodexBaseURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"http://127.0.0.1:15721/codex/v1", "http://127.0.0.1:15721"},
		{"http://127.0.0.1:15721/codex/v1/", "http://127.0.0.1:15721"},
		{"https://api.openai.com/v1", "https://api.openai.com"},
		{"https://api.openai.com/v1/", "https://api.openai.com"},
		{"http://localhost:11434", "http://localhost:11434"},
		{"", ""},
		{"http://127.0.0.1:15721/codex/v1/responses", "http://127.0.0.1:15721"},
	}
	for _, tt := range tests {
		got := normalizeCodexBaseURL(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeCodexBaseURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestReadAISModelCatalog(t *testing.T) {
	dir := t.TempDir()

	// Array form.
	path1 := filepath.Join(dir, "array.json")
	raw1, _ := json.Marshal([]map[string]string{
		{"slug": "model-a", "display_name": "Model A"},
		{"slug": "model-b"},
	})
	os.WriteFile(path1, raw1, 0600)

	models := readAISModelCatalog(path1)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "model-a" || models[0].Name != "Model A" {
		t.Fatalf("unexpected model: %+v", models[0])
	}
	if models[1].ID != "model-b" || models[1].Name != "model-b" {
		t.Fatalf("expected fallback to slug as name: %+v", models[1])
	}

	// Object form with "models" key.
	path2 := filepath.Join(dir, "object.json")
	raw2, _ := json.Marshal(map[string]any{
		"models": []map[string]string{
			{"slug": "model-c", "display_name": "Model C"},
		},
	})
	os.WriteFile(path2, raw2, 0600)

	models2 := readAISModelCatalog(path2)
	if len(models2) != 1 {
		t.Fatalf("expected 1 model from object form, got %d", len(models2))
	}
	if models2[0].ID != "model-c" {
		t.Fatalf("unexpected model from object form: %+v", models2[0])
	}
}
