package config

import "github.com/tim5wang/godex/internal/core/llm"

var defaultTrustedCommandPrefixes = []string{
	"cat ",
	"curl ",
	"date",
	"diff ",
	"echo ",
	"find ",
	"git diff",
	"git log",
	"git show",
	"git status",
	"grep ",
	"head ",
	"jq ",
	"ls",
	"pwd",
	"rg ",
	"sed -n",
	"tail ",
	"uname",
	"wc ",
	"wget ",
	"whoami",
}

func defaultConfigFile() ConfigFile {
	return ConfigFile{
		API: APISection{
			DefaultProfile:      "",
			DefaultModel:        "",
			AutoFallbackEnabled: true,
			Providers:           map[string]llm.ProviderConfig{},
			ModelStrategy: llm.StrategyConfig{
				Type: llm.StrategyFallback,
			},
			TimeoutSeconds: 600,
		},
		ACP: ACPSection{
			Agents: map[string]ACPAgentSection{},
		},
		Agent: AgentSection{
			CompressThreshold: 100000,
			Compaction: AgentCompactionSection{
				AutoEnabled:         true,
				TriggerTokens:       60000,
				TargetHistoryTokens: 12000,
				// Hybrid by default: LLM-backed compaction with rule-based
				// fallback gives far better continuity than the old fast-only
				// rule extraction (which lost too much information).
				Mode:               "hybrid",
				ModelProfileID:     "",
				MaxLatencyMS:       3000,
				KeepRecentMessages: 20,
				// DSH-style window-scaled policy: trigger ≈ 0.8×128k, verbatim
				// retention tail ≈ 0.16×128k. Explicit trigger_tokens /
				// retain_tokens override the scaled values.
				ContextWindowTokens: 128000,
				TriggerRatio:        0.8,
				RetainRatio:         0.16,
				RetainTokens:        0,
				// DSH tool-result-pruner defaults: prune tool results above 8192
				// characters to head (4096) + marker + tail (1024) in the LLM
				// summary input.
				PruneThresholdChars: 8192,
				PruneHeadChars:      4096,
				PruneTailChars:      1024,
			},
			MaxTurns: 1000,
			Profile:  AgentProfileGeneral,
			DefaultProfiles: AgentDefaultProfilesSection{
				ACP:    AgentProfileCoding,
				CLI:    AgentProfileCoding,
				TUI:    AgentProfileCoding,
				Web:    AgentProfileGeneral,
				Weixin: AgentProfileGeneral,
				Feishu: AgentProfileGeneral,
			},
		},
		Logging: LoggingSection{
			Level:      "info",
			FilePath:   "log/godex.log",
			AlsoStderr: true,
		},
		Web: WebSection{
			Token: "",
		},
		Cron: CronSection{
			Enabled:           true,
			TickSeconds:       1,
			DefaultTimezone:   "Local",
			MaxConcurrentRuns: 2,
		},
		Heartbeat: HeartbeatSection{
			Enabled:                false,
			TickSeconds:            30,
			ChecklistPath:          "HEARTBEAT.md",
			OKToken:                "HEARTBEAT_OK",
			DefaultIntervalSeconds: 1800,
			DefaultTimezone:        "Local",
			DefaultWatchdogScript:  "",
		},
		Control: ControlSection{
			NodeName:            "",
			NodeID:              "",
			DefaultNode:         "",
			TrustLevel:          "",
			CenterURL:           "",
			HeartbeatSeconds:    15,
			OfflineAfterSeconds: 60,
			Nodes:               []ControlNodeSection{},
			Forwards:            []ForwardSection{},
		},
		Runtime: RuntimeSection{
			Recovery: RuntimeRecoverySection{
				AutoResumeInterruptedTurns: false,
				AutoRepairSessions:         true,
			},
		},
		Storage: StorageSection{
			TmpTTLHours:                 72,
			ArtifactTTLHours:            168,
			BrowserCacheAutoClean:       true,
			BrowserCacheMaxMB:           256,
			SessionCheckpointKeepLatest: 20,
			SessionCheckpointTTLHours:   168,
			SessionCheckpointAutoPrune:  true,
			SessionBackend:              "json",
			SQLitePath:                  "",
		},
		Security: SecuritySection{
			Profile: "guarded-local",
			Screener: ScreenerSection{
				Enabled:  false,
				Shadow:   true,
				Provider: "llm",
			},
		},
		Memory: MemorySection{
			Strategy:         "per-turn",
			ConsolidateAfter: 10,
		},
		Team: TeamSection{
			LeadName:                "lead",
			TeamName:                "default",
			DefaultSkills:           []string{},
			TeammateWorkLimit:       50,
			TeammatePollSeconds:     5,
			TeammateIdleTimeoutSecs: 60,
		},
		Paths: PathsSection{
			StateDir:       "state",
			TeamDir:        "team",
			TasksDir:       "tasks",
			TodosDir:       "todos",
			MemoryDir:      "memory",
			RulesDir:       "rules",
			SkillsDir:      "skills",
			MCPConfigPath:  "mcp.json",
			TempDir:        "tmp",
			TranscriptsDir: "transcripts",
			SessionsDir:    "sessions",
		},
		Tools: ToolsSection{
			WebSearch: WebSearchSection{
				Enabled:         true,
				ProviderOrder:   []string{"brave", "exa", "tavily", "duckduckgo"},
				CacheTTLSeconds: 300,
				Browser: WebSearchBrowserSection{
					Engine:         "duckduckgo",
					EngineFallback: []string{"bing", "brave"},
					Engines: map[string]WebSearchBrowserEngineSection{
						"duckduckgo": {
							SearchURLTemplate: "https://duckduckgo.com/?q={{query}}&ia=web",
							BlockedHosts:      []string{"duckduckgo.com", "*.duckduckgo.com"},
						},
						"bing": {
							SearchURLTemplate: "https://www.bing.com/search?q={{query}}",
							BlockedHosts:      []string{"bing.com", "*.bing.com"},
						},
						"brave": {
							SearchURLTemplate: "https://search.brave.com/search?q={{query}}&source=web",
							BlockedHosts:      []string{"search.brave.com", "*.search.brave.com"},
						},
						"custom": {},
					},
					WaitNetworkIdleMS:    1500,
					WaitAfterLoadMS:      800,
					MaxScrolls:           0,
					ResultTimeoutSeconds: 20,
					PreferredHosts:       []string{},
				},
			},
			WebFetch: WebFetchSection{
				Enabled:           true,
				MaxChars:          60000,
				TimeoutSeconds:    30,
				Policy:            "allow_all",
				AllowedDomains:    []string{},
				BlockedDomains:    []string{},
				AllowPrivateHosts: false,
			},
			Glob: GlobSection{
				DefaultMaxResults: 200,
			},
			Subagent: SubagentSection{
				MaxBatchSize:         8,
				MaxConcurrentJobs:    4,
				DefaultMaxTurns:      45,
				MaxJobTimeoutMs:      7200000,
				ReadOnlyIsolation:    "shared_readonly",
				GitDirtyIsolation:    "dirty_overlay",
				NonGitWriteIsolation: "copy_snapshot",
				WorkspaceTTLHours:    168,
			},
			Execution: ExecutionSection{
				Mode:               "local",
				DockerImage:        "golang:1.26",
				DockerNetwork:      "",
				SSHTarget:          "",
				SSHWorkspace:       "",
				SSHOptions:         []string{},
				ToolTimeoutSeconds: 1800,
				ScopeWrite:         true,
			},
			Browser: BrowserSection{
				Enabled:              false,
				Headless:             true,
				BrowserPath:          "",
				CDPURL:               "",
				ActionTimeoutSeconds: 30,
				IdleTimeoutSeconds:   600,
				MaxPagesPerSession:   3,
				AllowPrivateHosts:    false,
				PersistentProfile:    false,
				CDPListen:            "",
				CDPRelayNode:         "",
				CDPRelayTarget:       "",
			},
			History: HistorySearchSection{
				Enabled: true,
				Auto: HistorySearchAutoSection{
					Enabled:                   true,
					MaxPerTurn:                1,
					DefaultScope:              "current_session",
					AllowArchiveOnClear:       true,
					AllowArchiveOnCompact:     true,
					AllowAllArchivesAutomatic: false,
					MinScore:                  3,
				},
				Cues: HistorySearchCueSection{
					Explicit: []string{
						"刚才", "之前", "上次", "前面", "聊天记录", "你说过", "我提过",
						"earlier", "previously", "chat history", "you said", "i mentioned",
					},
					Implicit: []string{
						"不是说过", "定过", "还记得", "previous", "remember", "mentioned before",
					},
				},
				Blocks: HistorySearchBlockSection{
					SessionSources: []string{"automation", "heartbeat", "cron", "review"},
				},
			},
			Permissions: PermissionsSection{
				BlockAutomationMutations:   true,
				InteractiveApprovalEnabled: true,
				InteractiveApprovalMode:    "manual",
				PendingTTLSeconds:          900,
				InteractiveApprovalSources: []string{"web", "gateway", "feishu", "weixin"},
				InteractiveApprovalTools: []string{
					"bash",
					"background",
					"write_file",
					"edit_file",
					"skill",
					"tool_exchange",
					"cron",
					"heartbeat",
					"browser",
					"desktop",
				},
				TrustedPathPrefixes:    []string{},
				TrustedCommandPrefixes: append([]string{}, defaultTrustedCommandPrefixes...),
			},
			LoopGuard: LoopGuardSection{
				Mode:                       "strict",
				MaxRecoveries:              5,
				MaxRepeatedTools:           8,
				MaxRepeatedPollingTools:    5,
				MaxStalledTaskPollingTools: 8,
			},
		},
		Media: MediaSection{
			Moonshot: MediaMoonshotSection{
				Enabled: false,
				BaseURL: "https://api.moonshot.ai/v1",
				APIKey:  "",
			},
			Document: MediaDocumentSection{
				MaxChars:      60000,
				PDFToTextPath: "pdftotext",
			},
			OCR: MediaOCRSection{
				Mode:          "auto",
				TesseractPath: "tesseract",
				MaxChars:      12000,
			},
			Audio: MediaAudioSection{
				Enabled:          false,
				FFmpegPath:       "ffmpeg",
				FFprobePath:      "ffprobe",
				WhisperCPPPath:   "whisper-cli",
				WhisperModelPath: "",
				MaxChars:         60000,
				VoiceEnabled:     false,
				VoiceEngineAddr:  "",
			},
			Video: MediaVideoSection{
				Enabled:                 false,
				KeyframeIntervalSeconds: 8,
				MaxFrames:               12,
			},
		},
		Channels: ChannelsSection{
			Feishu: FeishuSection{
				Enabled:   false,
				AppID:     "",
				AppSecret: "",
				Domain:    "lark",
			},
			Weixin: WeixinSection{
				Enabled:           false,
				BaseURL:           "https://ilinkai.weixin.qq.com",
				CDNBaseURL:        "https://novac2c.cdn.weixin.qq.com/c2c",
				AccountID:         "default",
				AllowFrom:         nil,
				RouteTag:          "",
				LongPollTimeoutMs: 35000,
				Proxy:             "",
			},
		},
	}
}
