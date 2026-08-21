package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/llm"
)

const (
	defaultProfileID            = "default"
	ProviderAnthropicCompatible = llm.ProviderAnthropicCompatible
	ProviderOpenAICompatible    = llm.ProviderOpenAICompatible
	ProviderOpenAICodex         = llm.ProviderOpenAICodex
)

// Config holds the fully resolved runtime configuration.
type Config struct {
	APIKey               string
	Model                string
	ReasoningEffort      string
	BaseURL              string
	DefaultProfileID     string
	DefaultModelRef      string
	FallbackProfileIDs   []string
	AutoFallbackEnabled  bool
	ModelProfiles        map[string]ModelProfileConfig
	LLMProviders         map[string]llm.ProviderConfig
	LLMStrategy          llm.StrategyConfig
	WebToken             string
	MaxTokens            int
	APITimeoutSeconds    int
	HomeDir              string
	WorkspaceDir         string
	ProjectDir           string
	ConfigFile           string
	HomeConfigFile       string
	ProjectConfigFile    string
	StateDir             string
	PackagesDir          string
	TeamDir              string
	TasksDir             string
	MemoryDir            string
	RulesDir             string
	SkillsDir            string
	TodosDir             string
	MCPConfigPath        string
	TempDir              string
	TranscriptsDir       string
	SessionsDir          string
	CompressThreshold    int
	Compaction           AgentCompactionConfig
	MaxTurns             int
	AgentProfile         string
	AgentDefaultProfiles AgentDefaultProfilesConfig
	LeadName             string
	TeamName             string
	DefaultSkills        []string
	TeammateWorkLimit    int
	TeammatePollEvery    time.Duration
	TeammateIdleFor      time.Duration
	EnvFile              string
	HomeEnvFile          string
	ProjectEnvFile       string
	Logging              LoggingConfig
	Security             SecurityConfig
	Cron                 CronConfig
	Heartbeat            HeartbeatConfig
	Control              ControlConfig
	Runtime              RuntimeConfig
	Storage              StorageConfig
	Tools                ToolsConfig
	Media                MediaConfig
	ACP                  ACPConfig
	Feishu               FeishuConfig
	Weixin               WeixinConfig
	Memory               MemoryConfig
}

type SecurityConfig struct {
	Profile string

	// Screener controls the content-level security screener (roadmap 6.1).
	Screener ScreenerConfig
}

// ScreenerConfig configures the content security screener.
type ScreenerConfig struct {
	// Enabled turns on the screener. When false (default) a no-op screener is
	// used and no content is classified.
	Enabled bool
	// Shadow records verdicts for audit without ever gating the pipeline.
	// Shadow mode is the recommended rollout state for 6.1.
	Shadow bool
	// Provider names the classifier provider for audit trails.
	Provider string
	// TimeoutMS bounds one classification call (default 10000).
	TimeoutMS int
	// MaxTokens bounds the classifier response (default 256).
	MaxTokens int
}

// MemoryConfig is the resolved durable-memory strategy configuration.
type MemoryConfig struct {
	// Strategy is the normalized memory strategy kind (per-turn/agent-only/consolidated).
	Strategy string
	// ConsolidateAfter is the pending-candidate count that triggers LLM
	// consolidation when Strategy is consolidated. <=0 means default.
	ConsolidateAfter int
	// SessionScope isolates durable memory per session (roadmap 6.2).
	SessionScope bool
}

type AgentDefaultProfilesConfig struct {
	ACP    string
	CLI    string
	TUI    string
	Web    string
	Weixin string
	Feishu string
}

type AgentCompactionConfig struct {
	AutoEnabled         bool
	TriggerTokens       int
	TargetHistoryTokens int
	Mode                string
	ModelProfileID      string
	MaxLatencyMS        int
	KeepRecentMessages  int
	ContextWindowTokens int
	TriggerRatio        float64
	RetainRatio         float64
	RetainTokens        int
	PruneThresholdChars int
	PruneHeadChars      int
	PruneTailChars      int
	ModelPolicies       []CompactionModelPolicy
}

// CompactionModelPolicy is one resolved per-model compaction policy override.
type CompactionModelPolicy struct {
	Provider            string
	Model               string
	ContextWindowTokens int
	TriggerTokens       int
	RetainTokens        int
	TriggerRatio        float64
	RetainRatio         float64
}

const (
	AgentProfileGeneral = "general"
	AgentProfileCoding  = "coding"
)

func NormalizeAgentProfile(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case AgentProfileCoding:
		return AgentProfileCoding
	default:
		return AgentProfileGeneral
	}
}

func NormalizeCompactionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "model", "deep":
		return "model"
	case "hybrid":
		return "hybrid"
	default:
		return "fast"
	}
}

func (c *Config) DefaultAgentProfileForChannel(channel string) string {
	if c == nil {
		return AgentProfileGeneral
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	switch channel {
	case "acp":
		return NormalizeAgentProfile(firstNonEmpty(c.AgentDefaultProfiles.ACP, c.AgentProfile))
	case "cli", "local":
		return NormalizeAgentProfile(firstNonEmpty(c.AgentDefaultProfiles.CLI, c.AgentProfile))
	case "tui":
		return NormalizeAgentProfile(firstNonEmpty(c.AgentDefaultProfiles.TUI, c.AgentProfile))
	case "weixin":
		return NormalizeAgentProfile(firstNonEmpty(c.AgentDefaultProfiles.Weixin, c.AgentProfile))
	case "feishu", "lark":
		return NormalizeAgentProfile(firstNonEmpty(c.AgentDefaultProfiles.Feishu, c.AgentProfile))
	case "web":
		return NormalizeAgentProfile(firstNonEmpty(c.AgentDefaultProfiles.Web, c.AgentProfile))
	default:
		return NormalizeAgentProfile(c.AgentProfile)
	}
}

// StorageConfig controls local runtime cache and artifact retention.
type StorageConfig struct {
	TmpTTLHours                 int
	ArtifactTTLHours            int
	BrowserCacheAutoClean       bool
	BrowserCacheMaxMB           int
	SessionCheckpointKeepLatest int
	SessionCheckpointTTLHours   int
	SessionCheckpointAutoPrune  bool
	SessionBackend              string
	SQLitePath                  string
}

// ModelProfileConfig describes one selectable model provider profile.
type ModelProfileConfig struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	BaseURL             string `json:"base_url"`
	APIKey              string `json:"api_key,omitempty"`
	MaxTokens           int    `json:"max_tokens"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	SupportsStreaming   bool   `json:"supports_streaming"`
	SupportsVision      bool   `json:"supports_vision"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
	RequestGzip         bool   `json:"request_gzip,omitempty"`
}

// ACPConfig describes external ACP-speaking agents available to tools.
type ACPConfig struct {
	Agents map[string]ACPAgentConfig
}

// ACPAgentConfig starts one external Agent Client Protocol process over stdio.
type ACPAgentConfig struct {
	ID             string            `json:"id"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Description    string            `json:"description,omitempty"`
}

// LoggingConfig controls package logger initialization.
type LoggingConfig struct {
	Level        string
	FilePath     string
	AlsoStderr   bool
	HasSensitive bool
}

// FeishuConfig holds the first-party Feishu/Lark channel settings.
type FeishuConfig struct {
	Enabled   bool
	AppID     string
	AppSecret string
	Domain    string
}

// WeixinConfig holds the first-party Weixin/iLink channel settings.
type WeixinConfig struct {
	Enabled           bool
	BaseURL           string
	CDNBaseURL        string
	AccountID         string
	AllowFrom         []string
	RouteTag          string
	LongPollTimeoutMs int
	Proxy             string
}

// CronConfig controls the persistent automation scheduler.
type CronConfig struct {
	Enabled           bool
	TickSeconds       int
	DefaultTimezone   string
	MaxConcurrentRuns int
}

// HeartbeatConfig controls the periodic proactive checklist loop.
type HeartbeatConfig struct {
	Enabled                bool
	TickSeconds            int
	ChecklistPath          string
	OKToken                string
	DefaultIntervalSeconds int
	DefaultTimezone        string
}

// ControlConfig controls multi-runtime registration and observability.
type ControlConfig struct {
	NodeName            string
	NodeID              string
	DefaultNode         string
	TrustLevel          string
	CenterURL           string
	Credential          string
	HeartbeatSeconds    int
	OfflineAfterSeconds int
	ForwardAllow        []string
	Nodes               []ControlNodeConfig
	Forwards            []ForwardConfig
}

// ControlNodeConfig describes a manually configured runtime node.
type ControlNodeConfig struct {
	ID           string   `json:"id" yaml:"id"`
	Name         string   `json:"name" yaml:"name"`
	Endpoint     string   `json:"endpoint" yaml:"endpoint"`
	WorkspaceDir string   `json:"workspace_dir" yaml:"workspace_dir"`
	GodexHome    string   `json:"godex_home" yaml:"godex_home"`
	Version      string   `json:"version" yaml:"version"`
	Capabilities []string `json:"capabilities" yaml:"capabilities"`
}

// ForwardConfig is the resolved form of a persistent forward tunnel.
type ForwardConfig struct {
	ID        string `json:"id" yaml:"id"`
	Name      string `json:"name" yaml:"name"`
	NodeID    string `json:"node_id" yaml:"node_id"`
	LocalPort int    `json:"local_port" yaml:"local_port"`
	Target    string `json:"target" yaml:"target"`
}

// RuntimeConfig controls backend runtime behavior that is not model/tool-specific.
type RuntimeConfig struct {
	Recovery RuntimeRecoveryConfig
}

// RuntimeRecoveryConfig controls safe recovery after process restarts.
type RuntimeRecoveryConfig struct {
	AutoResumeInterruptedTurns bool
	AutoRepairSessions         bool
}

// ToolsConfig holds first-party builtin capability settings.
type ToolsConfig struct {
	WebSearch   WebSearchConfig
	WebFetch    WebFetchConfig
	Glob        GlobConfig
	Subagent    SubagentConfig
	Execution   ToolExecutionConfig
	Browser     BrowserConfig
	Lightpanda  LightpandaConfig
	History     HistorySearchConfig
	Permissions PermissionConfig
	LoopGuard   LoopGuardConfig
}

// LightpandaConfig controls the lightpanda headless browser integration.
type LightpandaConfig struct {
	Enabled        bool
	BinaryPath     string
	AutoDownload   bool
	SearchEngine   string
	SearchTemplate string
	WaitNetworkMS  int
	ObeyRobots     bool
	LogLevel       string
}

// WebSearchConfig controls the built-in web_search tool.
type WebSearchConfig struct {
	Enabled         bool
	ProviderOrder   []string
	CacheTTLSeconds int
	Browser         WebSearchBrowserConfig
	BraveAPIKey     string
	ExaAPIKey       string
	TavilyAPIKey    string
	SerpAPIKey      string
}

type WebSearchBrowserConfig struct {
	Engine               string
	EngineFallback       []string
	Engines              map[string]WebSearchBrowserEngineConfig
	WaitNetworkIdleMS    int
	WaitAfterLoadMS      int
	MaxScrolls           int
	ResultTimeoutSeconds int
	PreferredHosts       []string
}

type WebSearchBrowserEngineConfig struct {
	SearchURLTemplate       string
	BlockedHosts            []string
	ResultContainerSelector string
	ResultLinkSelector      string
	ResultSnippetSelector   string
}

// WebFetchConfig controls the built-in web_fetch tool.
type WebFetchConfig struct {
	Enabled           bool
	MaxChars          int
	TimeoutSeconds    int
	Policy            string
	AllowedDomains    []string
	BlockedDomains    []string
	AllowPrivateHosts bool
}

// ToolExecutionConfig controls where command execution tools run.
type ToolExecutionConfig struct {
	Mode               string
	DockerImage        string
	DockerNetwork      string
	SSHTarget          string
	SSHWorkspace       string
	SSHOptions         []string
	ShellAllowPatterns []string
	ShellDenyPatterns  []string
	ToolTimeoutSeconds int
	// ScopeWrite rejects write-tool paths escaping the scope root (6.2 M4).
	ScopeWrite bool
}

// GlobConfig controls the built-in glob tool.
type GlobConfig struct {
	DefaultMaxResults int
}

// SubagentConfig controls durable subagent runtime limits.
type SubagentConfig struct {
	MaxBatchSize         int
	MaxConcurrentJobs    int
	DefaultMaxTurns      int
	MaxJobTimeoutMs      int
	ReadOnlyIsolation    string
	GitDirtyIsolation    string
	NonGitWriteIsolation string
	WorkspaceTTLHours    int
}

// BrowserConfig controls the built-in browser tool.
type BrowserConfig struct {
	Enabled              bool
	Headless             bool
	BrowserPath          string
	CDPURL               string
	ActionTimeoutSeconds int
	IdleTimeoutSeconds   int
	MaxPagesPerSession   int
	AllowPrivateHosts    bool
}

// HistorySearchConfig controls policy-driven history recall.
type HistorySearchConfig struct {
	Enabled bool
	Auto    HistorySearchAutoConfig
	Cues    HistorySearchCueConfig
	Blocks  HistorySearchBlockConfig
}

// HistorySearchAutoConfig controls weak automatic history recall exposure.
type HistorySearchAutoConfig struct {
	Enabled                   bool
	MaxPerTurn                int
	DefaultScope              string
	AllowArchiveOnClear       bool
	AllowArchiveOnCompact     bool
	AllowAllArchivesAutomatic bool
	MinScore                  int
}

// HistorySearchCueConfig lists explicit and implicit user cues for recall.
type HistorySearchCueConfig struct {
	Explicit []string
	Implicit []string
}

// HistorySearchBlockConfig lists session sources that should not auto-expose recall.
type HistorySearchBlockConfig struct {
	SessionSources []string
}

// PermissionConfig controls built-in tool permission policies and approvals.
type PermissionConfig struct {
	BlockAutomationMutations   bool
	InteractiveApprovalEnabled bool
	InteractiveApprovalMode    string
	InteractiveApprovalSources []string
	InteractiveApprovalTools   []string
	PendingTTLSeconds          int
	TrustedPathPrefixes        []string
	TrustedCommandPrefixes     []string
}

// LoopGuardConfig controls the conversation loop guard behavior including
// repeated-tool detection, stalled-polling detection, and recovery budget.
type LoopGuardConfig struct {
	Mode                       string
	MaxRecoveries              int
	MaxRepeatedTools           int
	MaxRepeatedPollingTools    int
	MaxStalledTaskPollingTools int
}

// MediaConfig controls shared attachment parsing and derived media artifacts.
type MediaConfig struct {
	Moonshot MoonshotMediaConfig
	Document DocumentMediaConfig
	OCR      OCRMediaConfig
	Audio    AudioMediaConfig
	Video    VideoMediaConfig
}

// MoonshotMediaConfig controls optional Moonshot file-extract preprocessing.
type MoonshotMediaConfig struct {
	Enabled bool
	BaseURL string
	APIKey  string
}

// DocumentMediaConfig controls document extraction defaults.
type DocumentMediaConfig struct {
	MaxChars      int
	PDFToTextPath string
}

// OCRMediaConfig controls image OCR extraction behavior.
type OCRMediaConfig struct {
	Mode          string
	TesseractPath string
	MaxChars      int
}

// AudioMediaConfig controls local audio transcription.
type AudioMediaConfig struct {
	Enabled          bool
	FFmpegPath       string
	FFprobePath      string
	WhisperCPPPath   string
	WhisperModelPath string
	MaxChars         int
}

// VideoMediaConfig controls local video summarization support.
type VideoMediaConfig struct {
	Enabled                 bool
	KeyframeIntervalSeconds int
	MaxFrames               int
}

// Clone returns a deep copy of the runtime config.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	cloned := *c
	if len(c.ModelProfiles) > 0 {
		cloned.ModelProfiles = make(map[string]ModelProfileConfig, len(c.ModelProfiles))
		for id, profile := range c.ModelProfiles {
			cloned.ModelProfiles[id] = profile
		}
	}
	if len(c.FallbackProfileIDs) > 0 {
		cloned.FallbackProfileIDs = append([]string{}, c.FallbackProfileIDs...)
	}
	if len(c.LLMProviders) > 0 {
		cloned.LLMProviders = make(map[string]llm.ProviderConfig, len(c.LLMProviders))
		for id, provider := range c.LLMProviders {
			if len(provider.Models) > 0 {
				models := make(map[string]llm.ModelConfig, len(provider.Models))
				for modelID, model := range provider.Models {
					model.Tags = append([]string{}, model.Tags...)
					models[modelID] = model
				}
				provider.Models = models
			}
			cloned.LLMProviders[id] = provider
		}
	}
	cloned.LLMStrategy = llm.NormalizeStrategy(c.LLMStrategy)
	cloned.Security = c.Security
	if len(c.ACP.Agents) > 0 {
		cloned.ACP.Agents = make(map[string]ACPAgentConfig, len(c.ACP.Agents))
		for id, agent := range c.ACP.Agents {
			if len(agent.Args) > 0 {
				agent.Args = append([]string{}, agent.Args...)
			}
			if len(agent.Env) > 0 {
				env := make(map[string]string, len(agent.Env))
				for key, value := range agent.Env {
					env[key] = value
				}
				agent.Env = env
			}
			cloned.ACP.Agents[id] = agent
		}
	}
	if len(c.DefaultSkills) > 0 {
		cloned.DefaultSkills = append([]string{}, c.DefaultSkills...)
	}
	if len(c.Tools.WebSearch.ProviderOrder) > 0 {
		cloned.Tools.WebSearch.ProviderOrder = append([]string{}, c.Tools.WebSearch.ProviderOrder...)
	}
	if len(c.Tools.WebSearch.Browser.EngineFallback) > 0 {
		cloned.Tools.WebSearch.Browser.EngineFallback = append([]string{}, c.Tools.WebSearch.Browser.EngineFallback...)
	}
	if len(c.Tools.WebSearch.Browser.Engines) > 0 {
		cloned.Tools.WebSearch.Browser.Engines = make(map[string]WebSearchBrowserEngineConfig, len(c.Tools.WebSearch.Browser.Engines))
		for key, value := range c.Tools.WebSearch.Browser.Engines {
			if len(value.BlockedHosts) > 0 {
				value.BlockedHosts = append([]string{}, value.BlockedHosts...)
			}
			cloned.Tools.WebSearch.Browser.Engines[key] = value
		}
	}
	if len(c.Tools.WebSearch.Browser.PreferredHosts) > 0 {
		cloned.Tools.WebSearch.Browser.PreferredHosts = append([]string{}, c.Tools.WebSearch.Browser.PreferredHosts...)
	}
	if len(c.Tools.WebFetch.AllowedDomains) > 0 {
		cloned.Tools.WebFetch.AllowedDomains = append([]string{}, c.Tools.WebFetch.AllowedDomains...)
	}
	if len(c.Tools.WebFetch.BlockedDomains) > 0 {
		cloned.Tools.WebFetch.BlockedDomains = append([]string{}, c.Tools.WebFetch.BlockedDomains...)
	}
	if len(c.Tools.Execution.SSHOptions) > 0 {
		cloned.Tools.Execution.SSHOptions = append([]string{}, c.Tools.Execution.SSHOptions...)
	}
	if len(c.Tools.Execution.ShellAllowPatterns) > 0 {
		cloned.Tools.Execution.ShellAllowPatterns = append([]string{}, c.Tools.Execution.ShellAllowPatterns...)
	}
	if len(c.Tools.Execution.ShellDenyPatterns) > 0 {
		cloned.Tools.Execution.ShellDenyPatterns = append([]string{}, c.Tools.Execution.ShellDenyPatterns...)
	}
	if len(c.Tools.Permissions.InteractiveApprovalSources) > 0 {
		cloned.Tools.Permissions.InteractiveApprovalSources = append([]string{}, c.Tools.Permissions.InteractiveApprovalSources...)
	}
	if len(c.Tools.History.Cues.Explicit) > 0 {
		cloned.Tools.History.Cues.Explicit = append([]string{}, c.Tools.History.Cues.Explicit...)
	}
	if len(c.Tools.History.Cues.Implicit) > 0 {
		cloned.Tools.History.Cues.Implicit = append([]string{}, c.Tools.History.Cues.Implicit...)
	}
	if len(c.Tools.History.Blocks.SessionSources) > 0 {
		cloned.Tools.History.Blocks.SessionSources = append([]string{}, c.Tools.History.Blocks.SessionSources...)
	}
	if len(c.Tools.Permissions.InteractiveApprovalTools) > 0 {
		cloned.Tools.Permissions.InteractiveApprovalTools = append([]string{}, c.Tools.Permissions.InteractiveApprovalTools...)
	}
	if len(c.Tools.Permissions.TrustedPathPrefixes) > 0 {
		cloned.Tools.Permissions.TrustedPathPrefixes = append([]string{}, c.Tools.Permissions.TrustedPathPrefixes...)
	}
	if len(c.Tools.Permissions.TrustedCommandPrefixes) > 0 {
		cloned.Tools.Permissions.TrustedCommandPrefixes = append([]string{}, c.Tools.Permissions.TrustedCommandPrefixes...)
	}
	if len(c.Weixin.AllowFrom) > 0 {
		cloned.Weixin.AllowFrom = append([]string{}, c.Weixin.AllowFrom...)
	}
	return &cloned
}

// LLMRegistry returns the normalized provider/model registry used by runtime callers.
func (c *Config) LLMRegistry() llm.Registry {
	if c == nil {
		return llm.NewRegistry(nil, llm.StrategyConfig{})
	}
	return llm.NewRegistry(c.LLMProviders, c.LLMStrategy)
}

// StrategyModelProfiles returns the ordered invocation candidates for a primary profile id.
func (c *Config) StrategyModelProfiles(primaryProfileID string) []ModelProfileConfig {
	if c == nil {
		return nil
	}
	profiles := make([]ModelProfileConfig, 0)
	seen := map[string]struct{}{}
	appendProfile := func(profile ModelProfileConfig) {
		profile.ID = strings.TrimSpace(profile.ID)
		if profile.ID == "" {
			return
		}
		if strings.TrimSpace(profile.APIKey) == "" && strings.TrimSpace(c.APIKey) != "" && profile.ID == strings.TrimSpace(c.DefaultProfileID) {
			profile.APIKey = c.APIKey
		}
		if _, ok := seen[profile.ID]; ok {
			return
		}
		seen[profile.ID] = struct{}{}
		profiles = append(profiles, profile)
	}
	if profile, ok := c.ModelProfileByID(primaryProfileID); ok {
		appendProfile(profile)
	}
	registry := c.LLMRegistry()
	candidates := registry.StrategyCandidates(primaryProfileID)
	for _, candidate := range candidates {
		appendProfile(modelProfileFromLLMCandidate(candidate))
	}
	return profiles
}

// DefaultModelProfile returns the currently effective default model profile.
func (c *Config) DefaultModelProfile() ModelProfileConfig {
	if c == nil {
		return ModelProfileConfig{}
	}
	if profile, ok := c.ModelProfileByID(c.DefaultProfileID); ok {
		return profile
	}
	if len(c.ModelProfiles) > 0 {
		keys := make([]string, 0, len(c.ModelProfiles))
		for key := range c.ModelProfiles {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return c.ModelProfiles[keys[0]]
	}
	return ModelProfileConfig{
		ID:                defaultProfileID,
		Name:              "Default",
		Provider:          ProviderAnthropicCompatible,
		Model:             c.Model,
		BaseURL:           c.BaseURL,
		APIKey:            c.APIKey,
		MaxTokens:         c.MaxTokens,
		TimeoutSeconds:    c.APITimeoutSeconds,
		SupportsStreaming: true,
		SupportsVision:    likelyVisionModel(c.Model),
	}
}

// ModelProfileByID returns a normalized model profile by id.
func (c *Config) ModelProfileByID(id string) (ModelProfileConfig, bool) {
	if c == nil {
		return ModelProfileConfig{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = strings.TrimSpace(c.DefaultProfileID)
	}
	if id == "" {
		id = defaultProfileID
	}
	if profile, ok := c.ModelProfiles[id]; ok {
		return normalizeModelProfile(id, profile, c), true
	}
	if id == defaultProfileID {
		return normalizeModelProfile(defaultProfileID, ModelProfileConfig{
			ID:                defaultProfileID,
			Name:              "Default",
			Provider:          ProviderAnthropicCompatible,
			Model:             c.Model,
			BaseURL:           c.BaseURL,
			APIKey:            c.APIKey,
			MaxTokens:         c.MaxTokens,
			TimeoutSeconds:    c.APITimeoutSeconds,
			SupportsStreaming: true,
			SupportsVision:    likelyVisionModel(c.Model),
		}, c), true
	}
	return ModelProfileConfig{}, false
}

func normalizeModelProfile(id string, profile ModelProfileConfig, cfg *Config) ModelProfileConfig {
	profile.ID = strings.TrimSpace(firstNonEmpty(profile.ID, id, defaultProfileID))
	profile.Name = strings.TrimSpace(profile.Name)
	if profile.Name == "" {
		profile.Name = profile.ID
	}
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	switch provider {
	case "", "anthropic", ProviderAnthropicCompatible:
		provider = ProviderAnthropicCompatible
	case "openai", ProviderOpenAICompatible:
		provider = ProviderOpenAICompatible
	case ProviderOpenAICodex:
		provider = ProviderOpenAICodex
	}
	profile.Provider = provider
	if strings.TrimSpace(profile.Model) == "" && cfg != nil {
		profile.Model = cfg.Model
	}
	if strings.TrimSpace(profile.BaseURL) == "" && cfg != nil {
		profile.BaseURL = cfg.BaseURL
	}
	if strings.TrimSpace(profile.APIKey) == "" && cfg != nil {
		profile.APIKey = cfg.APIKey
	}
	if profile.MaxTokens <= 0 && cfg != nil {
		profile.MaxTokens = cfg.MaxTokens
	}
	if profile.TimeoutSeconds <= 0 && cfg != nil {
		profile.TimeoutSeconds = cfg.APITimeoutSeconds
	}
	if !profile.SupportsStreaming {
		profile.SupportsStreaming = true
	}
	if !profile.SupportsVision {
		profile.SupportsVision = likelyVisionModel(profile.Model)
	}
	profile.ReasoningEffort = normalizeReasoningEffort(profile.ReasoningEffort)
	return profile
}

func normalizeReasoningEffort(effort string) string {
	return llm.NormalizeReasoningEffort(effort)
}

func modelProfileFromLLMCandidate(candidate llm.Candidate) ModelProfileConfig {
	name := strings.TrimSpace(candidate.ModelName)
	if name == "" {
		name = candidate.ProfileID
	}
	if strings.TrimSpace(candidate.ProviderName) != "" && candidate.ProviderName != candidate.ProviderID {
		name = candidate.ProviderName + " / " + name
	}
	return ModelProfileConfig{
		ID:                  candidate.ProfileID,
		Name:                name,
		Provider:            candidate.ProviderType,
		Model:               candidate.Model,
		BaseURL:             candidate.BaseURL,
		APIKey:              candidate.APIKey,
		MaxTokens:           candidate.MaxTokens,
		TimeoutSeconds:      candidate.TimeoutSeconds,
		SupportsStreaming:   candidate.SupportsStreaming,
		SupportsVision:      candidate.SupportsVision,
		ReasoningEffort:     candidate.ReasoningEffort,
		ContextWindowTokens: candidate.ContextWindowTokens,
		RequestGzip:         candidate.RequestGzip,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// DefaultConfig returns the current effective configuration resolved through
// the YAML manager. It preserves the historical helper for tests and older
// call-sites, while the main process now constructs a Manager directly.
func DefaultConfig() *Config {
	manager, err := NewManager(Options{})
	if err != nil {
		workspace := defaultWorkspaceDir()
		home := defaultHomeDir()
		cfg := defaultResolvedConfig(home, workspace, defaultConfigPath(home), defaultEnvPath(home))
		return cfg
	}
	return manager.Current()
}

func defaultHomeDir() string {
	if override := strings.TrimSpace(os.Getenv("GODEX_HOME")); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return filepath.Join(defaultWorkspaceDir(), ".godex")
	}
	return filepath.Join(home, ".godex")
}

func defaultWorkspaceDir() string {
	workspace, _ := os.Getwd()
	if workspace != "" {
		return workspace
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return "."
	}
	return filepath.Join(home, "Documents", "leader_agent")
}

func defaultConfigPath(workspace string) string {
	return filepath.Join(workspace, "godex.yaml")
}

func defaultEnvPath(workspace string) string {
	return filepath.Join(workspace, ".env")
}

func defaultResolvedConfig(home, workspace, configFile, envFile string) *Config {
	if home == "" {
		home = defaultHomeDir()
	}
	if workspace == "" {
		workspace = defaultWorkspaceDir()
	}
	homeConfig := defaultConfigPath(home)
	projectConfig := defaultConfigPath(workspace)
	homeEnv := defaultEnvPath(home)
	projectEnv := defaultEnvPath(workspace)
	defaults := defaultConfigFile()
	return resolveConfigFile(defaults, home, workspace, configFile, envFile, homeConfig, projectConfig, homeEnv, projectEnv)
}

// EnsureDirs creates necessary directories.
func (c *Config) EnsureDirs() error {
	if err := os.MkdirAll(c.StateDir, 0755); err != nil {
		return err
	}

	dirs := []string{c.TeamDir, c.TasksDir, c.MemoryDir, c.RulesDir, c.SkillsDir, c.PackagesDir, c.TodosDir, c.TempDir, c.TranscriptsDir, c.SessionsDir}
	if strings.TrimSpace(c.Storage.SQLitePath) != "" {
		dirs = append(dirs, filepath.Dir(c.Storage.SQLitePath))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(c.TeamDir, "inbox"), 0755); err != nil {
		return err
	}
	return nil
}
