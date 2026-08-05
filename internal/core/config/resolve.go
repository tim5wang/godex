package config

import (
	"bytes"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) reload(createIfMissing bool) error {
	if createIfMissing {
		if err := os.MkdirAll(filepath.Dir(m.homeConfigPath), 0755); err != nil {
			return err
		}
		if _, err := os.Stat(m.homeConfigPath); os.IsNotExist(err) {
			defaults := defaultConfigFile()
			if err := m.writeConfigFile(defaults); err != nil {
				return err
			}
		}
	}

	file, err := parseConfigFile(m.homeConfigPath)
	if err != nil {
		return err
	}
	effectiveFile, err := m.effectiveConfigFile(file)
	if err != nil {
		return err
	}
	cfg, origins, err := m.resolve(effectiveFile)
	if err != nil {
		return err
	}
	m.stored = file
	m.current = cfg
	m.origins = origins
	m.revision++
	if m.lastApply.StorageStatus == "" && m.lastApply.RuntimeStatus == "" {
		m.lastApply = ApplyReport{
			AppliedAt:     time.Now(),
			StorageStatus: StorageStatusSaved,
			RuntimeStatus: RuntimeStatusApplied,
			Message:       "Loaded configuration from disk.",
		}
	}
	return nil
}

func (m *Manager) resolve(file ConfigFile) (*Config, map[string]fieldOrigin, error) {
	homeDotenvMap, err := readDotEnvFile(m.homeEnvPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	projectDotenvMap, err := readDotEnvFile(m.projectEnvPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	dotenvMap := mergeEnvMaps(homeDotenvMap, projectDotenvMap)
	origins := make(map[string]fieldOrigin)
	current := resolveConfigFile(file, m.homeDir, m.projectDir, m.configPath, m.envPath, m.homeConfigPath, m.projectConfigPath, m.homeEnvPath, m.projectEnvPath)

	resolveString := func(path string, yamlValue string, canonicalEnv string, apply func(string)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    yamlValue,
			Effective:    yamlValue,
		}
		value := yamlValue
		if dot := lookupEnvValue(dotenvMap, canonicalEnv); dot.Set {
			value = dot.Value
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = dot.Value
			origin.UsedEnv = dot.Name
			origin.Effective = dot.Value
		}
		if env := lookupProcessValue(canonicalEnv); env.Set {
			value = env.Value
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = env.Value
			origin.UsedEnv = env.Name
			origin.Effective = env.Value
		}
		origins[path] = origin
		apply(value)
	}

	resolveBool := func(path string, yamlValue bool, canonicalEnv string, apply func(bool)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    yamlValue,
			Effective:    yamlValue,
		}
		value := yamlValue
		if dot, ok := lookupBool(dotenvMap, canonicalEnv); ok {
			value = dot
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = dot
			origin.UsedEnv = canonicalEnv
			origin.Effective = dot
		}
		if env, ok := lookupProcessBool(canonicalEnv); ok {
			value = env
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = env
			origin.UsedEnv = canonicalEnv
			origin.Effective = env
		}
		origins[path] = origin
		apply(value)
	}

	resolveInt := func(path string, yamlValue int, canonicalEnv string, apply func(int)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    yamlValue,
			Effective:    yamlValue,
		}
		value := yamlValue
		if dot, ok, name := lookupInt(dotenvMap, canonicalEnv); ok {
			value = dot
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = dot
			origin.UsedEnv = name
			origin.Effective = dot
		}
		if env, ok, name := lookupProcessInt(canonicalEnv); ok {
			value = env
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = env
			origin.UsedEnv = name
			origin.Effective = env
		}
		origins[path] = origin
		apply(value)
	}

	resolveCSV := func(path string, yamlValue []string, canonicalEnv string, apply func([]string)) {
		origin := fieldOrigin{
			Source:       SourceYAML,
			CanonicalEnv: canonicalEnv,
			YAMLValue:    append([]string{}, yamlValue...),
			Effective:    append([]string{}, yamlValue...),
		}
		value := append([]string{}, yamlValue...)
		if dot, ok := lookupCSV(dotenvMap, canonicalEnv); ok {
			value = dot
			origin.Source = SourceDotEnv
			origin.OverriddenBy = SourceDotEnv
			origin.DotEnvValue = append([]string{}, dot...)
			origin.UsedEnv = canonicalEnv
			origin.Effective = append([]string{}, dot...)
		}
		if env, ok := lookupProcessCSV(canonicalEnv); ok {
			value = env
			origin.Source = SourceEnv
			origin.OverriddenBy = SourceEnv
			origin.EnvValue = append([]string{}, env...)
			origin.UsedEnv = canonicalEnv
			origin.Effective = append([]string{}, env...)
		}
		origins[path] = origin
		apply(value)
	}

	resolveString("api.default_model", file.API.DefaultModel, "GODEX_API_DEFAULT_MODEL", func(v string) { current.DefaultModelRef = v })
	resolveString("api.default_profile", file.API.DefaultProfile, "GODEX_API_DEFAULT_PROFILE", func(v string) { current.DefaultProfileID = v })
	resolveBool("api.auto_fallback_enabled", file.API.AutoFallbackEnabled, "GODEX_API_AUTO_FALLBACK_ENABLED", func(v bool) { current.AutoFallbackEnabled = v })
	resolveInt("api.timeout_seconds", file.API.TimeoutSeconds, "GODEX_API_TIMEOUT_SECONDS", func(v int) { current.APITimeoutSeconds = v })
	current.LLMProviders = llmProvidersFromConfigFile(file)
	applyProviderCredentialEnv(current.LLMProviders, dotenvMap)
	current.LLMStrategy = llmStrategyFromConfigFile(file)
	current.ModelProfiles = modelProfilesFromProviders(file, current.LLMProviders)
	current.DefaultModelRef = strings.TrimSpace(current.DefaultModelRef)
	current.DefaultProfileID = strings.TrimSpace(current.DefaultProfileID)
	if current.DefaultModelRef != "" {
		current.DefaultProfileID = current.DefaultModelRef
	}
	if current.DefaultProfileID == "" {
		current.DefaultProfileID = defaultProfileIDFromProfiles(current.ModelProfiles)
	}
	if _, ok := current.ModelProfiles[current.DefaultProfileID]; !ok {
		current.DefaultProfileID = defaultProfileIDFromProfiles(current.ModelProfiles)
	}
	current.DefaultModelRef = current.DefaultProfileID
	defaultProfile := current.ModelProfiles[current.DefaultProfileID]
	defaultProfile = normalizeModelProfile(current.DefaultProfileID, defaultProfile, current)
	apiDefaults := defaultConfigFile().API
	legacyIntOverride := func(path string, value, defaultValue int) bool {
		origin := origins[path]
		if value <= 0 {
			return false
		}
		return origin.Source == SourceEnv || origin.Source == SourceDotEnv || value != defaultValue
	}
	if isProviderModelProfileID(file, current.DefaultProfileID) {
		if legacyIntOverride("api.timeout_seconds", current.APITimeoutSeconds, apiDefaults.TimeoutSeconds) {
			defaultProfile.TimeoutSeconds = current.APITimeoutSeconds
		}
	}
	current.ModelProfiles[current.DefaultProfileID] = defaultProfile
	current.APIKey = defaultProfile.APIKey
	current.Model = defaultProfile.Model
	current.ReasoningEffort = defaultProfile.ReasoningEffort
	current.BaseURL = defaultProfile.BaseURL
	current.MaxTokens = defaultProfile.MaxTokens
	current.APITimeoutSeconds = defaultProfile.TimeoutSeconds
	origins["api.providers"] = fieldOrigin{Source: SourceYAML, YAMLValue: maskLLMProviders(file.API.Providers), Effective: maskLLMProviders(current.LLMProviders)}
	origins["api.model_strategy"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.API.ModelStrategy, Effective: current.LLMStrategy}
	origins["acp.agents"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.ACP.Agents, Effective: maskACPAgentSecrets(acpAgentsFromConfigFile(file))}
	resolveInt("agent.compress_threshold", file.Agent.CompressThreshold, "COMPRESS_THRESHOLD", func(v int) { current.CompressThreshold = v })
	resolveBool("agent.compaction.auto_enabled", file.Agent.Compaction.AutoEnabled, "GODEX_AGENT_COMPACTION_AUTO_ENABLED", func(v bool) {
		current.Compaction.AutoEnabled = v
	})
	resolveInt("agent.compaction.trigger_tokens", file.Agent.Compaction.TriggerTokens, "GODEX_AGENT_COMPACTION_TRIGGER_TOKENS", func(v int) {
		current.Compaction.TriggerTokens = positiveOrDefault(v, 60000)
	})
	resolveInt("agent.compaction.target_history_tokens", file.Agent.Compaction.TargetHistoryTokens, "GODEX_AGENT_COMPACTION_TARGET_HISTORY_TOKENS", func(v int) {
		current.Compaction.TargetHistoryTokens = positiveOrDefault(v, 12000)
	})
	resolveString("agent.compaction.mode", file.Agent.Compaction.Mode, "GODEX_AGENT_COMPACTION_MODE", func(v string) {
		current.Compaction.Mode = NormalizeCompactionMode(v)
	})
	resolveString("agent.compaction.model_profile_id", file.Agent.Compaction.ModelProfileID, "GODEX_AGENT_COMPACTION_MODEL_PROFILE_ID", func(v string) {
		current.Compaction.ModelProfileID = strings.TrimSpace(v)
	})
	resolveInt("agent.compaction.max_latency_ms", file.Agent.Compaction.MaxLatencyMS, "GODEX_AGENT_COMPACTION_MAX_LATENCY_MS", func(v int) {
		current.Compaction.MaxLatencyMS = positiveOrDefault(v, 3000)
	})
	resolveInt("agent.compaction.keep_recent_messages", file.Agent.Compaction.KeepRecentMessages, "GODEX_AGENT_COMPACTION_KEEP_RECENT_MESSAGES", func(v int) {
		current.Compaction.KeepRecentMessages = positiveOrDefault(v, 20)
	})
	resolveInt("agent.max_turns", file.Agent.MaxTurns, "GODEX_AGENT_MAX_TURNS", func(v int) { current.MaxTurns = v })
	resolveString("agent.profile", file.Agent.Profile, "GODEX_AGENT_PROFILE", func(v string) {
		current.AgentProfile = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.acp", file.Agent.DefaultProfiles.ACP, "GODEX_AGENT_DEFAULT_PROFILE_ACP", func(v string) {
		current.AgentDefaultProfiles.ACP = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.cli", file.Agent.DefaultProfiles.CLI, "GODEX_AGENT_DEFAULT_PROFILE_CLI", func(v string) {
		current.AgentDefaultProfiles.CLI = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.tui", file.Agent.DefaultProfiles.TUI, "GODEX_AGENT_DEFAULT_PROFILE_TUI", func(v string) {
		current.AgentDefaultProfiles.TUI = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.web", file.Agent.DefaultProfiles.Web, "GODEX_AGENT_DEFAULT_PROFILE_WEB", func(v string) {
		current.AgentDefaultProfiles.Web = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.weixin", file.Agent.DefaultProfiles.Weixin, "GODEX_AGENT_DEFAULT_PROFILE_WEIXIN", func(v string) {
		current.AgentDefaultProfiles.Weixin = NormalizeAgentProfile(v)
	})
	resolveString("agent.default_profiles.feishu", file.Agent.DefaultProfiles.Feishu, "GODEX_AGENT_DEFAULT_PROFILE_FEISHU", func(v string) {
		current.AgentDefaultProfiles.Feishu = NormalizeAgentProfile(v)
	})
	resolveString("logging.level", file.Logging.Level, "LOG_LEVEL", func(v string) { current.Logging.Level = v })
	resolveString("logging.file_path", file.Logging.FilePath, "LOG_FILE", func(v string) { current.Logging.FilePath = resolveLogPath(m.homeDir, v) })
	resolveBool("logging.also_stderr", file.Logging.AlsoStderr, "LOG_MIRROR_TO_STDERR", func(v bool) { current.Logging.AlsoStderr = v })
	resolveString("web.token", file.Web.Token, "GODEX_WEB_TOKEN", func(v string) { current.WebToken = v })
	resolveBool("cron.enabled", file.Cron.Enabled, "GODEX_CRON_ENABLED", func(v bool) { current.Cron.Enabled = v })
	resolveInt("cron.tick_seconds", file.Cron.TickSeconds, "GODEX_CRON_TICK_SECONDS", func(v int) { current.Cron.TickSeconds = v })
	resolveString("cron.default_timezone", file.Cron.DefaultTimezone, "GODEX_CRON_DEFAULT_TIMEZONE", func(v string) { current.Cron.DefaultTimezone = v })
	resolveInt("cron.max_concurrent_runs", file.Cron.MaxConcurrentRuns, "GODEX_CRON_MAX_CONCURRENT_RUNS", func(v int) { current.Cron.MaxConcurrentRuns = v })
	resolveBool("heartbeat.enabled", file.Heartbeat.Enabled, "GODEX_HEARTBEAT_ENABLED", func(v bool) { current.Heartbeat.Enabled = v })
	resolveInt("heartbeat.tick_seconds", file.Heartbeat.TickSeconds, "GODEX_HEARTBEAT_TICK_SECONDS", func(v int) { current.Heartbeat.TickSeconds = v })
	resolveString("heartbeat.checklist_path", file.Heartbeat.ChecklistPath, "GODEX_HEARTBEAT_CHECKLIST_PATH", func(v string) { current.Heartbeat.ChecklistPath = resolvePath(m.workspace, v) })
	resolveString("heartbeat.ok_token", file.Heartbeat.OKToken, "GODEX_HEARTBEAT_OK_TOKEN", func(v string) { current.Heartbeat.OKToken = v })
	resolveInt("heartbeat.default_interval_seconds", file.Heartbeat.DefaultIntervalSeconds, "GODEX_HEARTBEAT_DEFAULT_INTERVAL_SECONDS", func(v int) { current.Heartbeat.DefaultIntervalSeconds = v })
	resolveString("heartbeat.default_timezone", file.Heartbeat.DefaultTimezone, "GODEX_HEARTBEAT_DEFAULT_TIMEZONE", func(v string) { current.Heartbeat.DefaultTimezone = v })
	resolveString("control.node_name", file.Control.NodeName, "GODEX_CONTROL_NODE_NAME", func(v string) { current.Control.NodeName = v })
	resolveString("control.center_url", file.Control.CenterURL, "GODEX_CONTROL_CENTER_URL", func(v string) { current.Control.CenterURL = v })
	resolveInt("control.heartbeat_seconds", file.Control.HeartbeatSeconds, "GODEX_CONTROL_HEARTBEAT_SECONDS", func(v int) {
		current.Control.HeartbeatSeconds = positiveOrDefault(v, 15)
	})
	resolveInt("control.offline_after_seconds", file.Control.OfflineAfterSeconds, "GODEX_CONTROL_OFFLINE_AFTER_SECONDS", func(v int) {
		current.Control.OfflineAfterSeconds = positiveOrDefault(v, 60)
	})
	origins["control.nodes"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Control.Nodes, Effective: controlNodesFromConfigFile(file)}
	resolveBool("runtime.recovery.auto_resume_interrupted_turns", file.Runtime.Recovery.AutoResumeInterruptedTurns, "GODEX_RUNTIME_RECOVERY_AUTO_RESUME_INTERRUPTED_TURNS", func(v bool) {
		current.Runtime.Recovery.AutoResumeInterruptedTurns = v
	})
	resolveBool("runtime.recovery.auto_repair_sessions", file.Runtime.Recovery.AutoRepairSessions, "GODEX_RUNTIME_RECOVERY_AUTO_REPAIR_SESSIONS", func(v bool) {
		current.Runtime.Recovery.AutoRepairSessions = v
	})
	resolveInt("storage.tmp_ttl_hours", file.Storage.TmpTTLHours, "GODEX_STORAGE_TMP_TTL_HOURS", func(v int) {
		current.Storage.TmpTTLHours = positiveOrDefault(v, 72)
	})
	resolveInt("storage.artifact_ttl_hours", file.Storage.ArtifactTTLHours, "GODEX_STORAGE_ARTIFACT_TTL_HOURS", func(v int) {
		current.Storage.ArtifactTTLHours = positiveOrDefault(v, 168)
	})
	resolveBool("storage.browser_cache_auto_clean", file.Storage.BrowserCacheAutoClean, "GODEX_STORAGE_BROWSER_CACHE_AUTO_CLEAN", func(v bool) {
		current.Storage.BrowserCacheAutoClean = v
	})
	resolveInt("storage.browser_cache_max_mb", file.Storage.BrowserCacheMaxMB, "GODEX_STORAGE_BROWSER_CACHE_MAX_MB", func(v int) {
		current.Storage.BrowserCacheMaxMB = positiveOrDefault(v, 256)
	})
	resolveInt("storage.session_checkpoint_keep_latest", file.Storage.SessionCheckpointKeepLatest, "GODEX_STORAGE_SESSION_CHECKPOINT_KEEP_LATEST", func(v int) {
		current.Storage.SessionCheckpointKeepLatest = positiveOrDefault(v, 20)
	})
	resolveInt("storage.session_checkpoint_ttl_hours", file.Storage.SessionCheckpointTTLHours, "GODEX_STORAGE_SESSION_CHECKPOINT_TTL_HOURS", func(v int) {
		current.Storage.SessionCheckpointTTLHours = positiveOrDefault(v, 168)
	})
	resolveBool("storage.session_checkpoint_auto_prune", file.Storage.SessionCheckpointAutoPrune, "GODEX_STORAGE_SESSION_CHECKPOINT_AUTO_PRUNE", func(v bool) {
		current.Storage.SessionCheckpointAutoPrune = v
	})
	resolveString("storage.session_backend", file.Storage.SessionBackend, "GODEX_STORAGE_SESSION_BACKEND", func(v string) {
		current.Storage.SessionBackend = normalizeSessionStorageBackend(v)
	})
	resolveString("storage.sqlite_path", file.Storage.SQLitePath, "GODEX_STORAGE_SQLITE_PATH", func(v string) {
		current.Storage.SQLitePath = strings.TrimSpace(v)
	})
	resolveString("security.profile", file.Security.Profile, "GODEX_SECURITY_PROFILE", func(v string) {
		current.Security.Profile = normalizeSecurityProfileName(v)
	})
	resolveString("team.lead_name", file.Team.LeadName, "LEAD_NAME", func(v string) { current.LeadName = v })
	resolveString("team.team_name", file.Team.TeamName, "TEAM_NAME", func(v string) { current.TeamName = v })
	resolveCSV("team.default_skills", file.Team.DefaultSkills, "DEFAULT_SKILLS", func(v []string) { current.DefaultSkills = append([]string{}, v...) })
	resolveInt("team.teammate_work_limit", file.Team.TeammateWorkLimit, "TEAMMATE_WORK_ITERATIONS", func(v int) { current.TeammateWorkLimit = v })
	resolveInt("team.teammate_poll_seconds", file.Team.TeammatePollSeconds, "TEAMMATE_IDLE_POLL_INTERVAL_SECONDS", func(v int) { current.TeammatePollEvery = time.Duration(v) * time.Second })
	resolveInt("team.teammate_idle_timeout_seconds", file.Team.TeammateIdleTimeoutSecs, "TEAMMATE_IDLE_TIMEOUT_SECONDS", func(v int) { current.TeammateIdleFor = time.Duration(v) * time.Second })
	resolveString("channels.feishu.app_id", file.Channels.Feishu.AppID, "FEISHU_APP_ID", func(v string) { current.Feishu.AppID = v })
	resolveString("channels.feishu.app_secret", file.Channels.Feishu.AppSecret, "FEISHU_APP_SECRET", func(v string) { current.Feishu.AppSecret = v })
	resolveBool("channels.feishu.enabled", file.Channels.Feishu.Enabled, "FEISHU_ENABLED", func(v bool) { current.Feishu.Enabled = v })
	resolveString("channels.feishu.domain", file.Channels.Feishu.Domain, "FEISHU_DOMAIN", func(v string) { current.Feishu.Domain = v })
	resolveBool("channels.weixin.enabled", file.Channels.Weixin.Enabled, "WEIXIN_ENABLED", func(v bool) { current.Weixin.Enabled = v })
	resolveString("channels.weixin.base_url", file.Channels.Weixin.BaseURL, "WEIXIN_BASE_URL", func(v string) { current.Weixin.BaseURL = v })
	resolveString("channels.weixin.cdn_base_url", file.Channels.Weixin.CDNBaseURL, "WEIXIN_CDN_BASE_URL", func(v string) { current.Weixin.CDNBaseURL = v })
	resolveString("channels.weixin.account_id", file.Channels.Weixin.AccountID, "WEIXIN_ACCOUNT_ID", func(v string) { current.Weixin.AccountID = v })
	resolveCSV("channels.weixin.allow_from", file.Channels.Weixin.AllowFrom, "WEIXIN_ALLOW_FROM", func(v []string) { current.Weixin.AllowFrom = append([]string{}, v...) })
	resolveString("channels.weixin.route_tag", file.Channels.Weixin.RouteTag, "WEIXIN_ROUTE_TAG", func(v string) { current.Weixin.RouteTag = v })
	resolveInt("channels.weixin.long_poll_timeout_ms", file.Channels.Weixin.LongPollTimeoutMs, "WEIXIN_LONG_POLL_TIMEOUT_MS", func(v int) { current.Weixin.LongPollTimeoutMs = v })
	resolveString("channels.weixin.proxy", file.Channels.Weixin.Proxy, "WEIXIN_PROXY", func(v string) { current.Weixin.Proxy = v })
	resolveBool("tools.web_search.enabled", file.Tools.WebSearch.Enabled, "GODEX_WEB_SEARCH_ENABLED", func(v bool) { current.Tools.WebSearch.Enabled = v })
	resolveCSV("tools.web_search.provider_order", file.Tools.WebSearch.ProviderOrder, "GODEX_WEB_SEARCH_PROVIDER_ORDER", func(v []string) {
		current.Tools.WebSearch.ProviderOrder = append([]string{}, v...)
	})
	resolveInt("tools.web_search.cache_ttl_seconds", file.Tools.WebSearch.CacheTTLSeconds, "GODEX_WEB_SEARCH_CACHE_TTL_SECONDS", func(v int) {
		current.Tools.WebSearch.CacheTTLSeconds = v
	})
	resolveString("tools.web_search.browser.engine", file.Tools.WebSearch.Browser.Engine, "GODEX_WEB_SEARCH_BROWSER_ENGINE", func(v string) {
		current.Tools.WebSearch.Browser.Engine = v
	})
	resolveCSV("tools.web_search.browser.engine_fallback", file.Tools.WebSearch.Browser.EngineFallback, "GODEX_WEB_SEARCH_BROWSER_ENGINE_FALLBACK", func(v []string) {
		current.Tools.WebSearch.Browser.EngineFallback = append([]string{}, v...)
	})
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		engine := engine
		section := file.Tools.WebSearch.Browser.Engines[engine]
		envPrefix := "GODEX_WEB_SEARCH_BROWSER_" + strings.ToUpper(engine) + "_"
		pathPrefix := "tools.web_search.browser.engines." + engine + "."
		resolveString(pathPrefix+"search_url_template", section.SearchURLTemplate, envPrefix+"SEARCH_URL_TEMPLATE", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.SearchURLTemplate = v })
		})
		resolveCSV(pathPrefix+"blocked_hosts", section.BlockedHosts, envPrefix+"BLOCKED_HOSTS", func(v []string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.BlockedHosts = append([]string{}, v...) })
		})
		resolveString(pathPrefix+"result_container_selector", section.ResultContainerSelector, envPrefix+"RESULT_CONTAINER_SELECTOR", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.ResultContainerSelector = v })
		})
		resolveString(pathPrefix+"result_link_selector", section.ResultLinkSelector, envPrefix+"RESULT_LINK_SELECTOR", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.ResultLinkSelector = v })
		})
		resolveString(pathPrefix+"result_snippet_selector", section.ResultSnippetSelector, envPrefix+"RESULT_SNIPPET_SELECTOR", func(v string) {
			updateBrowserEngineConfig(&current.Tools.WebSearch.Browser, engine, func(cfg *WebSearchBrowserEngineConfig) { cfg.ResultSnippetSelector = v })
		})
	}
	resolveInt("tools.web_search.browser.wait_network_idle_ms", file.Tools.WebSearch.Browser.WaitNetworkIdleMS, "GODEX_WEB_SEARCH_BROWSER_WAIT_NETWORK_IDLE_MS", func(v int) {
		current.Tools.WebSearch.Browser.WaitNetworkIdleMS = v
	})
	resolveInt("tools.web_search.browser.wait_after_load_ms", file.Tools.WebSearch.Browser.WaitAfterLoadMS, "GODEX_WEB_SEARCH_BROWSER_WAIT_AFTER_LOAD_MS", func(v int) {
		current.Tools.WebSearch.Browser.WaitAfterLoadMS = v
	})
	resolveInt("tools.web_search.browser.max_scrolls", file.Tools.WebSearch.Browser.MaxScrolls, "GODEX_WEB_SEARCH_BROWSER_MAX_SCROLLS", func(v int) {
		current.Tools.WebSearch.Browser.MaxScrolls = v
	})
	resolveInt("tools.web_search.browser.result_timeout_seconds", file.Tools.WebSearch.Browser.ResultTimeoutSeconds, "GODEX_WEB_SEARCH_BROWSER_RESULT_TIMEOUT_SECONDS", func(v int) {
		current.Tools.WebSearch.Browser.ResultTimeoutSeconds = v
	})
	resolveCSV("tools.web_search.browser.preferred_hosts", file.Tools.WebSearch.Browser.PreferredHosts, "GODEX_WEB_SEARCH_BROWSER_PREFERRED_HOSTS", func(v []string) {
		current.Tools.WebSearch.Browser.PreferredHosts = append([]string{}, v...)
	})
	resolveString("tools.web_search.brave.api_key", file.Tools.WebSearch.Brave.APIKey, "GODEX_WEB_SEARCH_BRAVE_API_KEY", func(v string) {
		current.Tools.WebSearch.BraveAPIKey = v
	})
	resolveString("tools.web_search.exa.api_key", file.Tools.WebSearch.Exa.APIKey, "GODEX_WEB_SEARCH_EXA_API_KEY", func(v string) {
		current.Tools.WebSearch.ExaAPIKey = v
	})
	resolveString("tools.web_search.tavily.api_key", file.Tools.WebSearch.Tavily.APIKey, "GODEX_WEB_SEARCH_TAVILY_API_KEY", func(v string) {
		current.Tools.WebSearch.TavilyAPIKey = v
	})
	resolveString("tools.web_search.serpapi.api_key", file.Tools.WebSearch.SerpAPI.APIKey, "GODEX_WEB_SEARCH_SERPAPI_API_KEY", func(v string) {
		current.Tools.WebSearch.SerpAPIKey = v
	})
	resolveBool("tools.web_fetch.enabled", file.Tools.WebFetch.Enabled, "GODEX_WEB_FETCH_ENABLED", func(v bool) { current.Tools.WebFetch.Enabled = v })
	resolveInt("tools.web_fetch.max_chars", file.Tools.WebFetch.MaxChars, "GODEX_WEB_FETCH_MAX_CHARS", func(v int) {
		current.Tools.WebFetch.MaxChars = v
	})
	resolveInt("tools.web_fetch.timeout_seconds", file.Tools.WebFetch.TimeoutSeconds, "GODEX_WEB_FETCH_TIMEOUT_SECONDS", func(v int) {
		current.Tools.WebFetch.TimeoutSeconds = v
	})
	resolveString("tools.web_fetch.policy", file.Tools.WebFetch.Policy, "GODEX_WEB_FETCH_POLICY", func(v string) {
		current.Tools.WebFetch.Policy = v
	})
	resolveCSV("tools.web_fetch.allowed_domains", file.Tools.WebFetch.AllowedDomains, "GODEX_WEB_FETCH_ALLOWED_DOMAINS", func(v []string) {
		current.Tools.WebFetch.AllowedDomains = append([]string{}, v...)
	})
	resolveCSV("tools.web_fetch.blocked_domains", file.Tools.WebFetch.BlockedDomains, "GODEX_WEB_FETCH_BLOCKED_DOMAINS", func(v []string) {
		current.Tools.WebFetch.BlockedDomains = append([]string{}, v...)
	})
	resolveBool("tools.web_fetch.allow_private_hosts", file.Tools.WebFetch.AllowPrivateHosts, "GODEX_WEB_FETCH_ALLOW_PRIVATE_HOSTS", func(v bool) {
		current.Tools.WebFetch.AllowPrivateHosts = v
	})
	resolveInt("tools.glob.default_max_results", file.Tools.Glob.DefaultMaxResults, "GODEX_GLOB_DEFAULT_MAX_RESULTS", func(v int) {
		current.Tools.Glob.DefaultMaxResults = v
	})
	resolveInt("tools.subagent.max_batch_size", file.Tools.Subagent.MaxBatchSize, "GODEX_SUBAGENT_MAX_BATCH_SIZE", func(v int) {
		current.Tools.Subagent.MaxBatchSize = v
	})
	resolveInt("tools.subagent.max_concurrent_jobs", file.Tools.Subagent.MaxConcurrentJobs, "GODEX_SUBAGENT_MAX_CONCURRENT_JOBS", func(v int) {
		current.Tools.Subagent.MaxConcurrentJobs = v
	})
	resolveInt("tools.subagent.default_max_turns", file.Tools.Subagent.DefaultMaxTurns, "GODEX_SUBAGENT_DEFAULT_MAX_TURNS", func(v int) {
		current.Tools.Subagent.DefaultMaxTurns = v
	})
	resolveInt("tools.subagent.max_job_timeout_ms", file.Tools.Subagent.MaxJobTimeoutMs, "GODEX_SUBAGENT_MAX_JOB_TIMEOUT_MS", func(v int) {
		current.Tools.Subagent.MaxJobTimeoutMs = v
	})
	resolveString("tools.subagent.readonly_isolation", file.Tools.Subagent.ReadOnlyIsolation, "GODEX_SUBAGENT_READONLY_ISOLATION", func(v string) {
		current.Tools.Subagent.ReadOnlyIsolation = normalizeSubagentReadOnlyIsolation(v)
	})
	resolveString("tools.subagent.git_dirty_isolation", file.Tools.Subagent.GitDirtyIsolation, "GODEX_SUBAGENT_GIT_DIRTY_ISOLATION", func(v string) {
		current.Tools.Subagent.GitDirtyIsolation = normalizeSubagentGitDirtyIsolation(v)
	})
	resolveString("tools.subagent.non_git_write_isolation", file.Tools.Subagent.NonGitWriteIsolation, "GODEX_SUBAGENT_NON_GIT_WRITE_ISOLATION", func(v string) {
		current.Tools.Subagent.NonGitWriteIsolation = normalizeSubagentNonGitWriteIsolation(v)
	})
	resolveInt("tools.subagent.workspace_ttl_hours", file.Tools.Subagent.WorkspaceTTLHours, "GODEX_SUBAGENT_WORKSPACE_TTL_HOURS", func(v int) {
		current.Tools.Subagent.WorkspaceTTLHours = v
	})
	resolveString("tools.execution.mode", file.Tools.Execution.Mode, "GODEX_TOOLS_EXECUTION_MODE", func(v string) {
		current.Tools.Execution.Mode = v
	})
	resolveString("tools.execution.docker_image", file.Tools.Execution.DockerImage, "GODEX_TOOLS_EXECUTION_DOCKER_IMAGE", func(v string) {
		current.Tools.Execution.DockerImage = v
	})
	resolveString("tools.execution.docker_network", file.Tools.Execution.DockerNetwork, "GODEX_TOOLS_EXECUTION_DOCKER_NETWORK", func(v string) {
		current.Tools.Execution.DockerNetwork = v
	})
	resolveString("tools.execution.ssh_target", file.Tools.Execution.SSHTarget, "GODEX_TOOLS_EXECUTION_SSH_TARGET", func(v string) {
		current.Tools.Execution.SSHTarget = v
	})
	resolveString("tools.execution.ssh_workspace", file.Tools.Execution.SSHWorkspace, "GODEX_TOOLS_EXECUTION_SSH_WORKSPACE", func(v string) {
		current.Tools.Execution.SSHWorkspace = v
	})
	resolveCSV("tools.execution.ssh_options", file.Tools.Execution.SSHOptions, "GODEX_TOOLS_EXECUTION_SSH_OPTIONS", func(v []string) {
		current.Tools.Execution.SSHOptions = append([]string{}, v...)
	})
	resolveCSV("tools.execution.shell_allow_patterns", file.Tools.Execution.ShellAllowPatterns, "GODEX_TOOLS_EXECUTION_SHELL_ALLOW_PATTERNS", func(v []string) {
		current.Tools.Execution.ShellAllowPatterns = append([]string{}, v...)
	})
	resolveCSV("tools.execution.shell_deny_patterns", file.Tools.Execution.ShellDenyPatterns, "GODEX_TOOLS_EXECUTION_SHELL_DENY_PATTERNS", func(v []string) {
		current.Tools.Execution.ShellDenyPatterns = append([]string{}, v...)
	})
	resolveBool("tools.browser.enabled", file.Tools.Browser.Enabled, "GODEX_BROWSER_ENABLED", func(v bool) {
		current.Tools.Browser.Enabled = v
	})
	resolveBool("tools.browser.headless", file.Tools.Browser.Headless, "GODEX_BROWSER_HEADLESS", func(v bool) {
		current.Tools.Browser.Headless = v
	})
	resolveString("tools.browser.browser_path", file.Tools.Browser.BrowserPath, "GODEX_BROWSER_PATH", func(v string) {
		current.Tools.Browser.BrowserPath = v
	})
	resolveString("tools.browser.cdp_url", file.Tools.Browser.CDPURL, "GODEX_BROWSER_CDP_URL", func(v string) {
		current.Tools.Browser.CDPURL = v
	})
	resolveInt("tools.browser.action_timeout_seconds", file.Tools.Browser.ActionTimeoutSeconds, "GODEX_BROWSER_ACTION_TIMEOUT_SECONDS", func(v int) {
		current.Tools.Browser.ActionTimeoutSeconds = v
	})
	resolveInt("tools.browser.idle_timeout_seconds", file.Tools.Browser.IdleTimeoutSeconds, "GODEX_BROWSER_IDLE_TIMEOUT_SECONDS", func(v int) {
		current.Tools.Browser.IdleTimeoutSeconds = v
	})
	resolveInt("tools.browser.max_pages_per_session", file.Tools.Browser.MaxPagesPerSession, "GODEX_BROWSER_MAX_PAGES_PER_SESSION", func(v int) {
		current.Tools.Browser.MaxPagesPerSession = v
	})
	resolveBool("tools.browser.allow_private_hosts", file.Tools.Browser.AllowPrivateHosts, "GODEX_BROWSER_ALLOW_PRIVATE_HOSTS", func(v bool) {
		current.Tools.Browser.AllowPrivateHosts = v
	})
	resolveBool("tools.history_search.enabled", file.Tools.History.Enabled, "GODEX_TOOLS_HISTORY_SEARCH_ENABLED", func(v bool) {
		current.Tools.History.Enabled = v
	})
	resolveBool("tools.history_search.auto.enabled", file.Tools.History.Auto.Enabled, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ENABLED", func(v bool) {
		current.Tools.History.Auto.Enabled = v
	})
	resolveInt("tools.history_search.auto.max_per_turn", file.Tools.History.Auto.MaxPerTurn, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_MAX_PER_TURN", func(v int) {
		current.Tools.History.Auto.MaxPerTurn = v
	})
	resolveString("tools.history_search.auto.default_scope", file.Tools.History.Auto.DefaultScope, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_DEFAULT_SCOPE", func(v string) {
		current.Tools.History.Auto.DefaultScope = v
	})
	resolveBool("tools.history_search.auto.allow_archive_on_clear", file.Tools.History.Auto.AllowArchiveOnClear, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ARCHIVE_ON_CLEAR", func(v bool) {
		current.Tools.History.Auto.AllowArchiveOnClear = v
	})
	resolveBool("tools.history_search.auto.allow_archive_on_compact", file.Tools.History.Auto.AllowArchiveOnCompact, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ARCHIVE_ON_COMPACT", func(v bool) {
		current.Tools.History.Auto.AllowArchiveOnCompact = v
	})
	resolveBool("tools.history_search.auto.allow_all_archives_automatic", file.Tools.History.Auto.AllowAllArchivesAutomatic, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_ALLOW_ALL_ARCHIVES_AUTOMATIC", func(v bool) {
		current.Tools.History.Auto.AllowAllArchivesAutomatic = v
	})
	resolveInt("tools.history_search.auto.min_score", file.Tools.History.Auto.MinScore, "GODEX_TOOLS_HISTORY_SEARCH_AUTO_MIN_SCORE", func(v int) {
		current.Tools.History.Auto.MinScore = v
	})
	resolveCSV("tools.history_search.cues.explicit", file.Tools.History.Cues.Explicit, "GODEX_TOOLS_HISTORY_SEARCH_CUES_EXPLICIT", func(v []string) {
		current.Tools.History.Cues.Explicit = append([]string{}, v...)
	})
	resolveCSV("tools.history_search.cues.implicit", file.Tools.History.Cues.Implicit, "GODEX_TOOLS_HISTORY_SEARCH_CUES_IMPLICIT", func(v []string) {
		current.Tools.History.Cues.Implicit = append([]string{}, v...)
	})
	resolveCSV("tools.history_search.blocks.session_sources", file.Tools.History.Blocks.SessionSources, "GODEX_TOOLS_HISTORY_SEARCH_BLOCKS_SESSION_SOURCES", func(v []string) {
		current.Tools.History.Blocks.SessionSources = append([]string{}, v...)
	})
	resolveBool("tools.permissions.block_automation_mutations", file.Tools.Permissions.BlockAutomationMutations, "GODEX_TOOLS_PERMISSIONS_BLOCK_AUTOMATION_MUTATIONS", func(v bool) {
		current.Tools.Permissions.BlockAutomationMutations = v
	})
	resolveBool("tools.permissions.interactive_approval_enabled", file.Tools.Permissions.InteractiveApprovalEnabled, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_ENABLED", func(v bool) {
		current.Tools.Permissions.InteractiveApprovalEnabled = v
	})
	resolveString("tools.permissions.interactive_approval_mode", file.Tools.Permissions.InteractiveApprovalMode, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_MODE", func(v string) {
		current.Tools.Permissions.InteractiveApprovalMode = v
	})
	resolveInt("tools.permissions.pending_ttl_seconds", file.Tools.Permissions.PendingTTLSeconds, "GODEX_TOOLS_PERMISSIONS_PENDING_TTL_SECONDS", func(v int) {
		current.Tools.Permissions.PendingTTLSeconds = v
	})
	resolveCSV("tools.permissions.interactive_approval_sources", file.Tools.Permissions.InteractiveApprovalSources, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_SOURCES", func(v []string) {
		current.Tools.Permissions.InteractiveApprovalSources = append([]string{}, v...)
	})
	resolveCSV("tools.permissions.interactive_approval_tools", file.Tools.Permissions.InteractiveApprovalTools, "GODEX_TOOLS_PERMISSIONS_INTERACTIVE_APPROVAL_TOOLS", func(v []string) {
		current.Tools.Permissions.InteractiveApprovalTools = append([]string{}, v...)
	})
	resolveCSV("tools.permissions.trusted_path_prefixes", file.Tools.Permissions.TrustedPathPrefixes, "GODEX_TOOLS_PERMISSIONS_TRUSTED_PATH_PREFIXES", func(v []string) {
		current.Tools.Permissions.TrustedPathPrefixes = append([]string{}, v...)
	})
	resolveCSV("tools.permissions.trusted_command_prefixes", file.Tools.Permissions.TrustedCommandPrefixes, "GODEX_TOOLS_PERMISSIONS_TRUSTED_COMMAND_PREFIXES", func(v []string) {
		current.Tools.Permissions.TrustedCommandPrefixes = append([]string{}, v...)
	})
	resolveString("tools.loop_guard.mode", file.Tools.LoopGuard.Mode, "GODEX_LOOP_GUARD_MODE", func(v string) {
		current.Tools.LoopGuard.Mode = v
	})
	resolveInt("tools.loop_guard.max_recoveries", file.Tools.LoopGuard.MaxRecoveries, "GODEX_LOOP_GUARD_MAX_RECOVERIES", func(v int) {
		current.Tools.LoopGuard.MaxRecoveries = v
	})
	resolveInt("tools.loop_guard.max_repeated_tools", file.Tools.LoopGuard.MaxRepeatedTools, "GODEX_LOOP_GUARD_MAX_REPEATED_TOOLS", func(v int) {
		current.Tools.LoopGuard.MaxRepeatedTools = v
	})
	resolveInt("tools.loop_guard.max_repeated_polling_tools", file.Tools.LoopGuard.MaxRepeatedPollingTools, "GODEX_LOOP_GUARD_MAX_REPEATED_POLLING_TOOLS", func(v int) {
		current.Tools.LoopGuard.MaxRepeatedPollingTools = v
	})
	resolveInt("tools.loop_guard.max_stalled_task_polling_tools", file.Tools.LoopGuard.MaxStalledTaskPollingTools, "GODEX_LOOP_GUARD_MAX_STALLED_TASK_POLLING_TOOLS", func(v int) {
		current.Tools.LoopGuard.MaxStalledTaskPollingTools = v
	})
	resolveInt("tools.execution.tool_timeout_seconds", file.Tools.Execution.ToolTimeoutSeconds, "GODEX_TOOLS_EXECUTION_TOOL_TIMEOUT_SECONDS", func(v int) {
		current.Tools.Execution.ToolTimeoutSeconds = v
	})
	resolveBool("media.moonshot.enabled", file.Media.Moonshot.Enabled, "GODEX_MEDIA_MOONSHOT_ENABLED", func(v bool) {
		current.Media.Moonshot.Enabled = v
	})
	resolveString("media.moonshot.base_url", file.Media.Moonshot.BaseURL, "GODEX_MEDIA_MOONSHOT_BASE_URL", func(v string) {
		current.Media.Moonshot.BaseURL = v
	})
	resolveString("media.moonshot.api_key", file.Media.Moonshot.APIKey, "GODEX_MEDIA_MOONSHOT_API_KEY", func(v string) {
		current.Media.Moonshot.APIKey = v
	})
	resolveInt("media.document.max_chars", file.Media.Document.MaxChars, "GODEX_MEDIA_DOCUMENT_MAX_CHARS", func(v int) {
		current.Media.Document.MaxChars = v
	})
	resolveString("media.document.pdftotext_path", file.Media.Document.PDFToTextPath, "GODEX_MEDIA_DOCUMENT_PDFTOTEXT_PATH", func(v string) {
		current.Media.Document.PDFToTextPath = v
	})
	resolveString("media.ocr.mode", file.Media.OCR.Mode, "GODEX_MEDIA_OCR_MODE", func(v string) {
		current.Media.OCR.Mode = v
	})
	resolveString("media.ocr.tesseract_path", file.Media.OCR.TesseractPath, "GODEX_MEDIA_OCR_TESSERACT_PATH", func(v string) {
		current.Media.OCR.TesseractPath = v
	})
	resolveInt("media.ocr.max_chars", file.Media.OCR.MaxChars, "GODEX_MEDIA_OCR_MAX_CHARS", func(v int) {
		current.Media.OCR.MaxChars = v
	})
	resolveBool("media.audio.enabled", file.Media.Audio.Enabled, "GODEX_MEDIA_AUDIO_ENABLED", func(v bool) {
		current.Media.Audio.Enabled = v
	})
	resolveString("media.audio.ffmpeg_path", file.Media.Audio.FFmpegPath, "GODEX_MEDIA_AUDIO_FFMPEG_PATH", func(v string) {
		current.Media.Audio.FFmpegPath = v
	})
	resolveString("media.audio.ffprobe_path", file.Media.Audio.FFprobePath, "GODEX_MEDIA_AUDIO_FFPROBE_PATH", func(v string) {
		current.Media.Audio.FFprobePath = v
	})
	resolveString("media.audio.whisper_cpp_path", file.Media.Audio.WhisperCPPPath, "GODEX_MEDIA_AUDIO_WHISPER_CPP_PATH", func(v string) {
		current.Media.Audio.WhisperCPPPath = v
	})
	resolveString("media.audio.whisper_model_path", file.Media.Audio.WhisperModelPath, "GODEX_MEDIA_AUDIO_WHISPER_MODEL_PATH", func(v string) {
		current.Media.Audio.WhisperModelPath = resolvePath(m.workspace, v)
	})
	resolveInt("media.audio.max_chars", file.Media.Audio.MaxChars, "GODEX_MEDIA_AUDIO_MAX_CHARS", func(v int) {
		current.Media.Audio.MaxChars = v
	})
	resolveBool("media.video.enabled", file.Media.Video.Enabled, "GODEX_MEDIA_VIDEO_ENABLED", func(v bool) {
		current.Media.Video.Enabled = v
	})
	resolveInt("media.video.keyframe_interval_seconds", file.Media.Video.KeyframeIntervalSeconds, "GODEX_MEDIA_VIDEO_KEYFRAME_INTERVAL_SECONDS", func(v int) {
		current.Media.Video.KeyframeIntervalSeconds = v
	})
	resolveInt("media.video.max_frames", file.Media.Video.MaxFrames, "GODEX_MEDIA_VIDEO_MAX_FRAMES", func(v int) {
		current.Media.Video.MaxFrames = v
	})

	origins["paths.state_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.StateDir, Effective: file.Paths.StateDir}
	origins["paths.team_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TeamDir, Effective: file.Paths.TeamDir}
	origins["paths.tasks_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TasksDir, Effective: file.Paths.TasksDir}
	origins["paths.todos_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TodosDir, Effective: file.Paths.TodosDir}
	origins["paths.memory_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.MemoryDir, Effective: file.Paths.MemoryDir}
	origins["paths.rules_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.RulesDir, Effective: file.Paths.RulesDir}
	origins["paths.skills_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.SkillsDir, Effective: file.Paths.SkillsDir}
	origins["paths.mcp_config_path"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.MCPConfigPath, Effective: file.Paths.MCPConfigPath}
	origins["paths.temp_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TempDir, Effective: file.Paths.TempDir}
	origins["paths.transcripts_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.TranscriptsDir, Effective: file.Paths.TranscriptsDir}
	origins["paths.sessions_dir"] = fieldOrigin{Source: SourceYAML, YAMLValue: file.Paths.SessionsDir, Effective: file.Paths.SessionsDir}

	current.Logging.HasSensitive = strings.TrimSpace(current.Logging.FilePath) != "" ||
		strings.TrimSpace(current.WebToken) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.BraveAPIKey) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.ExaAPIKey) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.TavilyAPIKey) != "" ||
		strings.TrimSpace(current.Tools.WebSearch.SerpAPIKey) != "" ||
		strings.TrimSpace(current.Media.Moonshot.APIKey) != ""
	return current, origins, nil
}

func (m *Manager) viewLocked() View {
	stored := storedValues(m.stored)
	effective := effectiveValues(m.current)
	fields := make(map[string]FieldState, len(m.origins))
	for _, section := range m.schema {
		for _, field := range section.Fields {
			origin := m.origins[field.Path]
			state := FieldState{
				Source:       origin.Source,
				OverriddenBy: origin.OverriddenBy,
				Secret:       field.Secret,
				Masked:       field.Secret,
				LiveApply:    field.LiveApply,
				Env:          field.Env,
				Configured:   fieldConfigured(field.Path, m.stored, m.current, origin),
			}
			if origin.UsedEnv != "" && origin.CanonicalEnv != "" && origin.UsedEnv != origin.CanonicalEnv {
				state.DeprecatedEnv = origin.UsedEnv
			}
			fields[field.Path] = state
			if field.Secret && field.Type != "json" {
				stored[field.Path] = ""
				if state.Configured {
					effective[field.Path] = "********"
				} else {
					effective[field.Path] = ""
				}
			}
		}
	}
	return View{
		FilePath:          m.configPath,
		EnvFile:           m.envPath,
		HomeDir:           m.homeDir,
		ProjectDir:        m.projectDir,
		HomeConfigFile:    m.homeConfigPath,
		ProjectConfigFile: m.projectConfigPath,
		HomeEnvFile:       m.homeEnvPath,
		ProjectEnvFile:    m.projectEnvPath,
		Revision:          m.revision,
		StoredValues:      stored,
		EffectiveValues:   effective,
		Fields:            fields,
		LastApply:         cloneApplyReport(m.lastApply),
	}
}

func resolveConfigFile(file ConfigFile, homeDir, projectDir, configFile, envFile, homeConfigFile, projectConfigFile, homeEnvFile, projectEnvFile string) *Config {
	if projectDir == "" {
		projectDir = defaultWorkspaceDir()
	}
	if homeDir == "" {
		homeDir = defaultHomeDir()
	}
	workspace := projectDir
	modelProfiles := modelProfilesFromConfigFile(file)
	defaultProfileID := strings.TrimSpace(file.API.DefaultModel)
	if defaultProfileID == "" {
		defaultProfileID = strings.TrimSpace(file.API.DefaultProfile)
	}
	if defaultProfileID == "" {
		defaultProfileID = defaultProfileIDFromProfiles(modelProfiles)
	}
	if _, ok := modelProfiles[defaultProfileID]; !ok {
		defaultProfileID = defaultProfileIDFromProfiles(modelProfiles)
	}
	defaultProfile := modelProfiles[defaultProfileID]
	cfg := &Config{
		APIKey:              defaultProfile.APIKey,
		Model:               defaultProfile.Model,
		BaseURL:             defaultProfile.BaseURL,
		DefaultProfileID:    defaultProfileID,
		DefaultModelRef:     defaultProfileID,
		AutoFallbackEnabled: file.API.AutoFallbackEnabled,
		ModelProfiles:       modelProfiles,
		LLMProviders:        llmProvidersFromConfigFile(file),
		LLMStrategy:         llmStrategyFromConfigFile(file),
		APITimeoutSeconds:   defaultProfile.TimeoutSeconds,
		WebToken:            file.Web.Token,
		Cron: CronConfig{
			Enabled:           file.Cron.Enabled,
			TickSeconds:       file.Cron.TickSeconds,
			DefaultTimezone:   file.Cron.DefaultTimezone,
			MaxConcurrentRuns: file.Cron.MaxConcurrentRuns,
		},
		Heartbeat: HeartbeatConfig{
			Enabled:                file.Heartbeat.Enabled,
			TickSeconds:            file.Heartbeat.TickSeconds,
			ChecklistPath:          resolvePath(workspace, file.Heartbeat.ChecklistPath),
			OKToken:                file.Heartbeat.OKToken,
			DefaultIntervalSeconds: file.Heartbeat.DefaultIntervalSeconds,
			DefaultTimezone:        file.Heartbeat.DefaultTimezone,
		},
		Control: ControlConfig{
			NodeName:            file.Control.NodeName,
			CenterURL:           file.Control.CenterURL,
			HeartbeatSeconds:    file.Control.HeartbeatSeconds,
			OfflineAfterSeconds: file.Control.OfflineAfterSeconds,
			Nodes:               controlNodesFromConfigFile(file),
		},
		Runtime: RuntimeConfig{
			Recovery: RuntimeRecoveryConfig{
				AutoResumeInterruptedTurns: file.Runtime.Recovery.AutoResumeInterruptedTurns,
				AutoRepairSessions:         file.Runtime.Recovery.AutoRepairSessions,
			},
		},
		Storage: StorageConfig{
			TmpTTLHours:                 file.Storage.TmpTTLHours,
			ArtifactTTLHours:            file.Storage.ArtifactTTLHours,
			BrowserCacheAutoClean:       file.Storage.BrowserCacheAutoClean,
			BrowserCacheMaxMB:           file.Storage.BrowserCacheMaxMB,
			SessionCheckpointKeepLatest: file.Storage.SessionCheckpointKeepLatest,
			SessionCheckpointTTLHours:   file.Storage.SessionCheckpointTTLHours,
			SessionCheckpointAutoPrune:  file.Storage.SessionCheckpointAutoPrune,
			SessionBackend:              normalizeSessionStorageBackend(file.Storage.SessionBackend),
			SQLitePath:                  strings.TrimSpace(file.Storage.SQLitePath),
		},
		Security: SecurityConfig{
			Profile: normalizeSecurityProfileName(file.Security.Profile),
		},
		MaxTokens:         defaultProfile.MaxTokens,
		HomeDir:           homeDir,
		WorkspaceDir:      workspace,
		ProjectDir:        projectDir,
		ConfigFile:        configFile,
		HomeConfigFile:    homeConfigFile,
		ProjectConfigFile: projectConfigFile,
		StateDir:          resolvePath(homeDir, file.Paths.StateDir),
		TeamDir:           resolvePath(homeDir, file.Paths.TeamDir),
		TasksDir:          resolvePath(homeDir, file.Paths.TasksDir),
		MemoryDir:         resolvePath(homeDir, file.Paths.MemoryDir),
		RulesDir:          resolvePath(homeDir, file.Paths.RulesDir),
		SkillsDir:         resolvePath(homeDir, file.Paths.SkillsDir),
		PackagesDir:       resolvePath(homeDir, "packages"),
		TodosDir:          resolvePath(homeDir, file.Paths.TodosDir),
		MCPConfigPath:     resolvePath(homeDir, file.Paths.MCPConfigPath),
		TempDir:           resolvePath(homeDir, file.Paths.TempDir),
		TranscriptsDir:    resolvePath(homeDir, file.Paths.TranscriptsDir),
		SessionsDir:       resolvePath(homeDir, file.Paths.SessionsDir),
		CompressThreshold: file.Agent.CompressThreshold,
		Compaction: AgentCompactionConfig{
			AutoEnabled:         file.Agent.Compaction.AutoEnabled,
			TriggerTokens:       positiveOrDefault(file.Agent.Compaction.TriggerTokens, 60000),
			TargetHistoryTokens: positiveOrDefault(file.Agent.Compaction.TargetHistoryTokens, 12000),
			Mode:                NormalizeCompactionMode(file.Agent.Compaction.Mode),
			ModelProfileID:      strings.TrimSpace(file.Agent.Compaction.ModelProfileID),
			MaxLatencyMS:        positiveOrDefault(file.Agent.Compaction.MaxLatencyMS, 3000),
			KeepRecentMessages:  positiveOrDefault(file.Agent.Compaction.KeepRecentMessages, 20),
		},
		MaxTurns:     file.Agent.MaxTurns,
		AgentProfile: NormalizeAgentProfile(file.Agent.Profile),
		AgentDefaultProfiles: AgentDefaultProfilesConfig{
			ACP:    NormalizeAgentProfile(file.Agent.DefaultProfiles.ACP),
			CLI:    NormalizeAgentProfile(file.Agent.DefaultProfiles.CLI),
			TUI:    NormalizeAgentProfile(file.Agent.DefaultProfiles.TUI),
			Web:    NormalizeAgentProfile(file.Agent.DefaultProfiles.Web),
			Weixin: NormalizeAgentProfile(file.Agent.DefaultProfiles.Weixin),
			Feishu: NormalizeAgentProfile(file.Agent.DefaultProfiles.Feishu),
		},
		LeadName:          file.Team.LeadName,
		TeamName:          file.Team.TeamName,
		DefaultSkills:     append([]string{}, file.Team.DefaultSkills...),
		TeammateWorkLimit: file.Team.TeammateWorkLimit,
		TeammatePollEvery: time.Duration(file.Team.TeammatePollSeconds) * time.Second,
		TeammateIdleFor:   time.Duration(file.Team.TeammateIdleTimeoutSecs) * time.Second,
		EnvFile:           envFile,
		HomeEnvFile:       homeEnvFile,
		ProjectEnvFile:    projectEnvFile,
		Logging: LoggingConfig{
			Level:      file.Logging.Level,
			FilePath:   resolveLogPath(homeDir, file.Logging.FilePath),
			AlsoStderr: file.Logging.AlsoStderr,
		},
		Tools: ToolsConfig{
			WebSearch: WebSearchConfig{
				Enabled:         file.Tools.WebSearch.Enabled,
				ProviderOrder:   append([]string{}, file.Tools.WebSearch.ProviderOrder...),
				CacheTTLSeconds: file.Tools.WebSearch.CacheTTLSeconds,
				Browser: WebSearchBrowserConfig{
					Engine:               file.Tools.WebSearch.Browser.Engine,
					EngineFallback:       append([]string{}, file.Tools.WebSearch.Browser.EngineFallback...),
					Engines:              webSearchBrowserEnginesFromFile(file.Tools.WebSearch.Browser.Engines),
					WaitNetworkIdleMS:    file.Tools.WebSearch.Browser.WaitNetworkIdleMS,
					WaitAfterLoadMS:      file.Tools.WebSearch.Browser.WaitAfterLoadMS,
					MaxScrolls:           file.Tools.WebSearch.Browser.MaxScrolls,
					ResultTimeoutSeconds: file.Tools.WebSearch.Browser.ResultTimeoutSeconds,
					PreferredHosts:       append([]string{}, file.Tools.WebSearch.Browser.PreferredHosts...),
				},
				BraveAPIKey:  file.Tools.WebSearch.Brave.APIKey,
				ExaAPIKey:    file.Tools.WebSearch.Exa.APIKey,
				TavilyAPIKey: file.Tools.WebSearch.Tavily.APIKey,
				SerpAPIKey:   file.Tools.WebSearch.SerpAPI.APIKey,
			},
			WebFetch: WebFetchConfig{
				Enabled:           file.Tools.WebFetch.Enabled,
				MaxChars:          file.Tools.WebFetch.MaxChars,
				TimeoutSeconds:    file.Tools.WebFetch.TimeoutSeconds,
				Policy:            file.Tools.WebFetch.Policy,
				AllowedDomains:    append([]string{}, file.Tools.WebFetch.AllowedDomains...),
				BlockedDomains:    append([]string{}, file.Tools.WebFetch.BlockedDomains...),
				AllowPrivateHosts: file.Tools.WebFetch.AllowPrivateHosts,
			},
			Glob: GlobConfig{
				DefaultMaxResults: file.Tools.Glob.DefaultMaxResults,
			},
			Subagent: SubagentConfig{
				MaxBatchSize:         file.Tools.Subagent.MaxBatchSize,
				MaxConcurrentJobs:    file.Tools.Subagent.MaxConcurrentJobs,
				DefaultMaxTurns:      file.Tools.Subagent.DefaultMaxTurns,
				MaxJobTimeoutMs:      file.Tools.Subagent.MaxJobTimeoutMs,
				ReadOnlyIsolation:    normalizeSubagentReadOnlyIsolation(file.Tools.Subagent.ReadOnlyIsolation),
				GitDirtyIsolation:    normalizeSubagentGitDirtyIsolation(file.Tools.Subagent.GitDirtyIsolation),
				NonGitWriteIsolation: normalizeSubagentNonGitWriteIsolation(file.Tools.Subagent.NonGitWriteIsolation),
				WorkspaceTTLHours:    file.Tools.Subagent.WorkspaceTTLHours,
			},
			Execution: ToolExecutionConfig{
				Mode:               file.Tools.Execution.Mode,
				DockerImage:        file.Tools.Execution.DockerImage,
				DockerNetwork:      file.Tools.Execution.DockerNetwork,
				SSHTarget:          file.Tools.Execution.SSHTarget,
				SSHWorkspace:       file.Tools.Execution.SSHWorkspace,
				SSHOptions:         append([]string{}, file.Tools.Execution.SSHOptions...),
				ShellAllowPatterns: append([]string{}, file.Tools.Execution.ShellAllowPatterns...),
				ShellDenyPatterns:  append([]string{}, file.Tools.Execution.ShellDenyPatterns...),
				ToolTimeoutSeconds: file.Tools.Execution.ToolTimeoutSeconds,
			},
			Browser: BrowserConfig{
				Enabled:              file.Tools.Browser.Enabled,
				Headless:             file.Tools.Browser.Headless,
				BrowserPath:          file.Tools.Browser.BrowserPath,
				CDPURL:               file.Tools.Browser.CDPURL,
				ActionTimeoutSeconds: file.Tools.Browser.ActionTimeoutSeconds,
				IdleTimeoutSeconds:   file.Tools.Browser.IdleTimeoutSeconds,
				MaxPagesPerSession:   file.Tools.Browser.MaxPagesPerSession,
				AllowPrivateHosts:    file.Tools.Browser.AllowPrivateHosts,
			},
			Lightpanda: LightpandaConfig{
				Enabled:        file.Tools.Lightpanda.Enabled,
				BinaryPath:     file.Tools.Lightpanda.BinaryPath,
				AutoDownload:   file.Tools.Lightpanda.AutoDownload,
				SearchEngine:   file.Tools.Lightpanda.SearchEngine,
				SearchTemplate: file.Tools.Lightpanda.SearchTemplate,
				WaitNetworkMS:  file.Tools.Lightpanda.WaitNetworkMS,
				ObeyRobots:     file.Tools.Lightpanda.ObeyRobots,
				LogLevel:       file.Tools.Lightpanda.LogLevel,
			},
			History: HistorySearchConfig{
				Enabled: file.Tools.History.Enabled,
				Auto: HistorySearchAutoConfig{
					Enabled:                   file.Tools.History.Auto.Enabled,
					MaxPerTurn:                file.Tools.History.Auto.MaxPerTurn,
					DefaultScope:              file.Tools.History.Auto.DefaultScope,
					AllowArchiveOnClear:       file.Tools.History.Auto.AllowArchiveOnClear,
					AllowArchiveOnCompact:     file.Tools.History.Auto.AllowArchiveOnCompact,
					AllowAllArchivesAutomatic: file.Tools.History.Auto.AllowAllArchivesAutomatic,
					MinScore:                  file.Tools.History.Auto.MinScore,
				},
				Cues: HistorySearchCueConfig{
					Explicit: append([]string{}, file.Tools.History.Cues.Explicit...),
					Implicit: append([]string{}, file.Tools.History.Cues.Implicit...),
				},
				Blocks: HistorySearchBlockConfig{
					SessionSources: append([]string{}, file.Tools.History.Blocks.SessionSources...),
				},
			},
			Permissions: PermissionConfig{
				BlockAutomationMutations:   file.Tools.Permissions.BlockAutomationMutations,
				InteractiveApprovalEnabled: file.Tools.Permissions.InteractiveApprovalEnabled,
				InteractiveApprovalMode:    file.Tools.Permissions.InteractiveApprovalMode,
				InteractiveApprovalSources: append([]string{}, file.Tools.Permissions.InteractiveApprovalSources...),
				InteractiveApprovalTools:   append([]string{}, file.Tools.Permissions.InteractiveApprovalTools...),
				PendingTTLSeconds:          file.Tools.Permissions.PendingTTLSeconds,
				TrustedPathPrefixes:        append([]string{}, file.Tools.Permissions.TrustedPathPrefixes...),
				TrustedCommandPrefixes:     append([]string{}, file.Tools.Permissions.TrustedCommandPrefixes...),
			},
			LoopGuard: LoopGuardConfig{
				Mode:                       file.Tools.LoopGuard.Mode,
				MaxRecoveries:              file.Tools.LoopGuard.MaxRecoveries,
				MaxRepeatedTools:           file.Tools.LoopGuard.MaxRepeatedTools,
				MaxRepeatedPollingTools:    file.Tools.LoopGuard.MaxRepeatedPollingTools,
				MaxStalledTaskPollingTools: file.Tools.LoopGuard.MaxStalledTaskPollingTools,
			},
		},
		Media: MediaConfig{
			Moonshot: MoonshotMediaConfig{
				Enabled: file.Media.Moonshot.Enabled,
				BaseURL: file.Media.Moonshot.BaseURL,
				APIKey:  file.Media.Moonshot.APIKey,
			},
			Document: DocumentMediaConfig{
				MaxChars:      file.Media.Document.MaxChars,
				PDFToTextPath: file.Media.Document.PDFToTextPath,
			},
			OCR: OCRMediaConfig{
				Mode:          file.Media.OCR.Mode,
				TesseractPath: file.Media.OCR.TesseractPath,
				MaxChars:      file.Media.OCR.MaxChars,
			},
			Audio: AudioMediaConfig{
				Enabled:          file.Media.Audio.Enabled,
				FFmpegPath:       file.Media.Audio.FFmpegPath,
				FFprobePath:      file.Media.Audio.FFprobePath,
				WhisperCPPPath:   file.Media.Audio.WhisperCPPPath,
				WhisperModelPath: resolvePath(workspace, file.Media.Audio.WhisperModelPath),
				MaxChars:         file.Media.Audio.MaxChars,
			},
			Video: VideoMediaConfig{
				Enabled:                 file.Media.Video.Enabled,
				KeyframeIntervalSeconds: file.Media.Video.KeyframeIntervalSeconds,
				MaxFrames:               file.Media.Video.MaxFrames,
			},
		},
		ACP: ACPConfig{
			Agents: acpAgentsFromConfigFile(file),
		},
		Feishu: FeishuConfig{
			Enabled:   file.Channels.Feishu.Enabled,
			AppID:     file.Channels.Feishu.AppID,
			AppSecret: file.Channels.Feishu.AppSecret,
			Domain:    file.Channels.Feishu.Domain,
		},
		Weixin: WeixinConfig{
			Enabled:           file.Channels.Weixin.Enabled,
			BaseURL:           file.Channels.Weixin.BaseURL,
			CDNBaseURL:        file.Channels.Weixin.CDNBaseURL,
			AccountID:         file.Channels.Weixin.AccountID,
			AllowFrom:         append([]string{}, file.Channels.Weixin.AllowFrom...),
			RouteTag:          file.Channels.Weixin.RouteTag,
			LongPollTimeoutMs: file.Channels.Weixin.LongPollTimeoutMs,
			Proxy:             file.Channels.Weixin.Proxy,
		},
	}
	return cfg
}

func parseConfigFile(path string) (ConfigFile, error) {
	defaults := defaultConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigFile{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&defaults); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return defaults, nil
}

func (m *Manager) effectiveConfigFile(homeFile ConfigFile) (ConfigFile, error) {
	if strings.TrimSpace(m.projectConfigPath) == "" || filepath.Clean(m.projectConfigPath) == filepath.Clean(m.homeConfigPath) {
		return homeFile, nil
	}
	if _, err := os.Stat(m.projectConfigPath); err != nil {
		if os.IsNotExist(err) {
			return homeFile, nil
		}
		return ConfigFile{}, err
	}
	return mergeConfigFileLayer(homeFile, m.projectConfigPath)
}

func mergeConfigFileLayer(base ConfigFile, layerPath string) (ConfigFile, error) {
	baseMap, err := configFileToMap(base)
	if err != nil {
		return ConfigFile{}, err
	}
	data, err := os.ReadFile(layerPath)
	if err != nil {
		return ConfigFile{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return base, nil
	}
	validator := ConfigFile{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&validator); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", layerPath, err)
	}
	var layer map[string]any
	if err := yaml.Unmarshal(data, &layer); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", layerPath, err)
	}
	deepMergeYAML(baseMap, layer)
	return configFileFromMap(layerPath, baseMap)
}

func configFileToMap(file ConfigFile) (map[string]any, error) {
	data, err := yaml.Marshal(file)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func configFileFromMap(path string, values map[string]any) (ConfigFile, error) {
	data, err := yaml.Marshal(values)
	if err != nil {
		return ConfigFile{}, err
	}
	var file ConfigFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return ConfigFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return file, nil
}

func deepMergeYAML(dst, src map[string]any) {
	for key, srcValue := range src {
		srcMap, srcOK := srcValue.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			deepMergeYAML(dstMap, srcMap)
			continue
		}
		dst[key] = srcValue
	}
}
