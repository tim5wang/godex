package config

import (
	"fmt"
	"strings"
)

var toolStoredValueSetters = map[string]storedValueSetter{
	"web_search":     setToolWebSearchStoredValue,
	"web_fetch":      setToolWebFetchStoredValue,
	"glob":           setToolGlobStoredValue,
	"subagent":       setToolSubagentStoredValue,
	"execution":      setToolExecutionStoredValue,
	"browser":        setToolBrowserStoredValue,
	"history_search": setToolHistorySearchStoredValue,
	"permissions":    setToolPermissionsStoredValue,
	"loop_guard":     setToolLoopGuardStoredValue,
	"lightpanda":     setToolLightpandaStoredValue,
}

func setToolsStoredValue(file *ConfigFile, path string, value any) error {
	parts := strings.SplitN(path, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("unknown config field: %s", path)
	}
	setter, ok := toolStoredValueSetters[parts[1]]
	if !ok {
		return fmt.Errorf("unknown config field: %s", path)
	}
	return setter(file, path, value)
}

func setToolWebSearchStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.web_search.enabled":
		file.Tools.WebSearch.Enabled = asBool(value)
	case "tools.web_search.provider_order":
		file.Tools.WebSearch.ProviderOrder = asStringList(value)
	case "tools.web_search.cache_ttl_seconds":
		file.Tools.WebSearch.CacheTTLSeconds = asInt(value)
	case "tools.web_search.browser.engine":
		file.Tools.WebSearch.Browser.Engine = asString(value)
	case "tools.web_search.browser.engine_fallback":
		file.Tools.WebSearch.Browser.EngineFallback = asStringList(value)
	case "tools.web_search.browser.wait_network_idle_ms":
		file.Tools.WebSearch.Browser.WaitNetworkIdleMS = asInt(value)
	case "tools.web_search.browser.wait_after_load_ms":
		file.Tools.WebSearch.Browser.WaitAfterLoadMS = asInt(value)
	case "tools.web_search.browser.max_scrolls":
		file.Tools.WebSearch.Browser.MaxScrolls = asInt(value)
	case "tools.web_search.browser.result_timeout_seconds":
		file.Tools.WebSearch.Browser.ResultTimeoutSeconds = asInt(value)
	case "tools.web_search.browser.preferred_hosts":
		file.Tools.WebSearch.Browser.PreferredHosts = asStringList(value)
	case "tools.web_search.brave.api_key":
		file.Tools.WebSearch.Brave.APIKey = asString(value)
	case "tools.web_search.exa.api_key":
		file.Tools.WebSearch.Exa.APIKey = asString(value)
	case "tools.web_search.tavily.api_key":
		file.Tools.WebSearch.Tavily.APIKey = asString(value)
	case "tools.web_search.serpapi.api_key":
		file.Tools.WebSearch.SerpAPI.APIKey = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolWebFetchStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.web_fetch.enabled":
		file.Tools.WebFetch.Enabled = asBool(value)
	case "tools.web_fetch.max_chars":
		file.Tools.WebFetch.MaxChars = asInt(value)
	case "tools.web_fetch.timeout_seconds":
		file.Tools.WebFetch.TimeoutSeconds = asInt(value)
	case "tools.web_fetch.policy":
		file.Tools.WebFetch.Policy = asString(value)
	case "tools.web_fetch.allowed_domains":
		file.Tools.WebFetch.AllowedDomains = asStringList(value)
	case "tools.web_fetch.blocked_domains":
		file.Tools.WebFetch.BlockedDomains = asStringList(value)
	case "tools.web_fetch.allow_private_hosts":
		file.Tools.WebFetch.AllowPrivateHosts = asBool(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolGlobStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.glob.default_max_results":
		file.Tools.Glob.DefaultMaxResults = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolSubagentStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.subagent.max_batch_size":
		file.Tools.Subagent.MaxBatchSize = asInt(value)
	case "tools.subagent.max_concurrent_jobs":
		file.Tools.Subagent.MaxConcurrentJobs = asInt(value)
	case "tools.subagent.default_max_turns":
		file.Tools.Subagent.DefaultMaxTurns = asInt(value)
	case "tools.subagent.max_job_timeout_ms":
		file.Tools.Subagent.MaxJobTimeoutMs = asInt(value)
	case "tools.subagent.readonly_isolation":
		file.Tools.Subagent.ReadOnlyIsolation = normalizeSubagentReadOnlyIsolation(asString(value))
	case "tools.subagent.git_dirty_isolation":
		file.Tools.Subagent.GitDirtyIsolation = normalizeSubagentGitDirtyIsolation(asString(value))
	case "tools.subagent.non_git_write_isolation":
		file.Tools.Subagent.NonGitWriteIsolation = normalizeSubagentNonGitWriteIsolation(asString(value))
	case "tools.subagent.workspace_ttl_hours":
		file.Tools.Subagent.WorkspaceTTLHours = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolExecutionStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.execution.mode":
		file.Tools.Execution.Mode = asString(value)
	case "tools.execution.docker_image":
		file.Tools.Execution.DockerImage = asString(value)
	case "tools.execution.docker_network":
		file.Tools.Execution.DockerNetwork = asString(value)
	case "tools.execution.ssh_target":
		file.Tools.Execution.SSHTarget = asString(value)
	case "tools.execution.ssh_workspace":
		file.Tools.Execution.SSHWorkspace = asString(value)
	case "tools.execution.ssh_options":
		file.Tools.Execution.SSHOptions = asStringList(value)
	case "tools.execution.shell_allow_patterns":
		file.Tools.Execution.ShellAllowPatterns = asStringList(value)
	case "tools.execution.shell_deny_patterns":
		file.Tools.Execution.ShellDenyPatterns = asStringList(value)
	case "tools.execution.scope_write":
		file.Tools.Execution.ScopeWrite = asBool(value)
	case "tools.execution.tool_timeout_seconds":
		file.Tools.Execution.ToolTimeoutSeconds = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolBrowserStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.browser.enabled":
		file.Tools.Browser.Enabled = asBool(value)
	case "tools.browser.headless":
		file.Tools.Browser.Headless = asBool(value)
	case "tools.browser.browser_path":
		file.Tools.Browser.BrowserPath = asString(value)
	case "tools.browser.cdp_url":
		file.Tools.Browser.CDPURL = asString(value)
	case "tools.browser.action_timeout_seconds":
		file.Tools.Browser.ActionTimeoutSeconds = asInt(value)
	case "tools.browser.idle_timeout_seconds":
		file.Tools.Browser.IdleTimeoutSeconds = asInt(value)
	case "tools.browser.max_pages_per_session":
		file.Tools.Browser.MaxPagesPerSession = asInt(value)
	case "tools.browser.allow_private_hosts":
		file.Tools.Browser.AllowPrivateHosts = asBool(value)
	case "tools.browser.persistent_profile":
		file.Tools.Browser.PersistentProfile = asBool(value)
	case "tools.browser.cdp_listen":
		file.Tools.Browser.CDPListen = asString(value)
	case "tools.browser.cdp_relay_node":
		file.Tools.Browser.CDPRelayNode = asString(value)
	case "tools.browser.cdp_relay_target":
		file.Tools.Browser.CDPRelayTarget = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolHistorySearchStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.history_search.enabled":
		file.Tools.History.Enabled = asBool(value)
	case "tools.history_search.auto.enabled":
		file.Tools.History.Auto.Enabled = asBool(value)
	case "tools.history_search.auto.max_per_turn":
		file.Tools.History.Auto.MaxPerTurn = asInt(value)
	case "tools.history_search.auto.default_scope":
		file.Tools.History.Auto.DefaultScope = asString(value)
	case "tools.history_search.auto.allow_archive_on_clear":
		file.Tools.History.Auto.AllowArchiveOnClear = asBool(value)
	case "tools.history_search.auto.allow_archive_on_compact":
		file.Tools.History.Auto.AllowArchiveOnCompact = asBool(value)
	case "tools.history_search.auto.allow_all_archives_automatic":
		file.Tools.History.Auto.AllowAllArchivesAutomatic = asBool(value)
	case "tools.history_search.auto.min_score":
		file.Tools.History.Auto.MinScore = asInt(value)
	case "tools.history_search.cues.explicit":
		file.Tools.History.Cues.Explicit = asStringList(value)
	case "tools.history_search.cues.implicit":
		file.Tools.History.Cues.Implicit = asStringList(value)
	case "tools.history_search.blocks.session_sources":
		file.Tools.History.Blocks.SessionSources = asStringList(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolPermissionsStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.permissions.block_automation_mutations":
		file.Tools.Permissions.BlockAutomationMutations = asBool(value)
	case "tools.permissions.interactive_approval_enabled":
		file.Tools.Permissions.InteractiveApprovalEnabled = asBool(value)
	case "tools.permissions.interactive_approval_mode":
		file.Tools.Permissions.InteractiveApprovalMode = asString(value)
	case "tools.permissions.pending_ttl_seconds":
		file.Tools.Permissions.PendingTTLSeconds = asInt(value)
	case "tools.permissions.interactive_approval_sources":
		file.Tools.Permissions.InteractiveApprovalSources = asStringList(value)
	case "tools.permissions.interactive_approval_tools":
		file.Tools.Permissions.InteractiveApprovalTools = asStringList(value)
	case "tools.permissions.trusted_path_prefixes":
		file.Tools.Permissions.TrustedPathPrefixes = asStringList(value)
	case "tools.permissions.trusted_command_prefixes":
		file.Tools.Permissions.TrustedCommandPrefixes = asStringList(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolLoopGuardStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.loop_guard.mode":
		file.Tools.LoopGuard.Mode = asString(value)
	case "tools.loop_guard.max_recoveries":
		file.Tools.LoopGuard.MaxRecoveries = asInt(value)
	case "tools.loop_guard.max_repeated_tools":
		file.Tools.LoopGuard.MaxRepeatedTools = asInt(value)
	case "tools.loop_guard.max_repeated_polling_tools":
		file.Tools.LoopGuard.MaxRepeatedPollingTools = asInt(value)
	case "tools.loop_guard.max_stalled_task_polling_tools":
		file.Tools.LoopGuard.MaxStalledTaskPollingTools = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setToolLightpandaStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "tools.lightpanda.enabled":
		file.Tools.Lightpanda.Enabled = asBool(value)
	case "tools.lightpanda.binary_path":
		file.Tools.Lightpanda.BinaryPath = asString(value)
	case "tools.lightpanda.auto_download":
		file.Tools.Lightpanda.AutoDownload = asBool(value)
	case "tools.lightpanda.search_engine":
		file.Tools.Lightpanda.SearchEngine = asString(value)
	case "tools.lightpanda.search_template":
		file.Tools.Lightpanda.SearchTemplate = asString(value)
	case "tools.lightpanda.wait_network_ms":
		file.Tools.Lightpanda.WaitNetworkMS = asInt(value)
	case "tools.lightpanda.obey_robots":
		file.Tools.Lightpanda.ObeyRobots = asBool(value)
	case "tools.lightpanda.log_level":
		file.Tools.Lightpanda.LogLevel = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}
