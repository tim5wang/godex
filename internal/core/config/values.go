package config

import (
	"fmt"
	"strings"
	"time"
)

func applyStoredValues(file *ConfigFile, req UpdateRequest) error {
	if file == nil {
		return fmt.Errorf("missing config file state")
	}
	canonicalAPITouched := false
	clearSecrets := make(map[string]struct{}, len(req.ClearSecrets))
	for _, path := range req.ClearSecrets {
		clearSecrets[strings.TrimSpace(path)] = struct{}{}
	}

	for _, section := range baseSchema() {
		for _, field := range section.Fields {
			value, ok := req.Values[field.Path]
			if field.Secret {
				if _, clear := clearSecrets[field.Path]; clear {
					value = ""
					ok = true
				}
				if !ok {
					continue
				}
				if field.Type == "json" {
					if value == nil {
						continue
					}
					if text, isString := value.(string); isString && strings.TrimSpace(text) == "" {
						continue
					}
				} else {
					if asString := strings.TrimSpace(asString(value)); asString == "" {
						continue
					} else {
						value = asString
					}
				}
			}
			if !ok {
				continue
			}
			if isCanonicalAPIPath(field.Path) {
				canonicalAPITouched = true
			}
			if err := setStoredValue(file, field.Path, field.Type, value); err != nil {
				return err
			}
		}
	}
	if canonicalAPITouched {
		clearLegacyAPIFields(file)
	}
	for path := range clearSecrets {
		switch path {
		case "api.providers":
			for id, provider := range file.API.Providers {
				provider.APIKey = ""
				file.API.Providers[id] = provider
			}
		case "web.token":
			file.Web.Token = ""
		case "tools.web_search.brave.api_key":
			file.Tools.WebSearch.Brave.APIKey = ""
		case "tools.web_search.exa.api_key":
			file.Tools.WebSearch.Exa.APIKey = ""
		case "tools.web_search.tavily.api_key":
			file.Tools.WebSearch.Tavily.APIKey = ""
		case "media.moonshot.api_key":
			file.Media.Moonshot.APIKey = ""
		case "channels.feishu.app_id":
			file.Channels.Feishu.AppID = ""
		case "channels.feishu.app_secret":
			file.Channels.Feishu.AppSecret = ""
		}
	}
	return nil
}

func isCanonicalAPIPath(path string) bool {
	switch path {
	case "api.default_model", "api.providers", "api.model_strategy":
		return true
	default:
		return false
	}
}

func clearLegacyAPIFields(file *ConfigFile) {
	file.API.DefaultProfile = ""
	file.API.AutoFallbackEnabled = true
	file.API.TimeoutSeconds = 0
}

func storedValues(file ConfigFile) map[string]any {
	values := map[string]any{
		"api.default_model":                                      file.API.DefaultModel,
		"api.default_profile":                                    file.API.DefaultProfile,
		"api.auto_fallback_enabled":                              file.API.AutoFallbackEnabled,
		"api.providers":                                          maskLLMProviders(llmProvidersFromConfigFile(file)),
		"api.model_strategy":                                     llmStrategyFromConfigFile(file),
		"acp.agents":                                             maskACPAgentSections(file.ACP.Agents),
		"acp.bridge_client_mcp_servers":                          file.ACP.BridgeClientMCPServers,
		"api.timeout_seconds":                                    file.API.TimeoutSeconds,
		"agent.compress_threshold":                               file.Agent.CompressThreshold,
		"agent.compaction.auto_enabled":                          file.Agent.Compaction.AutoEnabled,
		"agent.compaction.trigger_tokens":                        file.Agent.Compaction.TriggerTokens,
		"agent.compaction.target_history_tokens":                 file.Agent.Compaction.TargetHistoryTokens,
		"agent.compaction.mode":                                  NormalizeCompactionMode(file.Agent.Compaction.Mode),
		"agent.compaction.model_profile_id":                      strings.TrimSpace(file.Agent.Compaction.ModelProfileID),
		"agent.compaction.max_latency_ms":                        file.Agent.Compaction.MaxLatencyMS,
		"agent.compaction.keep_recent_messages":                  file.Agent.Compaction.KeepRecentMessages,
		"agent.compaction.context_window_tokens":                 file.Agent.Compaction.ContextWindowTokens,
		"agent.compaction.trigger_ratio":                         file.Agent.Compaction.TriggerRatio,
		"agent.compaction.retain_ratio":                          file.Agent.Compaction.RetainRatio,
		"agent.compaction.retain_tokens":                         file.Agent.Compaction.RetainTokens,
		"agent.compaction.prune_threshold_chars":                 file.Agent.Compaction.PruneThresholdChars,
		"agent.compaction.prune_head_chars":                      file.Agent.Compaction.PruneHeadChars,
		"agent.compaction.prune_tail_chars":                      file.Agent.Compaction.PruneTailChars,
		"agent.max_turns":                                        file.Agent.MaxTurns,
		"agent.profile":                                          NormalizeAgentProfile(file.Agent.Profile),
		"agent.default_profiles.acp":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.ACP),
		"agent.default_profiles.cli":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.CLI),
		"agent.default_profiles.tui":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.TUI),
		"agent.default_profiles.web":                             NormalizeAgentProfile(file.Agent.DefaultProfiles.Web),
		"agent.default_profiles.weixin":                          NormalizeAgentProfile(file.Agent.DefaultProfiles.Weixin),
		"agent.default_profiles.feishu":                          NormalizeAgentProfile(file.Agent.DefaultProfiles.Feishu),
		"logging.level":                                          file.Logging.Level,
		"logging.file_path":                                      file.Logging.FilePath,
		"logging.also_stderr":                                    file.Logging.AlsoStderr,
		"web.token":                                              "",
		"cron.enabled":                                           file.Cron.Enabled,
		"cron.tick_seconds":                                      file.Cron.TickSeconds,
		"cron.default_timezone":                                  file.Cron.DefaultTimezone,
		"cron.max_concurrent_runs":                               file.Cron.MaxConcurrentRuns,
		"heartbeat.enabled":                                      file.Heartbeat.Enabled,
		"heartbeat.tick_seconds":                                 file.Heartbeat.TickSeconds,
		"heartbeat.checklist_path":                               file.Heartbeat.ChecklistPath,
		"heartbeat.ok_token":                                     file.Heartbeat.OKToken,
		"heartbeat.default_interval_seconds":                     file.Heartbeat.DefaultIntervalSeconds,
		"heartbeat.default_timezone":                             file.Heartbeat.DefaultTimezone,
		"heartbeat.default_watchdog_script":                      file.Heartbeat.DefaultWatchdogScript,
		"control.node_name":                                      file.Control.NodeName,
		"control.node_id":                                        file.Control.NodeID,
		"control.default_node":                                   file.Control.DefaultNode,
		"control.trust_level":                                    file.Control.TrustLevel,
		"control.center_url":                                     file.Control.CenterURL,
		"control.credential":                                     "",
		"control.heartbeat_seconds":                              file.Control.HeartbeatSeconds,
		"control.offline_after_seconds":                          file.Control.OfflineAfterSeconds,
		"control.forward_allow":                                  append([]string{}, file.Control.ForwardAllow...),
		"control.nodes":                                          append([]ControlNodeSection{}, file.Control.Nodes...),
		"control.forwards":                                       append([]ForwardSection{}, file.Control.Forwards...),
		"runtime.recovery.auto_resume_interrupted_turns":         file.Runtime.Recovery.AutoResumeInterruptedTurns,
		"runtime.recovery.auto_repair_sessions":                  file.Runtime.Recovery.AutoRepairSessions,
		"security.profile":                                       normalizeSecurityProfileName(file.Security.Profile),
		"security.screener.enabled":                              file.Security.Screener.Enabled,
		"security.screener.shadow":                               file.Security.Screener.Shadow,
		"security.screener.provider":                             strings.TrimSpace(file.Security.Screener.Provider),
		"security.screener.timeout_ms":                           file.Security.Screener.TimeoutMS,
		"security.screener.max_tokens":                           file.Security.Screener.MaxTokens,
		"team.lead_name":                                         file.Team.LeadName,
		"team.team_name":                                         file.Team.TeamName,
		"team.default_skills":                                    append([]string{}, file.Team.DefaultSkills...),
		"team.teammate_work_limit":                               file.Team.TeammateWorkLimit,
		"team.teammate_poll_seconds":                             file.Team.TeammatePollSeconds,
		"team.teammate_idle_timeout_seconds":                     file.Team.TeammateIdleTimeoutSecs,
		"paths.state_dir":                                        file.Paths.StateDir,
		"paths.team_dir":                                         file.Paths.TeamDir,
		"paths.tasks_dir":                                        file.Paths.TasksDir,
		"paths.todos_dir":                                        file.Paths.TodosDir,
		"paths.memory_dir":                                       file.Paths.MemoryDir,
		"paths.rules_dir":                                        file.Paths.RulesDir,
		"paths.skills_dir":                                       file.Paths.SkillsDir,
		"paths.mcp_config_path":                                  file.Paths.MCPConfigPath,
		"paths.temp_dir":                                         file.Paths.TempDir,
		"paths.transcripts_dir":                                  file.Paths.TranscriptsDir,
		"paths.sessions_dir":                                     file.Paths.SessionsDir,
		"storage.tmp_ttl_hours":                                  file.Storage.TmpTTLHours,
		"storage.artifact_ttl_hours":                             file.Storage.ArtifactTTLHours,
		"storage.browser_cache_auto_clean":                       file.Storage.BrowserCacheAutoClean,
		"storage.browser_cache_max_mb":                           file.Storage.BrowserCacheMaxMB,
		"storage.session_checkpoint_keep_latest":                 file.Storage.SessionCheckpointKeepLatest,
		"storage.session_checkpoint_ttl_hours":                   file.Storage.SessionCheckpointTTLHours,
		"storage.session_checkpoint_auto_prune":                  file.Storage.SessionCheckpointAutoPrune,
		"storage.session_backend":                                normalizeSessionStorageBackend(file.Storage.SessionBackend),
		"storage.sqlite_path":                                    file.Storage.SQLitePath,
		"memory.strategy":                                        normalizeMemoryStrategyKind(file.Memory.Strategy),
		"memory.consolidate_after":                               file.Memory.ConsolidateAfter,
		"memory.session_scope":                                   file.Memory.SessionScope,
		"tools.web_search.enabled":                               file.Tools.WebSearch.Enabled,
		"tools.web_search.provider_order":                        append([]string{}, file.Tools.WebSearch.ProviderOrder...),
		"tools.web_search.cache_ttl_seconds":                     file.Tools.WebSearch.CacheTTLSeconds,
		"tools.web_search.browser.engine":                        file.Tools.WebSearch.Browser.Engine,
		"tools.web_search.browser.engine_fallback":               append([]string{}, file.Tools.WebSearch.Browser.EngineFallback...),
		"tools.web_search.browser.wait_network_idle_ms":          file.Tools.WebSearch.Browser.WaitNetworkIdleMS,
		"tools.web_search.browser.wait_after_load_ms":            file.Tools.WebSearch.Browser.WaitAfterLoadMS,
		"tools.web_search.browser.max_scrolls":                   file.Tools.WebSearch.Browser.MaxScrolls,
		"tools.web_search.browser.result_timeout_seconds":        file.Tools.WebSearch.Browser.ResultTimeoutSeconds,
		"tools.web_search.browser.preferred_hosts":               append([]string{}, file.Tools.WebSearch.Browser.PreferredHosts...),
		"tools.web_search.brave.api_key":                         "",
		"tools.web_search.exa.api_key":                           "",
		"tools.web_search.tavily.api_key":                        "",
		"tools.web_search.serpapi.api_key":                       "",
		"tools.web_fetch.enabled":                                file.Tools.WebFetch.Enabled,
		"tools.web_fetch.max_chars":                              file.Tools.WebFetch.MaxChars,
		"tools.web_fetch.timeout_seconds":                        file.Tools.WebFetch.TimeoutSeconds,
		"tools.web_fetch.policy":                                 file.Tools.WebFetch.Policy,
		"tools.web_fetch.allowed_domains":                        append([]string{}, file.Tools.WebFetch.AllowedDomains...),
		"tools.web_fetch.blocked_domains":                        append([]string{}, file.Tools.WebFetch.BlockedDomains...),
		"tools.web_fetch.allow_private_hosts":                    file.Tools.WebFetch.AllowPrivateHosts,
		"tools.glob.default_max_results":                         file.Tools.Glob.DefaultMaxResults,
		"tools.subagent.max_batch_size":                          file.Tools.Subagent.MaxBatchSize,
		"tools.subagent.max_concurrent_jobs":                     file.Tools.Subagent.MaxConcurrentJobs,
		"tools.subagent.default_max_turns":                       file.Tools.Subagent.DefaultMaxTurns,
		"tools.subagent.max_job_timeout_ms":                      file.Tools.Subagent.MaxJobTimeoutMs,
		"tools.subagent.readonly_isolation":                      normalizeSubagentReadOnlyIsolation(file.Tools.Subagent.ReadOnlyIsolation),
		"tools.subagent.git_dirty_isolation":                     normalizeSubagentGitDirtyIsolation(file.Tools.Subagent.GitDirtyIsolation),
		"tools.subagent.non_git_write_isolation":                 normalizeSubagentNonGitWriteIsolation(file.Tools.Subagent.NonGitWriteIsolation),
		"tools.subagent.workspace_ttl_hours":                     file.Tools.Subagent.WorkspaceTTLHours,
		"tools.execution.mode":                                   file.Tools.Execution.Mode,
		"tools.execution.docker_image":                           file.Tools.Execution.DockerImage,
		"tools.execution.docker_network":                         file.Tools.Execution.DockerNetwork,
		"tools.execution.ssh_target":                             file.Tools.Execution.SSHTarget,
		"tools.execution.ssh_workspace":                          file.Tools.Execution.SSHWorkspace,
		"tools.execution.ssh_options":                            append([]string{}, file.Tools.Execution.SSHOptions...),
		"tools.execution.shell_allow_patterns":                   append([]string{}, file.Tools.Execution.ShellAllowPatterns...),
		"tools.execution.shell_deny_patterns":                    append([]string{}, file.Tools.Execution.ShellDenyPatterns...),
		"tools.execution.scope_write":                            file.Tools.Execution.ScopeWrite,
		"tools.browser.enabled":                                  file.Tools.Browser.Enabled,
		"tools.browser.headless":                                 file.Tools.Browser.Headless,
		"tools.browser.browser_path":                             file.Tools.Browser.BrowserPath,
		"tools.browser.cdp_url":                                  file.Tools.Browser.CDPURL,
		"tools.browser.action_timeout_seconds":                   file.Tools.Browser.ActionTimeoutSeconds,
		"tools.browser.idle_timeout_seconds":                     file.Tools.Browser.IdleTimeoutSeconds,
		"tools.browser.max_pages_per_session":                    file.Tools.Browser.MaxPagesPerSession,
		"tools.browser.allow_private_hosts":                      file.Tools.Browser.AllowPrivateHosts,
		"tools.browser.persistent_profile":                       file.Tools.Browser.PersistentProfile,
		"tools.browser.cdp_listen":                               file.Tools.Browser.CDPListen,
		"tools.browser.cdp_relay_node":                           file.Tools.Browser.CDPRelayNode,
		"tools.browser.cdp_relay_target":                         file.Tools.Browser.CDPRelayTarget,
		"tools.history_search.enabled":                           file.Tools.History.Enabled,
		"tools.history_search.auto.enabled":                      file.Tools.History.Auto.Enabled,
		"tools.history_search.auto.max_per_turn":                 file.Tools.History.Auto.MaxPerTurn,
		"tools.history_search.auto.default_scope":                file.Tools.History.Auto.DefaultScope,
		"tools.history_search.auto.allow_archive_on_clear":       file.Tools.History.Auto.AllowArchiveOnClear,
		"tools.history_search.auto.allow_archive_on_compact":     file.Tools.History.Auto.AllowArchiveOnCompact,
		"tools.history_search.auto.allow_all_archives_automatic": file.Tools.History.Auto.AllowAllArchivesAutomatic,
		"tools.history_search.auto.min_score":                    file.Tools.History.Auto.MinScore,
		"tools.history_search.cues.explicit":                     append([]string{}, file.Tools.History.Cues.Explicit...),
		"tools.history_search.cues.implicit":                     append([]string{}, file.Tools.History.Cues.Implicit...),
		"tools.history_search.blocks.session_sources":            append([]string{}, file.Tools.History.Blocks.SessionSources...),
		"tools.permissions.block_automation_mutations":           file.Tools.Permissions.BlockAutomationMutations,
		"tools.permissions.interactive_approval_enabled":         file.Tools.Permissions.InteractiveApprovalEnabled,
		"tools.permissions.interactive_approval_mode":            file.Tools.Permissions.InteractiveApprovalMode,
		"tools.permissions.pending_ttl_seconds":                  file.Tools.Permissions.PendingTTLSeconds,
		"tools.permissions.interactive_approval_sources":         append([]string{}, file.Tools.Permissions.InteractiveApprovalSources...),
		"tools.permissions.interactive_approval_tools":           append([]string{}, file.Tools.Permissions.InteractiveApprovalTools...),
		"tools.permissions.trusted_path_prefixes":                append([]string{}, file.Tools.Permissions.TrustedPathPrefixes...),
		"tools.permissions.trusted_command_prefixes":             append([]string{}, file.Tools.Permissions.TrustedCommandPrefixes...),
		"tools.loop_guard.mode":                                  file.Tools.LoopGuard.Mode,
		"tools.loop_guard.max_recoveries":                        file.Tools.LoopGuard.MaxRecoveries,
		"tools.loop_guard.max_repeated_tools":                    file.Tools.LoopGuard.MaxRepeatedTools,
		"tools.loop_guard.max_repeated_polling_tools":            file.Tools.LoopGuard.MaxRepeatedPollingTools,
		"tools.loop_guard.max_stalled_task_polling_tools":        file.Tools.LoopGuard.MaxStalledTaskPollingTools,
		"tools.execution.tool_timeout_seconds":                   file.Tools.Execution.ToolTimeoutSeconds,
		"tools.lightpanda.enabled":                               file.Tools.Lightpanda.Enabled,
		"tools.lightpanda.binary_path":                           file.Tools.Lightpanda.BinaryPath,
		"tools.lightpanda.auto_download":                         file.Tools.Lightpanda.AutoDownload,
		"tools.lightpanda.search_engine":                         file.Tools.Lightpanda.SearchEngine,
		"tools.lightpanda.search_template":                       file.Tools.Lightpanda.SearchTemplate,
		"tools.lightpanda.wait_network_ms":                       file.Tools.Lightpanda.WaitNetworkMS,
		"tools.lightpanda.obey_robots":                           file.Tools.Lightpanda.ObeyRobots,
		"tools.lightpanda.log_level":                             file.Tools.Lightpanda.LogLevel,
		"media.moonshot.enabled":                                 file.Media.Moonshot.Enabled,
		"media.moonshot.base_url":                                file.Media.Moonshot.BaseURL,
		"media.moonshot.api_key":                                 "",
		"media.document.max_chars":                               file.Media.Document.MaxChars,
		"media.document.pdftotext_path":                          file.Media.Document.PDFToTextPath,
		"media.ocr.mode":                                         file.Media.OCR.Mode,
		"media.ocr.tesseract_path":                               file.Media.OCR.TesseractPath,
		"media.ocr.max_chars":                                    file.Media.OCR.MaxChars,
		"media.audio.enabled":                                    file.Media.Audio.Enabled,
		"media.audio.ffmpeg_path":                                file.Media.Audio.FFmpegPath,
		"media.audio.ffprobe_path":                               file.Media.Audio.FFprobePath,
		"media.audio.whisper_cpp_path":                           file.Media.Audio.WhisperCPPPath,
		"media.audio.whisper_model_path":                         file.Media.Audio.WhisperModelPath,
		"media.audio.max_chars":                                  file.Media.Audio.MaxChars,
		"media.audio.voice_enabled":                              file.Media.Audio.VoiceEnabled,
		"media.audio.voice_engine_addr":                          file.Media.Audio.VoiceEngineAddr,
		"media.video.enabled":                                    file.Media.Video.Enabled,
		"media.video.keyframe_interval_seconds":                  file.Media.Video.KeyframeIntervalSeconds,
		"media.video.max_frames":                                 file.Media.Video.MaxFrames,
		"channels.feishu.enabled":                                file.Channels.Feishu.Enabled,
		"channels.feishu.app_id":                                 "",
		"channels.feishu.app_secret":                             "",
		"channels.feishu.domain":                                 file.Channels.Feishu.Domain,
		"channels.weixin.enabled":                                file.Channels.Weixin.Enabled,
		"channels.weixin.base_url":                               file.Channels.Weixin.BaseURL,
		"channels.weixin.cdn_base_url":                           file.Channels.Weixin.CDNBaseURL,
		"channels.weixin.account_id":                             file.Channels.Weixin.AccountID,
		"channels.weixin.allow_from":                             append([]string{}, file.Channels.Weixin.AllowFrom...),
		"channels.weixin.route_tag":                              file.Channels.Weixin.RouteTag,
		"channels.weixin.long_poll_timeout_ms":                   file.Channels.Weixin.LongPollTimeoutMs,
		"channels.weixin.proxy":                                  file.Channels.Weixin.Proxy,
	}
	addBrowserEngineFileValues(values, file.Tools.WebSearch.Browser.Engines)
	return values
}

func effectiveValues(cfg *Config) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	values := map[string]any{
		"api.default_model":                                      cfg.DefaultModelRef,
		"api.default_profile":                                    cfg.DefaultProfileID,
		"api.auto_fallback_enabled":                              cfg.AutoFallbackEnabled,
		"api.providers":                                          maskLLMProviders(cfg.LLMProviders),
		"api.model_strategy":                                     cfg.LLMStrategy,
		"acp.agents":                                             maskACPAgentSecrets(cfg.ACP.Agents),
		"acp.bridge_client_mcp_servers":                          cfg.ACP.BridgeClientMCPServers,
		"api.timeout_seconds":                                    cfg.APITimeoutSeconds,
		"agent.compress_threshold":                               cfg.CompressThreshold,
		"agent.compaction.auto_enabled":                          cfg.Compaction.AutoEnabled,
		"agent.compaction.trigger_tokens":                        cfg.Compaction.TriggerTokens,
		"agent.compaction.target_history_tokens":                 cfg.Compaction.TargetHistoryTokens,
		"agent.compaction.mode":                                  cfg.Compaction.Mode,
		"agent.compaction.model_profile_id":                      cfg.Compaction.ModelProfileID,
		"agent.compaction.max_latency_ms":                        cfg.Compaction.MaxLatencyMS,
		"agent.compaction.keep_recent_messages":                  cfg.Compaction.KeepRecentMessages,
		"agent.compaction.context_window_tokens":                 cfg.Compaction.ContextWindowTokens,
		"agent.compaction.trigger_ratio":                         cfg.Compaction.TriggerRatio,
		"agent.compaction.retain_ratio":                          cfg.Compaction.RetainRatio,
		"agent.compaction.retain_tokens":                         cfg.Compaction.RetainTokens,
		"agent.compaction.prune_threshold_chars":                 cfg.Compaction.PruneThresholdChars,
		"agent.compaction.prune_head_chars":                      cfg.Compaction.PruneHeadChars,
		"agent.compaction.prune_tail_chars":                      cfg.Compaction.PruneTailChars,
		"agent.max_turns":                                        cfg.MaxTurns,
		"agent.profile":                                          cfg.AgentProfile,
		"agent.default_profiles.acp":                             cfg.AgentDefaultProfiles.ACP,
		"agent.default_profiles.cli":                             cfg.AgentDefaultProfiles.CLI,
		"agent.default_profiles.tui":                             cfg.AgentDefaultProfiles.TUI,
		"agent.default_profiles.web":                             cfg.AgentDefaultProfiles.Web,
		"agent.default_profiles.weixin":                          cfg.AgentDefaultProfiles.Weixin,
		"agent.default_profiles.feishu":                          cfg.AgentDefaultProfiles.Feishu,
		"logging.level":                                          cfg.Logging.Level,
		"logging.file_path":                                      cfg.Logging.FilePath,
		"logging.also_stderr":                                    cfg.Logging.AlsoStderr,
		"web.token":                                              cfg.WebToken,
		"cron.enabled":                                           cfg.Cron.Enabled,
		"cron.tick_seconds":                                      cfg.Cron.TickSeconds,
		"cron.default_timezone":                                  cfg.Cron.DefaultTimezone,
		"cron.max_concurrent_runs":                               cfg.Cron.MaxConcurrentRuns,
		"heartbeat.enabled":                                      cfg.Heartbeat.Enabled,
		"heartbeat.tick_seconds":                                 cfg.Heartbeat.TickSeconds,
		"heartbeat.checklist_path":                               cfg.Heartbeat.ChecklistPath,
		"heartbeat.ok_token":                                     cfg.Heartbeat.OKToken,
		"heartbeat.default_interval_seconds":                     cfg.Heartbeat.DefaultIntervalSeconds,
		"heartbeat.default_timezone":                             cfg.Heartbeat.DefaultTimezone,
		"heartbeat.default_watchdog_script":                      cfg.Heartbeat.DefaultWatchdogScript,
		"control.node_name":                                      cfg.Control.NodeName,
		"control.node_id":                                        cfg.Control.NodeID,
		"control.default_node":                                   cfg.Control.DefaultNode,
		"control.trust_level":                                    cfg.Control.TrustLevel,
		"control.center_url":                                     cfg.Control.CenterURL,
		"control.credential":                                     cfg.Control.Credential,
		"control.heartbeat_seconds":                              cfg.Control.HeartbeatSeconds,
		"control.offline_after_seconds":                          cfg.Control.OfflineAfterSeconds,
		"control.forward_allow":                                  append([]string{}, cfg.Control.ForwardAllow...),
		"control.nodes":                                          append([]ControlNodeConfig{}, cfg.Control.Nodes...),
		"control.forwards":                                       append([]ForwardConfig{}, cfg.Control.Forwards...),
		"runtime.recovery.auto_resume_interrupted_turns":         cfg.Runtime.Recovery.AutoResumeInterruptedTurns,
		"runtime.recovery.auto_repair_sessions":                  cfg.Runtime.Recovery.AutoRepairSessions,
		"security.profile":                                       cfg.Security.Profile,
		"security.screener.enabled":                              cfg.Security.Screener.Enabled,
		"security.screener.shadow":                               cfg.Security.Screener.Shadow,
		"security.screener.provider":                             cfg.Security.Screener.Provider,
		"security.screener.timeout_ms":                           cfg.Security.Screener.TimeoutMS,
		"security.screener.max_tokens":                           cfg.Security.Screener.MaxTokens,
		"team.lead_name":                                         cfg.LeadName,
		"team.team_name":                                         cfg.TeamName,
		"team.default_skills":                                    append([]string{}, cfg.DefaultSkills...),
		"team.teammate_work_limit":                               cfg.TeammateWorkLimit,
		"team.teammate_poll_seconds":                             int(cfg.TeammatePollEvery / time.Second),
		"team.teammate_idle_timeout_seconds":                     int(cfg.TeammateIdleFor / time.Second),
		"paths.state_dir":                                        cfg.StateDir,
		"paths.team_dir":                                         cfg.TeamDir,
		"paths.tasks_dir":                                        cfg.TasksDir,
		"paths.todos_dir":                                        cfg.TodosDir,
		"paths.memory_dir":                                       cfg.MemoryDir,
		"paths.rules_dir":                                        cfg.RulesDir,
		"paths.skills_dir":                                       cfg.SkillsDir,
		"paths.mcp_config_path":                                  cfg.MCPConfigPath,
		"paths.temp_dir":                                         cfg.TempDir,
		"paths.transcripts_dir":                                  cfg.TranscriptsDir,
		"paths.sessions_dir":                                     cfg.SessionsDir,
		"storage.tmp_ttl_hours":                                  cfg.Storage.TmpTTLHours,
		"storage.artifact_ttl_hours":                             cfg.Storage.ArtifactTTLHours,
		"storage.browser_cache_auto_clean":                       cfg.Storage.BrowserCacheAutoClean,
		"storage.browser_cache_max_mb":                           cfg.Storage.BrowserCacheMaxMB,
		"storage.session_checkpoint_keep_latest":                 cfg.Storage.SessionCheckpointKeepLatest,
		"storage.session_checkpoint_ttl_hours":                   cfg.Storage.SessionCheckpointTTLHours,
		"storage.session_checkpoint_auto_prune":                  cfg.Storage.SessionCheckpointAutoPrune,
		"storage.session_backend":                                normalizeSessionStorageBackend(cfg.Storage.SessionBackend),
		"storage.sqlite_path":                                    cfg.Storage.SQLitePath,
		"memory.strategy":                                        normalizeMemoryStrategyKind(cfg.Memory.Strategy),
		"memory.consolidate_after":                               cfg.Memory.ConsolidateAfter,
		"memory.session_scope":                                   cfg.Memory.SessionScope,
		"tools.web_search.enabled":                               cfg.Tools.WebSearch.Enabled,
		"tools.web_search.provider_order":                        append([]string{}, cfg.Tools.WebSearch.ProviderOrder...),
		"tools.web_search.cache_ttl_seconds":                     cfg.Tools.WebSearch.CacheTTLSeconds,
		"tools.web_search.browser.engine":                        cfg.Tools.WebSearch.Browser.Engine,
		"tools.web_search.browser.engine_fallback":               append([]string{}, cfg.Tools.WebSearch.Browser.EngineFallback...),
		"tools.web_search.browser.wait_network_idle_ms":          cfg.Tools.WebSearch.Browser.WaitNetworkIdleMS,
		"tools.web_search.browser.wait_after_load_ms":            cfg.Tools.WebSearch.Browser.WaitAfterLoadMS,
		"tools.web_search.browser.max_scrolls":                   cfg.Tools.WebSearch.Browser.MaxScrolls,
		"tools.web_search.browser.result_timeout_seconds":        cfg.Tools.WebSearch.Browser.ResultTimeoutSeconds,
		"tools.web_search.browser.preferred_hosts":               append([]string{}, cfg.Tools.WebSearch.Browser.PreferredHosts...),
		"tools.web_search.brave.api_key":                         cfg.Tools.WebSearch.BraveAPIKey,
		"tools.web_search.exa.api_key":                           cfg.Tools.WebSearch.ExaAPIKey,
		"tools.web_search.tavily.api_key":                        cfg.Tools.WebSearch.TavilyAPIKey,
		"tools.web_search.serpapi.api_key":                       cfg.Tools.WebSearch.SerpAPIKey,
		"tools.web_fetch.enabled":                                cfg.Tools.WebFetch.Enabled,
		"tools.web_fetch.max_chars":                              cfg.Tools.WebFetch.MaxChars,
		"tools.web_fetch.timeout_seconds":                        cfg.Tools.WebFetch.TimeoutSeconds,
		"tools.web_fetch.policy":                                 cfg.Tools.WebFetch.Policy,
		"tools.web_fetch.allowed_domains":                        append([]string{}, cfg.Tools.WebFetch.AllowedDomains...),
		"tools.web_fetch.blocked_domains":                        append([]string{}, cfg.Tools.WebFetch.BlockedDomains...),
		"tools.web_fetch.allow_private_hosts":                    cfg.Tools.WebFetch.AllowPrivateHosts,
		"tools.glob.default_max_results":                         cfg.Tools.Glob.DefaultMaxResults,
		"tools.subagent.max_batch_size":                          cfg.Tools.Subagent.MaxBatchSize,
		"tools.subagent.max_concurrent_jobs":                     cfg.Tools.Subagent.MaxConcurrentJobs,
		"tools.subagent.default_max_turns":                       cfg.Tools.Subagent.DefaultMaxTurns,
		"tools.subagent.max_job_timeout_ms":                      cfg.Tools.Subagent.MaxJobTimeoutMs,
		"tools.subagent.readonly_isolation":                      cfg.Tools.Subagent.ReadOnlyIsolation,
		"tools.subagent.git_dirty_isolation":                     cfg.Tools.Subagent.GitDirtyIsolation,
		"tools.subagent.non_git_write_isolation":                 cfg.Tools.Subagent.NonGitWriteIsolation,
		"tools.subagent.workspace_ttl_hours":                     cfg.Tools.Subagent.WorkspaceTTLHours,
		"tools.execution.mode":                                   cfg.Tools.Execution.Mode,
		"tools.execution.docker_image":                           cfg.Tools.Execution.DockerImage,
		"tools.execution.docker_network":                         cfg.Tools.Execution.DockerNetwork,
		"tools.execution.ssh_target":                             cfg.Tools.Execution.SSHTarget,
		"tools.execution.ssh_workspace":                          cfg.Tools.Execution.SSHWorkspace,
		"tools.execution.ssh_options":                            append([]string{}, cfg.Tools.Execution.SSHOptions...),
		"tools.execution.shell_allow_patterns":                   append([]string{}, cfg.Tools.Execution.ShellAllowPatterns...),
		"tools.execution.shell_deny_patterns":                    append([]string{}, cfg.Tools.Execution.ShellDenyPatterns...),
		"tools.execution.scope_write":                            cfg.Tools.Execution.ScopeWrite,
		"tools.browser.enabled":                                  cfg.Tools.Browser.Enabled,
		"tools.browser.headless":                                 cfg.Tools.Browser.Headless,
		"tools.browser.browser_path":                             cfg.Tools.Browser.BrowserPath,
		"tools.browser.cdp_url":                                  cfg.Tools.Browser.CDPURL,
		"tools.browser.action_timeout_seconds":                   cfg.Tools.Browser.ActionTimeoutSeconds,
		"tools.browser.idle_timeout_seconds":                     cfg.Tools.Browser.IdleTimeoutSeconds,
		"tools.browser.max_pages_per_session":                    cfg.Tools.Browser.MaxPagesPerSession,
		"tools.browser.allow_private_hosts":                      cfg.Tools.Browser.AllowPrivateHosts,
		"tools.browser.persistent_profile":                       cfg.Tools.Browser.PersistentProfile,
		"tools.browser.cdp_listen":                               cfg.Tools.Browser.CDPListen,
		"tools.browser.cdp_relay_node":                           cfg.Tools.Browser.CDPRelayNode,
		"tools.browser.cdp_relay_target":                         cfg.Tools.Browser.CDPRelayTarget,
		"tools.history_search.enabled":                           cfg.Tools.History.Enabled,
		"tools.history_search.auto.enabled":                      cfg.Tools.History.Auto.Enabled,
		"tools.history_search.auto.max_per_turn":                 cfg.Tools.History.Auto.MaxPerTurn,
		"tools.history_search.auto.default_scope":                cfg.Tools.History.Auto.DefaultScope,
		"tools.history_search.auto.allow_archive_on_clear":       cfg.Tools.History.Auto.AllowArchiveOnClear,
		"tools.history_search.auto.allow_archive_on_compact":     cfg.Tools.History.Auto.AllowArchiveOnCompact,
		"tools.history_search.auto.allow_all_archives_automatic": cfg.Tools.History.Auto.AllowAllArchivesAutomatic,
		"tools.history_search.auto.min_score":                    cfg.Tools.History.Auto.MinScore,
		"tools.history_search.cues.explicit":                     append([]string{}, cfg.Tools.History.Cues.Explicit...),
		"tools.history_search.cues.implicit":                     append([]string{}, cfg.Tools.History.Cues.Implicit...),
		"tools.history_search.blocks.session_sources":            append([]string{}, cfg.Tools.History.Blocks.SessionSources...),
		"tools.permissions.block_automation_mutations":           cfg.Tools.Permissions.BlockAutomationMutations,
		"tools.permissions.interactive_approval_enabled":         cfg.Tools.Permissions.InteractiveApprovalEnabled,
		"tools.permissions.interactive_approval_mode":            cfg.Tools.Permissions.InteractiveApprovalMode,
		"tools.permissions.pending_ttl_seconds":                  cfg.Tools.Permissions.PendingTTLSeconds,
		"tools.permissions.interactive_approval_sources":         append([]string{}, cfg.Tools.Permissions.InteractiveApprovalSources...),
		"tools.permissions.interactive_approval_tools":           append([]string{}, cfg.Tools.Permissions.InteractiveApprovalTools...),
		"tools.permissions.trusted_path_prefixes":                append([]string{}, cfg.Tools.Permissions.TrustedPathPrefixes...),
		"tools.permissions.trusted_command_prefixes":             append([]string{}, cfg.Tools.Permissions.TrustedCommandPrefixes...),
		"tools.loop_guard.mode":                                  cfg.Tools.LoopGuard.Mode,
		"tools.loop_guard.max_recoveries":                        cfg.Tools.LoopGuard.MaxRecoveries,
		"tools.loop_guard.max_repeated_tools":                    cfg.Tools.LoopGuard.MaxRepeatedTools,
		"tools.loop_guard.max_repeated_polling_tools":            cfg.Tools.LoopGuard.MaxRepeatedPollingTools,
		"tools.loop_guard.max_stalled_task_polling_tools":        cfg.Tools.LoopGuard.MaxStalledTaskPollingTools,
		"tools.execution.tool_timeout_seconds":                   cfg.Tools.Execution.ToolTimeoutSeconds,
		"tools.lightpanda.enabled":                               cfg.Tools.Lightpanda.Enabled,
		"tools.lightpanda.binary_path":                           cfg.Tools.Lightpanda.BinaryPath,
		"tools.lightpanda.auto_download":                         cfg.Tools.Lightpanda.AutoDownload,
		"tools.lightpanda.search_engine":                         cfg.Tools.Lightpanda.SearchEngine,
		"tools.lightpanda.search_template":                       cfg.Tools.Lightpanda.SearchTemplate,
		"tools.lightpanda.wait_network_ms":                       cfg.Tools.Lightpanda.WaitNetworkMS,
		"tools.lightpanda.obey_robots":                           cfg.Tools.Lightpanda.ObeyRobots,
		"tools.lightpanda.log_level":                             cfg.Tools.Lightpanda.LogLevel,
		"media.moonshot.enabled":                                 cfg.Media.Moonshot.Enabled,
		"media.moonshot.base_url":                                cfg.Media.Moonshot.BaseURL,
		"media.moonshot.api_key":                                 cfg.Media.Moonshot.APIKey,
		"media.document.max_chars":                               cfg.Media.Document.MaxChars,
		"media.document.pdftotext_path":                          cfg.Media.Document.PDFToTextPath,
		"media.ocr.mode":                                         cfg.Media.OCR.Mode,
		"media.ocr.tesseract_path":                               cfg.Media.OCR.TesseractPath,
		"media.ocr.max_chars":                                    cfg.Media.OCR.MaxChars,
		"media.audio.enabled":                                    cfg.Media.Audio.Enabled,
		"media.audio.ffmpeg_path":                                cfg.Media.Audio.FFmpegPath,
		"media.audio.ffprobe_path":                               cfg.Media.Audio.FFprobePath,
		"media.audio.whisper_cpp_path":                           cfg.Media.Audio.WhisperCPPPath,
		"media.audio.whisper_model_path":                         cfg.Media.Audio.WhisperModelPath,
		"media.audio.max_chars":                                  cfg.Media.Audio.MaxChars,
		"media.audio.voice_enabled":                              cfg.Media.Audio.VoiceEnabled,
		"media.audio.voice_engine_addr":                          cfg.Media.Audio.VoiceEngineAddr,
		"media.video.enabled":                                    cfg.Media.Video.Enabled,
		"media.video.keyframe_interval_seconds":                  cfg.Media.Video.KeyframeIntervalSeconds,
		"media.video.max_frames":                                 cfg.Media.Video.MaxFrames,
		"channels.feishu.enabled":                                cfg.Feishu.Enabled,
		"channels.feishu.app_id":                                 cfg.Feishu.AppID,
		"channels.feishu.app_secret":                             cfg.Feishu.AppSecret,
		"channels.feishu.domain":                                 cfg.Feishu.Domain,
		"channels.weixin.enabled":                                cfg.Weixin.Enabled,
		"channels.weixin.base_url":                               cfg.Weixin.BaseURL,
		"channels.weixin.cdn_base_url":                           cfg.Weixin.CDNBaseURL,
		"channels.weixin.account_id":                             cfg.Weixin.AccountID,
		"channels.weixin.allow_from":                             append([]string{}, cfg.Weixin.AllowFrom...),
		"channels.weixin.route_tag":                              cfg.Weixin.RouteTag,
		"channels.weixin.long_poll_timeout_ms":                   cfg.Weixin.LongPollTimeoutMs,
		"channels.weixin.proxy":                                  cfg.Weixin.Proxy,
	}
	addBrowserEngineEffectiveValues(values, cfg.Tools.WebSearch.Browser.Engines)
	return values
}

func addBrowserEngineFileValues(values map[string]any, engines map[string]WebSearchBrowserEngineSection) {
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		cfg := engines[engine]
		prefix := "tools.web_search.browser.engines." + engine + "."
		values[prefix+"search_url_template"] = cfg.SearchURLTemplate
		values[prefix+"blocked_hosts"] = append([]string{}, cfg.BlockedHosts...)
		values[prefix+"result_container_selector"] = cfg.ResultContainerSelector
		values[prefix+"result_link_selector"] = cfg.ResultLinkSelector
		values[prefix+"result_snippet_selector"] = cfg.ResultSnippetSelector
	}
}

func addBrowserEngineEffectiveValues(values map[string]any, engines map[string]WebSearchBrowserEngineConfig) {
	for _, engine := range []string{"duckduckgo", "bing", "brave", "custom"} {
		cfg := engines[engine]
		prefix := "tools.web_search.browser.engines." + engine + "."
		values[prefix+"search_url_template"] = cfg.SearchURLTemplate
		values[prefix+"blocked_hosts"] = append([]string{}, cfg.BlockedHosts...)
		values[prefix+"result_container_selector"] = cfg.ResultContainerSelector
		values[prefix+"result_link_selector"] = cfg.ResultLinkSelector
		values[prefix+"result_snippet_selector"] = cfg.ResultSnippetSelector
	}
}

func fieldConfigured(path string, stored ConfigFile, current *Config, origin fieldOrigin) bool {
	switch path {
	case "api.providers":
		return len(stored.API.Providers) > 0 || len(current.LLMProviders) > 0
	case "web.token":
		return strings.TrimSpace(current.WebToken) != "" || strings.TrimSpace(stored.Web.Token) != ""
	case "tools.web_search.brave.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.BraveAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.Brave.APIKey) != ""
	case "tools.web_search.exa.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.ExaAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.Exa.APIKey) != ""
	case "tools.web_search.tavily.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.TavilyAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.Tavily.APIKey) != ""
	case "tools.web_search.serpapi.api_key":
		return strings.TrimSpace(current.Tools.WebSearch.SerpAPIKey) != "" || strings.TrimSpace(stored.Tools.WebSearch.SerpAPI.APIKey) != ""
	case "media.moonshot.api_key":
		return strings.TrimSpace(current.Media.Moonshot.APIKey) != "" || strings.TrimSpace(stored.Media.Moonshot.APIKey) != ""
	case "channels.feishu.app_id":
		return strings.TrimSpace(current.Feishu.AppID) != "" || strings.TrimSpace(stored.Channels.Feishu.AppID) != ""
	case "channels.feishu.app_secret":
		return strings.TrimSpace(current.Feishu.AppSecret) != "" || strings.TrimSpace(stored.Channels.Feishu.AppSecret) != ""
	default:
		return origin.Effective != nil
	}
}
