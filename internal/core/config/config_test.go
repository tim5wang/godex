package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/llm"
)

func testHomeForWorkspace(workspace string) string {
	return filepath.Join(workspace, "home")
}

func useTestHome(t *testing.T, workspace string) string {
	t.Helper()
	home := testHomeForWorkspace(workspace)
	t.Setenv("GODEX_HOME", home)
	return home
}

func newTestManager(t *testing.T, workspace string) *Manager {
	t.Helper()
	manager, err := NewManager(Options{HomeDir: testHomeForWorkspace(workspace), WorkspaceDir: workspace})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func testProviderMap(model, apiKey string) map[string]llm.ProviderConfig {
	return map[string]llm.ProviderConfig{
		"anthropic": {
			Name:           "Anthropic",
			Type:           ProviderAnthropicCompatible,
			BaseURL:        "https://api.anthropic.com",
			APIKey:         apiKey,
			CredentialKind: "api-key",
			TimeoutSeconds: 600,
			Models: map[string]llm.ModelConfig{
				"sonnet": {
					Name:              "Claude Sonnet",
					Model:             model,
					MaxTokens:         4096,
					SupportsStreaming: true,
					SupportsVision:    true,
				},
			},
		},
	}
}

func testNonAnthropicProviders() map[string]llm.ProviderConfig {
	return map[string]llm.ProviderConfig{
		"deepseek": {
			Name:           "deepseek",
			Type:           ProviderOpenAICompatible,
			BaseURL:        "https://api.deepseek.com",
			APIKey:         "sk-deepseek-test",
			CredentialKind: "api-key",
			TimeoutSeconds: 600,
			Models: map[string]llm.ModelConfig{
				"v4-flash": {
					Name:              "v4-flash",
					Model:             "deepseek-v4-flash",
					MaxTokens:         4096,
					SupportsStreaming: true,
					SupportsVision:    true,
				},
			},
		},
		"mimo": {
			Name:           "mimo",
			Type:           ProviderOpenAICompatible,
			BaseURL:        "https://token-plan-cn.xiaomimimo.com/v1/chat/completions",
			APIKey:         "sk-mimo-test",
			CredentialKind: "api-key",
			TimeoutSeconds: 600,
			Models: map[string]llm.ModelConfig{
				"fast": {
					Name:              "MiMo Fast",
					Model:             "mimo-fast",
					MaxTokens:         4096,
					SupportsStreaming: true,
				},
			},
		},
	}
}

func TestDefaultConfigReadsRuntimeSettingsFromEnv(t *testing.T) {
	workspace := t.TempDir()
	useTestHome(t, workspace)
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	defer os.Chdir(previousWD)

	t.Setenv("LEAD_NAME", "captain")
	t.Setenv("TEAM_NAME", "delta")
	t.Setenv("DEFAULT_SKILLS", "alpha, beta")
	t.Setenv("TEAMMATE_WORK_ITERATIONS", "12")
	t.Setenv("TEAMMATE_IDLE_POLL_INTERVAL_SECONDS", "3")
	t.Setenv("TEAMMATE_IDLE_TIMEOUT_SECONDS", "21")
	t.Setenv("GODEX_WEB_TOKEN", "secret-token")
	t.Setenv("GODEX_API_TIMEOUT_SECONDS", "777")
	t.Setenv("GODEX_AGENT_MAX_TURNS", "77")
	t.Setenv("GODEX_AGENT_PROFILE", "coding")
	t.Setenv("GODEX_AGENT_DEFAULT_PROFILE_ACP", "coding")
	t.Setenv("GODEX_AGENT_DEFAULT_PROFILE_CLI", "coding")
	t.Setenv("GODEX_AGENT_DEFAULT_PROFILE_TUI", "coding")
	t.Setenv("GODEX_AGENT_DEFAULT_PROFILE_WEB", "general")
	t.Setenv("GODEX_AGENT_DEFAULT_PROFILE_WEIXIN", "general")
	t.Setenv("GODEX_AGENT_DEFAULT_PROFILE_FEISHU", "general")
	t.Setenv("GODEX_SECURITY_PROFILE", "dev-repair")
	t.Setenv("FEISHU_ENABLED", "true")
	t.Setenv("FEISHU_APP_ID", "cli_app")
	t.Setenv("FEISHU_APP_SECRET", "top-secret")
	t.Setenv("FEISHU_DOMAIN", "feishu")
	t.Setenv("GODEX_WEB_SEARCH_BRAVE_API_KEY", "brave-secret")
	t.Setenv("GODEX_WEB_FETCH_POLICY", "allowlist")
	t.Setenv("GODEX_WEB_FETCH_ALLOWED_DOMAINS", "example.com, *.example.org")
	t.Setenv("GODEX_TOOLS_EXECUTION_MODE", "docker")
	t.Setenv("GODEX_TOOLS_EXECUTION_DOCKER_IMAGE", "golang:1.26")
	t.Setenv("GODEX_TOOLS_EXECUTION_DOCKER_NETWORK", "none")
	t.Setenv("GODEX_TOOLS_EXECUTION_SHELL_ALLOW_PATTERNS", "go test*,rg *")
	t.Setenv("GODEX_TOOLS_EXECUTION_SHELL_DENY_PATTERNS", "curl http://169.254.*")
	t.Setenv("GODEX_SUBAGENT_MAX_BATCH_SIZE", "12")
	t.Setenv("GODEX_SUBAGENT_MAX_CONCURRENT_JOBS", "5")
	t.Setenv("GODEX_SUBAGENT_DEFAULT_MAX_TURNS", "55")
	t.Setenv("GODEX_SUBAGENT_MAX_JOB_TIMEOUT_MS", "123456")
	t.Setenv("GODEX_RUNTIME_RECOVERY_AUTO_RESUME_INTERRUPTED_TURNS", "true")
	t.Setenv("GODEX_RUNTIME_RECOVERY_AUTO_REPAIR_SESSIONS", "false")
	t.Setenv("GODEX_STORAGE_SESSION_BACKEND", "sqlite")
	t.Setenv("GODEX_STORAGE_SQLITE_PATH", filepath.Join(workspace, "session-store.sqlite"))
	t.Setenv("GODEX_BROWSER_ENABLED", "true")
	t.Setenv("GODEX_BROWSER_PATH", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	t.Setenv("WEIXIN_ENABLED", "true")
	t.Setenv("WEIXIN_BASE_URL", "https://ilink.example.com")
	t.Setenv("WEIXIN_CDN_BASE_URL", "https://cdn.example.com")
	t.Setenv("WEIXIN_ACCOUNT_ID", "primary")
	t.Setenv("WEIXIN_ALLOW_FROM", "user-a,user-b")
	t.Setenv("WEIXIN_ROUTE_TAG", "wechat")
	t.Setenv("WEIXIN_LONG_POLL_TIMEOUT_MS", "36000")
	t.Setenv("WEIXIN_PROXY", "http://127.0.0.1:8080")

	cfg := DefaultConfig()
	if cfg.LeadName != "captain" {
		t.Fatalf("expected lead name %q, got %q", "captain", cfg.LeadName)
	}
	if cfg.TeamName != "delta" {
		t.Fatalf("expected team name %q, got %q", "delta", cfg.TeamName)
	}
	if got := len(cfg.DefaultSkills); got != 2 || cfg.DefaultSkills[0] != "alpha" || cfg.DefaultSkills[1] != "beta" {
		t.Fatalf("expected default skills [alpha beta], got %#v", cfg.DefaultSkills)
	}
	if cfg.TeammateWorkLimit != 12 {
		t.Fatalf("expected teammate work limit %d, got %d", 12, cfg.TeammateWorkLimit)
	}
	if cfg.TeammatePollEvery != 3*time.Second {
		t.Fatalf("expected poll interval %s, got %s", 3*time.Second, cfg.TeammatePollEvery)
	}
	if cfg.TeammateIdleFor != 21*time.Second {
		t.Fatalf("expected idle timeout %s, got %s", 21*time.Second, cfg.TeammateIdleFor)
	}
	if cfg.WebToken != "secret-token" {
		t.Fatalf("expected web token to be loaded, got %q", cfg.WebToken)
	}
	if cfg.APITimeoutSeconds != 777 {
		t.Fatalf("expected API timeout %d, got %d", 777, cfg.APITimeoutSeconds)
	}
	if cfg.MaxTurns != 77 {
		t.Fatalf("expected agent max turns %d, got %d", 77, cfg.MaxTurns)
	}
	if cfg.AgentProfile != AgentProfileCoding {
		t.Fatalf("expected agent profile %q, got %q", AgentProfileCoding, cfg.AgentProfile)
	}
	if got := cfg.DefaultAgentProfileForChannel("acp"); got != AgentProfileCoding {
		t.Fatalf("expected acp default profile %q, got %q", AgentProfileCoding, got)
	}
	if got := cfg.DefaultAgentProfileForChannel("web"); got != AgentProfileGeneral {
		t.Fatalf("expected web default profile %q, got %q", AgentProfileGeneral, got)
	}
	if cfg.Security.Profile != "dev/repair" {
		t.Fatalf("expected security profile alias to normalize to dev/repair, got %q", cfg.Security.Profile)
	}
	if !cfg.Feishu.Enabled {
		t.Fatalf("expected feishu to be enabled from env")
	}
	if cfg.Tools.Execution.Mode != "docker" || cfg.Tools.Execution.DockerImage != "golang:1.26" || cfg.Tools.Execution.DockerNetwork != "none" {
		t.Fatalf("unexpected execution config: %#v", cfg.Tools.Execution)
	}
	if len(cfg.Tools.Execution.ShellAllowPatterns) != 2 || cfg.Tools.Execution.ShellDenyPatterns[0] != "curl http://169.254.*" {
		t.Fatalf("unexpected shell execution patterns: %#v", cfg.Tools.Execution)
	}
	if cfg.Tools.Subagent.MaxBatchSize != 12 || cfg.Tools.Subagent.MaxConcurrentJobs != 5 || cfg.Tools.Subagent.DefaultMaxTurns != 55 || cfg.Tools.Subagent.MaxJobTimeoutMs != 123456 {
		t.Fatalf("unexpected subagent config: %#v", cfg.Tools.Subagent)
	}
	if !cfg.Runtime.Recovery.AutoResumeInterruptedTurns {
		t.Fatalf("expected runtime recovery auto-resume from env")
	}
	if cfg.Runtime.Recovery.AutoRepairSessions {
		t.Fatalf("expected runtime recovery auto-repair to be disabled from env")
	}
	if cfg.Storage.SessionBackend != "sqlite" || cfg.Storage.SQLitePath == "" {
		t.Fatalf("expected storage backend env overrides, got %+v", cfg.Storage)
	}
	if cfg.Feishu.AppID != "cli_app" || cfg.Feishu.AppSecret != "top-secret" || cfg.Feishu.Domain != "feishu" {
		t.Fatalf("unexpected feishu config: %#v", cfg.Feishu)
	}
	if cfg.Tools.WebSearch.BraveAPIKey != "brave-secret" {
		t.Fatalf("expected brave api key from env, got %q", cfg.Tools.WebSearch.BraveAPIKey)
	}
	if cfg.Tools.WebFetch.Policy != "allowlist" || len(cfg.Tools.WebFetch.AllowedDomains) != 2 {
		t.Fatalf("unexpected web fetch config: %#v", cfg.Tools.WebFetch)
	}
	if !cfg.Tools.Browser.Enabled {
		t.Fatalf("expected browser to be enabled from env")
	}
	if cfg.Tools.Browser.BrowserPath != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" {
		t.Fatalf("expected browser path from env, got %q", cfg.Tools.Browser.BrowserPath)
	}
	if !cfg.Weixin.Enabled {
		t.Fatalf("expected weixin to be enabled from env")
	}
	if cfg.Weixin.BaseURL != "https://ilink.example.com" || cfg.Weixin.CDNBaseURL != "https://cdn.example.com" {
		t.Fatalf("unexpected weixin URLs: %#v", cfg.Weixin)
	}
	if cfg.Weixin.AccountID != "primary" || cfg.Weixin.RouteTag != "wechat" || cfg.Weixin.LongPollTimeoutMs != 36000 {
		t.Fatalf("unexpected weixin config: %#v", cfg.Weixin)
	}
	if len(cfg.Weixin.AllowFrom) != 2 || cfg.Weixin.AllowFrom[0] != "user-a" || cfg.Weixin.AllowFrom[1] != "user-b" {
		t.Fatalf("unexpected weixin allow_from: %#v", cfg.Weixin.AllowFrom)
	}
}

func TestDefaultConfigIncludesTempDir(t *testing.T) {
	workspace := t.TempDir()
	useTestHome(t, workspace)
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	defer os.Chdir(previousWD)

	cfg := DefaultConfig()
	want := filepath.Join(cfg.HomeDir, "tmp")
	if cfg.TempDir != want {
		t.Fatalf("expected temp dir %q, got %q", want, cfg.TempDir)
	}
	if got := cfg.MemoryDir; got != filepath.Join(cfg.HomeDir, "memory") {
		t.Fatalf("expected memory dir %q, got %q", filepath.Join(cfg.HomeDir, "memory"), got)
	}
	if got := cfg.RulesDir; got != filepath.Join(cfg.HomeDir, "rules") {
		t.Fatalf("expected rules dir %q, got %q", filepath.Join(cfg.HomeDir, "rules"), got)
	}
	if got := cfg.SkillsDir; got != filepath.Join(cfg.HomeDir, "skills") {
		t.Fatalf("expected skills dir %q, got %q", filepath.Join(cfg.HomeDir, "skills"), got)
	}
	if got := cfg.MCPConfigPath; got != filepath.Join(cfg.HomeDir, "mcp.json") {
		t.Fatalf("expected MCP config path %q, got %q", filepath.Join(cfg.HomeDir, "mcp.json"), got)
	}
	if got := cfg.TodosDir; got != filepath.Join(cfg.HomeDir, "todos") {
		t.Fatalf("expected todos dir %q, got %q", filepath.Join(cfg.HomeDir, "todos"), got)
	}
}

func TestEnsureDirsCreatesTempDir(t *testing.T) {
	workspace := t.TempDir()
	cfg := &Config{
		WorkspaceDir:   workspace,
		StateDir:       filepath.Join(workspace, ".godex"),
		TeamDir:        filepath.Join(workspace, ".godex", ".team"),
		TasksDir:       filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:       filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:      filepath.Join(workspace, ".godex", "memory"),
		RulesDir:       filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:      filepath.Join(workspace, ".godex", "skills"),
		TempDir:        filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir: filepath.Join(workspace, ".godex", ".transcripts"),
	}

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if _, err := os.Stat(cfg.TempDir); err != nil {
		t.Fatalf("expected temp dir %s to exist: %v", cfg.TempDir, err)
	}
	if _, err := os.Stat(cfg.MemoryDir); err != nil {
		t.Fatalf("expected memory dir %s to exist: %v", cfg.MemoryDir, err)
	}
	if _, err := os.Stat(cfg.TodosDir); err != nil {
		t.Fatalf("expected todos dir %s to exist: %v", cfg.TodosDir, err)
	}
	if _, err := os.Stat(cfg.RulesDir); err != nil {
		t.Fatalf("expected rules dir %s to exist: %v", cfg.RulesDir, err)
	}
}

func TestDefaultConfigDoesNotEnableMissingDefaultSkills(t *testing.T) {
	workspace := t.TempDir()
	useTestHome(t, workspace)
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	defer os.Chdir(previousWD)

	cfg := DefaultConfig()
	if len(cfg.DefaultSkills) != 0 {
		t.Fatalf("expected no default skills by default, got %#v", cfg.DefaultSkills)
	}
}

func TestDefaultConfigDoesNotInjectAnthropicProvider(t *testing.T) {
	workspace := t.TempDir()
	useTestHome(t, workspace)
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	defer os.Chdir(previousWD)

	cfg := DefaultConfig()
	if _, ok := cfg.LLMProviders["anthropic"]; ok {
		t.Fatalf("expected default config not to inject anthropic provider: %#v", cfg.LLMProviders["anthropic"])
	}
}

func TestManagerReloadDoesNotMergeDefaultProvidersIntoConfiguredProviders(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"api.default_model":  "deepseek.v4-flash",
			"api.providers":      testNonAnthropicProviders(),
			"api.model_strategy": map[string]any{"type": "fallback", "candidates": []any{map[string]any{"provider": "deepseek", "model": "v4-flash"}, map[string]any{"provider": "mimo", "model": "fast"}}},
		},
	})
	if err != nil {
		t.Fatalf("update providers: %v", err)
	}
	if _, ok := manager.Current().LLMProviders["anthropic"]; ok {
		t.Fatalf("did not expect anthropic before reload")
	}

	if _, err := manager.ReloadFromDisk(t.Context()); err != nil {
		t.Fatalf("reload from disk: %v", err)
	}
	providers := manager.Current().LLMProviders
	if _, ok := providers["anthropic"]; ok {
		t.Fatalf("reload merged unexpected default anthropic provider: %#v", providers["anthropic"])
	}
	if _, ok := providers["deepseek"]; !ok {
		t.Fatalf("expected deepseek provider after reload: %#v", providers)
	}
	if _, ok := providers["mimo"]; !ok {
		t.Fatalf("expected mimo provider after reload: %#v", providers)
	}
}

func TestDefaultConfigIncludesSubagentWorkspacePolicy(t *testing.T) {
	workspace := t.TempDir()
	useTestHome(t, workspace)
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	defer os.Chdir(previousWD)

	cfg := DefaultConfig()
	if cfg.Tools.Subagent.ReadOnlyIsolation != "shared_readonly" {
		t.Fatalf("expected readonly shared isolation default, got %#v", cfg.Tools.Subagent)
	}
	if cfg.Tools.Subagent.GitDirtyIsolation != "dirty_overlay" {
		t.Fatalf("expected dirty git overlay default, got %#v", cfg.Tools.Subagent)
	}
	if cfg.Tools.Subagent.NonGitWriteIsolation != "copy_snapshot" {
		t.Fatalf("expected non-git write fallback to copy_snapshot, got %#v", cfg.Tools.Subagent)
	}
	if cfg.Tools.Subagent.WorkspaceTTLHours != 168 {
		t.Fatalf("expected workspace TTL 168h, got %#v", cfg.Tools.Subagent)
	}
}

func TestDefaultConfigIncludesStoragePolicy(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Storage.TmpTTLHours != 72 {
		t.Fatalf("expected tmp TTL default, got %#v", cfg.Storage)
	}
	if cfg.Storage.ArtifactTTLHours != 168 {
		t.Fatalf("expected artifact TTL default, got %#v", cfg.Storage)
	}
	if !cfg.Storage.BrowserCacheAutoClean {
		t.Fatalf("expected browser cache auto clean enabled, got %#v", cfg.Storage)
	}
	if cfg.Storage.BrowserCacheMaxMB != 256 {
		t.Fatalf("expected browser cache max MB default, got %#v", cfg.Storage)
	}
	if cfg.Storage.SessionCheckpointKeepLatest != 20 {
		t.Fatalf("expected checkpoint keep latest default, got %#v", cfg.Storage)
	}
	if cfg.Storage.SessionCheckpointTTLHours != 168 {
		t.Fatalf("expected checkpoint TTL default, got %#v", cfg.Storage)
	}
	if !cfg.Storage.SessionCheckpointAutoPrune {
		t.Fatalf("expected checkpoint auto prune enabled, got %#v", cfg.Storage)
	}
	if cfg.Storage.SessionBackend != "json" {
		t.Fatalf("expected json session backend default, got %#v", cfg.Storage)
	}
	if cfg.Storage.SQLitePath != "" {
		t.Fatalf("expected empty sqlite path default, got %#v", cfg.Storage)
	}
}

func TestManagerUpdateStorageConfig(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"storage.tmp_ttl_hours":                  24,
			"storage.artifact_ttl_hours":             336,
			"storage.browser_cache_auto_clean":       false,
			"storage.browser_cache_max_mb":           128,
			"storage.session_checkpoint_keep_latest": 7,
			"storage.session_checkpoint_ttl_hours":   48,
			"storage.session_checkpoint_auto_prune":  false,
			"storage.session_backend":                "sqlite",
			"storage.sqlite_path":                    filepath.Join(workspace, "sessions.sqlite"),
		},
	})
	if err != nil {
		t.Fatalf("update storage config: %v", err)
	}

	if got := manager.Current().Storage.TmpTTLHours; got != 24 {
		t.Fatalf("expected effective tmp TTL, got %d", got)
	}
	if got := view.StoredValues["storage.artifact_ttl_hours"]; got != 336 {
		t.Fatalf("expected stored artifact TTL, got %#v", got)
	}
	if got := view.EffectiveValues["storage.browser_cache_auto_clean"]; got != false {
		t.Fatalf("expected effective browser auto clean false, got %#v", got)
	}
	if got := manager.Current().Storage.SessionCheckpointKeepLatest; got != 7 {
		t.Fatalf("expected checkpoint keep latest, got %d", got)
	}
	if got := manager.Current().Storage.SessionCheckpointAutoPrune; got {
		t.Fatalf("expected checkpoint auto prune disabled")
	}
	if got := manager.Current().Storage.SessionBackend; got != "sqlite" {
		t.Fatalf("expected sqlite session backend, got %q", got)
	}
	if got := view.EffectiveValues["storage.sqlite_path"]; got == "" {
		t.Fatalf("expected effective sqlite path, got %#v", got)
	}
}

func TestManagerAppliesYAMLDotEnvAndEnvPrecedence(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte(strings.Join([]string{
		"LEAD_NAME=dotenv-lead",
	}, "\n")), 0644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	manager := newTestManager(t, workspace)
	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"team.lead_name": "from-yaml",
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if got := manager.Current().LeadName; got != "dotenv-lead" {
		t.Fatalf("expected dotenv to win over yaml, got %q", got)
	}
	if got := view.Fields["team.lead_name"].Source; got != SourceDotEnv {
		t.Fatalf("expected team.lead_name source dotenv, got %q", got)
	}
}

func TestLegacyFlatAPIFieldsAreNotSupported(t *testing.T) {
	legacyPaths := []string{
		"api.key",
		"api.model",
		"api.base_url",
		"api.max_tokens",
		"api.profiles",
		"api.fallback_profiles",
	}

	for _, section := range baseSchema() {
		for _, field := range section.Fields {
			for _, legacyPath := range legacyPaths {
				if field.Path == legacyPath {
					t.Fatalf("legacy field %q should not be in settings schema", legacyPath)
				}
			}
		}
	}

	rendered, err := renderConfigTemplate(defaultConfigFile())
	if err != nil {
		t.Fatalf("render config template: %v", err)
	}
	template := string(rendered)
	for _, legacySnippet := range []string{
		"\n  key:",
		"\n  model:",
		"\n  base_url:",
		"\n  max_tokens:",
		"\n  profiles:",
		"\n  fallback_profiles:",
	} {
		if strings.Contains(template, legacySnippet) {
			t.Fatalf("template still contains legacy API snippet %q:\n%s", legacySnippet, template)
		}
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "godex.yaml"), []byte("api:\n  model: stale\n"), 0644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	if _, err := NewManager(Options{WorkspaceDir: workspace, HomeDir: workspace}); err == nil {
		t.Fatal("expected legacy api.model to be rejected as an unknown field")
	}
}

func TestManagerUpdateRevealAndDoctor(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"api.providers":                   testProviderMap("claude-sonnet-4-20250514", "sk-ant-test"),
			"web.token":                       "shared-secret",
			"logging.level":                   "debug",
			"tools.web_search.brave.api_key":  "brave-key",
			"tools.web_fetch.allowed_domains": []string{"example.com"},
			"tools.web_fetch.policy":          "allowlist",
			"media.moonshot.api_key":          "moonshot-key",
			"channels.feishu.enabled":         true,
			"channels.feishu.app_id":          "app-id",
			"channels.feishu.app_secret":      "app-secret",
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	revealed, err := manager.Reveal("tools.web_search.brave.api_key")
	if err != nil {
		t.Fatalf("reveal brave key: %v", err)
	}
	if revealed != "brave-key" {
		t.Fatalf("unexpected revealed brave key %q", revealed)
	}
	revealed, err = manager.Reveal("media.moonshot.api_key")
	if err != nil {
		t.Fatalf("reveal moonshot key: %v", err)
	}
	if revealed != "moonshot-key" {
		t.Fatalf("unexpected revealed moonshot key %q", revealed)
	}

	report := manager.Doctor()
	if report.Errors != 0 {
		t.Fatalf("expected doctor success, got %#v", report)
	}
}

func TestManagerSchemaIncludesWeixinSection(t *testing.T) {
	manager := newTestManager(t, t.TempDir())

	var found bool
	for _, section := range manager.Schema() {
		if section.ID == "channels-weixin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected channels-weixin schema section")
	}
}

func TestManagerSchemaIncludesToolsPermissionsSection(t *testing.T) {
	manager := newTestManager(t, t.TempDir())

	var found bool
	for _, section := range manager.Schema() {
		if section.ID == "tools-permissions" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected tools-permissions schema section")
	}
}

func TestManagerSchemaIncludesHistorySearchSection(t *testing.T) {
	manager := newTestManager(t, t.TempDir())

	var found bool
	for _, section := range manager.Schema() {
		if section.ID == "tools-history-search" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected tools-history-search schema section")
	}
}

func TestManagerSchemaIncludesToolsExecutionSection(t *testing.T) {
	manager := newTestManager(t, t.TempDir())

	var found bool
	for _, section := range manager.Schema() {
		if section.ID == "tools-execution" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected tools-execution schema section")
	}
}

func TestManagerSchemaIncludesToolsSubagentSection(t *testing.T) {
	manager := newTestManager(t, t.TempDir())

	var found bool
	for _, section := range manager.Schema() {
		if section.ID == "tools-subagent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected tools-subagent schema section")
	}
}

func TestDoctorWarnsWhenWeixinEnabledButNotSetup(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)
	if _, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"channels.weixin.enabled":  true,
			"channels.weixin.base_url": "https://ilinkai.weixin.qq.com",
		},
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	var sawSetup, sawAllowFrom bool
	for _, check := range report.Checks {
		switch check.Code {
		case "weixin_not_setup":
			sawSetup = true
		case "weixin_allow_from_empty":
			sawAllowFrom = true
		}
	}
	if !sawSetup {
		t.Fatalf("expected weixin_not_setup in doctor report: %#v", report.Checks)
	}
	if !sawAllowFrom {
		t.Fatalf("expected weixin_allow_from_empty in doctor report: %#v", report.Checks)
	}
}

func TestDoctorWarnsForMissingDefaultSkills(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)
	if _, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"team.default_skills": []string{"alpha", "beta", "alpha"},
		},
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	var count int
	for _, check := range report.Checks {
		if check.Code == "default_skill_missing" {
			count++
			if !strings.Contains(check.Message, "Default skill") {
				t.Fatalf("unexpected default skill warning: %#v", check)
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected warnings for two unique missing default skills, got %d: %#v", count, report.Checks)
	}
}

func TestManagerReloadFromDiskAppliesConfig(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)
	meta := manager.Meta()
	data, err := os.ReadFile(meta.HomeConfigFile)
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	replaced := strings.Replace(string(data), "lead_name: lead", "lead_name: disk-lead", 1)
	if replaced == string(data) {
		t.Fatalf("expected default lead_name in config")
	}
	if err := os.WriteFile(meta.HomeConfigFile, []byte(replaced), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	applied := false
	manager.SetApplier(func(ctx context.Context, oldCfg, newCfg *Config) ApplyReport {
		_ = ctx
		if oldCfg.LeadName == "lead" && newCfg.LeadName == "disk-lead" {
			applied = true
		}
		return ApplyReport{StorageStatus: StorageStatusSaved, RuntimeStatus: RuntimeStatusApplied}
	})

	view, err := manager.ReloadFromDisk(t.Context())
	if err != nil {
		t.Fatalf("reload from disk: %v", err)
	}
	if !applied {
		t.Fatal("expected reload applier to run")
	}
	if got := manager.Current().LeadName; got != "disk-lead" {
		t.Fatalf("expected current config to reload from disk, got %q", got)
	}
	if got := view.LastApply.RuntimeStatus; got != RuntimeStatusApplied {
		t.Fatalf("expected applied reload status, got %q", got)
	}
}

func TestManagerDoctorWarnsForWebFallbackOnlyAndPrivateHosts(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.web_search.enabled":            true,
			"tools.web_fetch.allow_private_hosts": true,
			"tools.browser.enabled":               true,
			"tools.browser.allow_private_hosts":   true,
			"media.audio.enabled":                 true,
			"media.video.enabled":                 true,
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	var sawFallback, sawFetchPrivate, sawBrowserPrivate, sawWhisperMissing, sawVideoInfo bool
	for _, check := range report.Checks {
		switch check.Code {
		case "web_search_fallback_only":
			sawFallback = true
		case "web_fetch_private_hosts_enabled":
			sawFetchPrivate = true
		case "browser_private_hosts_enabled":
			sawBrowserPrivate = true
		case "whisper_model_missing", "whisper_model_not_found":
			sawWhisperMissing = true
		case "video_summary_local_pipeline":
			sawVideoInfo = true
		}
	}
	if !sawFallback || !sawFetchPrivate || !sawBrowserPrivate || !sawWhisperMissing || !sawVideoInfo {
		t.Fatalf("expected doctor warnings, got %#v", report.Checks)
	}
}

func TestManagerDoctorDoesNotWarnFallbackOnlyWhenBrowserProviderEnabled(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.web_search.enabled":        true,
			"tools.web_search.provider_order": []string{"browser", "duckduckgo"},
			"tools.browser.enabled":           true,
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	for _, check := range report.Checks {
		if check.Code == "web_search_fallback_only" {
			t.Fatalf("did not expect fallback-only warning when browser provider is enabled: %#v", report.Checks)
		}
	}
}

func TestManagerLoadsWebSearchBrowserProviderConfig(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.web_search.provider_order":                                 []string{"browser", "duckduckgo"},
			"tools.web_search.browser.engine":                                 "bing",
			"tools.web_search.browser.engine_fallback":                        []string{"brave", "duckduckgo"},
			"tools.web_search.browser.engines.bing.search_url_template":       "https://www.bing.com/search?q={{query}}",
			"tools.web_search.browser.engines.bing.blocked_hosts":             []string{"bing.com"},
			"tools.web_search.browser.engines.bing.result_container_selector": "li.b_algo",
			"tools.web_search.browser.engines.bing.result_link_selector":      "h2 a",
			"tools.web_search.browser.engines.bing.result_snippet_selector":   ".b_caption p",
			"tools.web_search.browser.wait_network_idle_ms":                   900,
			"tools.web_search.browser.wait_after_load_ms":                     250,
			"tools.web_search.browser.max_scrolls":                            2,
			"tools.web_search.browser.result_timeout_seconds":                 12,
			"tools.web_search.browser.preferred_hosts":                        []string{"learn.microsoft.com"},
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	cfg := manager.Current().Tools.WebSearch.Browser
	bing := cfg.Engines["bing"]
	if cfg.Engine != "bing" || bing.SearchURLTemplate != "https://www.bing.com/search?q={{query}}" {
		t.Fatalf("unexpected browser search config: %#v", cfg)
	}
	if len(cfg.EngineFallback) != 2 || cfg.EngineFallback[0] != "brave" || cfg.EngineFallback[1] != "duckduckgo" {
		t.Fatalf("unexpected engine fallback: %#v", cfg.EngineFallback)
	}
	if cfg.WaitNetworkIdleMS != 900 || cfg.WaitAfterLoadMS != 250 || cfg.MaxScrolls != 2 || cfg.ResultTimeoutSeconds != 12 {
		t.Fatalf("unexpected browser search timing config: %#v", cfg)
	}
	if len(bing.BlockedHosts) != 1 || bing.BlockedHosts[0] != "bing.com" {
		t.Fatalf("unexpected blocked hosts: %#v", bing.BlockedHosts)
	}
	if len(cfg.PreferredHosts) != 1 || cfg.PreferredHosts[0] != "learn.microsoft.com" {
		t.Fatalf("unexpected preferred hosts: %#v", cfg.PreferredHosts)
	}
	if bing.ResultContainerSelector != "li.b_algo" || bing.ResultLinkSelector != "h2 a" || bing.ResultSnippetSelector != ".b_caption p" {
		t.Fatalf("unexpected selector config: %#v", bing)
	}
	if got := view.EffectiveValues["tools.web_search.browser.engine"]; got != "bing" {
		t.Fatalf("expected effective browser engine, got %#v", got)
	}
}

func TestDefaultConfigIncludesWebSearchBrowserProviderDefaults(t *testing.T) {
	cfg := DefaultConfig().Tools.WebSearch.Browser
	if cfg.Engine != "duckduckgo" {
		t.Fatalf("expected duckduckgo browser engine, got %q", cfg.Engine)
	}
	if len(cfg.EngineFallback) == 0 {
		t.Fatalf("expected default engine fallback, got %#v", cfg.EngineFallback)
	}
	duckduckgo := cfg.Engines["duckduckgo"]
	if !strings.Contains(duckduckgo.SearchURLTemplate, "{{query}}") {
		t.Fatalf("expected query placeholder in template, got %q", duckduckgo.SearchURLTemplate)
	}
	if cfg.WaitNetworkIdleMS <= 0 || cfg.WaitAfterLoadMS <= 0 || cfg.ResultTimeoutSeconds <= 0 {
		t.Fatalf("expected positive browser search timeouts, got %#v", cfg)
	}
	if len(duckduckgo.BlockedHosts) == 0 {
		t.Fatalf("expected default blocked hosts, got %#v", duckduckgo.BlockedHosts)
	}
}

func TestManagerDoctorWarnsWhenInteractiveApprovalsDisabled(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.permissions.interactive_approval_enabled": false,
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	for _, check := range report.Checks {
		if check.Code == "permissions_interactive_approval_disabled" {
			return
		}
	}
	t.Fatalf("expected permissions_interactive_approval_disabled warning, got %#v", report.Checks)
}

func TestManagerDoctorWarnsWhenTrustedPathPrefixTooBroad(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.permissions.trusted_path_prefixes": []string{"."},
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	for _, check := range report.Checks {
		if check.Code == "permissions_trusted_path_prefix_broad" {
			return
		}
	}
	t.Fatalf("expected permissions_trusted_path_prefix_broad warning, got %#v", report.Checks)
}

func TestManagerDoctorWarnsWhenInteractiveApprovalsUseYOLOMode(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.permissions.interactive_approval_mode": "yolo",
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	report := manager.Doctor()
	for _, check := range report.Checks {
		if check.Code == "permissions_interactive_approval_yolo_mode" {
			return
		}
	}
	t.Fatalf("expected permissions_interactive_approval_yolo_mode warning, got %#v", report.Checks)
}

func TestManagerSchemaIncludesBuiltInToolSections(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	schema := manager.Schema()
	sections := make(map[string]SectionSchema, len(schema))
	for _, section := range schema {
		sections[section.ID] = section
	}

	for _, id := range []string{"tools-web-search", "tools-web-fetch", "tools-glob", "tools-browser", "tools-history-search", "media-moonshot", "media-document", "media-ocr", "media-audio", "media-video"} {
		section, ok := sections[id]
		if !ok {
			t.Fatalf("expected schema section %q to exist", id)
		}
		if len(section.Fields) == 0 {
			t.Fatalf("expected schema section %q to expose fields", id)
		}
	}
}

func TestManagerUpdateHistorySearchConfig(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	_, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"tools.history_search.enabled":                           true,
			"tools.history_search.auto.enabled":                      true,
			"tools.history_search.auto.max_per_turn":                 2,
			"tools.history_search.auto.default_scope":                "session_archive",
			"tools.history_search.auto.allow_archive_on_clear":       false,
			"tools.history_search.auto.allow_archive_on_compact":     true,
			"tools.history_search.auto.allow_all_archives_automatic": false,
			"tools.history_search.auto.min_score":                    5,
			"tools.history_search.cues.explicit":                     []string{"上次", "聊天记录"},
			"tools.history_search.cues.implicit":                     []string{"定过"},
			"tools.history_search.blocks.session_sources":            []string{"cron", "review"},
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	cfg := manager.Current()
	if !cfg.Tools.History.Enabled || !cfg.Tools.History.Auto.Enabled {
		t.Fatalf("expected history search to be enabled, got %#v", cfg.Tools.History)
	}
	if cfg.Tools.History.Auto.MaxPerTurn != 2 || cfg.Tools.History.Auto.DefaultScope != "session_archive" || cfg.Tools.History.Auto.MinScore != 5 {
		t.Fatalf("unexpected history auto config: %#v", cfg.Tools.History.Auto)
	}
	if cfg.Tools.History.Auto.AllowArchiveOnClear {
		t.Fatalf("expected allow_archive_on_clear to be false")
	}
	if len(cfg.Tools.History.Cues.Explicit) != 2 || len(cfg.Tools.History.Blocks.SessionSources) != 2 {
		t.Fatalf("unexpected history cues/blocks: %#v", cfg.Tools.History)
	}
}

func TestManagerSupportsProviderModelStrategyConfig(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	providers := map[string]any{
		"anthropic": map[string]any{
			"name":            "Anthropic",
			"type":            "anthropic",
			"base_url":        "https://api.anthropic.com",
			"api_key":         "sk-ant-test",
			"timeout_seconds": 321,
			"models": map[string]any{
				"sonnet": map[string]any{
					"name":             "Claude Sonnet",
					"model":            "claude-sonnet-test",
					"max_tokens":       8192,
					"reasoning_effort": "high",
				},
			},
		},
		"openai": map[string]any{
			"name":     "OpenAI",
			"type":     "openai",
			"base_url": "https://api.openai.example/v1",
			"api_key":  "sk-openai-test",
			"models": map[string]any{
				"gpt": map[string]any{
					"model":           "gpt-test",
					"supports_vision": true,
				},
			},
		},
	}
	strategy := map[string]any{
		"type": "fallback",
		"candidates": []any{
			map[string]any{"provider": "anthropic", "model": "sonnet"},
			map[string]any{"provider": "openai", "model": "gpt"},
		},
	}

	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"api.default_model":  "anthropic.sonnet",
			"api.providers":      providers,
			"api.model_strategy": strategy,
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	cfg := manager.Current()
	if cfg.DefaultModelRef != "anthropic.sonnet" || cfg.DefaultProfileID != "anthropic.sonnet" {
		t.Fatalf("unexpected default model ids: %q %q", cfg.DefaultModelRef, cfg.DefaultProfileID)
	}
	if got := cfg.Model; got != "claude-sonnet-test" {
		t.Fatalf("expected provider model to win, got %q", got)
	}
	if got := cfg.ReasoningEffort; got != "high" {
		t.Fatalf("expected provider reasoning effort to win, got %q", got)
	}
	if got := cfg.APIKey; got != "sk-ant-test" {
		t.Fatalf("expected provider key to win, got %q", got)
	}
	if _, ok := cfg.ModelProfileByID("openai.gpt"); !ok {
		t.Fatalf("expected openai.gpt profile to be exposed")
	}
	candidates := cfg.StrategyModelProfiles(cfg.DefaultProfileID)
	if len(candidates) != 2 || candidates[0].ID != "anthropic.sonnet" || candidates[1].ID != "openai.gpt" {
		t.Fatalf("unexpected strategy candidates: %#v", candidates)
	}
	if candidates[0].ReasoningEffort != "high" {
		t.Fatalf("expected strategy candidate reasoning effort, got %#v", candidates[0])
	}
	if candidates[1].ReasoningEffort != "" {
		t.Fatalf("expected unset candidate reasoning effort to stay unset, got %#v", candidates[1])
	}
	if text := strings.TrimSpace(asString(view.EffectiveValues["api.providers"])); text == "" || !strings.Contains(text, "********") {
		t.Fatalf("expected masked providers in settings view, got %#v", view.EffectiveValues["api.providers"])
	}
	revealed, err := manager.Reveal("api.providers")
	if err != nil {
		t.Fatalf("reveal providers: %v", err)
	}
	if !strings.Contains(revealed, "sk-openai-test") {
		t.Fatalf("expected reveal to include provider secret, got %q", revealed)
	}
}

func TestStrategyModelProfilesKeepsCustomPrimaryBeforeStrategyCandidates(t *testing.T) {
	cfg := &Config{
		APIKey:            "sk-default",
		Model:             "claude-sonnet-test",
		BaseURL:           "https://api.anthropic.example",
		MaxTokens:         4096,
		APITimeoutSeconds: 60,
		DefaultProfileID:  "anthropic.sonnet",
		LLMProviders: map[string]llm.ProviderConfig{
			"anthropic": {
				ID:      "anthropic",
				Name:    "Anthropic",
				Type:    ProviderAnthropicCompatible,
				BaseURL: "https://api.anthropic.example",
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
			Type: "fallback",
			Candidates: []llm.ModelRef{
				{Provider: "anthropic", Model: "sonnet"},
			},
		},
		ModelProfiles: map[string]ModelProfileConfig{
			"anthropic.sonnet": {
				ID:       "anthropic.sonnet",
				Provider: ProviderAnthropicCompatible,
				Model:    "claude-sonnet-test",
				BaseURL:  "https://api.anthropic.example",
				APIKey:   "sk-ant",
			},
			"openai-codex": {
				ID:       "openai-codex",
				Name:     "OpenAI Codex / Codex",
				Provider: ProviderOpenAICodex,
				Model:    "gpt-5.5",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "codex-token",
			},
		},
	}

	profiles := cfg.StrategyModelProfiles("openai-codex")
	if len(profiles) != 2 {
		t.Fatalf("expected selected custom primary plus strategy fallback, got %#v", profiles)
	}
	if profiles[0].ID != "openai-codex" || profiles[0].Model != "gpt-5.5" {
		t.Fatalf("expected custom selected profile first, got %#v", profiles[0])
	}
	if profiles[1].ID != "anthropic.sonnet" || profiles[1].Model != "claude-sonnet-test" {
		t.Fatalf("expected configured fallback second, got %#v", profiles[1])
	}
}

func TestManagerUpdateKeepsEffectiveConfigWhenRuntimeApplyFails(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	oldModel := manager.Current().Model
	manager.SetApplier(func(_ context.Context, oldCfg, newCfg *Config) ApplyReport {
		if oldCfg.Model != oldModel {
			t.Fatalf("expected old model %q, got %q", oldModel, oldCfg.Model)
		}
		if newCfg.Model != "claude-opus-test" {
			t.Fatalf("expected new model to be updated, got %q", newCfg.Model)
		}
		return ApplyReport{
			RuntimeStatus: RuntimeStatusFailed,
			Message:       "runtime apply failed",
			Errors:        []string{"boom"},
		}
	})

	view, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"api.providers": testProviderMap("claude-opus-test", "sk-ant-test"),
		},
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	if got := manager.stored.API.Providers["anthropic"].Models["sonnet"].Model; got != "claude-opus-test" {
		t.Fatalf("expected stored provider model to change, got %#v", got)
	}
	if got := view.EffectiveValues["api.default_model"]; got != manager.Current().DefaultModelRef {
		t.Fatalf("expected effective default model to stay %q, got %#v", manager.Current().DefaultModelRef, got)
	}
	if got := manager.Current().Model; got != oldModel {
		t.Fatalf("expected current model to stay %q, got %q", oldModel, got)
	}
	if got := view.LastApply.StorageStatus; got != StorageStatusSaved {
		t.Fatalf("expected saved storage status, got %q", got)
	}
	if got := view.LastApply.RuntimeStatus; got != RuntimeStatusFailed {
		t.Fatalf("expected failed runtime status, got %q", got)
	}
}

func TestManagerUpdatePreservesCurrentOnWriteFailure(t *testing.T) {
	workspace := t.TempDir()
	manager := newTestManager(t, workspace)

	oldModel := manager.Current().Model
	configDir := filepath.Dir(manager.Meta().FilePath)
	if err := os.Chmod(configDir, 0500); err != nil {
		t.Fatalf("chmod config dir: %v", err)
	}
	defer os.Chmod(configDir, 0700)

	if _, err := manager.Update(t.Context(), UpdateRequest{
		Values: map[string]any{
			"api.providers": testProviderMap("claude-opus-test", "sk-ant-test"),
		},
	}); err == nil {
		t.Fatal("expected write failure")
	}

	if got := manager.Current().Model; got != oldModel {
		t.Fatalf("expected current model to stay %q, got %q", oldModel, got)
	}
	meta := manager.Meta()
	if got := meta.LastApply.StorageStatus; got != StorageStatusSaveFailed {
		t.Fatalf("expected save_failed, got %q", got)
	}
	if got := meta.LastApply.RuntimeStatus; got != RuntimeStatusSkipped {
		t.Fatalf("expected skipped runtime status, got %q", got)
	}
}
