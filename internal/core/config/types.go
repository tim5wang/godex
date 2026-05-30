package config

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/llm"
)

type Options struct {
	WorkspaceDir string
	HomeDir      string
	ProjectDir   string
	ConfigPath   string
	EnvFile      string
}

// ConfigFile is the canonical YAML shape persisted to godex.yaml.
type ConfigFile struct {
	API       APISection       `yaml:"api"`
	ACP       ACPSection       `yaml:"acp"`
	Agent     AgentSection     `yaml:"agent"`
	Logging   LoggingSection   `yaml:"logging"`
	Web       WebSection       `yaml:"web"`
	Cron      CronSection      `yaml:"cron"`
	Heartbeat HeartbeatSection `yaml:"heartbeat"`
	Control   ControlSection   `yaml:"control"`
	Runtime   RuntimeSection   `yaml:"runtime"`
	Security  SecuritySection  `yaml:"security"`
	Storage   StorageSection   `yaml:"storage"`
	Team      TeamSection      `yaml:"team"`
	Paths     PathsSection     `yaml:"paths"`
	Tools     ToolsSection     `yaml:"tools"`
	Media     MediaSection     `yaml:"media"`
	Channels  ChannelsSection  `yaml:"channels"`
}

type SecuritySection struct {
	Profile string `yaml:"profile"`
}

type StorageSection struct {
	TmpTTLHours                 int    `yaml:"tmp_ttl_hours"`
	ArtifactTTLHours            int    `yaml:"artifact_ttl_hours"`
	BrowserCacheAutoClean       bool   `yaml:"browser_cache_auto_clean"`
	BrowserCacheMaxMB           int    `yaml:"browser_cache_max_mb"`
	SessionCheckpointKeepLatest int    `yaml:"session_checkpoint_keep_latest"`
	SessionCheckpointTTLHours   int    `yaml:"session_checkpoint_ttl_hours"`
	SessionCheckpointAutoPrune  bool   `yaml:"session_checkpoint_auto_prune"`
	SessionBackend              string `yaml:"session_backend"`
	SQLitePath                  string `yaml:"sqlite_path"`
}

type APISection struct {
	DefaultProfile      string                        `yaml:"default_profile"`
	DefaultModel        string                        `yaml:"default_model"`
	AutoFallbackEnabled bool                          `yaml:"auto_fallback_enabled"`
	Providers           map[string]llm.ProviderConfig `yaml:"providers"`
	ModelStrategy       llm.StrategyConfig            `yaml:"model_strategy"`
	TimeoutSeconds      int                           `yaml:"timeout_seconds"`
}

type ACPSection struct {
	Agents map[string]ACPAgentSection `yaml:"agents"`
}

type ACPAgentSection struct {
	Command        string            `yaml:"command" json:"command"`
	Args           []string          `yaml:"args" json:"args,omitempty"`
	Env            map[string]string `yaml:"env" json:"env,omitempty"`
	TimeoutSeconds int               `yaml:"timeout_seconds" json:"timeout_seconds,omitempty"`
	Description    string            `yaml:"description" json:"description,omitempty"`
}

type AgentSection struct {
	CompressThreshold int                         `yaml:"compress_threshold"`
	Compaction        AgentCompactionSection      `yaml:"compaction"`
	MaxTurns          int                         `yaml:"max_turns"`
	Profile           string                      `yaml:"profile"`
	DefaultProfiles   AgentDefaultProfilesSection `yaml:"default_profiles"`
}

type AgentCompactionSection struct {
	AutoEnabled         bool   `yaml:"auto_enabled"`
	TriggerTokens       int    `yaml:"trigger_tokens"`
	TargetHistoryTokens int    `yaml:"target_history_tokens"`
	Mode                string `yaml:"mode"`
	ModelProfileID      string `yaml:"model_profile_id"`
	MaxLatencyMS        int    `yaml:"max_latency_ms"`
}

type AgentDefaultProfilesSection struct {
	ACP    string `yaml:"acp"`
	CLI    string `yaml:"cli"`
	TUI    string `yaml:"tui"`
	Web    string `yaml:"web"`
	Weixin string `yaml:"weixin"`
	Feishu string `yaml:"feishu"`
}

type LoggingSection struct {
	Level      string `yaml:"level"`
	FilePath   string `yaml:"file_path"`
	AlsoStderr bool   `yaml:"also_stderr"`
}

type WebSection struct {
	Token string `yaml:"token"`
}

type CronSection struct {
	Enabled           bool   `yaml:"enabled"`
	TickSeconds       int    `yaml:"tick_seconds"`
	DefaultTimezone   string `yaml:"default_timezone"`
	MaxConcurrentRuns int    `yaml:"max_concurrent_runs"`
}

type HeartbeatSection struct {
	Enabled                bool   `yaml:"enabled"`
	TickSeconds            int    `yaml:"tick_seconds"`
	ChecklistPath          string `yaml:"checklist_path"`
	OKToken                string `yaml:"ok_token"`
	DefaultIntervalSeconds int    `yaml:"default_interval_seconds"`
	DefaultTimezone        string `yaml:"default_timezone"`
}

type ControlSection struct {
	NodeName            string               `yaml:"node_name"`
	CenterURL           string               `yaml:"center_url"`
	HeartbeatSeconds    int                  `yaml:"heartbeat_seconds"`
	OfflineAfterSeconds int                  `yaml:"offline_after_seconds"`
	Nodes               []ControlNodeSection `yaml:"nodes"`
}

type ControlNodeSection struct {
	ID           string   `yaml:"id" json:"id"`
	Name         string   `yaml:"name" json:"name,omitempty"`
	Endpoint     string   `yaml:"endpoint" json:"endpoint,omitempty"`
	WorkspaceDir string   `yaml:"workspace_dir" json:"workspace_dir,omitempty"`
	GodexHome    string   `yaml:"godex_home" json:"godex_home,omitempty"`
	Version      string   `yaml:"version" json:"version,omitempty"`
	Capabilities []string `yaml:"capabilities" json:"capabilities,omitempty"`
}

type RuntimeSection struct {
	Recovery RuntimeRecoverySection `yaml:"recovery"`
}

type RuntimeRecoverySection struct {
	AutoResumeInterruptedTurns bool `yaml:"auto_resume_interrupted_turns"`
	AutoRepairSessions         bool `yaml:"auto_repair_sessions"`
}

type TeamSection struct {
	LeadName                string   `yaml:"lead_name"`
	TeamName                string   `yaml:"team_name"`
	DefaultSkills           []string `yaml:"default_skills"`
	TeammateWorkLimit       int      `yaml:"teammate_work_limit"`
	TeammatePollSeconds     int      `yaml:"teammate_poll_seconds"`
	TeammateIdleTimeoutSecs int      `yaml:"teammate_idle_timeout_seconds"`
}

type PathsSection struct {
	StateDir       string `yaml:"state_dir"`
	TeamDir        string `yaml:"team_dir"`
	TasksDir       string `yaml:"tasks_dir"`
	TodosDir       string `yaml:"todos_dir"`
	MemoryDir      string `yaml:"memory_dir"`
	RulesDir       string `yaml:"rules_dir"`
	SkillsDir      string `yaml:"skills_dir"`
	MCPConfigPath  string `yaml:"mcp_config_path"`
	TempDir        string `yaml:"temp_dir"`
	TranscriptsDir string `yaml:"transcripts_dir"`
	SessionsDir    string `yaml:"sessions_dir"`
}

type ChannelsSection struct {
	Feishu FeishuSection `yaml:"feishu"`
	Weixin WeixinSection `yaml:"weixin"`
}

type MediaSection struct {
	Moonshot MediaMoonshotSection `yaml:"moonshot"`
	Document MediaDocumentSection `yaml:"document"`
	OCR      MediaOCRSection      `yaml:"ocr"`
	Audio    MediaAudioSection    `yaml:"audio"`
	Video    MediaVideoSection    `yaml:"video"`
}

type FeishuSection struct {
	Enabled   bool   `yaml:"enabled"`
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	Domain    string `yaml:"domain"`
}

type WeixinSection struct {
	Enabled           bool     `yaml:"enabled"`
	BaseURL           string   `yaml:"base_url"`
	CDNBaseURL        string   `yaml:"cdn_base_url"`
	AccountID         string   `yaml:"account_id"`
	AllowFrom         []string `yaml:"allow_from"`
	RouteTag          string   `yaml:"route_tag"`
	LongPollTimeoutMs int      `yaml:"long_poll_timeout_ms"`
	Proxy             string   `yaml:"proxy"`
}

type ToolsSection struct {
	WebSearch   WebSearchSection     `yaml:"web_search"`
	WebFetch    WebFetchSection      `yaml:"web_fetch"`
	Glob        GlobSection          `yaml:"glob"`
	Subagent    SubagentSection      `yaml:"subagent"`
	Execution   ExecutionSection     `yaml:"execution"`
	Browser     BrowserSection       `yaml:"browser"`
	Lightpanda  LightpandaSection    `yaml:"lightpanda"`
	History     HistorySearchSection `yaml:"history_search"`
	Permissions PermissionsSection   `yaml:"permissions"`
}

type LightpandaSection struct {
	Enabled        bool   `yaml:"enabled"`
	BinaryPath     string `yaml:"binary_path"`
	AutoDownload   bool   `yaml:"auto_download"`
	SearchEngine   string `yaml:"search_engine"`
	SearchTemplate string `yaml:"search_template"`
	WaitNetworkMS  int    `yaml:"wait_network_ms"`
	ObeyRobots     bool   `yaml:"obey_robots"`
	LogLevel       string `yaml:"log_level"`
}

type WebSearchSection struct {
	Enabled         bool                    `yaml:"enabled"`
	ProviderOrder   []string                `yaml:"provider_order"`
	CacheTTLSeconds int                     `yaml:"cache_ttl_seconds"`
	Browser         WebSearchBrowserSection `yaml:"browser"`
	Brave           APIKeyRefSection        `yaml:"brave"`
	Exa             APIKeyRefSection        `yaml:"exa"`
	Tavily          APIKeyRefSection        `yaml:"tavily"`
	SerpAPI         APIKeyRefSection        `yaml:"serpapi"`
}

type WebSearchBrowserSection struct {
	Engine               string                                   `yaml:"engine"`
	EngineFallback       []string                                 `yaml:"engine_fallback"`
	Engines              map[string]WebSearchBrowserEngineSection `yaml:"engines"`
	WaitNetworkIdleMS    int                                      `yaml:"wait_network_idle_ms"`
	WaitAfterLoadMS      int                                      `yaml:"wait_after_load_ms"`
	MaxScrolls           int                                      `yaml:"max_scrolls"`
	ResultTimeoutSeconds int                                      `yaml:"result_timeout_seconds"`
	PreferredHosts       []string                                 `yaml:"preferred_hosts"`
}

type WebSearchBrowserEngineSection struct {
	SearchURLTemplate       string   `yaml:"search_url_template"`
	BlockedHosts            []string `yaml:"blocked_hosts"`
	ResultContainerSelector string   `yaml:"result_container_selector"`
	ResultLinkSelector      string   `yaml:"result_link_selector"`
	ResultSnippetSelector   string   `yaml:"result_snippet_selector"`
}

type WebFetchSection struct {
	Enabled           bool     `yaml:"enabled"`
	MaxChars          int      `yaml:"max_chars"`
	TimeoutSeconds    int      `yaml:"timeout_seconds"`
	Policy            string   `yaml:"policy"`
	AllowedDomains    []string `yaml:"allowed_domains"`
	BlockedDomains    []string `yaml:"blocked_domains"`
	AllowPrivateHosts bool     `yaml:"allow_private_hosts"`
}

type GlobSection struct {
	DefaultMaxResults int `yaml:"default_max_results"`
}

type SubagentSection struct {
	MaxBatchSize         int    `yaml:"max_batch_size"`
	MaxConcurrentJobs    int    `yaml:"max_concurrent_jobs"`
	DefaultMaxTurns      int    `yaml:"default_max_turns"`
	MaxJobTimeoutMs      int    `yaml:"max_job_timeout_ms"`
	ReadOnlyIsolation    string `yaml:"readonly_isolation"`
	GitDirtyIsolation    string `yaml:"git_dirty_isolation"`
	NonGitWriteIsolation string `yaml:"non_git_write_isolation"`
	WorkspaceTTLHours    int    `yaml:"workspace_ttl_hours"`
}

type ExecutionSection struct {
	Mode               string   `yaml:"mode"`
	DockerImage        string   `yaml:"docker_image"`
	DockerNetwork      string   `yaml:"docker_network"`
	SSHTarget          string   `yaml:"ssh_target"`
	SSHWorkspace       string   `yaml:"ssh_workspace"`
	SSHOptions         []string `yaml:"ssh_options"`
	ShellAllowPatterns []string `yaml:"shell_allow_patterns"`
	ShellDenyPatterns  []string `yaml:"shell_deny_patterns"`
}

type BrowserSection struct {
	Enabled              bool   `yaml:"enabled"`
	Headless             bool   `yaml:"headless"`
	BrowserPath          string `yaml:"browser_path"`
	CDPURL               string `yaml:"cdp_url"`
	ActionTimeoutSeconds int    `yaml:"action_timeout_seconds"`
	IdleTimeoutSeconds   int    `yaml:"idle_timeout_seconds"`
	MaxPagesPerSession   int    `yaml:"max_pages_per_session"`
	AllowPrivateHosts    bool   `yaml:"allow_private_hosts"`
}

type HistorySearchSection struct {
	Enabled bool                      `yaml:"enabled"`
	Auto    HistorySearchAutoSection  `yaml:"auto"`
	Cues    HistorySearchCueSection   `yaml:"cues"`
	Blocks  HistorySearchBlockSection `yaml:"blocks"`
}

type HistorySearchAutoSection struct {
	Enabled                   bool   `yaml:"enabled"`
	MaxPerTurn                int    `yaml:"max_per_turn"`
	DefaultScope              string `yaml:"default_scope"`
	AllowArchiveOnClear       bool   `yaml:"allow_archive_on_clear"`
	AllowArchiveOnCompact     bool   `yaml:"allow_archive_on_compact"`
	AllowAllArchivesAutomatic bool   `yaml:"allow_all_archives_automatic"`
	MinScore                  int    `yaml:"min_score"`
}

type HistorySearchCueSection struct {
	Explicit []string `yaml:"explicit"`
	Implicit []string `yaml:"implicit"`
}

type HistorySearchBlockSection struct {
	SessionSources []string `yaml:"session_sources"`
}

type PermissionsSection struct {
	BlockAutomationMutations   bool     `yaml:"block_automation_mutations"`
	InteractiveApprovalEnabled bool     `yaml:"interactive_approval_enabled"`
	InteractiveApprovalMode    string   `yaml:"interactive_approval_mode"`
	InteractiveApprovalSources []string `yaml:"interactive_approval_sources"`
	InteractiveApprovalTools   []string `yaml:"interactive_approval_tools"`
	PendingTTLSeconds          int      `yaml:"pending_ttl_seconds"`
	TrustedPathPrefixes        []string `yaml:"trusted_path_prefixes"`
	TrustedCommandPrefixes     []string `yaml:"trusted_command_prefixes"`
}

type APIKeyRefSection struct {
	APIKey string `yaml:"api_key"`
}

type MediaMoonshotSection struct {
	Enabled bool   `yaml:"enabled"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type MediaDocumentSection struct {
	MaxChars      int    `yaml:"max_chars"`
	PDFToTextPath string `yaml:"pdftotext_path"`
}

type MediaOCRSection struct {
	Mode          string `yaml:"mode"`
	TesseractPath string `yaml:"tesseract_path"`
	MaxChars      int    `yaml:"max_chars"`
}

type MediaAudioSection struct {
	Enabled          bool   `yaml:"enabled"`
	FFmpegPath       string `yaml:"ffmpeg_path"`
	FFprobePath      string `yaml:"ffprobe_path"`
	WhisperCPPPath   string `yaml:"whisper_cpp_path"`
	WhisperModelPath string `yaml:"whisper_model_path"`
	MaxChars         int    `yaml:"max_chars"`
}

type MediaVideoSection struct {
	Enabled                 bool `yaml:"enabled"`
	KeyframeIntervalSeconds int  `yaml:"keyframe_interval_seconds"`
	MaxFrames               int  `yaml:"max_frames"`
}

// Source describes where one effective value comes from.
type Source string

const (
	SourceDefault Source = "default"
	SourceYAML    Source = "yaml"
	SourceDotEnv  Source = "dotenv"
	SourceEnv     Source = "env"
)

// FieldSchema describes one editable config field for the Web UI.
type FieldSchema struct {
	Path        string   `json:"path"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Secret      bool     `json:"secret,omitempty"`
	LiveApply   bool     `json:"live_apply,omitempty"`
	Env         string   `json:"env,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// SectionSchema groups a set of config fields for UI rendering.
type SectionSchema struct {
	ID          string        `json:"id"`
	Label       string        `json:"label"`
	Description string        `json:"description,omitempty"`
	Fields      []FieldSchema `json:"fields"`
}

// FieldState reports source and masking status for one field.
type FieldState struct {
	Source        Source `json:"source"`
	OverriddenBy  Source `json:"overridden_by,omitempty"`
	Secret        bool   `json:"secret,omitempty"`
	Masked        bool   `json:"masked,omitempty"`
	Configured    bool   `json:"configured,omitempty"`
	LiveApply     bool   `json:"live_apply,omitempty"`
	Env           string `json:"env,omitempty"`
	DeprecatedEnv string `json:"deprecated_env,omitempty"`
}

type StorageStatus string
type RuntimeStatus string

const (
	StorageStatusSaved      StorageStatus = "saved"
	StorageStatusSaveFailed StorageStatus = "save_failed"

	RuntimeStatusApplied            RuntimeStatus = "applied"
	RuntimeStatusAppliedWithWarning RuntimeStatus = "applied_with_warnings"
	RuntimeStatusFailed             RuntimeStatus = "failed"
	RuntimeStatusSkipped            RuntimeStatus = "skipped"
)

// ApplyReport captures the result of the last config apply attempt.
type ApplyReport struct {
	AppliedAt     time.Time     `json:"applied_at,omitempty"`
	StorageStatus StorageStatus `json:"storage_status,omitempty"`
	RuntimeStatus RuntimeStatus `json:"runtime_status,omitempty"`
	Message       string        `json:"message,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	Errors        []string      `json:"errors,omitempty"`
}

// Meta describes config storage state.
type Meta struct {
	FilePath          string      `json:"file_path"`
	EnvFile           string      `json:"env_file"`
	HomeDir           string      `json:"home_dir,omitempty"`
	ProjectDir        string      `json:"project_dir,omitempty"`
	HomeConfigFile    string      `json:"home_config_file,omitempty"`
	ProjectConfigFile string      `json:"project_config_file,omitempty"`
	HomeEnvFile       string      `json:"home_env_file,omitempty"`
	ProjectEnvFile    string      `json:"project_env_file,omitempty"`
	Revision          int64       `json:"revision"`
	LastApply         ApplyReport `json:"last_apply"`
}

// View is the editor-facing config payload.
type View struct {
	FilePath          string                `json:"file_path"`
	EnvFile           string                `json:"env_file"`
	HomeDir           string                `json:"home_dir,omitempty"`
	ProjectDir        string                `json:"project_dir,omitempty"`
	HomeConfigFile    string                `json:"home_config_file,omitempty"`
	ProjectConfigFile string                `json:"project_config_file,omitempty"`
	HomeEnvFile       string                `json:"home_env_file,omitempty"`
	ProjectEnvFile    string                `json:"project_env_file,omitempty"`
	Revision          int64                 `json:"revision"`
	StoredValues      map[string]any        `json:"stored_values"`
	EffectiveValues   map[string]any        `json:"effective_values"`
	Fields            map[string]FieldState `json:"fields"`
	LastApply         ApplyReport           `json:"last_apply"`
}

// UpdateRequest saves edited YAML values back into godex.yaml.
type UpdateRequest struct {
	Values       map[string]any `json:"values,omitempty"`
	ClearSecrets []string       `json:"clear_secrets,omitempty"`
}

// DoctorCheck is one config diagnosis item.
type DoctorCheck struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// DoctorReport aggregates config diagnostics.
type DoctorReport struct {
	GeneratedAt       time.Time     `json:"generated_at"`
	HomeDir           string        `json:"home_dir,omitempty"`
	ProjectDir        string        `json:"project_dir,omitempty"`
	HomeConfigFile    string        `json:"home_config_file,omitempty"`
	ProjectConfigFile string        `json:"project_config_file,omitempty"`
	HomeEnvFile       string        `json:"home_env_file,omitempty"`
	ProjectEnvFile    string        `json:"project_env_file,omitempty"`
	Errors            int           `json:"errors"`
	Warnings          int           `json:"warnings"`
	Infos             int           `json:"infos"`
	Checks            []DoctorCheck `json:"checks"`
	LastApply         ApplyReport   `json:"last_apply"`
}

// Text renders a concise command-friendly doctor output.
func (r DoctorReport) Text() string {
	lines := []string{
		fmt.Sprintf("Doctor summary: %d error(s), %d warning(s), %d info item(s)", r.Errors, r.Warnings, r.Infos),
	}
	for _, check := range r.Checks {
		line := fmt.Sprintf("[%s] %s", strings.ToUpper(check.Severity), check.Message)
		if check.Path != "" {
			line = fmt.Sprintf("[%s] %s: %s", strings.ToUpper(check.Severity), check.Path, check.Message)
		}
		if check.Suggestion != "" {
			line += " | " + check.Suggestion
		}
		lines = append(lines, line)
	}
	if r.LastApply.StorageStatus != "" || r.LastApply.RuntimeStatus != "" {
		lines = append(lines, fmt.Sprintf("Last apply: storage=%s runtime=%s", defaultApplyStorage(r.LastApply.StorageStatus), defaultApplyRuntime(r.LastApply.RuntimeStatus)))
		if r.LastApply.Message != "" {
			lines = append(lines, "  "+r.LastApply.Message)
		}
		for _, warning := range r.LastApply.Warnings {
			lines = append(lines, "  warning: "+warning)
		}
		for _, err := range r.LastApply.Errors {
			lines = append(lines, "  error: "+err)
		}
	}
	return strings.Join(lines, "\n")
}

// ApplyFunc applies one new effective config to the live runtime.
type ApplyFunc func(context.Context, *Config, *Config) ApplyReport

type envValue struct {
	Value string
	Name  string
	Set   bool
}

type fieldOrigin struct {
	Source       Source
	OverriddenBy Source
	CanonicalEnv string
	UsedEnv      string
	YAMLValue    any
	DotEnvValue  any
	EnvValue     any
	Effective    any
}

// Manager owns canonical YAML config loading, writing, diagnostics, and live apply.
type Manager struct {
	mu                sync.RWMutex
	workspace         string
	homeDir           string
	projectDir        string
	configPath        string
	envPath           string
	homeConfigPath    string
	projectConfigPath string
	homeEnvPath       string
	projectEnvPath    string

	stored    ConfigFile
	current   *Config
	origins   map[string]fieldOrigin
	revision  int64
	schema    []SectionSchema
	lastApply ApplyReport
	applier   ApplyFunc

	doctorAugmenter func(DoctorReport) DoctorReport
}
