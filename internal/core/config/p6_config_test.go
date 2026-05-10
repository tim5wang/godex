package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerLoadsHomeProjectEnvPrecedenceAndWritesHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"team:",
		"  lead_name: home-lead",
		"  team_name: home-team",
		"paths:",
		"  skills_dir: skills",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "godex.yaml"), []byte(strings.Join([]string{
		"team:",
		"  team_name: project-team",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("LEAD_NAME=home-env-lead\nTEAM_NAME=home-env-team\n"), 0600); err != nil {
		t.Fatalf("write home env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("TEAM_NAME=project-env-team\n"), 0600); err != nil {
		t.Fatalf("write project env: %v", err)
	}
	t.Setenv("LEAD_NAME", "process-lead")

	manager, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	cfg := manager.Current()
	if cfg.HomeDir != home {
		t.Fatalf("expected home dir %q, got %q", home, cfg.HomeDir)
	}
	if cfg.WorkspaceDir != project {
		t.Fatalf("expected project dir %q, got %q", project, cfg.WorkspaceDir)
	}
	if cfg.LeadName != "process-lead" {
		t.Fatalf("expected process env lead to win, got %q", cfg.LeadName)
	}
	if cfg.TeamName != "project-env-team" {
		t.Fatalf("expected project env team to win, got %q", cfg.TeamName)
	}
	if cfg.SkillsDir != filepath.Join(home, "skills") {
		t.Fatalf("expected global skills dir, got %q", cfg.SkillsDir)
	}
	if cfg.MemoryDir != filepath.Join(home, "memory") {
		t.Fatalf("expected global memory dir, got %q", cfg.MemoryDir)
	}
	if cfg.SessionsDir != filepath.Join(home, "sessions") {
		t.Fatalf("expected global sessions dir, got %q", cfg.SessionsDir)
	}
	if cfg.TempDir != filepath.Join(home, "tmp") {
		t.Fatalf("expected global temp dir, got %q", cfg.TempDir)
	}

	if _, err := manager.Update(t.Context(), UpdateRequest{Values: map[string]any{"team.lead_name": "saved-home-lead"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	homeData, err := os.ReadFile(filepath.Join(home, "godex.yaml"))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if !strings.Contains(string(homeData), "saved-home-lead") {
		t.Fatalf("expected update to write home config, got:\n%s", homeData)
	}
	projectData, err := os.ReadFile(filepath.Join(project, "godex.yaml"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectData), "saved-home-lead") {
		t.Fatalf("project config should not receive global config edits, got:\n%s", projectData)
	}
}

func TestManagerDefaultsLoggingToHomeLogDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	manager, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	want := filepath.Join(home, "log", "godex.log")
	if got := manager.Current().Logging.FilePath; got != want {
		t.Fatalf("expected default log file %q, got %q", want, got)
	}
}

func TestManagerMigratesLegacyHomeRootLogPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"logging:",
		"  file_path: " + filepath.Join(home, "godex.log"),
	}, "\n")), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	manager, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	want := filepath.Join(home, "log", "godex.log")
	if got := manager.Current().Logging.FilePath; got != want {
		t.Fatalf("expected legacy log file to migrate to %q, got %q", want, got)
	}
}

func TestManagerKeepsConfiguredCodexOAuthModelsAndClampsMaxTokens(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"api:",
		"  default_model: codex.codex",
		"  providers:",
		"    codex:",
		"      name: Codex",
		"      type: openai_codex",
		"      base_url: https://chatgpt.com/backend-api/codex",
		"      api_key_env: GODEX_OPENAI_CODEX_OAUTH_TOKEN",
		"      credential_kind: codex-oauth",
		"      models:",
		"        codex:",
		"          name: GPT-5.5",
		"          model: gpt-5.5",
		"          max_tokens: 1000000",
		"        gpt-5.4:",
		"          name: GPT-5.4",
		"          model: gpt-5.4",
		"          max_tokens: 1000000",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	manager, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	cfg := manager.Current()
	profile, ok := cfg.ModelProfileByID("codex.codex")
	if !ok {
		t.Fatal("expected configured codex OAuth model gpt-5.5 to remain available")
	}
	if profile.Model != "gpt-5.5" {
		t.Fatalf("expected configured codex model gpt-5.5, got %q", profile.Model)
	}
	if profile.MaxTokens != 4096 {
		t.Fatalf("expected codex OAuth max tokens to be normalized to 4096, got %d", profile.MaxTokens)
	}
	fallbackProfile, ok := cfg.ModelProfileByID("codex.gpt-5.4")
	if !ok {
		t.Fatal("expected supported codex OAuth model gpt-5.4 to remain")
	}
	if fallbackProfile.MaxTokens != 4096 {
		t.Fatalf("expected codex OAuth fallback max tokens to be normalized to 4096, got %d", fallbackProfile.MaxTokens)
	}
	if cfg.DefaultProfileID != "codex.codex" {
		t.Fatalf("expected configured codex default to remain selected, got %q", cfg.DefaultProfileID)
	}
}

func TestManagerResolvesProviderAPIKeyFromEnvReference(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "godex.yaml"), []byte(strings.Join([]string{
		"api:",
		"  default_profile: openai.gpt",
		"  providers:",
		"    openai:",
		"      name: OpenAI",
		"      type: openai_compatible",
		"      base_url: https://api.openai.com/v1",
		"      api_key_env: OPENAI_API_KEY",
		"      credential_kind: api-key",
		"      models:",
		"        gpt:",
		"          model: gpt-5.4-mini",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("OPENAI_API_KEY=sk-home-secret\n"), 0600); err != nil {
		t.Fatalf("write home env: %v", err)
	}

	manager, err := NewManager(Options{HomeDir: home, WorkspaceDir: project})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	provider := manager.Current().LLMProviders["openai"]
	if provider.APIKey != "sk-home-secret" {
		t.Fatalf("expected provider API key from home env, got %q", provider.APIKey)
	}
	if provider.APIKeyEnv != "OPENAI_API_KEY" || provider.CredentialKind != "api-key" {
		t.Fatalf("expected provider credential metadata, got %+v", provider)
	}
	view := manager.View()
	effective, ok := view.EffectiveValues["api.providers"].(map[string]llmProviderForTest)
	_ = effective
	if ok {
		t.Fatalf("api.providers effective values should remain opaque/masked, got typed test helper unexpectedly")
	}
	revealed, err := manager.Reveal("api.providers")
	if err != nil {
		t.Fatalf("reveal providers: %v", err)
	}
	if strings.Contains(revealed, "sk-home-secret") {
		t.Fatalf("provider secret from env must not be written into YAML reveal: %s", revealed)
	}
}

type llmProviderForTest struct{}
