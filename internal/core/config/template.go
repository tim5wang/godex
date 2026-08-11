package config

import (
	"bytes"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

func renderConfigTemplate(file ConfigFile) ([]byte, error) {
	tpl := template.Must(template.New("godex-config").Funcs(template.FuncMap{
		"yamlString": func(v string) string {
			out, err := yaml.Marshal(v)
			if err != nil {
				return "\"\""
			}
			return strings.TrimSpace(string(out))
		},
		"yamlList": func(values []string) string {
			return renderYAMLList(values, 4)
		},
		"yamlListIndent": func(values []string, indent int) string {
			return renderYAMLList(values, indent)
		},
		"yamlValue": func(value any, indent int) string {
			return renderYAMLValue(value, indent)
		},
	}).Parse(configTemplate))

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, file); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderYAMLList(values []string, indent int) string {
	if len(values) == 0 {
		return "[]"
	}
	if indent < 0 {
		indent = 0
	}
	prefix := strings.Repeat(" ", indent)
	var b strings.Builder
	for _, value := range values {
		b.WriteString("\n")
		b.WriteString(prefix)
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(string(mustMarshalYAML(value))))
	}
	return b.String()
}

func mustMarshalYAML(value any) []byte {
	out, err := yaml.Marshal(value)
	if err != nil {
		return []byte("\"\"")
	}
	return bytes.TrimSpace(out)
}

func renderYAMLValue(value any, indent int) string {
	out, err := yaml.Marshal(value)
	if err != nil {
		return "{}"
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "{}"
	}
	if indent < 0 {
		indent = 0
	}
	prefix := strings.Repeat(" ", indent)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return "\n" + strings.Join(lines, "\n")
}

const configTemplate = `# =============================================================================
# GoDex Canonical Configuration — Self-Describing Design Document
# =============================================================================
#
# This file IS the design specification for GoDex's runtime behavior.
# Every section below maps directly to internal types in the codebase:
#   internal/core/config/types.go   — ConfigFile, APISection, AgentSection, ...
#   internal/core/llm/types.go      — ProviderConfig, ModelConfig, Registry
#   internal/core/config/defaults.go — defaultConfigFile()
#
# When GoDex starts, it reads this YAML, overlays .env and process environment
# variables, validates the result, and builds its internal runtime state.
# GoDex code can also REWRITE this file (e.g., from the Web UI /config editor),
# preserving all comments and structure through the template engine.
#
# HOW GODEX READS THIS CONFIG (Self-Understanding Guide):
#   1. config.Manager.NewManager() loads godex.yaml → unmarshals into ConfigFile
#   2. Manager overlays .env file values using godotenv
#   3. Manager overlays OS process environment variables
#   4. The result becomes the "effective config" (Manager.Current())
#   5. llm.NewRegistry(providers, strategy) builds the model routing table
#   6. Agent wiring uses cfg.Tools.*, cfg.Agent.*, etc. to configure tools
#
# CONFIGURATION PRECEDENCE (highest to lowest):
#   process environment > .env file > godex.yaml > hard-coded defaults
#
# Each field below documents its environment variable override name.
# Example: "timeout_seconds: 600" can be overridden with GODEX_API_TIMEOUT_SECONDS=30
#
# QUICK START — the absolute minimum you need:
#   1. Set up at least one provider under api.providers (see examples below)
#   2. Set your API key in .env (recommended) or as api_key_env in the provider
#   3. Run "godex doctor" to verify everything works
#
# Run "godex init --interactive" for a guided configuration wizard.
# Run "godex doctor" to diagnose configuration issues.
#
# This file is safe for GoDex to rewrite. Comments are regenerated on save.
# =============================================================================

api:
  # ===========================================================================
  # LLM PROVIDERS — the heart of GoDex's intelligence.
  # ===========================================================================
  # GoDex uses a provider → model abstraction to support any LLM backend.
  # The internal routing table is built by llm.NewRegistry(providers, strategy).
  #
  # Provider type determines the HTTP protocol GoDex will speak:
  #   anthropic_compatible — Anthropic Messages API   (POST /v1/messages)
  #   openai_compatible    — OpenAI Chat Completions   (POST /v1/chat/completions)
  #   openai_codex         — OpenAI Codex OAuth flow   (device-code + token exchange)
  #
  # Each provider block:
  #   name:             display name shown in UI and logs
  #   type:             one of the three provider types above
  #   base_url:         API endpoint origin (e.g. https://api.anthropic.com)
  #                     Do NOT add trailing /v1 — GoDex appends protocol paths.
  #   api_key:          plaintext API key (⚠️ avoid committing to VCS)
  #   api_key_env:      env var name holding the key (RECOMMENDED for security)
  #   credential_kind:  "api-key" (default) or "oauth"
  #   timeout_seconds:  HTTP request timeout, default 600
  #   models:           map of model_id → ModelConfig (at least one required)
  #
  # Each model block:
  #   name:              human-readable model name
  #   model:             API model string sent to the provider
  #   max_tokens:        max output tokens, default 4096
  #   supports_streaming: SSE streaming, default true
  #   supports_vision:   image input support, auto-detected from model name
  #   reasoning_effort:  optional hint: none|minimal|low|medium|high|xhigh
  #   tags:              string labels for model selection in profiles
  #
  # ── PROVIDER CONFIGURATION EXAMPLES ────────────────────────────────────────
  # Uncomment any block below and edit to enable. Mix and match as needed.
  #
  # ── Anthropic Claude (anthropic_compatible) ──
  # Recommended for: general coding, reasoning, and analysis.
  #   anthropic:
  #     name: Anthropic
  #     type: anthropic_compatible
  #     base_url: https://api.anthropic.com
  #     api_key_env: ANTHROPIC_API_KEY          # put key in .env
  #     models:
  #       sonnet:
  #         name: Claude Sonnet 4
  #         model: claude-sonnet-4-20250514
  #         max_tokens: 8192
  #       opus:
  #         name: Claude Opus 4
  #         model: claude-opus-4-20250514
  #         max_tokens: 8192
  #       haiku:
  #         name: Claude Haiku 4
  #         model: claude-haiku-4-20250514
  #         max_tokens: 4096
  #
  # ── OpenAI GPT (openai_compatible) ──
  # Recommended for: broad task coverage, vision, structured output.
  #   openai:
  #     name: OpenAI
  #     type: openai_compatible
  #     base_url: https://api.openai.com
  #     api_key_env: OPENAI_API_KEY
  #     models:
  #       gpt4o:
  #         name: GPT-4o
  #         model: gpt-4o
  #         max_tokens: 16384
  #       gpt4mini:
  #         name: GPT-4o Mini
  #         model: gpt-4o-mini
  #         max_tokens: 16384
  #       o3:
  #         name: o3
  #         model: o3
  #         max_tokens: 16384
  #
  # ── DeepSeek (openai_compatible) ──
  # Recommended for: cost-effective reasoning, Chinese-language tasks.
  #   deepseek:
  #     name: DeepSeek
  #     type: openai_compatible
  #     base_url: https://api.deepseek.com
  #     api_key_env: DEEPSEEK_API_KEY
  #     models:
  #       v3:
  #         name: DeepSeek V3
  #         model: deepseek-chat
  #         max_tokens: 8192
  #       r1:
  #         name: DeepSeek R1
  #         model: deepseek-reasoner
  #         max_tokens: 8192
  #
  # ── Ollama / Local LLM (openai_compatible) ──
  # Recommended for: offline work, privacy, zero cost.
  # Requires a running Ollama server (e.g. "ollama serve").
  #   ollama:
  #     name: Ollama
  #     type: openai_compatible
  #     base_url: http://localhost:11434
  #     api_key: ollama                          # dummy key, not sent
  #     models:
  #       llama3:
  #         name: Llama 3
  #         model: llama3
  #         max_tokens: 4096
  #       qwen3:
  #         name: Qwen 3
  #         model: qwen3
  #         max_tokens: 8192
  #
  # ── OpenRouter (openai_compatible) ──
  # Recommended for: multi-model access through a single API.
  # Model string format: provider/model-name (e.g. anthropic/claude-sonnet-4)
  #   openrouter:
  #     name: OpenRouter
  #     type: openai_compatible
  #     base_url: https://openrouter.ai/api
  #     api_key_env: OPENROUTER_API_KEY
  #     models:
  #       sonnet:
  #         name: Claude Sonnet via OpenRouter
  #         model: anthropic/claude-sonnet-4
  #         max_tokens: 8192
  #       gpt4o:
  #         name: GPT-4o via OpenRouter
  #         model: openai/gpt-4o
  #         max_tokens: 16384
  #
  # ── Groq (openai_compatible) ──
  # Recommended for: ultra-fast inference with open-source models.
  #   groq:
  #     name: Groq
  #     type: openai_compatible
  #     base_url: https://api.groq.com/openai
  #     api_key_env: GROQ_API_KEY
  #     models:
  #       llama3:
  #         name: Llama 3 70B
  #         model: llama3-70b-8192
  #         max_tokens: 8192
  #
  # ── Moonshot / Kimi (openai_compatible) ──
  # Recommended for: Chinese-language tasks, long-context processing.
  #   moonshot:
  #     name: Moonshot
  #     type: openai_compatible
  #     base_url: https://api.moonshot.cn
  #     api_key_env: MOONSHOT_API_KEY
  #     models:
  #       kimi:
  #         name: Kimi K2.5
  #         model: kimi-k2.5
  #         max_tokens: 8192
  #
  # ── ZhipuAI / GLM (openai_compatible) ──
  # Recommended for: Chinese-language tasks, cost-effective models.
  #   zhipu:
  #     name: ZhipuAI
  #     type: openai_compatible
  #     base_url: https://open.bigmodel.cn/api/paas/v4
  #     api_key_env: ZHIPU_API_KEY
  #     models:
  #       glm4:
  #         name: GLM-4
  #         model: glm-4
  #         max_tokens: 4096
  #
  # ── Qwen / Tongyi Bailian (openai_compatible) ──
  # Recommended for: Chinese-language tasks, Alibaba Cloud ecosystem.
  #   qwen:
  #     name: Qwen (Tongyi)
  #     type: openai_compatible
  #     base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
  #     api_key_env: DASHSCOPE_API_KEY
  #     models:
  #       qwen3:
  #         name: Qwen3 235B
  #         model: qwen3-235b-a22b
  #         max_tokens: 8192
  #
  # ── Custom / Self-hosted (openai_compatible) ──
  # For any OpenAI-compatible endpoint (vLLM, LiteLLM, local proxy, etc.).
  #   custom:
  #     name: Custom LLM
  #     type: openai_compatible
  #     base_url: https://your-llm-proxy.example.com
  #     api_key_env: CUSTOM_API_KEY
  #     models:
  #       default:
  #         name: Custom Model
  #         model: your-model-name
  #         max_tokens: 4096
  # ===========================================================================
  providers:{{ yamlValue .API.Providers 4 }}

  # Default provider.model reference used for new sessions.
  # Format: <provider_id>.<model_id> (e.g. "anthropic.sonnet").
  # When empty, GoDex uses the first available provider+model.
  # Environment override: GODEX_API_DEFAULT_MODEL.
  default_model: {{ yamlString .API.DefaultModel }}

  # Automatically fall back to the next candidate when the primary model fails.
  # Environment override: GODEX_AUTO_FALLBACK.
  auto_fallback_enabled: {{ .API.AutoFallbackEnabled }}

  # HTTP timeout in seconds for each LLM API call. Environment override: GODEX_API_TIMEOUT_SECONDS.
  timeout_seconds: {{ .API.TimeoutSeconds }}

  # Runtime model selection strategy.
  #   primary:     always use the first listed candidate
  #   fallback:    try candidates in order on failure (default)
  #   round_robin: rotate through candidates
  model_strategy:{{ yamlValue .API.ModelStrategy 4 }}

acp:
  # External Agent Client Protocol agents callable through the acp_agent tool.
  agents:{{ yamlValue .ACP.Agents 4 }}

agent:
  # Approximate threshold before session compression is useful.
  compress_threshold: {{ .Agent.CompressThreshold }}
  # Fast compaction is used by default to keep model calls off the hot path.
  compaction:
    auto_enabled: {{ .Agent.Compaction.AutoEnabled }}
    trigger_tokens: {{ .Agent.Compaction.TriggerTokens }}
    target_history_tokens: {{ .Agent.Compaction.TargetHistoryTokens }}
    mode: {{ yamlString .Agent.Compaction.Mode }}
    model_profile_id: {{ yamlString .Agent.Compaction.ModelProfileID }}
    max_latency_ms: {{ .Agent.Compaction.MaxLatencyMS }}
    keep_recent_messages: {{ .Agent.Compaction.KeepRecentMessages }}
  # Maximum model/tool loop iterations for one main-agent turn. Environment override: GODEX_AGENT_MAX_TURNS.
  max_turns: {{ .Agent.MaxTurns }}
  # general | coding. Environment override: GODEX_AGENT_PROFILE.
  profile: {{ yamlString .Agent.Profile }}
  # Default task profile by entrypoint. ACP/CLI/TUI default to coding; Web and IM default to general.
  default_profiles:
    acp: {{ yamlString .Agent.DefaultProfiles.ACP }}
    cli: {{ yamlString .Agent.DefaultProfiles.CLI }}
    tui: {{ yamlString .Agent.DefaultProfiles.TUI }}
    web: {{ yamlString .Agent.DefaultProfiles.Web }}
    weixin: {{ yamlString .Agent.DefaultProfiles.Weixin }}
    feishu: {{ yamlString .Agent.DefaultProfiles.Feishu }}

logging:
  # debug | info | warn | error. Environment override: LOG_LEVEL.
  level: {{ yamlString .Logging.Level }}
  # Log file path. Relative paths resolve under GODEX_HOME; default is log/godex.log. Environment override: LOG_FILE.
  file_path: {{ yamlString .Logging.FilePath }}
  # Mirror file logs to stderr too. Environment override: LOG_MIRROR_TO_STDERR.
  also_stderr: {{ .Logging.AlsoStderr }}

web:
  # Shared bearer token for HTTP chat + config APIs. Environment override: GODEX_WEB_TOKEN.
  token: {{ yamlString .Web.Token }}

cron:
  # Enable the background cron scheduler in serve mode. Environment override: GODEX_CRON_ENABLED.
  enabled: {{ .Cron.Enabled }}
  # Scheduler polling interval in seconds. Environment override: GODEX_CRON_TICK_SECONDS.
  tick_seconds: {{ .Cron.TickSeconds }}
  # Default timezone for cron jobs. Environment override: GODEX_CRON_DEFAULT_TIMEZONE.
  default_timezone: {{ yamlString .Cron.DefaultTimezone }}
  # Maximum concurrent cron job executions. Environment override: GODEX_CRON_MAX_CONCURRENT_RUNS.
  max_concurrent_runs: {{ .Cron.MaxConcurrentRuns }}

heartbeat:
  # Enable the background heartbeat loop in serve mode. Environment override: GODEX_HEARTBEAT_ENABLED.
  enabled: {{ .Heartbeat.Enabled }}
  # Heartbeat polling interval in seconds. Environment override: GODEX_HEARTBEAT_TICK_SECONDS.
  tick_seconds: {{ .Heartbeat.TickSeconds }}
  # Default HEARTBEAT checklist path. Environment override: GODEX_HEARTBEAT_CHECKLIST_PATH.
  checklist_path: {{ yamlString .Heartbeat.ChecklistPath }}
  # Suppress delivery when heartbeat output contains this token. Environment override: GODEX_HEARTBEAT_OK_TOKEN.
  ok_token: {{ yamlString .Heartbeat.OKToken }}
  # Default heartbeat interval in seconds. Environment override: GODEX_HEARTBEAT_DEFAULT_INTERVAL_SECONDS.
  default_interval_seconds: {{ .Heartbeat.DefaultIntervalSeconds }}
  # Default timezone for heartbeat scheduling. Environment override: GODEX_HEARTBEAT_DEFAULT_TIMEZONE.
  default_timezone: {{ yamlString .Heartbeat.DefaultTimezone }}

control:
  # Human-readable runtime name in the control plane. Environment override: GODEX_CONTROL_NODE_NAME.
  node_name: {{ yamlString .Control.NodeName }}
  # Stable node id in the control plane, used by 'godex node exec --node <id>'. Environment override: GODEX_CONTROL_NODE_ID.
  node_id: {{ yamlString .Control.NodeID }}
  # Default node id used by 'godex node exec' when --node is omitted. Environment override: GODEX_CONTROL_DEFAULT_NODE.
  default_node: {{ yamlString .Control.DefaultNode }}
  # Trust level reported when registering to a center. Environment override: GODEX_CONTROL_TRUST_LEVEL.
  trust_level: {{ yamlString .Control.TrustLevel }}
  # Optional central Godex service URL for auto-registration. Environment override: GODEX_CONTROL_CENTER_URL.
  center_url: {{ yamlString .Control.CenterURL }}
  # Heartbeat interval in seconds. Environment override: GODEX_CONTROL_HEARTBEAT_SECONDS.
  heartbeat_seconds: {{ .Control.HeartbeatSeconds }}
  # Mark nodes offline after this many seconds without heartbeat. Environment override: GODEX_CONTROL_OFFLINE_AFTER_SECONDS.
  offline_after_seconds: {{ .Control.OfflineAfterSeconds }}
  # Manually known nodes for the control plane dashboard.
  nodes:
{{ yamlValue .Control.Nodes 4 }}

runtime:
  recovery:
    # Automatically queue a recovery turn after detecting an interrupted turn. Environment override: GODEX_RUNTIME_RECOVERY_AUTO_RESUME_INTERRUPTED_TURNS.
    auto_resume_interrupted_turns: {{ .Runtime.Recovery.AutoResumeInterruptedTurns }}
    # Automatically repair low-risk session JSON/checkpoint inconsistencies on startup/open. Environment override: GODEX_RUNTIME_RECOVERY_AUTO_REPAIR_SESSIONS.
    auto_repair_sessions: {{ .Runtime.Recovery.AutoRepairSessions }}

security:
  # trusted-local | guarded-local | sandboxed | strict | host-privileged | dev/repair. Environment override: GODEX_SECURITY_PROFILE.
  profile: {{ yamlString .Security.Profile }}
  # Content-level security screener (roadmap 6.1). Shadow mode records verdicts for audit without gating the pipeline.
  screener:
    # Enable the content screener. Environment override: GODEX_SECURITY_SCREENER_ENABLED.
    enabled: {{ .Security.Screener.Enabled }}
    # Record verdicts without blocking. Recommended rollout state. Environment override: GODEX_SECURITY_SCREENER_SHADOW.
    shadow: {{ .Security.Screener.Shadow }}
    # Classifier provider label for audit trails. Environment override: GODEX_SECURITY_SCREENER_PROVIDER.
    provider: {{ yamlString .Security.Screener.Provider }}
    # Per-classification timeout in ms. Environment override: GODEX_SECURITY_SCREENER_TIMEOUT_MS.
    timeout_ms: {{ .Security.Screener.TimeoutMS }}
    # Classifier response token cap. Environment override: GODEX_SECURITY_SCREENER_MAX_TOKENS.
    max_tokens: {{ .Security.Screener.MaxTokens }}

storage:
  # Retention hint for temporary runtime files. Environment override: GODEX_STORAGE_TMP_TTL_HOURS.
  tmp_ttl_hours: {{ .Storage.TmpTTLHours }}
  # Retention window for generated artifacts and web_fetch spill files. Environment override: GODEX_STORAGE_ARTIFACT_TTL_HOURS.
  artifact_ttl_hours: {{ .Storage.ArtifactTTLHours }}
  # Clean rebuildable Chromium cache before launching the local browser. Environment override: GODEX_STORAGE_BROWSER_CACHE_AUTO_CLEAN.
  browser_cache_auto_clean: {{ .Storage.BrowserCacheAutoClean }}
  # Soft browser cache size target for diagnostics. Environment override: GODEX_STORAGE_BROWSER_CACHE_MAX_MB.
  browser_cache_max_mb: {{ .Storage.BrowserCacheMaxMB }}
  # Number of latest checkpoints to keep per session. Environment override: GODEX_STORAGE_SESSION_CHECKPOINT_KEEP_LATEST.
  session_checkpoint_keep_latest: {{ .Storage.SessionCheckpointKeepLatest }}
  # Session checkpoint retention window. Environment override: GODEX_STORAGE_SESSION_CHECKPOINT_TTL_HOURS.
  session_checkpoint_ttl_hours: {{ .Storage.SessionCheckpointTTLHours }}
  # Prune old checkpoints after writing a new checkpoint. Environment override: GODEX_STORAGE_SESSION_CHECKPOINT_AUTO_PRUNE.
  session_checkpoint_auto_prune: {{ .Storage.SessionCheckpointAutoPrune }}
  # Session storage backend: json or sqlite. Environment override: GODEX_STORAGE_SESSION_BACKEND.
  session_backend: {{ yamlString .Storage.SessionBackend }}
  # Optional SQLite database path for session storage. Environment override: GODEX_STORAGE_SQLITE_PATH.
  sqlite_path: {{ yamlString .Storage.SQLitePath }}

team:
  # Lead identity used across local sessions and teammate tools.
  lead_name: {{ yamlString .Team.LeadName }}
  # Team name used by teammate runtime state.
  team_name: {{ yamlString .Team.TeamName }}
  # Skills auto-loaded into fresh sessions. Environment override: DEFAULT_SKILLS.
  default_skills: {{ yamlListIndent .Team.DefaultSkills 4 }}
  # Teammate loop iteration limit. Environment override: TEAMMATE_WORK_ITERATIONS.
  teammate_work_limit: {{ .Team.TeammateWorkLimit }}
  # Idle poll interval in seconds. Environment override: TEAMMATE_IDLE_POLL_INTERVAL_SECONDS.
  teammate_poll_seconds: {{ .Team.TeammatePollSeconds }}
  # Idle timeout in seconds. Environment override: TEAMMATE_IDLE_TIMEOUT_SECONDS.
  teammate_idle_timeout_seconds: {{ .Team.TeammateIdleTimeoutSecs }}

paths:
  # Runtime state paths are relative to GODEX_HOME by default.
  # Use absolute paths only when you intentionally want a project-local state directory.
  state_dir: {{ yamlString .Paths.StateDir }}
  team_dir: {{ yamlString .Paths.TeamDir }}
  tasks_dir: {{ yamlString .Paths.TasksDir }}
  todos_dir: {{ yamlString .Paths.TodosDir }}
  memory_dir: {{ yamlString .Paths.MemoryDir }}
  rules_dir: {{ yamlString .Paths.RulesDir }}
  skills_dir: {{ yamlString .Paths.SkillsDir }}
  mcp_config_path: {{ yamlString .Paths.MCPConfigPath }}
  temp_dir: {{ yamlString .Paths.TempDir }}
  transcripts_dir: {{ yamlString .Paths.TranscriptsDir }}
  sessions_dir: {{ yamlString .Paths.SessionsDir }}

tools:
  web_search:
    # Enable the built-in web_search tool. Environment override: GODEX_WEB_SEARCH_ENABLED.
    enabled: {{ .Tools.WebSearch.Enabled }}
    # Provider order. Supported values: brave, exa, tavily, browser, duckduckgo. Environment override: GODEX_WEB_SEARCH_PROVIDER_ORDER.
    provider_order: {{ yamlListIndent .Tools.WebSearch.ProviderOrder 6 }}
    # Search cache lifetime in seconds. Environment override: GODEX_WEB_SEARCH_CACHE_TTL_SECONDS.
    cache_ttl_seconds: {{ .Tools.WebSearch.CacheTTLSeconds }}
    browser:
      {{ $duckduckgo := index .Tools.WebSearch.Browser.Engines "duckduckgo" }}{{ $bing := index .Tools.WebSearch.Browser.Engines "bing" }}{{ $brave := index .Tools.WebSearch.Browser.Engines "brave" }}{{ $custom := index .Tools.WebSearch.Browser.Engines "custom" }}
      # Browser search engine preset: duckduckgo, bing, brave, or custom. Environment override: GODEX_WEB_SEARCH_BROWSER_ENGINE.
      engine: {{ yamlString .Tools.WebSearch.Browser.Engine }}
      # Engines to try when the primary browser engine fails or returns no results. Environment override: GODEX_WEB_SEARCH_BROWSER_ENGINE_FALLBACK.
      engine_fallback: {{ yamlListIndent .Tools.WebSearch.Browser.EngineFallback 8 }}
      # Network idle wait before extracting browser search results. Environment override: GODEX_WEB_SEARCH_BROWSER_WAIT_NETWORK_IDLE_MS.
      wait_network_idle_ms: {{ .Tools.WebSearch.Browser.WaitNetworkIdleMS }}
      # Extra wait after page load before extraction. Environment override: GODEX_WEB_SEARCH_BROWSER_WAIT_AFTER_LOAD_MS.
      wait_after_load_ms: {{ .Tools.WebSearch.Browser.WaitAfterLoadMS }}
      # Number of scroll attempts before extraction. Environment override: GODEX_WEB_SEARCH_BROWSER_MAX_SCROLLS.
      max_scrolls: {{ .Tools.WebSearch.Browser.MaxScrolls }}
      # Timeout for one browser search request. Environment override: GODEX_WEB_SEARCH_BROWSER_RESULT_TIMEOUT_SECONDS.
      result_timeout_seconds: {{ .Tools.WebSearch.Browser.ResultTimeoutSeconds }}
      # Hosts that receive a small ranking boost. Environment override: GODEX_WEB_SEARCH_BROWSER_PREFERRED_HOSTS.
      preferred_hosts: {{ yamlListIndent .Tools.WebSearch.Browser.PreferredHosts 8 }}
      engines:
        duckduckgo:
          # URL template with {{ "{{query}}" }} placeholder. Environment override: GODEX_WEB_SEARCH_BROWSER_DUCKDUCKGO_SEARCH_URL_TEMPLATE.
          search_url_template: {{ yamlString $duckduckgo.SearchURLTemplate }}
          # Hosts filtered from DuckDuckGo results. Supports *.example.com. Environment override: GODEX_WEB_SEARCH_BROWSER_DUCKDUCKGO_BLOCKED_HOSTS.
          blocked_hosts: {{ yamlListIndent $duckduckgo.BlockedHosts 12 }}
          # Optional CSS selector for one result block. Example: .result. Environment override: GODEX_WEB_SEARCH_BROWSER_DUCKDUCKGO_RESULT_CONTAINER_SELECTOR.
          result_container_selector: {{ yamlString $duckduckgo.ResultContainerSelector }}
          # Optional CSS selector for the title link inside each block. Example: .result__a. Environment override: GODEX_WEB_SEARCH_BROWSER_DUCKDUCKGO_RESULT_LINK_SELECTOR.
          result_link_selector: {{ yamlString $duckduckgo.ResultLinkSelector }}
          # Optional CSS selector for summary text inside each block. Example: .result__snippet. Environment override: GODEX_WEB_SEARCH_BROWSER_DUCKDUCKGO_RESULT_SNIPPET_SELECTOR.
          result_snippet_selector: {{ yamlString $duckduckgo.ResultSnippetSelector }}
        bing:
          search_url_template: {{ yamlString $bing.SearchURLTemplate }}
          blocked_hosts: {{ yamlListIndent $bing.BlockedHosts 12 }}
          result_container_selector: {{ yamlString $bing.ResultContainerSelector }}
          result_link_selector: {{ yamlString $bing.ResultLinkSelector }}
          result_snippet_selector: {{ yamlString $bing.ResultSnippetSelector }}
        brave:
          search_url_template: {{ yamlString $brave.SearchURLTemplate }}
          blocked_hosts: {{ yamlListIndent $brave.BlockedHosts 12 }}
          result_container_selector: {{ yamlString $brave.ResultContainerSelector }}
          result_link_selector: {{ yamlString $brave.ResultLinkSelector }}
          result_snippet_selector: {{ yamlString $brave.ResultSnippetSelector }}
        custom:
          search_url_template: {{ yamlString $custom.SearchURLTemplate }}
          blocked_hosts: {{ yamlListIndent $custom.BlockedHosts 12 }}
          result_container_selector: {{ yamlString $custom.ResultContainerSelector }}
          result_link_selector: {{ yamlString $custom.ResultLinkSelector }}
          result_snippet_selector: {{ yamlString $custom.ResultSnippetSelector }}
    brave:
      # Optional Brave Search API key. Environment override: GODEX_WEB_SEARCH_BRAVE_API_KEY.
      api_key: {{ yamlString .Tools.WebSearch.Brave.APIKey }}
    exa:
      # Optional Exa API key. Environment override: GODEX_WEB_SEARCH_EXA_API_KEY.
      api_key: {{ yamlString .Tools.WebSearch.Exa.APIKey }}
    tavily:
      # Optional Tavily API key. Environment override: GODEX_WEB_SEARCH_TAVILY_API_KEY.
      api_key: {{ yamlString .Tools.WebSearch.Tavily.APIKey }}

  web_fetch:
    # Enable the built-in web_fetch tool. Environment override: GODEX_WEB_FETCH_ENABLED.
    enabled: {{ .Tools.WebFetch.Enabled }}
    # Default maximum extracted characters. Environment override: GODEX_WEB_FETCH_MAX_CHARS.
    max_chars: {{ .Tools.WebFetch.MaxChars }}
    # HTTP timeout in seconds. Environment override: GODEX_WEB_FETCH_TIMEOUT_SECONDS.
    timeout_seconds: {{ .Tools.WebFetch.TimeoutSeconds }}
    # allow_all | allowlist. Environment override: GODEX_WEB_FETCH_POLICY.
    policy: {{ yamlString .Tools.WebFetch.Policy }}
    # Optional allowlist when policy=allowlist. Environment override: GODEX_WEB_FETCH_ALLOWED_DOMAINS.
    allowed_domains: {{ yamlListIndent .Tools.WebFetch.AllowedDomains 6 }}
    # Domains blocked regardless of policy. Supports *.example.com. Environment override: GODEX_WEB_FETCH_BLOCKED_DOMAINS.
    blocked_domains: {{ yamlListIndent .Tools.WebFetch.BlockedDomains 6 }}
    # Allow localhost/private-network fetches. Environment override: GODEX_WEB_FETCH_ALLOW_PRIVATE_HOSTS.
    allow_private_hosts: {{ .Tools.WebFetch.AllowPrivateHosts }}

  glob:
    # Default maximum matches returned by the glob tool. Environment override: GODEX_GLOB_DEFAULT_MAX_RESULTS.
    default_max_results: {{ .Tools.Glob.DefaultMaxResults }}

  subagent:
    # Maximum subagent jobs accepted by one batch call. Environment override: GODEX_SUBAGENT_MAX_BATCH_SIZE.
    max_batch_size: {{ .Tools.Subagent.MaxBatchSize }}
    # Maximum durable subagent jobs running concurrently. Environment override: GODEX_SUBAGENT_MAX_CONCURRENT_JOBS.
    max_concurrent_jobs: {{ .Tools.Subagent.MaxConcurrentJobs }}
    # Default and minimum turn budget for one durable subagent. Model-provided max_turns below this value are raised to this value. Environment override: GODEX_SUBAGENT_DEFAULT_MAX_TURNS.
    default_max_turns: {{ .Tools.Subagent.DefaultMaxTurns }}
    # Maximum explicit per-job timeout in milliseconds. Zero disables the cap. Environment override: GODEX_SUBAGENT_MAX_JOB_TIMEOUT_MS.
    max_job_timeout_ms: {{ .Tools.Subagent.MaxJobTimeoutMs }}
    # Isolation for read-only subagents: shared_readonly | snapshot. Environment override: GODEX_SUBAGENT_READONLY_ISOLATION.
    readonly_isolation: {{ yamlString .Tools.Subagent.ReadOnlyIsolation }}
    # Isolation for dirty Git repositories: snapshot | dirty_overlay. Environment override: GODEX_SUBAGENT_GIT_DIRTY_ISOLATION.
    git_dirty_isolation: {{ yamlString .Tools.Subagent.GitDirtyIsolation }}
    # Isolation for non-Git write tasks: copy_snapshot | shared_with_approval | deny. Environment override: GODEX_SUBAGENT_NON_GIT_WRITE_ISOLATION.
    non_git_write_isolation: {{ yamlString .Tools.Subagent.NonGitWriteIsolation }}
    # Retention period for terminal subagent workspaces. Environment override: GODEX_SUBAGENT_WORKSPACE_TTL_HOURS.
    workspace_ttl_hours: {{ .Tools.Subagent.WorkspaceTTLHours }}

  execution:
    # Command execution backend for bash/background. Values: local, docker, ssh. Environment override: GODEX_TOOLS_EXECUTION_MODE.
    mode: {{ yamlString .Tools.Execution.Mode }}
    # Container image used when mode=docker. The workspace is mounted at /workspace. Environment override: GODEX_TOOLS_EXECUTION_DOCKER_IMAGE.
    docker_image: {{ yamlString .Tools.Execution.DockerImage }}
    # Optional Docker network such as none, bridge, host, or a custom network. Environment override: GODEX_TOOLS_EXECUTION_DOCKER_NETWORK.
    docker_network: {{ yamlString .Tools.Execution.DockerNetwork }}
    # SSH target used when mode=ssh, such as user@host. Environment override: GODEX_TOOLS_EXECUTION_SSH_TARGET.
    ssh_target: {{ yamlString .Tools.Execution.SSHTarget }}
    # Remote workspace path used when mode=ssh. Environment override: GODEX_TOOLS_EXECUTION_SSH_WORKSPACE.
    ssh_workspace: {{ yamlString .Tools.Execution.SSHWorkspace }}
    # Extra ssh options, such as -o BatchMode=yes. Environment override: GODEX_TOOLS_EXECUTION_SSH_OPTIONS.
    ssh_options: {{ yamlListIndent .Tools.Execution.SSHOptions 6 }}
    # Optional command glob/prefix patterns allowed for bash/background. Empty means no extra allow restriction. Environment override: GODEX_TOOLS_EXECUTION_SHELL_ALLOW_PATTERNS.
    shell_allow_patterns: {{ yamlListIndent .Tools.Execution.ShellAllowPatterns 6 }}
    # Command glob/prefix patterns denied for bash/background before execution. Environment override: GODEX_TOOLS_EXECUTION_SHELL_DENY_PATTERNS.
    shell_deny_patterns: {{ yamlListIndent .Tools.Execution.ShellDenyPatterns 6 }}
    # Maximum seconds a single tool call can run before timing out. Default 1800 (30 min). Environment override: GODEX_TOOLS_EXECUTION_TOOL_TIMEOUT_SECONDS.
    tool_timeout_seconds: {{ .Tools.Execution.ToolTimeoutSeconds }}

  browser:
    # Enable the built-in browser tool. Environment override: GODEX_BROWSER_ENABLED.
    enabled: {{ .Tools.Browser.Enabled }}
    # Launch local browser in headless mode. Environment override: GODEX_BROWSER_HEADLESS.
    headless: {{ .Tools.Browser.Headless }}
    # Optional local Chrome/Chromium binary to launch directly. Environment override: GODEX_BROWSER_PATH.
    browser_path: {{ yamlString .Tools.Browser.BrowserPath }}
    # Optional remote Chrome DevTools endpoint. Environment override: GODEX_BROWSER_CDP_URL.
    cdp_url: {{ yamlString .Tools.Browser.CDPURL }}
    # Browser action timeout in seconds. Environment override: GODEX_BROWSER_ACTION_TIMEOUT_SECONDS.
    action_timeout_seconds: {{ .Tools.Browser.ActionTimeoutSeconds }}
    # Idle page cleanup timeout in seconds. Environment override: GODEX_BROWSER_IDLE_TIMEOUT_SECONDS.
    idle_timeout_seconds: {{ .Tools.Browser.IdleTimeoutSeconds }}
    # Maximum open pages tracked per session. Environment override: GODEX_BROWSER_MAX_PAGES_PER_SESSION.
    max_pages_per_session: {{ .Tools.Browser.MaxPagesPerSession }}
    # Allow localhost/private-network navigation. Environment override: GODEX_BROWSER_ALLOW_PRIVATE_HOSTS.
    allow_private_hosts: {{ .Tools.Browser.AllowPrivateHosts }}

  lightpanda:
    # Enable Lightpanda as an alternative search and fetch backend. Environment override: GODEX_LIGHTPANDA_ENABLED.
    enabled: {{ .Tools.Lightpanda.Enabled }}
    # Optional explicit path to the lightpanda binary. Leave empty to auto-resolve. Environment override: GODEX_LIGHTPANDA_BINARY_PATH.
    binary_path: {{ yamlString .Tools.Lightpanda.BinaryPath }}
    # Automatically download the lightpanda binary if not found locally. Environment override: GODEX_LIGHTPANDA_AUTO_DOWNLOAD.
    auto_download: {{ .Tools.Lightpanda.AutoDownload }}
    # Default search engine used by Lightpanda for web search. Environment override: GODEX_LIGHTPANDA_SEARCH_ENGINE.
    search_engine: {{ yamlString .Tools.Lightpanda.SearchEngine }}
    # Custom search URL template. Use {{ "{{query}}" }} for URL-encoded query. Leave empty for engine default. Environment override: GODEX_LIGHTPANDA_SEARCH_TEMPLATE.
    search_template: {{ yamlString .Tools.Lightpanda.SearchTemplate }}
    # Milliseconds to wait for network idle before extracting page content. Environment override: GODEX_LIGHTPANDA_WAIT_NETWORK_MS.
    wait_network_ms: {{ .Tools.Lightpanda.WaitNetworkMS }}
    # Respect robots.txt restrictions when fetching pages via Lightpanda. Environment override: GODEX_LIGHTPANDA_OBEY_ROBOTS.
    obey_robots: {{ .Tools.Lightpanda.ObeyRobots }}
    # Lightpanda binary log verbosity. Environment override: GODEX_LIGHTPANDA_LOG_LEVEL.
    log_level: {{ yamlString .Tools.Lightpanda.LogLevel }}

  history_search:
    # Enable policy-driven history recall. Environment override: GODEX_TOOLS_HISTORY_SEARCH_ENABLED.
    enabled: {{ .Tools.History.Enabled }}
    auto:
      # Allow weak automatic exposure when the policy score is high enough. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_ENABLED.
      enabled: {{ .Tools.History.Auto.Enabled }}
      # Maximum number of automatic history recalls per turn. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_MAX_PER_TURN.
      max_per_turn: {{ .Tools.History.Auto.MaxPerTurn }}
      # Default automatic recall scope. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_DEFAULT_SCOPE.
      default_scope: {{ yamlString .Tools.History.Auto.DefaultScope }}
      # Permit automatic escalation to session archives when active history is unavailable. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ARCHIVE_ON_CLEAR.
      allow_archive_on_clear: {{ .Tools.History.Auto.AllowArchiveOnClear }}
      # Permit automatic escalation to session archives after compaction. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ARCHIVE_ON_COMPACT.
      allow_archive_on_compact: {{ .Tools.History.Auto.AllowArchiveOnCompact }}
      # Keep false unless you explicitly want broad automatic recall. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ALL_ARCHIVES_AUTOMATIC.
      allow_all_archives_automatic: {{ .Tools.History.Auto.AllowAllArchivesAutomatic }}
      # Minimum score required for weak automatic exposure. Environment override: GODEX_TOOLS_HISTORY_SEARCH_AUTO_MIN_SCORE.
      min_score: {{ .Tools.History.Auto.MinScore }}
    cues:
      # Phrases that strongly indicate the user wants to search prior conversation. Environment override: GODEX_TOOLS_HISTORY_SEARCH_CUES_EXPLICIT.
      explicit: {{ yamlListIndent .Tools.History.Cues.Explicit 8 }}
      # Softer phrases that suggest recall may help. Environment override: GODEX_TOOLS_HISTORY_SEARCH_CUES_IMPLICIT.
      implicit: {{ yamlListIndent .Tools.History.Cues.Implicit 8 }}
    blocks:
      # Session sources that should not auto-expose history recall. Environment override: GODEX_TOOLS_HISTORY_SEARCH_BLOCKS_SESSION_SOURCES.
      session_sources: {{ yamlListIndent .Tools.History.Blocks.SessionSources 8 }}

  permissions:
    # Deny cron/heartbeat/tool bundle mutations during active automation runs. Environment override: GODEX_TOOLS_PERMISSIONS_BLOCK_AUTOMATION_MUTATIONS.
    block_automation_mutations: {{ .Tools.Permissions.BlockAutomationMutations }}
    # Require approval before selected remote sources can invoke protected tools. Environment override: GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_ENABLED.
    interactive_approval_enabled: {{ .Tools.Permissions.InteractiveApprovalEnabled }}
    # manual keeps a pending approval queue; review asks a reviewer subagent first; yolo auto-approves matching protected remote tool calls. Environment override: GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_MODE.
    interactive_approval_mode: {{ yamlString .Tools.Permissions.InteractiveApprovalMode }}
    # Pending approval time-to-live in seconds. Environment override: GODEX_TOOLS_PERMISSIONS_PENDING_TTL_SECONDS.
    pending_ttl_seconds: {{ .Tools.Permissions.PendingTTLSeconds }}
    # Remote sources that require approval. Environment override: GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_SOURCES.
    interactive_approval_sources: {{ yamlListIndent .Tools.Permissions.InteractiveApprovalSources 6 }}
    # Tool names subject to approval for the configured sources. Environment override: GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_TOOLS.
    interactive_approval_tools: {{ yamlListIndent .Tools.Permissions.InteractiveApprovalTools 6 }}
    # Path prefixes that bypass approval for matching remote file/install actions. Environment override: GODEX_TOOLS_PERMISSIONS_TRUSTED_PATH_PREFIXES.
    trusted_path_prefixes: {{ yamlListIndent .Tools.Permissions.TrustedPathPrefixes 6 }}
    # Shell command prefixes that bypass approval when any declared paths are also trusted. Environment override: GODEX_TOOLS_PERMISSIONS_TRUSTED_COMMAND_PREFIXES.
    trusted_command_prefixes: {{ yamlListIndent .Tools.Permissions.TrustedCommandPrefixes 6 }}

  loop_guard:
    # strict may abort after recovery budget; balanced recovers infinitely; warn always recovers without state tracking. Environment override: GODEX_LOOP_GUARD_MODE.
    mode: {{ yamlString .Tools.LoopGuard.Mode }}
    # Total recovery attempts before abort (strict mode only). Environment override: GODEX_LOOP_GUARD_MAX_RECOVERIES.
    max_recoveries: {{ .Tools.LoopGuard.MaxRecoveries }}
    # Consecutive identical tool+input+output calls before loop guard recovery. Environment override: GODEX_LOOP_GUARD_MAX_REPEATED_TOOLS.
    max_repeated_tools: {{ .Tools.LoopGuard.MaxRepeatedTools }}
    # Consecutive identical polling tool input before loop guard recovery. Environment override: GODEX_LOOP_GUARD_MAX_REPEATED_POLLING_TOOLS.
    max_repeated_polling_tools: {{ .Tools.LoopGuard.MaxRepeatedPollingTools }}
    # Consecutive no-progress task polls before loop guard recovery. Environment override: GODEX_LOOP_GUARD_MAX_STALLED_TASK_POLLING_TOOLS.
    max_stalled_task_polling_tools: {{ .Tools.LoopGuard.MaxStalledTaskPollingTools }}

media:
  moonshot:
    # Enable Moonshot file-extract preprocessing for documents and OCR. Environment override: GODEX_MEDIA_MOONSHOT_ENABLED.
    enabled: {{ .Media.Moonshot.Enabled }}
    # Moonshot OpenAI-compatible base URL. Environment override: GODEX_MEDIA_MOONSHOT_BASE_URL.
    base_url: {{ yamlString .Media.Moonshot.BaseURL }}
    # Moonshot API key for media preprocessing. Environment override: GODEX_MEDIA_MOONSHOT_API_KEY.
    api_key: {{ yamlString .Media.Moonshot.APIKey }}

  document:
    # Maximum extracted document characters injected into one turn. Environment override: GODEX_MEDIA_DOCUMENT_MAX_CHARS.
    max_chars: {{ .Media.Document.MaxChars }}
    # Local pdftotext binary for PDF fallback extraction. Environment override: GODEX_MEDIA_DOCUMENT_PDFTOTEXT_PATH.
    pdftotext_path: {{ yamlString .Media.Document.PDFToTextPath }}

  ocr:
    # auto | moonshot | tesseract | disabled. Environment override: GODEX_MEDIA_OCR_MODE.
    mode: {{ yamlString .Media.OCR.Mode }}
    # Local tesseract binary used for OCR fallback. Environment override: GODEX_MEDIA_OCR_TESSERACT_PATH.
    tesseract_path: {{ yamlString .Media.OCR.TesseractPath }}
    # Maximum OCR characters injected into one turn. Environment override: GODEX_MEDIA_OCR_MAX_CHARS.
    max_chars: {{ .Media.OCR.MaxChars }}

  audio:
    # Enable local audio transcription preprocessing. Environment override: GODEX_MEDIA_AUDIO_ENABLED.
    enabled: {{ .Media.Audio.Enabled }}
    # Local ffmpeg binary. Environment override: GODEX_MEDIA_AUDIO_FFMPEG_PATH.
    ffmpeg_path: {{ yamlString .Media.Audio.FFmpegPath }}
    # Local ffprobe binary. Environment override: GODEX_MEDIA_AUDIO_FFPROBE_PATH.
    ffprobe_path: {{ yamlString .Media.Audio.FFprobePath }}
    # whisper.cpp CLI executable. Environment override: GODEX_MEDIA_AUDIO_WHISPER_CPP_PATH.
    whisper_cpp_path: {{ yamlString .Media.Audio.WhisperCPPPath }}
    # Local whisper.cpp model file. Environment override: GODEX_MEDIA_AUDIO_WHISPER_MODEL_PATH.
    whisper_model_path: {{ yamlString .Media.Audio.WhisperModelPath }}
    # Maximum transcript characters injected into one turn. Environment override: GODEX_MEDIA_AUDIO_MAX_CHARS.
    max_chars: {{ .Media.Audio.MaxChars }}

  video:
    # Enable local video transcript + keyframe preprocessing. Environment override: GODEX_MEDIA_VIDEO_ENABLED.
    enabled: {{ .Media.Video.Enabled }}
    # Preferred keyframe interval in seconds. Environment override: GODEX_MEDIA_VIDEO_KEYFRAME_INTERVAL_SECONDS.
    keyframe_interval_seconds: {{ .Media.Video.KeyframeIntervalSeconds }}
    # Maximum keyframes injected into one turn. Environment override: GODEX_MEDIA_VIDEO_MAX_FRAMES.
    max_frames: {{ .Media.Video.MaxFrames }}

channels:
  feishu:
    # Enable the Feishu/Lark channel. Environment override: FEISHU_ENABLED.
    enabled: {{ .Channels.Feishu.Enabled }}
    # Feishu/Lark app ID. Environment override: FEISHU_APP_ID.
    app_id: {{ yamlString .Channels.Feishu.AppID }}
    # Feishu/Lark app secret. Environment override: FEISHU_APP_SECRET.
    app_secret: {{ yamlString .Channels.Feishu.AppSecret }}
    # lark | feishu. Environment override: FEISHU_DOMAIN.
    domain: {{ yamlString .Channels.Feishu.Domain }}

  weixin:
    # Enable the Weixin/iLink channel. Environment override: WEIXIN_ENABLED.
    enabled: {{ .Channels.Weixin.Enabled }}
    # iLink HTTP API base URL. Environment override: WEIXIN_BASE_URL.
    base_url: {{ yamlString .Channels.Weixin.BaseURL }}
    # Weixin CDN base URL for later media phases. Environment override: WEIXIN_CDN_BASE_URL.
    cdn_base_url: {{ yamlString .Channels.Weixin.CDNBaseURL }}
    # Logical local account bucket for persisted credentials. Environment override: WEIXIN_ACCOUNT_ID.
    account_id: {{ yamlString .Channels.Weixin.AccountID }}
    # Optional allowlist of iLink user IDs. Empty or * allows all senders. Environment override: WEIXIN_ALLOW_FROM.
    allow_from: {{ yamlListIndent .Channels.Weixin.AllowFrom 6 }}
    # Optional metadata route tag attached to inbound sessions. Environment override: WEIXIN_ROUTE_TAG.
    route_tag: {{ yamlString .Channels.Weixin.RouteTag }}
    # Requested long-poll hold timeout in milliseconds. Environment override: WEIXIN_LONG_POLL_TIMEOUT_MS.
    long_poll_timeout_ms: {{ .Channels.Weixin.LongPollTimeoutMs }}
    # Optional HTTP(S) proxy URL for iLink requests. Environment override: WEIXIN_PROXY.
    proxy: {{ yamlString .Channels.Weixin.Proxy }}
`
