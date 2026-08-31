package config

import (
	"fmt"
	"strings"
)

type storedValueSetter func(*ConfigFile, string, any) error

var storedValueSetters = map[string]storedValueSetter{
	"api":       setAPIStoredValue,
	"acp":       setACPStoredValue,
	"agent":     setAgentStoredValue,
	"logging":   setLoggingStoredValue,
	"web":       setWebStoredValue,
	"cron":      setCronStoredValue,
	"heartbeat": setHeartbeatStoredValue,
	"control":   setControlStoredValue,
	"runtime":   setRuntimeStoredValue,
	"security":  setSecurityStoredValue,
	"team":      setTeamStoredValue,
	"paths":     setPathsStoredValue,
	"storage":   setStorageStoredValue,
	"memory":    setMemoryStoredValue,
	"tools":     setToolsStoredValue,
	"media":     setMediaStoredValue,
	"channels":  setChannelsStoredValue,
}

func setStoredValue(file *ConfigFile, path, kind string, value any) error {
	if strings.HasPrefix(path, "tools.web_search.browser.engines.") {
		setBrowserEngineFileValue(&file.Tools.WebSearch.Browser, path, value)
		return nil
	}
	domain := path
	if index := strings.IndexByte(path, '.'); index >= 0 {
		domain = path[:index]
	}
	setter, ok := storedValueSetters[domain]
	if !ok {
		return fmt.Errorf("unknown config field: %s", path)
	}
	return setter(file, path, value)
}

func setAPIStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "api.default_model":
		file.API.DefaultModel = asString(value)
	case "api.default_profile":
		file.API.DefaultProfile = asString(value)
	case "api.auto_fallback_enabled":
		file.API.AutoFallbackEnabled = asBool(value)
	case "api.providers":
		providers, err := asLLMProviders(value)
		if err != nil {
			return err
		}
		for id, provider := range providers {
			if strings.TrimSpace(provider.APIKey) == "********" {
				provider.APIKey = file.API.Providers[id].APIKey
				providers[id] = provider
			}
		}
		file.API.Providers = providers
	case "api.model_strategy":
		strategy, err := asLLMStrategy(value)
		if err != nil {
			return err
		}
		file.API.ModelStrategy = strategy
	case "api.timeout_seconds":
		file.API.TimeoutSeconds = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setACPStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "acp.agents":
		agents, err := asACPAgents(value)
		if err != nil {
			return err
		}
		for id, agent := range agents {
			if len(agent.Env) == 0 {
				continue
			}
			for key, envValue := range agent.Env {
				if strings.TrimSpace(envValue) == "********" {
					if existing, ok := file.ACP.Agents[id]; ok && existing.Env != nil {
						agent.Env[key] = existing.Env[key]
					}
				}
			}
			agents[id] = agent
		}
		file.ACP.Agents = agents
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setAgentStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "agent.compress_threshold":
		file.Agent.CompressThreshold = asInt(value)
	case "agent.compaction.auto_enabled":
		file.Agent.Compaction.AutoEnabled = asBool(value)
	case "agent.compaction.trigger_tokens":
		file.Agent.Compaction.TriggerTokens = asInt(value)
	case "agent.compaction.target_history_tokens":
		file.Agent.Compaction.TargetHistoryTokens = asInt(value)
	case "agent.compaction.mode":
		file.Agent.Compaction.Mode = NormalizeCompactionMode(asString(value))
	case "agent.compaction.model_profile_id":
		file.Agent.Compaction.ModelProfileID = strings.TrimSpace(asString(value))
	case "agent.compaction.max_latency_ms":
		file.Agent.Compaction.MaxLatencyMS = asInt(value)
	case "agent.compaction.keep_recent_messages":
		file.Agent.Compaction.KeepRecentMessages = asInt(value)
	case "agent.compaction.context_window_tokens":
		file.Agent.Compaction.ContextWindowTokens = asInt(value)
	case "agent.compaction.trigger_ratio":
		file.Agent.Compaction.TriggerRatio = asFloat(value)
	case "agent.compaction.retain_ratio":
		file.Agent.Compaction.RetainRatio = asFloat(value)
	case "agent.compaction.retain_tokens":
		file.Agent.Compaction.RetainTokens = asInt(value)
	case "agent.compaction.prune_threshold_chars":
		file.Agent.Compaction.PruneThresholdChars = asInt(value)
	case "agent.compaction.prune_head_chars":
		file.Agent.Compaction.PruneHeadChars = asInt(value)
	case "agent.compaction.prune_tail_chars":
		file.Agent.Compaction.PruneTailChars = asInt(value)
	case "agent.max_turns":
		file.Agent.MaxTurns = asInt(value)
	case "agent.profile":
		file.Agent.Profile = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.acp":
		file.Agent.DefaultProfiles.ACP = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.cli":
		file.Agent.DefaultProfiles.CLI = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.tui":
		file.Agent.DefaultProfiles.TUI = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.web":
		file.Agent.DefaultProfiles.Web = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.weixin":
		file.Agent.DefaultProfiles.Weixin = NormalizeAgentProfile(asString(value))
	case "agent.default_profiles.feishu":
		file.Agent.DefaultProfiles.Feishu = NormalizeAgentProfile(asString(value))
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setLoggingStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "logging.level":
		file.Logging.Level = asString(value)
	case "logging.file_path":
		file.Logging.FilePath = asString(value)
	case "logging.also_stderr":
		file.Logging.AlsoStderr = asBool(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setWebStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "web.token":
		file.Web.Token = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setCronStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "cron.enabled":
		file.Cron.Enabled = asBool(value)
	case "cron.tick_seconds":
		file.Cron.TickSeconds = asInt(value)
	case "cron.default_timezone":
		file.Cron.DefaultTimezone = asString(value)
	case "cron.max_concurrent_runs":
		file.Cron.MaxConcurrentRuns = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setHeartbeatStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "heartbeat.enabled":
		file.Heartbeat.Enabled = asBool(value)
	case "heartbeat.tick_seconds":
		file.Heartbeat.TickSeconds = asInt(value)
	case "heartbeat.checklist_path":
		file.Heartbeat.ChecklistPath = asString(value)
	case "heartbeat.ok_token":
		file.Heartbeat.OKToken = asString(value)
	case "heartbeat.default_interval_seconds":
		file.Heartbeat.DefaultIntervalSeconds = asInt(value)
	case "heartbeat.default_timezone":
		file.Heartbeat.DefaultTimezone = asString(value)
	case "heartbeat.default_watchdog_script":
		file.Heartbeat.DefaultWatchdogScript = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setControlStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "control.node_name":
		file.Control.NodeName = asString(value)
	case "control.node_id":
		file.Control.NodeID = asString(value)
	case "control.default_node":
		file.Control.DefaultNode = asString(value)
	case "control.trust_level":
		file.Control.TrustLevel = asString(value)
	case "control.center_url":
		file.Control.CenterURL = asString(value)
	case "control.credential":
		file.Control.Credential = asString(value)
	case "control.heartbeat_seconds":
		file.Control.HeartbeatSeconds = asInt(value)
	case "control.offline_after_seconds":
		file.Control.OfflineAfterSeconds = asInt(value)
	case "control.nodes":
		file.Control.Nodes = asControlNodeSections(value)
	case "control.forwards":
		file.Control.Forwards = asForwardSections(value)
	case "control.forward_allow":
		file.Control.ForwardAllow = asStringList(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setRuntimeStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "runtime.recovery.auto_resume_interrupted_turns":
		file.Runtime.Recovery.AutoResumeInterruptedTurns = asBool(value)
	case "runtime.recovery.auto_repair_sessions":
		file.Runtime.Recovery.AutoRepairSessions = asBool(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setSecurityStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "security.profile":
		file.Security.Profile = normalizeSecurityProfileName(asString(value))
	case "security.screener.enabled":
		file.Security.Screener.Enabled = asBool(value)
	case "security.screener.shadow":
		file.Security.Screener.Shadow = asBool(value)
	case "security.screener.provider":
		file.Security.Screener.Provider = strings.TrimSpace(asString(value))
	case "security.screener.timeout_ms":
		file.Security.Screener.TimeoutMS = asInt(value)
	case "security.screener.max_tokens":
		file.Security.Screener.MaxTokens = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setTeamStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "team.lead_name":
		file.Team.LeadName = asString(value)
	case "team.team_name":
		file.Team.TeamName = asString(value)
	case "team.default_skills":
		file.Team.DefaultSkills = asStringList(value)
	case "team.teammate_work_limit":
		file.Team.TeammateWorkLimit = asInt(value)
	case "team.teammate_poll_seconds":
		file.Team.TeammatePollSeconds = asInt(value)
	case "team.teammate_idle_timeout_seconds":
		file.Team.TeammateIdleTimeoutSecs = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setPathsStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "paths.state_dir":
		file.Paths.StateDir = asString(value)
	case "paths.team_dir":
		file.Paths.TeamDir = asString(value)
	case "paths.tasks_dir":
		file.Paths.TasksDir = asString(value)
	case "paths.todos_dir":
		file.Paths.TodosDir = asString(value)
	case "paths.memory_dir":
		file.Paths.MemoryDir = asString(value)
	case "paths.rules_dir":
		file.Paths.RulesDir = asString(value)
	case "paths.skills_dir":
		file.Paths.SkillsDir = asString(value)
	case "paths.mcp_config_path":
		file.Paths.MCPConfigPath = asString(value)
	case "paths.temp_dir":
		file.Paths.TempDir = asString(value)
	case "paths.transcripts_dir":
		file.Paths.TranscriptsDir = asString(value)
	case "paths.sessions_dir":
		file.Paths.SessionsDir = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setStorageStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "storage.tmp_ttl_hours":
		file.Storage.TmpTTLHours = asInt(value)
	case "storage.artifact_ttl_hours":
		file.Storage.ArtifactTTLHours = asInt(value)
	case "storage.browser_cache_auto_clean":
		file.Storage.BrowserCacheAutoClean = asBool(value)
	case "storage.browser_cache_max_mb":
		file.Storage.BrowserCacheMaxMB = asInt(value)
	case "storage.session_checkpoint_keep_latest":
		file.Storage.SessionCheckpointKeepLatest = asInt(value)
	case "storage.session_checkpoint_ttl_hours":
		file.Storage.SessionCheckpointTTLHours = asInt(value)
	case "storage.session_checkpoint_auto_prune":
		file.Storage.SessionCheckpointAutoPrune = asBool(value)
	case "storage.session_backend":
		file.Storage.SessionBackend = normalizeSessionStorageBackend(asString(value))
	case "storage.sqlite_path":
		file.Storage.SQLitePath = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setMemoryStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "memory.strategy":
		file.Memory.Strategy = normalizeMemoryStrategyKind(asString(value))
	case "memory.consolidate_after":
		file.Memory.ConsolidateAfter = asInt(value)
	case "memory.session_scope":
		file.Memory.SessionScope = asBool(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setMediaStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "media.moonshot.enabled":
		file.Media.Moonshot.Enabled = asBool(value)
	case "media.moonshot.base_url":
		file.Media.Moonshot.BaseURL = asString(value)
	case "media.moonshot.api_key":
		file.Media.Moonshot.APIKey = asString(value)
	case "media.document.max_chars":
		file.Media.Document.MaxChars = asInt(value)
	case "media.document.pdftotext_path":
		file.Media.Document.PDFToTextPath = asString(value)
	case "media.ocr.mode":
		file.Media.OCR.Mode = asString(value)
	case "media.ocr.tesseract_path":
		file.Media.OCR.TesseractPath = asString(value)
	case "media.ocr.max_chars":
		file.Media.OCR.MaxChars = asInt(value)
	case "media.audio.enabled":
		file.Media.Audio.Enabled = asBool(value)
	case "media.audio.ffmpeg_path":
		file.Media.Audio.FFmpegPath = asString(value)
	case "media.audio.ffprobe_path":
		file.Media.Audio.FFprobePath = asString(value)
	case "media.audio.whisper_cpp_path":
		file.Media.Audio.WhisperCPPPath = asString(value)
	case "media.audio.whisper_model_path":
		file.Media.Audio.WhisperModelPath = asString(value)
	case "media.audio.max_chars":
		file.Media.Audio.MaxChars = asInt(value)
	case "media.audio.voice_enabled":
		file.Media.Audio.VoiceEnabled = asBool(value)
	case "media.audio.voice_engine_addr":
		file.Media.Audio.VoiceEngineAddr = asString(value)
	case "media.video.enabled":
		file.Media.Video.Enabled = asBool(value)
	case "media.video.keyframe_interval_seconds":
		file.Media.Video.KeyframeIntervalSeconds = asInt(value)
	case "media.video.max_frames":
		file.Media.Video.MaxFrames = asInt(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}

func setChannelsStoredValue(file *ConfigFile, path string, value any) error {
	switch path {
	case "channels.feishu.enabled":
		file.Channels.Feishu.Enabled = asBool(value)
	case "channels.feishu.app_id":
		file.Channels.Feishu.AppID = asString(value)
	case "channels.feishu.app_secret":
		file.Channels.Feishu.AppSecret = asString(value)
	case "channels.feishu.domain":
		file.Channels.Feishu.Domain = asString(value)
	case "channels.weixin.enabled":
		file.Channels.Weixin.Enabled = asBool(value)
	case "channels.weixin.base_url":
		file.Channels.Weixin.BaseURL = asString(value)
	case "channels.weixin.cdn_base_url":
		file.Channels.Weixin.CDNBaseURL = asString(value)
	case "channels.weixin.account_id":
		file.Channels.Weixin.AccountID = asString(value)
	case "channels.weixin.allow_from":
		file.Channels.Weixin.AllowFrom = asStringList(value)
	case "channels.weixin.route_tag":
		file.Channels.Weixin.RouteTag = asString(value)
	case "channels.weixin.long_poll_timeout_ms":
		file.Channels.Weixin.LongPollTimeoutMs = asInt(value)
	case "channels.weixin.proxy":
		file.Channels.Weixin.Proxy = asString(value)
	default:
		return fmt.Errorf("unknown config field: %s", path)
	}
	return nil
}
