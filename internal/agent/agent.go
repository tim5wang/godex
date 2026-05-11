package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/insights"
	"github.com/tim5wang/godex/internal/core/instructions"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/core/media"
	"github.com/tim5wang/godex/internal/core/memory"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/automation"
	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/platform/stringutil"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/services/historysearch"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/tools/teamtools"
)

// Agent is the main agent orchestrator.
type Agent struct {
	cfg           *config.Config
	toolHandler   *tools.ToolHandler
	todoMgr       *todo.Manager
	skillLoader   *skill.Loader
	instrLoader   *instructions.Loader
	memoryMgr     *memory.Manager
	memoryExt     *memory.Extractor
	mcpMgr        *mcp.Manager
	compressor    *compress.Compressor
	summarizer    compress.SessionSummarizer
	taskMgr       *task.Manager
	bgMgr         *background.Manager
	webSearch     *tools.WebSearchService
	webFetch      *tools.WebFetchService
	browser       *tools.BrowserService
	permissions   *tools.PermissionManager
	historySearch tools.HistorySearchRuntime
	sessionAdmin  tools.SessionAdminRuntime
	cron          tools.CronManager
	heartbeat     tools.HeartbeatManager
	media         *media.Processor
	msgBus        *message.Bus
	teamMgr       *teammate.Manager
	subagentJobs  *subagentJobStore
	workflows     *workflowStore
	client        conversation.Caller

	prompts              conversation.PromptLayers
	messages             []protocol.Message
	pendingResume        *PendingResumeState
	idleRequested        bool
	activeSkills         map[string]*activeSkillState
	transcriptRefs       []string
	historyVersion       int64
	lastCompactedVersion int64
	now                  func() time.Time
	mu                   sync.Mutex
}

const (
	bundleCoreCode   = "core_code"
	bundlePlanning   = "planning"
	bundleBackground = "background"
	bundleTaskBoard  = "task_board"
	bundleTeam       = "team"
	bundleSubagent   = "subagent"
	bundleMCP        = "mcp"
	bundleWeb        = "web"
	bundleBrowser    = "browser"
	bundleDesktop    = "desktop"
	bundlePackages   = "packages"
	bundleExternal   = "external_agents"
)

type activeSkillState struct {
	catalog       skill.CatalogEntry
	core          string
	expanded      map[string]string
	expandedOrder []string
}

func (s *activeSkillState) loadedSections() []string {
	sections := make([]string, 0, 1+len(s.expandedOrder))
	if strings.TrimSpace(s.core) != "" {
		sections = append(sections, "core")
	}
	sections = append(sections, s.expandedOrder...)
	return sections
}

type dependencies struct {
	taskMgr      *task.Manager
	msgBus       *message.Bus
	client       conversation.Caller
	skillLoader  *skill.Loader
	instrLoader  *instructions.Loader
	memoryMgr    *memory.Manager
	memoryExt    *memory.Extractor
	mcpMgr       *mcp.Manager
	compressor   *compress.Compressor
	summarizer   compress.SessionSummarizer
	bgMgr        *background.Manager
	webSearch    *tools.WebSearchService
	webFetch     *tools.WebFetchService
	browser      *tools.BrowserService
	permissions  *tools.PermissionManager
	history      *historysearch.Service
	sessionAdmin *sessionadmin.Service
	cron         tools.CronManager
	heartbeat    tools.HeartbeatManager
	media        *media.Processor
	teamMgr      *teammate.Manager
	subagentJobs *subagentJobStore
	workflows    *workflowStore
	todoMgr      *todo.Manager
}

// New creates a new agent.
func New(cfg *config.Config) *Agent {
	return newAgentWithDependencies(cfg, buildDependencies(cfg))
}

func buildDependencies(cfg *config.Config) dependencies {
	taskMgr := task.NewManager(cfg.TasksDir)
	msgBus := loadMessageBus(cfg.TeamDir)
	client := callerForConfigProfile(cfg, cfg.DefaultModelProfile())
	skillLoader := newSkillLoader(cfg, client)
	memoryMgr := memory.NewManager(cfg.MemoryDir)
	compressor := compress.NewCompressor(cfg.TranscriptsDir)
	ruleSummarizer := compress.NewRuleBasedSessionSummarizer(compressor)
	sessionSummarizer := compress.SessionSummarizer(ruleSummarizer)
	if strings.TrimSpace(cfg.APIKey) != "" {
		sessionSummarizer = compress.NewLLMSessionSummarizer(client, cfg.Model, min(cfg.MaxTokens, 2048), compressor, ruleSummarizer)
	}
	webFetch := tools.NewWebFetchService(cfg.Tools.WebFetch, cfg.TempDir)
	webSearch := tools.NewWebSearchService(cfg.Tools.WebSearch)
	browser := tools.NewBrowserService(cfg.Tools.Browser, cfg.TempDir, cfg.Storage)
	webSearch.SetPreviewFetcher(webFetch)
	webSearch.SetBrowserSearcher(tools.NewBrowserSearchProvider(browser, cfg.Tools.WebSearch.Browser))
	return dependencies{
		taskMgr:      taskMgr,
		msgBus:       msgBus,
		client:       client,
		skillLoader:  skillLoader,
		instrLoader:  instructions.NewLoader(),
		memoryMgr:    memoryMgr,
		memoryExt:    memory.NewExtractor(memoryMgr, cfg.TempDir),
		mcpMgr:       mcp.NewManager(cfg.MCPConfigPath, cfg.WorkspaceDir, cfg.TempDir),
		compressor:   compressor,
		summarizer:   sessionSummarizer,
		bgMgr:        background.NewManagerWithStore(filepath.Join(cfg.StateDir, "background")),
		webSearch:    webSearch,
		webFetch:     webFetch,
		browser:      browser,
		permissions:  tools.NewPermissionManagerForPolicy(permissionPolicyFromConfig(cfg)),
		history:      historysearch.NewService(cfg),
		media:        media.NewProcessor(cfg.Media, cfg.WorkspaceDir, cfg.SessionsDir, cfg.TempDir),
		teamMgr:      newTeamManager(cfg, taskMgr, msgBus, client),
		subagentJobs: newSubagentJobStore(subagentJobsDir(cfg)),
		workflows:    newWorkflowStore(filepath.Join(cfg.StateDir, "workflows")),
		todoMgr:      todo.NewManager(cfg.TodosDir),
	}
}

func apiRequestTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.APITimeoutSeconds <= 0 {
		return 600 * time.Second
	}
	return time.Duration(cfg.APITimeoutSeconds) * time.Second
}

func callerForConfigProfile(cfg *config.Config, primary config.ModelProfileConfig) conversation.Caller {
	if cfg == nil {
		return conversation.NewCallerForProfile(primary)
	}
	profiles := cfg.StrategyModelProfiles(primary.ID)
	strategy := llm.NormalizeStrategy(cfg.LLMStrategy).Type
	if len(profiles) <= 1 {
		profiles = []config.ModelProfileConfig{primary}
		if cfg.AutoFallbackEnabled {
			for _, id := range cfg.FallbackProfileIDs {
				profile, ok := cfg.ModelProfileByID(id)
				if !ok || strings.TrimSpace(profile.ID) == strings.TrimSpace(primary.ID) {
					continue
				}
				profiles = append(profiles, profile)
			}
		}
		strategy = llm.StrategyFallback
	}
	if !cfg.AutoFallbackEnabled && strategy == llm.StrategyFallback {
		strategy = llm.StrategyPrimary
	}
	return conversation.NewStrategyCallerForProfiles(strategy, profiles)
}

func permissionPolicyFromConfig(cfg *config.Config) tools.PermissionPolicy {
	if cfg == nil {
		return tools.DefaultPermissionPolicy()
	}
	if strings.TrimSpace(cfg.Security.Profile) == "" &&
		!cfg.Tools.Permissions.BlockAutomationMutations &&
		!cfg.Tools.Permissions.InteractiveApprovalEnabled &&
		cfg.Tools.Permissions.InteractiveApprovalMode == "" &&
		len(cfg.Tools.Permissions.InteractiveApprovalSources) == 0 &&
		len(cfg.Tools.Permissions.InteractiveApprovalTools) == 0 &&
		len(cfg.Tools.Permissions.TrustedPathPrefixes) == 0 &&
		len(cfg.Tools.Permissions.TrustedCommandPrefixes) == 0 {
		return tools.DefaultPermissionPolicy()
	}
	policy := tools.PermissionPolicyForSecurityProfile(cfg.Security.Profile, cfg.Tools.Permissions.InteractiveApprovalMode)
	policy.BlockAutomationMutations = cfg.Tools.Permissions.BlockAutomationMutations
	if cfg.Tools.Permissions.InteractiveApprovalMode != "" {
		policy.InteractiveApproval.Mode = cfg.Tools.Permissions.InteractiveApprovalMode
	}
	policy.InteractiveApproval.Enabled = cfg.Tools.Permissions.InteractiveApprovalEnabled
	if len(cfg.Tools.Permissions.InteractiveApprovalSources) > 0 {
		policy.InteractiveApproval.Sources = append([]string{}, cfg.Tools.Permissions.InteractiveApprovalSources...)
	}
	if len(cfg.Tools.Permissions.InteractiveApprovalTools) > 0 {
		policy.InteractiveApproval.Tools = append([]string{}, cfg.Tools.Permissions.InteractiveApprovalTools...)
	}
	if len(cfg.Tools.Permissions.TrustedPathPrefixes) > 0 {
		policy.InteractiveApproval.TrustedPathPrefixes = append([]string{}, cfg.Tools.Permissions.TrustedPathPrefixes...)
	}
	if len(cfg.Tools.Permissions.TrustedCommandPrefixes) > 0 {
		policy.InteractiveApproval.TrustedCommandPrefixes = append([]string{}, cfg.Tools.Permissions.TrustedCommandPrefixes...)
	}
	return policy
}

func newAgentWithDependencies(cfg *config.Config, deps dependencies) *Agent {
	handler := tools.NewToolHandler()
	if deps.subagentJobs == nil {
		deps.subagentJobs = newSubagentJobStore(subagentJobsDir(cfg))
	}
	if deps.workflows == nil {
		deps.workflows = newWorkflowStore(filepath.Join(cfg.StateDir, "workflows"))
	}
	if deps.summarizer == nil {
		deps.summarizer = compress.NewRuleBasedSessionSummarizer(deps.compressor)
	}
	agent := &Agent{
		cfg:            cfg,
		toolHandler:    handler,
		todoMgr:        deps.todoMgr,
		skillLoader:    deps.skillLoader,
		instrLoader:    deps.instrLoader,
		memoryMgr:      deps.memoryMgr,
		memoryExt:      deps.memoryExt,
		mcpMgr:         deps.mcpMgr,
		compressor:     deps.compressor,
		summarizer:     deps.summarizer,
		taskMgr:        deps.taskMgr,
		bgMgr:          deps.bgMgr,
		webSearch:      deps.webSearch,
		webFetch:       deps.webFetch,
		browser:        deps.browser,
		permissions:    deps.permissions,
		historySearch:  nil,
		sessionAdmin:   nil,
		cron:           deps.cron,
		heartbeat:      deps.heartbeat,
		media:          deps.media,
		msgBus:         deps.msgBus,
		teamMgr:        deps.teamMgr,
		subagentJobs:   deps.subagentJobs,
		workflows:      deps.workflows,
		client:         deps.client,
		messages:       []protocol.Message{},
		activeSkills:   make(map[string]*activeSkillState),
		transcriptRefs: nil,
		now:            time.Now,
		prompts: conversation.PromptLayers{
			Base: "You are a helpful AI agent working inside this workspace. Use available tools and skills to solve tasks.",
		},
	}
	if deps.sessionAdmin != nil {
		agent.sessionAdmin = deps.sessionAdmin.Bind(agent)
	}
	if deps.history != nil {
		agent.historySearch = deps.history.Bind(agent)
	}
	agent.toolHandler.AddBeforeInterceptors(tools.NewPermissionInterceptorWithReview(deps.permissions, agent.reviewPermissionRequest))
	return agent
}

func loadMessageBus(teamDir string) *message.Bus {
	inboxDir := fmt.Sprintf("%s/inbox", teamDir)
	msgBus := message.NewBus(inboxDir)
	if err := msgBus.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load persisted inbox messages: %v\n", err)
	}
	return msgBus
}

func newSkillLoader(cfg *config.Config, client conversation.Caller) *skill.Loader {
	skillLoader := skill.NewLoader(cfg.SkillsDir)
	if strings.TrimSpace(cfg.APIKey) != "" {
		skillLoader.SetFallbackNormalizer(skill.NewLLMNormalizer(client, cfg.Model, min(cfg.MaxTokens, 2048)))
	}
	return skillLoader
}

func newTeamManager(cfg *config.Config, taskMgr *task.Manager, msgBus *message.Bus, client conversation.Caller) *teammate.Manager {
	teamMgr := teammate.NewManager(cfg.WorkspaceDir, cfg.TeamDir, taskMgr, msgBus, cfg.Model, client)
	teamMgr.Configure(teammate.RuntimeConfig{
		TeamName:         cfg.TeamName,
		WorkLoopLimit:    cfg.TeammateWorkLimit,
		IdlePollInterval: cfg.TeammatePollEvery,
		IdleTimeout:      cfg.TeammateIdleFor,
	})
	return teamMgr
}

func (a *Agent) registerToolTo(handler *tools.ToolHandler, tool tools.Tool, meta tools.ToolMeta) {
	handler.RegisterWithMeta(tool, meta)
}

func (a *Agent) registerTool(tool tools.Tool, meta tools.ToolMeta) {
	a.registerToolTo(a.toolHandler, tool, meta)
}

func executionConfigFromRuntime(cfg config.ToolExecutionConfig) tooling.ExecutionConfig {
	return tooling.ExecutionConfig{
		Mode:               cfg.Mode,
		DockerImage:        cfg.DockerImage,
		DockerNetwork:      cfg.DockerNetwork,
		SSHTarget:          cfg.SSHTarget,
		SSHWorkspace:       cfg.SSHWorkspace,
		SSHOptions:         append([]string{}, cfg.SSHOptions...),
		ShellAllowPatterns: append([]string{}, cfg.ShellAllowPatterns...),
		ShellDenyPatterns:  append([]string{}, cfg.ShellDenyPatterns...),
	}
}

// RegisterTools registers available tools.
func (a *Agent) RegisterTools() {
	a.registerToolsWith(a.toolHandler)
}

func (a *Agent) registerToolsWith(handler *tools.ToolHandler) {
	execution := executionConfigFromRuntime(a.cfg.Tools.Execution)
	a.registerToolTo(handler, tools.NewBashToolWithExecution(a.cfg.WorkspaceDir, a.cfg.TempDir, execution), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewGlobTool(a.cfg.WorkspaceDir, a.cfg.Tools.Glob.DefaultMaxResults), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewReadFileTool(a.cfg.WorkspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewWriteFileTool(a.cfg.WorkspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewEditFileTool(a.cfg.WorkspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewAttachFileTool(a.cfg.WorkspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})

	a.registerToolTo(handler, tools.NewTaskCreateTool(a.taskMgr), tools.ToolMeta{
		Bundle:  bundleTaskBoard,
		Summary: "persistent task board operations",
	})
	a.registerToolTo(handler, tools.NewTaskGetTool(a.taskMgr), tools.ToolMeta{
		Bundle:  bundleTaskBoard,
		Summary: "persistent task board operations",
	})
	a.registerToolTo(handler, tools.NewTaskListTool(a.taskMgr), tools.ToolMeta{
		Bundle:  bundleTaskBoard,
		Summary: "persistent task board operations",
	})
	a.registerToolTo(handler, tools.NewTaskUpdateTool(a.taskMgr), tools.ToolMeta{
		Bundle:  bundleTaskBoard,
		Summary: "persistent task board operations",
	})
	a.registerToolTo(handler, tools.NewClaimTaskTool(a.taskMgr), tools.ToolMeta{
		Bundle:  bundleTaskBoard,
		Summary: "persistent task board operations",
	})

	a.registerToolTo(handler, teamtools.NewReadInboxTool(a.msgBus, a.cfg.LeadName), tools.ToolMeta{
		Bundle:  bundleTeam,
		Summary: "teammate inbox, messaging, and approval workflows",
	})
	a.registerToolTo(handler, teamtools.NewSendMessageTool(a.msgBus, a.cfg.LeadName), tools.ToolMeta{
		Bundle:  bundleTeam,
		Summary: "teammate inbox, messaging, and approval workflows",
	})
	a.registerToolTo(handler, teamtools.NewBroadcastTool(a.msgBus, a.teamMgr, a.cfg.LeadName), tools.ToolMeta{
		Bundle:  bundleTeam,
		Summary: "teammate inbox, messaging, and approval workflows",
	})
	a.registerToolTo(handler, teamtools.NewShutdownRequestTool(a.teamMgr), tools.ToolMeta{
		Bundle:  bundleTeam,
		Summary: "teammate inbox, messaging, and approval workflows",
	})
	a.registerToolTo(handler, teamtools.NewListTool(a.teamMgr), tools.ToolMeta{
		Bundle:  bundleTeam,
		Summary: "teammate inbox, messaging, and approval workflows",
	})
	a.registerToolTo(handler, teamtools.NewPlanApprovalTool(a.msgBus, a.teamMgr, a.cfg.LeadName), tools.ToolMeta{
		Bundle:  bundleTeam,
		Summary: "teammate inbox, messaging, and approval workflows",
	})

	a.registerToolTo(handler, tools.NewListSkillsTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewListSkillSourcesTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewInstallSkillTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewListPackagesTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewInstallPackageTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewRemovePackageTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewListPromptsTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewListPackageCommandsTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package command declarations"})
	a.registerToolTo(handler, tools.NewListPackageRolesTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package subagent role declarations"})
	a.registerToolTo(handler, tools.NewLoadSkillTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewExpandSkillTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewUnloadSkillTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewListMemoryTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewGetMemoryTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewSearchMemoryTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewListMemoryCandidatesTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewAcceptMemoryCandidateTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewDismissMemoryCandidateTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewRememberMemoryTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewForgetMemoryTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewCompressTool(a), tools.ToolMeta{AlwaysActive: true})
	if a.historySearch != nil {
		a.registerToolTo(handler, tools.NewHistorySearchTool(a.historySearch), tools.ToolMeta{AlwaysActive: true})
	}
	if a.sessionAdmin != nil {
		a.registerToolTo(handler, tools.NewManageSessionTool(a.sessionAdmin), tools.ToolMeta{AlwaysActive: true})
	}
	a.registerToolTo(handler, tools.NewToolExchangeTool(handler), tools.ToolMeta{AlwaysActive: true})
	if a.cron != nil {
		a.registerToolTo(handler, tools.NewCronTool(a.cron), tools.ToolMeta{AlwaysActive: true})
	}
	if a.heartbeat != nil {
		a.registerToolTo(handler, tools.NewHeartbeatTool(a.heartbeat), tools.ToolMeta{AlwaysActive: true})
	}

	if a.webSearch != nil && a.cfg.Tools.WebSearch.Enabled {
		a.registerToolTo(handler, tools.NewWebSearchTool(a.webSearch), tools.ToolMeta{
			Bundle:  bundleWeb,
			Summary: "current information lookup and page fetching",
		})
	}
	if a.webFetch != nil && a.cfg.Tools.WebFetch.Enabled {
		a.registerToolTo(handler, tools.NewWebFetchTool(a.webFetch), tools.ToolMeta{
			Bundle:  bundleWeb,
			Summary: "current information lookup and page fetching",
		})
	}
	if a.browser != nil && a.cfg.Tools.Browser.Enabled {
		a.registerToolTo(handler, tools.NewBrowserTool(a.browser, a.cfg.WorkspaceDir), tools.ToolMeta{
			Bundle:  bundleBrowser,
			Summary: "interactive browser automation for dynamic pages",
		})
	}
	a.registerToolTo(handler, tools.NewDesktopTool(tools.NewDesktopService(a.cfg.TempDir)), tools.ToolMeta{
		Bundle:  bundleDesktop,
		Summary: "local desktop screenshots, clipboard, keyboard, mouse, and window inspection",
	})

	a.registerToolTo(handler, tools.NewBackgroundRunToolWithExecution(a.bgMgr, a.cfg.WorkspaceDir, a.cfg.TempDir, execution), tools.ToolMeta{
		Bundle:  bundleBackground,
		Summary: "long-running command execution and status checks",
	})
	a.registerToolTo(handler, tools.NewCheckBackgroundTool(a.bgMgr), tools.ToolMeta{
		Bundle:  bundleBackground,
		Summary: "long-running command execution and status checks",
	})
	a.registerToolTo(handler, newWorkflowTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "isolated delegated exploration or implementation work",
	})
	a.registerToolTo(handler, newLongTaskTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "Ralph-style prioritized long task orchestration over durable workflow nodes",
	})
	a.registerSubagentTool(handler)
	a.registerToolTo(handler, tools.NewACPAgentTool(a.cfg.ACP.Agents, a.cfg.WorkspaceDir), tools.ToolMeta{
		Bundle:  bundleExternal,
		Summary: "external ACP agent delegation over stdio",
	})
	a.registerToolTo(handler, tools.NewListMCPResourcesTool(a.mcpMgr), tools.ToolMeta{
		Bundle:  bundleMCP,
		Summary: "configured MCP resource servers",
	})
	a.registerToolTo(handler, tools.NewReadMCPResourceTool(a.mcpMgr), tools.ToolMeta{
		Bundle:  bundleMCP,
		Summary: "configured MCP resource servers",
	})
	a.registerToolTo(handler, tools.NewTodoWriteTool(a.todoMgr), tools.ToolMeta{
		Bundle:        bundlePlanning,
		Summary:       "lightweight todo planning and progress tracking",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewTodoListTool(a.todoMgr), tools.ToolMeta{
		Bundle:        bundlePlanning,
		Summary:       "lightweight todo planning and progress tracking",
		DefaultActive: true,
	})
	handler.ActivateDefaults()
}

// AddMessage adds a user message.
func (a *Agent) AddMessage(content string) {
	a.AddEnvelope(message.NewCLIEnvelope("repl", a.cfg.LeadName, content, a.now()))
}

// AddEnvelope adds a generic user-facing runtime envelope into the conversation.
func (a *Agent) AddEnvelope(envelope message.Envelope) {
	a.appendMessage(envelope.ToProtocolMessage(protocol.RoleUser, "", false))
}

// AppendRuntimeFeedback appends model-visible runtime guidance without treating
// it as a new user-submitted message in the UI timeline.
func (a *Agent) AppendRuntimeFeedback(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.appendMessage(protocol.NewEphemeralTextMessage(protocol.KindBackground, text))
}

func (a *Agent) handleTool(ctx context.Context, name string, input map[string]interface{}) (string, error) {
	result, err := a.handleToolResult(ctx, name, input)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

func (a *Agent) handleToolResult(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
	result, err := a.toolHandler.HandleResult(ctx, name, input)
	if err != nil {
		execution := conversation.ToolExecutionResult{
			ArtifactPaths: append([]string{}, result.ArtifactPaths...),
		}
		if toolResultHasModelOutput(result) {
			output, outputErr := result.OutputString()
			if outputErr != nil {
				return conversation.ToolExecutionResult{}, outputErr
			}
			execution.Output = output
		}
		return execution, err
	}
	if name == "history_search" {
		if state := historyRecallTurnStateFromContext(ctx); state != nil {
			state.consumeAutomaticExposure()
		}
	}
	output, err := result.OutputString()
	if err != nil {
		return conversation.ToolExecutionResult{}, err
	}
	return conversation.ToolExecutionResult{
		Output:        output,
		ArtifactPaths: append([]string{}, result.ArtifactPaths...),
	}, nil
}

// RunPackageSmokeCommand executes one explicitly requested package smoke command
// through the normal tool handler and permission chain.
func (a *Agent) RunPackageSmokeCommand(ctx context.Context, runtimeCtx automation.SessionContext, command string) (tools.ToolResult, error) {
	ctx = tools.WithSessionContext(ctx, runtimeCtx)
	return a.toolHandler.HandleResult(ctx, "bash", map[string]interface{}{"command": command})
}

func toolResultHasModelOutput(result tools.ToolResult) bool {
	return result.Text != "" || result.Structured != nil
}

// LoadSkill loads a skill into system prompt.
func (a *Agent) LoadSkill(name string) error {
	_, err := a.ActivateSkill(name)
	return err
}

// ActivateSkill loads the skill core into session prompt state if it is not already active.
func (a *Agent) ActivateSkill(name string) (tools.SkillActivation, error) {
	skillDef, err := a.skillLoader.Load(name)
	if err != nil {
		return tools.SkillActivation{}, err
	}
	entry := a.resolveSkillEntry(a.skillLoader.CatalogEntryFor(skillDef))
	skillID := entry.ID
	entry = a.catalogEntryWithSuiteMetadata(skillID, entry)

	a.mu.Lock()
	defer a.mu.Unlock()
	if state, ok := a.activeSkills[skillID]; ok {
		return skillActivationResult(state, "already_active"), nil
	}
	a.activeSkills[skillID] = &activeSkillState{
		catalog:  entry,
		core:     skillDef.Core,
		expanded: make(map[string]string),
	}
	return skillActivationResult(a.activeSkills[skillID], "activated"), nil
}

// InstallSkill installs a new skill source into the workspace skills directory.
func (a *Agent) InstallSkill(source, name string) (tools.SkillInstallResult, error) {
	result, err := a.skillLoader.Install(source, name)
	if err != nil {
		return tools.SkillInstallResult{}, err
	}
	return tools.SkillInstallResult{
		ID:                 result.ID,
		Name:               result.Name,
		Status:             result.Status,
		Source:             result.Source,
		SourceOrigin:       result.SourceOrigin,
		Trust:              result.Trust,
		Version:            result.Version,
		Categories:         append([]string{}, result.Categories...),
		InstalledPath:      result.InstalledPath,
		Description:        result.Description,
		Sections:           append([]string{}, result.Sections...),
		RecommendedBundles: append([]string{}, result.RecommendedBundles...),
		Compatibility:      result.Compatibility,
		Warnings:           append([]string{}, result.Warnings...),
		InstallMemory:      cloneSkillInstallMemory(result.InstallMemory),
	}, nil
}

// NormalizeSkill explicitly enriches one skill with the configured LLM normalizer.
func (a *Agent) NormalizeSkill(ctx context.Context, name string) (skill.CatalogEntry, error) {
	skillDef, err := a.skillLoader.NormalizeSkill(ctx, name)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	return a.resolveSkillEntry(a.skillLoader.CatalogEntryFor(skillDef)), nil
}

// RemoveSkill deletes an installed skill and removes it from the active session stack.
func (a *Agent) RemoveSkill(name string) (tools.SkillRemoveResult, error) {
	result, err := a.skillLoader.Remove(name)
	if err != nil {
		return tools.SkillRemoveResult{}, err
	}

	wasActive := false
	a.mu.Lock()
	if skillID := a.findActiveSkillKeyLocked(result.ID); skillID != "" {
		if _, ok := a.activeSkills[skillID]; ok {
			delete(a.activeSkills, skillID)
			wasActive = true
		}
	}
	a.mu.Unlock()

	return tools.SkillRemoveResult{
		ID:          result.ID,
		Name:        result.Name,
		Status:      result.Status,
		RemovedPath: result.RemovedPath,
		WasActive:   wasActive,
	}, nil
}

// ExpandSkill loads additional named sections for an already active skill.
func (a *Agent) ExpandSkill(name string, sections []string) (tools.SkillExpansion, error) {
	a.mu.Lock()
	skillID := a.findActiveSkillKeyLocked(name)
	state, ok := a.activeSkills[skillID]
	a.mu.Unlock()
	if !ok {
		return tools.SkillExpansion{}, skillNotActiveError(name)
	}

	resolved, err := a.skillLoader.GetSections(skillID, sections)
	if err != nil {
		return tools.SkillExpansion{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	state = a.activeSkills[skillID]
	if state == nil {
		return tools.SkillExpansion{}, skillNotActiveError(name)
	}

	expandedNow := make([]string, 0, len(resolved))
	for _, section := range resolved {
		if _, ok := state.expanded[section.Name]; ok {
			continue
		}
		state.expanded[section.Name] = section.Content
		state.expandedOrder = append(state.expandedOrder, section.Name)
		expandedNow = append(expandedNow, section.Name)
	}

	status := "already_loaded"
	if len(expandedNow) > 0 {
		status = "expanded"
	}
	return tools.SkillExpansion{
		ID:                 state.catalog.ID,
		Name:               state.catalog.Name,
		Status:             status,
		ExpandedSections:   expandedNow,
		LoadedSections:     state.loadedSections(),
		AvailableSections:  append([]string{}, state.catalog.Sections...),
		RecommendedBundles: append([]string{}, state.catalog.RecommendedBundles...),
		Compatibility:      state.catalog.Compatibility,
	}, nil
}

// ListSkills returns the discoverable skill catalog for the current workspace.
func (a *Agent) ListSkills() ([]skill.CatalogEntry, error) {
	items, err := a.skillLoader.Catalog(a.cfg.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	resolved := make([]skill.CatalogEntry, 0, len(items))
	for _, item := range items {
		resolved = append(resolved, a.resolveSkillEntry(item))
	}
	decorateSkillSuiteMetadata(resolved)
	return resolved, nil
}

// ListSkillSources returns curated install sources with current installed-state decoration.
func (a *Agent) ListSkillSources() ([]tools.SkillSourceEntry, error) {
	return a.listSkillSources("")
}

// SearchSkillSources returns curated install sources plus skills.sh matches for the query.
func (a *Agent) SearchSkillSources(query string) ([]tools.SkillSourceEntry, error) {
	return a.listSkillSources(query)
}

func (a *Agent) listSkillSources(query string) ([]tools.SkillSourceEntry, error) {
	items, err := skill.SourceCatalog(a.cfg.WorkspaceDir, a.cfg.SkillsDir)
	if strings.TrimSpace(query) != "" {
		items, err = skill.SearchSourceCatalog(a.cfg.WorkspaceDir, a.cfg.SkillsDir, query)
	}
	if err != nil {
		return nil, err
	}
	result := make([]tools.SkillSourceEntry, 0, len(items))
	for _, item := range items {
		result = append(result, tools.SkillSourceEntry{
			ID:               item.ID,
			Name:             item.Name,
			Summary:          item.Summary,
			Source:           item.Source,
			SkillName:        item.SkillName,
			Tags:             append([]string{}, item.Tags...),
			Categories:       append([]string{}, item.Categories...),
			Version:          item.Version,
			Trust:            item.Trust,
			Origin:           item.Origin,
			Installs:         item.Installs,
			Warnings:         append([]string{}, item.Warnings...),
			InstallSupported: item.InstallSupported,
			InstallSource:    item.InstallSource,
			InstallName:      item.InstallName,
			InstallReason:    item.InstallReason,
			Installed:        item.Installed,
			InstalledPath:    item.InstalledPath,
			InstallMemory:    cloneSkillInstallMemory(item.InstallMemory),
		})
	}
	return result, nil
}

// ListPackages lists installed Godex packages.
func (a *Agent) ListPackages() ([]tools.PackageEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).List()
	if err != nil {
		return nil, err
	}
	return packageEntriesFromRegistry(items), nil
}

// InstallPackage installs one declaration-only Godex package.
func (a *Agent) InstallPackage(source string) (tools.PackageEntry, error) {
	item, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).Install(source)
	if err != nil {
		return tools.PackageEntry{}, err
	}
	return packageEntryFromRegistry(item), nil
}

// RemovePackage removes one installed Godex package.
func (a *Agent) RemovePackage(name string) (tools.PackageEntry, error) {
	item, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).Remove(name)
	if err != nil {
		return tools.PackageEntry{}, err
	}
	return packageEntryFromRegistry(item), nil
}

// ListPrompts lists prompt templates installed through packages.
func (a *Agent) ListPrompts(includeContent bool) ([]tools.PromptEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).ListPrompts(includeContent)
	if err != nil {
		return nil, err
	}
	out := make([]tools.PromptEntry, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PromptEntry{
			PackageName: item.PackageName,
			Name:        item.Name,
			Path:        item.Path,
			Content:     item.Content,
		})
	}
	return out, nil
}

// ListPackageCommands lists slash-command workflow declarations installed through packages.
func (a *Agent) ListPackageCommands(includeContent bool) ([]tools.PackageCommandEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).ListCommands(includeContent)
	if err != nil {
		return nil, err
	}
	out := make([]tools.PackageCommandEntry, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PackageCommandEntry{
			PackageName:        item.PackageName,
			Name:               item.Name,
			Namespace:          item.Namespace,
			Description:        item.Description,
			Mode:               item.Mode,
			PromptPath:         item.PromptPath,
			Prompt:             item.Prompt,
			Aliases:            append([]string{}, item.Aliases...),
			Roles:              append([]string{}, item.Roles...),
			WriteScope:         append([]string{}, item.WriteScope...),
			Permissions:        append([]string{}, item.Permissions...),
			Capabilities:       append([]string{}, item.Capabilities...),
			ToolPolicy:         append([]string{}, item.ToolPolicy...),
			RecommendedBundles: append([]string{}, item.RecommendedBundles...),
			Path:               item.Path,
		})
	}
	return out, nil
}

// ListPackageRoles lists named subagent roles installed through packages.
func (a *Agent) ListPackageRoles(includeContent bool) ([]tools.PackageRoleEntry, error) {
	items, err := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir).ListRoles(includeContent)
	if err != nil {
		return nil, err
	}
	out := make([]tools.PackageRoleEntry, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PackageRoleEntry{
			PackageName:    item.PackageName,
			ID:             item.ID,
			Name:           item.Name,
			Description:    item.Description,
			BasePrompt:     item.BasePrompt,
			DefaultBundles: append([]string{}, item.DefaultBundles...),
			Tools:          append([]string{}, item.Tools...),
			WriteEnabled:   item.WriteEnabled,
			Capabilities:   append([]string{}, item.Capabilities...),
			ToolPolicy:     append([]string{}, item.ToolPolicy...),
			ModelHint:      item.ModelHint,
			BudgetHint:     item.BudgetHint,
			Display:        roleDisplayMap(item.Display),
			Path:           item.Path,
		})
	}
	return out, nil
}

func roleDisplayMap(display pkgregistry.Display) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(display.Label) != "" {
		out["label"] = strings.TrimSpace(display.Label)
	}
	if strings.TrimSpace(display.Color) != "" {
		out["color"] = strings.TrimSpace(display.Color)
	}
	if strings.TrimSpace(display.Icon) != "" {
		out["icon"] = strings.TrimSpace(display.Icon)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func packageEntriesFromRegistry(items []pkgregistry.Entry) []tools.PackageEntry {
	out := make([]tools.PackageEntry, 0, len(items))
	for _, item := range items {
		out = append(out, packageEntryFromRegistry(item))
	}
	return out
}

func packageEntryFromRegistry(item pkgregistry.Entry) tools.PackageEntry {
	return tools.PackageEntry{
		Name:               item.Name,
		Version:            item.Version,
		Description:        item.Description,
		Source:             item.Source,
		Digest:             item.Digest,
		Path:               item.Path,
		InstalledAt:        item.InstalledAt.Format(time.RFC3339),
		Resources:          packageResourceMap(item.Resources),
		App:                packageAppFromRegistry(item.App),
		Permissions:        append([]string{}, item.Permissions...),
		Capabilities:       append([]string{}, item.Capabilities...),
		ToolPolicy:         append([]string{}, item.ToolPolicy...),
		SmokeTests:         packageSmokeTests(item.SmokeTests),
		RecommendedBundles: append([]string{}, item.RecommendedBundles...),
		Trust:              item.Trust,
	}
}

func packageAppFromRegistry(item pkgregistry.AppManifest) tools.PackageAppEntry {
	item = pkgregistry.NormalizeAppManifest(item)
	if pkgregistry.AppManifestEmpty(item) {
		return tools.PackageAppEntry{}
	}
	config := make(map[string]any, len(item.Config))
	for key, value := range item.Config {
		config[key] = value
	}
	return tools.PackageAppEntry{
		Kind:   item.Kind,
		ID:     item.ID,
		Label:  item.Label,
		Config: config,
	}
}

func packageSmokeTests(items []pkgregistry.SmokeTest) []tools.PackageSmokeTest {
	out := make([]tools.PackageSmokeTest, 0, len(items))
	for _, item := range items {
		out = append(out, tools.PackageSmokeTest{
			Name:                item.Name,
			Command:             item.Command,
			WorkingDir:          item.WorkingDir,
			TimeoutSeconds:      item.TimeoutSeconds,
			RequiredPermissions: append([]string{}, item.RequiredPermissions...),
			ExpectedExitCode:    item.ExpectedExitCode,
		})
	}
	return out
}

func packageResourceMap(resources pkgregistry.Resources) map[string][]string {
	out := map[string][]string{}
	if len(resources.Skills) > 0 {
		out["skills"] = append([]string{}, resources.Skills...)
	}
	if len(resources.Prompts) > 0 {
		out["prompts"] = append([]string{}, resources.Prompts...)
	}
	if len(resources.Commands) > 0 {
		out["commands"] = append([]string{}, resources.Commands...)
	}
	if len(resources.Roles) > 0 {
		out["roles"] = append([]string{}, resources.Roles...)
	}
	if len(resources.Docs) > 0 {
		out["docs"] = append([]string{}, resources.Docs...)
	}
	if len(resources.Assets) > 0 {
		out["assets"] = append([]string{}, resources.Assets...)
	}
	return out
}

// GetSkill returns one discoverable skill's lightweight metadata.
func (a *Agent) GetSkill(name string) (skill.CatalogEntry, error) {
	if items, err := a.ListSkills(); err == nil {
		for _, item := range items {
			if strings.EqualFold(item.ID, name) || strings.EqualFold(item.Name, name) {
				return item, nil
			}
		}
	}
	skillDef, err := a.skillLoader.Load(name)
	if err != nil {
		return skill.CatalogEntry{}, err
	}
	entry := a.resolveSkillEntry(a.skillLoader.CatalogEntryFor(skillDef))
	decorateSkillSuiteMetadata([]skill.CatalogEntry{entry})
	return entry, nil
}

func (a *Agent) catalogEntryWithSuiteMetadata(id string, fallback skill.CatalogEntry) skill.CatalogEntry {
	items, err := a.ListSkills()
	if err != nil {
		decorateSkillSuiteMetadata([]skill.CatalogEntry{fallback})
		return fallback
	}
	for _, item := range items {
		if strings.EqualFold(item.ID, id) || strings.EqualFold(item.Name, id) {
			return item
		}
	}
	decorateSkillSuiteMetadata([]skill.CatalogEntry{fallback})
	return fallback
}

// ActiveSkills returns detailed state for currently activated skills.
func (a *Agent) ActiveSkills() ([]tools.SkillActivation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.activeSkills))
	for name := range a.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]tools.SkillActivation, 0, len(names))
	for _, name := range names {
		state := a.activeSkills[name]
		if state == nil {
			continue
		}
		items = append(items, skillActivationResult(state, "active"))
	}
	return items, nil
}

// UnloadSkill removes an active skill from the session prompt state.
func (a *Agent) UnloadSkill(name string) (tools.SkillActivation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	skillID := a.findActiveSkillKeyLocked(name)
	state, ok := a.activeSkills[skillID]
	if !ok || state == nil {
		return tools.SkillActivation{}, skillNotActiveError(name)
	}
	result := skillActivationResult(state, "unloaded")
	result.LoadedSections = nil
	delete(a.activeSkills, skillID)
	return result, nil
}

// GetMessages returns current messages.
func (a *Agent) GetMessages() []protocol.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.CloneMessages(a.messages)
}

// TranscriptRefs returns persisted transcript archive references for the session.
func (a *Agent) TranscriptRefs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.transcriptRefs...)
}

// HistorySearchRuntime exposes the session-bound history search runtime.
func (a *Agent) HistorySearchRuntime() tools.HistorySearchRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.historySearch
}

// ClearMessages clears conversation prompt state while keeping durable session
// records such as timeline, turns, permissions, memory, and tasks intact.
func (a *Agent) ClearMessages() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = nil
	a.transcriptRefs = nil
	a.pendingResume = nil
	a.toolHandler.ResetActiveToolsToDefaults()
	a.historyVersion++
	a.lastCompactedVersion = 0
}

// TruncateMessages resets the transcript to the first count messages.
func (a *Agent) TruncateMessages(count int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case count <= 0:
		a.messages = nil
	case count >= len(a.messages):
		return
	default:
		a.messages = protocol.CloneMessages(a.messages[:count])
	}
	a.historyVersion++
}

// InspectContext summarizes the current prompt budget without mutating history.
func (a *Agent) InspectContext(ctx context.Context, sessionID string) (tools.ContextInspection, error) {
	system, err := a.buildRuntimeSystemPrompt(agentProfileFromContext(ctx))
	if err != nil {
		return tools.ContextInspection{}, err
	}
	history, _ := a.messageState()
	history = dedupeRepeatedLargeToolResultSummaries(history)
	memoryMessages, _, err := a.collectMemoryMessages(history)
	if err != nil {
		return tools.ContextInspection{}, err
	}
	agentProfile := agentProfileFromContext(ctx)
	promptStateSections, err := a.buildDynamicRuntimePromptSections(agentProfile)
	if err != nil {
		return tools.ContextInspection{}, err
	}
	promptStateMessages := runtimePromptMessages(promptStateSections)
	runtimeMessages, _ := a.collectRuntimeMessages()
	allRuntimeMessages := append(protocol.CloneMessages(promptStateMessages), runtimeMessages...)

	toolSchemas := a.toolHandler.ActiveSchemas()
	estimate := estimateContextBudget(system, history, memoryMessages, allRuntimeMessages, toolSchemas, a.cfg.CompressThreshold)
	pendingCount := 0
	if a.permissions != nil && strings.TrimSpace(sessionID) != "" {
		pendingCount = len(a.permissions.ListPending(sessionID))
	}
	return tools.ContextInspection{
		SessionID:                     strings.TrimSpace(sessionID),
		MessageCount:                  len(history),
		TokenEstimate:                 estimate.Breakdown.Total,
		HistoryTokenEstimate:          estimate.Breakdown.History,
		TotalTokenEstimate:            estimate.Breakdown.Total,
		TokenBreakdown:                estimate.Breakdown,
		PrefixCache:                   prefixCacheInspection(system, toolSchemas, history, promptStateSections, promptStateMessages),
		CompressThreshold:             a.cfg.CompressThreshold,
		SuggestCompact:                len(estimate.Reasons) > 0,
		CompressionReasons:            append([]string{}, estimate.Reasons...),
		ActiveSkillCount:              len(a.ActiveSkillNames()),
		PendingPermissionCount:        pendingCount,
		LargeToolResultReferenceCount: estimate.LargeToolResultReferenceCount,
		ToolResultReferences:          append([]tools.ToolResultReference{}, estimate.ToolResultReferences...),
	}, nil
}

// CompactConversation manually compacts the persistent conversation history.
func (a *Agent) CompactConversation() (string, error) {
	system, err := a.buildRuntimeSystemPrompt()
	if err != nil {
		return "", err
	}
	history, _ := a.messageState()
	if len(history) == 0 {
		return "No messages to compress", nil
	}

	result, err := a.summarizer.SummarizeSession(context.Background(), compress.SessionSummaryRequest{
		System:               system,
		History:              protocol.CloneMessages(history),
		RecentUserMessages:   recentPersistentUserMessages(history, 6),
		ContinuationSnapshot: a.continuationSnapshot("", history),
	})
	if err != nil {
		return "", err
	}
	compacted := result.Messages

	a.storeCompactedMessages(compacted)

	summary := "Conversation compressed."
	if len(compacted) > 0 {
		if text := protocol.MessageText(compacted[0]); text != "" {
			summary = text
		}
	}
	return summary, nil
}

// SetIdle sets the idle state.
func (a *Agent) SetIdle(idle bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.idleRequested = idle
}

// TaskMgr returns the task manager.
func (a *Agent) TaskMgr() *task.Manager {
	return a.taskMgr
}

// TeamMgr returns the team manager.
func (a *Agent) TeamMgr() *teammate.Manager {
	return a.teamMgr
}

// ToolCatalog returns the current tool bundle/catalog state.
func (a *Agent) ToolCatalog() tools.ToolCatalog {
	return a.toolHandler.Catalog()
}

// PendingPermissions returns session-scoped pending permission approvals.
func (a *Agent) PendingPermissions(sessionID string) []tools.PendingPermission {
	if a.permissions == nil {
		return nil
	}
	return a.permissions.ListPending(sessionID)
}

func (a *Agent) pendingResumeState() *PendingResumeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return clonePendingResumeState(a.pendingResume)
}

// PendingResumeState returns the currently blocked user turn, if any.
func (a *Agent) PendingResumeState() *PendingResumeState {
	return a.pendingResumeState()
}

// SetPendingResume stores one blocked user turn for replay after approval.
func (a *Agent) SetPendingResume(requestID string, priorMessageCount int, envelope message.Envelope, runtimeCtx automation.SessionContext, injections ...message.Envelope) {
	a.mu.Lock()
	defer a.mu.Unlock()
	normalizedInjections := make([]message.Envelope, 0, len(injections))
	for _, item := range injections {
		normalizedInjections = append(normalizedInjections, item.Normalized())
	}
	a.pendingResume = &PendingResumeState{
		RequestID:         strings.TrimSpace(requestID),
		PriorMessageCount: priorMessageCount,
		Envelope:          envelope.Normalized(),
		Injections:        normalizedInjections,
		RuntimeContext:    runtimeCtx.Clone(),
	}
}

// ClearPendingResume discards any blocked-turn replay state.
func (a *Agent) ClearPendingResume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingResume = nil
}

// ApprovePendingPermission resolves a pending permission request.
func (a *Agent) ApprovePendingPermission(sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error) {
	if a.permissions == nil {
		return tools.PermissionResolution{}, fmt.Errorf("permission manager unavailable")
	}
	return a.permissions.ApprovePending(sessionID, requestID, scope)
}

// DenyPendingPermission resolves a pending permission request with denial.
func (a *Agent) DenyPendingPermission(sessionID, requestID, reason string) (tools.PermissionResolution, error) {
	if a.permissions == nil {
		return tools.PermissionResolution{}, fmt.Errorf("permission manager unavailable")
	}
	return a.permissions.DenyPending(sessionID, requestID, reason)
}

// ActiveSkillNames returns the currently activated skill names.
func (a *Agent) ActiveSkillNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	names := make([]string, 0, len(a.activeSkills))
	for name := range a.activeSkills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MsgBus returns the message bus.
func (a *Agent) MsgBus() *message.Bus {
	return a.msgBus
}

// TodoMgr returns the todo manager.
func (a *Agent) TodoMgr() *todo.Manager {
	return a.todoMgr
}

// MemoryMgr returns the durable memory manager.
func (a *Agent) MemoryMgr() *memory.Manager {
	return a.memoryMgr
}

// CurrentModel returns the currently configured model for this session runtime.
func (a *Agent) CurrentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.cfg.Model)
}

func (a *Agent) appendMessage(msg protocol.Message) {
	if len(msg.Content) == 0 {
		return
	}
	if msg.Metadata == nil {
		msg.Metadata = &protocol.Metadata{}
	}
	if strings.TrimSpace(msg.Metadata.Timestamp) == "" {
		msg.Metadata.Timestamp = a.now().UTC().Format(time.RFC3339Nano)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, msg.Clone())
	a.historyVersion++
}

// AppendAssistantText appends one assistant-visible text reply into the session transcript.
func (a *Agent) AppendAssistantText(text string, kind protocol.MessageKind) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	msg := protocol.NewTextMessage(protocol.RoleAssistant, text)
	if kind != "" || !a.now().IsZero() {
		msg.Metadata = &protocol.Metadata{Kind: kind, Timestamp: a.now().UTC().Format(time.RFC3339Nano)}
	}
	a.appendMessage(msg)
}

// AppendAssistantDelivery appends one assistant-visible message with optional
// attachment metadata into the session transcript.
func (a *Agent) AppendAssistantDelivery(text string, kind protocol.MessageKind, attachments []message.AttachmentRef) {
	msg := protocol.NewTextMessage(protocol.RoleAssistant, strings.TrimSpace(text))
	if kind != "" || len(attachments) > 0 || !a.now().IsZero() {
		msg.Metadata = &protocol.Metadata{Kind: kind, Timestamp: a.now().UTC().Format(time.RFC3339Nano)}
	}
	if len(attachments) > 0 {
		if msg.Metadata == nil {
			msg.Metadata = &protocol.Metadata{}
		}
		msg.Metadata.Attachments = make([]protocol.Attachment, 0, len(attachments))
		for _, attachment := range attachments {
			msg.Metadata.Attachments = append(msg.Metadata.Attachments, protocol.Attachment{
				ID:        attachment.ID,
				Name:      attachment.Name,
				MIMEType:  attachment.MIMEType,
				Path:      attachment.Path,
				URL:       attachment.URL,
				SizeBytes: attachment.SizeBytes,
			})
		}
	}
	a.appendMessage(msg)
}

func skillActivationResult(state *activeSkillState, status string) tools.SkillActivation {
	return tools.SkillActivation{
		ID:                 state.catalog.ID,
		Name:               state.catalog.Name,
		Status:             status,
		Description:        state.catalog.Description,
		LoadedSections:     state.loadedSections(),
		AvailableSections:  append([]string{}, state.catalog.Sections...),
		RecommendedBundles: append([]string{}, state.catalog.RecommendedBundles...),
		Compatibility:      state.catalog.Compatibility,
		SkillKind:          state.catalog.SkillKind,
		SuiteID:            state.catalog.SuiteID,
		ChildSkillCount:    state.catalog.ChildSkillCount,
		ChildSkillIDs:      append([]string{}, state.catalog.ChildSkillIDs...),
		ChildSkillHint:     state.catalog.ChildSkillHint,
	}
}

const maxCatalogChildSkillIDs = 80

func decorateSkillSuiteMetadata(items []skill.CatalogEntry) {
	childrenBySuite := make(map[string][]string)
	for _, item := range items {
		suiteID, ok := splitSuiteSkillID(item.ID)
		if !ok {
			continue
		}
		childrenBySuite[suiteID] = append(childrenBySuite[suiteID], item.ID)
	}
	for suiteID := range childrenBySuite {
		sort.Strings(childrenBySuite[suiteID])
	}

	for i := range items {
		suiteID, isChild := splitSuiteSkillID(items[i].ID)
		if isChild {
			items[i].SkillKind = "child_skill"
			items[i].SuiteID = suiteID
			continue
		}
		children := childrenBySuite[items[i].ID]
		if len(children) == 0 {
			items[i].SkillKind = "root_skill"
			continue
		}
		items[i].SkillKind = "suite_root"
		items[i].ChildSkillCount = len(children)
		items[i].ChildSkillIDs = append([]string{}, children...)
		if len(items[i].ChildSkillIDs) > maxCatalogChildSkillIDs {
			items[i].ChildSkillIDs = items[i].ChildSkillIDs[:maxCatalogChildSkillIDs]
		}
		items[i].ChildSkillHint = fmt.Sprintf("Use list_skills with suite=%q and offset/limit to inspect child details, then load_skill with an exact child id.", items[i].ID)
	}
}

func splitSuiteSkillID(id string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(id), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[0], true
}

func (a *Agent) findActiveSkillKeyLocked(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if _, ok := a.activeSkills[name]; ok {
		return name
	}
	for skillID, state := range a.activeSkills {
		if state == nil {
			continue
		}
		if strings.EqualFold(skillID, name) || strings.EqualFold(state.catalog.ID, name) || strings.EqualFold(state.catalog.Name, name) {
			return skillID
		}
	}
	return name
}

func skillNotActiveError(name string) error {
	return fmt.Errorf("%w: skill %q is not active", skill.ErrSkillConflict, name)
}

func (a *Agent) resolveSkillEntry(entry skill.CatalogEntry) skill.CatalogEntry {
	if a.mcpMgr.HasConfiguredServers() && stringutil.Contains(entry.Compatibility.MissingCapabilities, "mcp") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "mcp")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Configured MCP servers are available via the mcp bundle.")
	}
	if entry.Requires.NamedSubagents && stringutil.Contains(entry.Compatibility.MissingCapabilities, "named_subagents") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "named_subagents")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Named subagent roles are accepted by the durable subagent runtime and preserved in timeline events.")
	}
	if entry.Requires.SlashCommandRuntime && stringutil.Contains(entry.Compatibility.MissingCapabilities, "slash_command_runtime") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "slash_command_runtime")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "GoDex provides a native slash-command runtime; third-party command names should be declared through package command specs.")
	}
	if entry.Requires.ContextFork && stringutil.Contains(entry.Compatibility.MissingCapabilities, "context_fork") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "context_fork")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Fork-context skills can be adapted through the existing subagent runtime.")
	}
	if entry.Requires.Hooks && supportsHookWhitelist(entry.Requires.HookNames) && stringutil.Contains(entry.Compatibility.MissingCapabilities, "hooks") {
		entry.Compatibility.MissingCapabilities = stringutil.Remove(entry.Compatibility.MissingCapabilities, "hooks")
		entry.Compatibility.Notes = stringutil.AppendUnique(entry.Compatibility.Notes, "Whitelisted hook hints are recognized as advisory runtime guidance.")
	}
	if len(entry.Compatibility.MissingCapabilities) == 0 && entry.Compatibility.Status == skill.CompatibilityDegradedSupported {
		entry.Compatibility.Status = skill.CompatibilityNativeSupported
	}
	return entry
}

func supportsHookWhitelist(hookNames []string) bool {
	if len(hookNames) == 0 {
		return false
	}
	allowed := map[string]struct{}{
		"on_start":    {},
		"on_complete": {},
		"on_error":    {},
	}
	for _, hookName := range hookNames {
		if _, ok := allowed[hookName]; !ok {
			return false
		}
	}
	return true
}

func cloneSkillInstallMemory(memory *skill.InstallMemory) *skill.InstallMemory {
	if memory == nil {
		return nil
	}
	return &skill.InstallMemory{
		Source:        memory.Source,
		SourceEntryID: memory.SourceEntryID,
		SourceOrigin:  memory.SourceOrigin,
		Trust:         memory.Trust,
		Version:       memory.Version,
		Categories:    append([]string{}, memory.Categories...),
		InstalledAt:   memory.InstalledAt,
	}
}

func (a *Agent) registerSubagentTool(handler *tools.ToolHandler) {
	a.registerToolTo(handler, newSubagentTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "isolated delegated exploration or implementation work",
	})
}

type subagentArgs struct {
	Action          string              `json:"action,omitempty"`
	JobID           string              `json:"job_id,omitempty"`
	JobIDs          []string            `json:"job_ids,omitempty"`
	Prompt          string              `json:"prompt,omitempty"`
	AgentType       string              `json:"agent_type,omitempty"`
	Mode            string              `json:"mode,omitempty"`
	WriteScope      []string            `json:"write_scope,omitempty"`
	RequiredBundles []string            `json:"required_bundles,omitempty"`
	RequiredTools   []string            `json:"required_tools,omitempty"`
	Limit           int                 `json:"limit,omitempty"`
	TimeoutMS       int                 `json:"timeout_ms,omitempty"`
	JobTimeoutMS    int                 `json:"job_timeout_ms,omitempty"`
	MaxTurns        int                 `json:"max_turns,omitempty"`
	Wait            bool                `json:"wait,omitempty"`
	Tasks           []subagentBatchItem `json:"tasks,omitempty"`
}

type subagentBatchItem struct {
	Prompt          string   `json:"prompt,omitempty"`
	AgentType       string   `json:"agent_type,omitempty"`
	WriteScope      []string `json:"write_scope,omitempty"`
	RequiredBundles []string `json:"required_bundles,omitempty"`
	RequiredTools   []string `json:"required_tools,omitempty"`
	JobTimeoutMS    int      `json:"job_timeout_ms,omitempty"`
	MaxTurns        int      `json:"max_turns,omitempty"`
}

type subagentLogsView struct {
	JobID    string                        `json:"job_id"`
	Status   string                        `json:"status,omitempty"`
	Count    int                           `json:"count"`
	Total    int                           `json:"total"`
	Progress []DurableSubagentProgressView `json:"progress"`
}

type subagentModelJobView struct {
	JobID         string    `json:"job_id"`
	SessionID     string    `json:"session_id,omitempty"`
	ParentTurnID  string    `json:"parent_turn_id,omitempty"`
	IdentityID    string    `json:"identity_id,omitempty"`
	AgentType     string    `json:"agent_type,omitempty"`
	RoleID        string    `json:"role_id,omitempty"`
	RoleName      string    `json:"role_name,omitempty"`
	Status        string    `json:"status,omitempty"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	MergeStatus   string    `json:"merge_status,omitempty"`
	LastPhase     string    `json:"last_phase,omitempty"`
	LastMessage   string    `json:"last_message,omitempty"`
	LastToolName  string    `json:"last_tool_name,omitempty"`
	ProgressCount int       `json:"progress_count"`
	ResultPreview string    `json:"result_preview,omitempty"`
	ResultBytes   int       `json:"result_bytes,omitempty"`
	ResultDigest  string    `json:"result_digest,omitempty"`
}

type subagentBatchView struct {
	Status  string                   `json:"status"`
	Total   int                      `json:"total"`
	Started int                      `json:"started"`
	Failed  int                      `json:"failed"`
	Jobs    []subagentModelJobView   `json:"jobs,omitempty"`
	Errors  []subagentBatchErrorView `json:"errors,omitempty"`
	Wait    *subagentWaitView        `json:"wait,omitempty"`
}

type subagentBatchErrorView struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

type subagentWaitView struct {
	Status    string                 `json:"status"`
	Mode      string                 `json:"mode"`
	TimeoutMS int                    `json:"timeout_ms"`
	Total     int                    `json:"total"`
	Completed int                    `json:"completed"`
	Running   int                    `json:"running"`
	Failed    int                    `json:"failed"`
	Jobs      []subagentModelJobView `json:"jobs"`
}

type subagentRunView struct {
	Status  string               `json:"status"`
	JobID   string               `json:"job_id"`
	Job     subagentModelJobView `json:"job"`
	Wait    subagentWaitView     `json:"wait"`
	Result  string               `json:"result,omitempty"`
	Timeout bool                 `json:"timeout,omitempty"`
}

const (
	defaultSubagentWaitTimeoutMS = 30000
	maxSubagentWaitTimeoutMS     = 120000
	subagentResultPreviewLimit   = 2000
)

func newSubagentTool(agent *Agent) tools.Tool {
	return tools.NewTypedTool(tools.NewToolSpec("task", "Run or manage durable subagents for isolated exploration or work. Use action='run' to create a visible durable job and wait for its result, 'start' for one durable background job, 'batch' for multiple durable jobs, and 'wait' to wait for any/all durable jobs. Use 'status' for compact state, 'logs' only for bounded progress diagnostics, and 'review'/'merge' for diffs. Prefer wait over repeated status polling.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"run", "start", "batch", "wait", "status", "logs", "list", "cancel", "resume", "review", "merge"},
				"description": "Subagent action to perform",
			},
			"job_id": map[string]string{
				"type":        "string",
				"description": "Durable subagent job id for status, logs, wait, cancel, resume, review, or merge",
			},
			"job_ids": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Durable subagent job ids for action='wait'",
			},
			"prompt": map[string]string{
				"type":        "string",
				"description": "The task prompt for the subagent",
			},
			"agent_type": map[string]interface{}{
				"type":        "string",
				"description": "Type or named role of subagent to spawn. Explore is read-only; general-purpose can write within write_scope; package role ids are preserved for visualization and prompt guidance.",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"sync", "async", "any", "all"},
				"description": "Compatibility alias: async starts a durable job; sync starts a visible durable job and waits. For action='wait', choose any or all.",
			},
			"write_scope": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Workspace-relative paths a write-capable durable subagent may edit",
			},
			"required_bundles": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Tool bundles this subagent needs, such as web for web_search/web_fetch. The parent agent must have the bundle active before it can be inherited.",
			},
			"required_tools": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Specific tools this subagent needs. Tools are inherited only when active in the parent agent.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "For action='logs', number of recent progress events to return. Defaults to 20 and caps at 80.",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "For action='wait' or batch wait=true, wait timeout in milliseconds. Defaults to 30000 and caps at 120000.",
			},
			"job_timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "For action='start', optional per-job runtime timeout in milliseconds. Defaults to disabled and caps at tools.subagent.max_job_timeout_ms.",
			},
			"wait": map[string]interface{}{
				"type":        "boolean",
				"description": "For action='batch', wait for started jobs after launching them.",
			},
			"tasks": map[string]interface{}{
				"type":        "array",
				"description": "For action='batch', durable subagent jobs to start, capped by tools.subagent.max_batch_size.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"prompt": map[string]string{
							"type":        "string",
							"description": "The task prompt for this subagent.",
						},
						"agent_type": map[string]string{
							"type":        "string",
							"description": "Type or named role of this subagent.",
						},
						"write_scope": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "Workspace-relative paths this durable subagent may edit.",
						},
						"required_bundles": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "Tool bundles this subagent needs, such as web.",
						},
						"required_tools": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "Specific active parent tools this subagent needs.",
						},
						"job_timeout_ms": map[string]interface{}{
							"type":        "integer",
							"description": "Optional per-job runtime timeout in milliseconds. Defaults to disabled and caps at tools.subagent.max_job_timeout_ms.",
						},
					},
				},
			},
		},
	}, nil), func(ctx context.Context, args subagentArgs) (tools.ToolResult, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		if action == "" {
			if strings.EqualFold(strings.TrimSpace(args.Mode), "async") {
				action = "start"
			} else {
				action = "run"
			}
		}
		switch action {
		case "list":
			return tools.ToolResult{Structured: formatSubagentJobList(agent.subagentJobs.List())}, nil
		case "status":
			job, err := agent.subagentJobs.Get(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "logs":
			job, err := agent.subagentJobs.Get(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentLogs(job, args.Limit)}, nil
		case "wait":
			result, err := waitSubagents(ctx, agent, subagentWaitRequest{
				JobID:     args.JobID,
				JobIDs:    args.JobIDs,
				Mode:      args.Mode,
				TimeoutMS: args.TimeoutMS,
			})
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: result}, nil
		case "cancel":
			job, err := agent.subagentJobs.Cancel(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			subagentEventTargetFromContext(ctx).emit(job, "canceled", "Subagent job canceled.", "", "", job.Error, "")
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "resume":
			job, err := agent.ResumeDurableSubagentWithContext(ctx, args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "review":
			review, err := agent.ReviewDurableSubagent(args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: review}, nil
		case "merge":
			result, err := agent.MergeDurableSubagentWithContext(ctx, args.JobID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: result}, nil
		case "start":
			prompt := strings.TrimSpace(args.Prompt)
			if prompt == "" {
				return tools.ToolResult{}, fmt.Errorf("missing prompt argument")
			}
			job, err := agent.startDurableSubagentWithContext(ctx, durableSubagentStartRequest{
				Prompt:          prompt,
				AgentType:       args.AgentType,
				WriteScope:      args.WriteScope,
				RequiredBundles: args.RequiredBundles,
				RequiredTools:   args.RequiredTools,
				MaxTurns:        args.MaxTurns,
				JobTimeoutMS:    args.JobTimeoutMS,
			})
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.ToolResult{Structured: formatSubagentModelJob(job)}, nil
		case "batch":
			result := startSubagentBatch(ctx, agent, args.Tasks, args.Wait, args.TimeoutMS)
			return tools.ToolResult{Structured: result}, nil
		case "run":
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported subagent action %q", action)
		}
		prompt := strings.TrimSpace(args.Prompt)
		if prompt == "" {
			return tools.ToolResult{}, fmt.Errorf("missing prompt argument")
		}
		agentType := strings.TrimSpace(args.AgentType)
		if agentType == "" {
			agentType = "Explore"
		}
		result, err := agent.runDurableSubagentSync(ctx, durableSubagentStartRequest{
			Prompt:          prompt,
			AgentType:       agentType,
			WriteScope:      args.WriteScope,
			RequiredBundles: args.RequiredBundles,
			RequiredTools:   args.RequiredTools,
			MaxTurns:        args.MaxTurns,
			JobTimeoutMS:    args.JobTimeoutMS,
		}, args.TimeoutMS)
		if err != nil {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{Structured: result}, nil
	})
}

func formatSubagentJobList(jobs []*subagentJob) []subagentModelJobView {
	out := make([]subagentModelJobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, formatSubagentModelJob(job))
	}
	return out
}

func formatSubagentModelJob(job *subagentJob) subagentModelJobView {
	if job == nil {
		return subagentModelJobView{}
	}
	progress := durableSubagentProgressViews(job.Progress)
	view := subagentModelJobView{
		JobID:         job.ID,
		SessionID:     job.SessionID,
		ParentTurnID:  job.ParentTurnID,
		IdentityID:    job.Identity.ID,
		AgentType:     job.AgentType,
		RoleID:        job.RoleID,
		RoleName:      job.RoleName,
		Status:        string(job.Status),
		Error:         job.Error,
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
		FinishedAt:    job.FinishedAt,
		MergeStatus:   job.MergeStatus,
		ProgressCount: len(job.Progress),
	}
	if len(progress) > 0 {
		latest := progress[len(progress)-1]
		view.LastPhase = latest.Phase
		view.LastMessage = latest.Message
		view.LastToolName = latest.ToolName
	}
	for i := len(progress) - 1; i >= 0; i-- {
		if strings.TrimSpace(view.LastToolName) == "" && strings.TrimSpace(progress[i].ToolName) != "" {
			view.LastToolName = progress[i].ToolName
		}
		if strings.TrimSpace(view.LastMessage) == "" && strings.TrimSpace(progress[i].Message) != "" {
			view.LastMessage = progress[i].Message
		}
	}
	if subagentStatusTerminal(job.Status) && strings.TrimSpace(job.Result) != "" {
		view.ResultPreview = previewSubagentResultForModel(job.Result)
		view.ResultBytes = len([]byte(job.Result))
		view.ResultDigest = subagentResultDigest(job.Result)
	}
	return view
}

func formatSubagentLogs(job *subagentJob, limit int) subagentLogsView {
	if limit <= 0 {
		limit = 20
	}
	if limit > subagentProgressLimit {
		limit = subagentProgressLimit
	}
	total := len(job.Progress)
	progress := job.Progress
	if total > limit {
		progress = progress[total-limit:]
	}
	return subagentLogsView{
		JobID:    job.ID,
		Status:   string(job.Status),
		Count:    len(progress),
		Total:    total,
		Progress: durableSubagentProgressViews(progress),
	}
}

func startSubagentBatch(ctx context.Context, agent *Agent, tasks []subagentBatchItem, wait bool, timeoutMS int) subagentBatchView {
	total := len(tasks)
	view := subagentBatchView{
		Status: "started",
		Total:  total,
		Jobs:   make([]subagentModelJobView, 0, len(tasks)),
		Errors: make([]subagentBatchErrorView, 0),
	}
	if total == 0 {
		view.Status = "failed"
		view.Failed = 1
		view.Errors = append(view.Errors, subagentBatchErrorView{Index: 0, Error: "missing tasks argument"})
		return view
	}
	batchLimit := agent.subagentBatchLimit()
	if len(tasks) > batchLimit {
		for i := batchLimit; i < len(tasks); i++ {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: i, Error: fmt.Sprintf("batch limit is %d tasks", batchLimit)})
		}
		tasks = tasks[:batchLimit]
	}
	for i, item := range tasks {
		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: i, Error: "missing prompt argument"})
			continue
		}
		job, err := agent.startDurableSubagentWithContext(ctx, durableSubagentStartRequest{
			Prompt:          prompt,
			AgentType:       item.AgentType,
			WriteScope:      item.WriteScope,
			RequiredBundles: item.RequiredBundles,
			RequiredTools:   item.RequiredTools,
			MaxTurns:        item.MaxTurns,
			JobTimeoutMS:    item.JobTimeoutMS,
		})
		if err != nil {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: i, Error: err.Error()})
			continue
		}
		view.Jobs = append(view.Jobs, formatSubagentModelJob(job))
	}
	view.Started = len(view.Jobs)
	view.Failed = len(view.Errors)
	if view.Failed > 0 && view.Started == 0 {
		view.Status = "failed"
	} else if view.Failed > 0 {
		view.Status = "partial"
	}
	if wait && view.Started > 0 {
		jobIDs := make([]string, 0, len(view.Jobs))
		for _, job := range view.Jobs {
			jobIDs = append(jobIDs, job.JobID)
		}
		waitView, err := waitSubagents(ctx, agent, subagentWaitRequest{
			JobIDs:    jobIDs,
			Mode:      "all",
			TimeoutMS: timeoutMS,
		})
		if err != nil {
			view.Errors = append(view.Errors, subagentBatchErrorView{Index: -1, Error: err.Error()})
			view.Failed = len(view.Errors)
			if view.Started == 0 {
				view.Status = "failed"
			} else {
				view.Status = "partial"
			}
		} else {
			view.Wait = &waitView
			view.Jobs = waitView.Jobs
			view.Status = waitView.Status
		}
	}
	return view
}

func (a *Agent) runDurableSubagentSync(ctx context.Context, req durableSubagentStartRequest, timeoutMS int) (subagentRunView, error) {
	job, err := a.startDurableSubagentWithContext(ctx, req)
	if err != nil {
		return subagentRunView{}, err
	}
	waitView, err := waitSubagents(ctx, a, subagentWaitRequest{
		JobID:      job.ID,
		Mode:       "all",
		TimeoutMS:  timeoutMS,
		Indefinite: timeoutMS <= 0,
	})
	if err != nil {
		return subagentRunView{}, err
	}
	view := subagentRunView{
		Status:  waitView.Status,
		JobID:   job.ID,
		Wait:    waitView,
		Timeout: waitView.Status == "timeout",
	}
	if len(waitView.Jobs) > 0 {
		view.Job = waitView.Jobs[0]
		view.Result = waitView.Jobs[0].ResultPreview
	}
	return view, nil
}

type subagentWaitRequest struct {
	JobID      string
	JobIDs     []string
	Mode       string
	TimeoutMS  int
	Indefinite bool
}

func waitSubagents(ctx context.Context, agent *Agent, req subagentWaitRequest) (subagentWaitView, error) {
	jobIDs := normalizeSubagentWaitJobIDs(req.JobID, req.JobIDs)
	if len(jobIDs) == 0 {
		return subagentWaitView{}, fmt.Errorf("missing job_ids argument")
	}
	mode := normalizeSubagentWaitMode(req.Mode)
	timeoutMS := 0
	if !req.Indefinite {
		timeoutMS = normalizeSubagentWaitTimeoutMS(req.TimeoutMS)
	}
	updates, unsubscribe := agent.subagentJobs.Watch()
	defer unsubscribe()
	deadline := time.Time{}
	if !req.Indefinite {
		deadline = time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	}
	for {
		view, err := snapshotSubagentWait(agent, jobIDs, mode, timeoutMS)
		if err != nil {
			return subagentWaitView{}, err
		}
		if subagentWaitSatisfied(view) {
			view.Status = "completed"
			return view, nil
		}
		if req.Indefinite {
			select {
			case <-ctx.Done():
				view.Status = "interrupted"
				return view, nil
			case <-updates:
			}
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			view.Status = "timeout"
			return view, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			view.Status = "timeout"
			return view, nil
		case <-updates:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func snapshotSubagentWait(agent *Agent, jobIDs []string, mode string, timeoutMS int) (subagentWaitView, error) {
	view := subagentWaitView{
		Status:    "running",
		Mode:      mode,
		TimeoutMS: timeoutMS,
		Total:     len(jobIDs),
		Jobs:      make([]subagentModelJobView, 0, len(jobIDs)),
	}
	for _, id := range jobIDs {
		job, err := agent.subagentJobs.Get(id)
		if err != nil {
			return subagentWaitView{}, err
		}
		item := formatSubagentModelJob(job)
		view.Jobs = append(view.Jobs, item)
		switch {
		case subagentStatusTerminal(job.Status):
			view.Completed++
			if job.Status == subagentStatusError || job.Status == subagentStatusTimeout {
				view.Failed++
			}
		default:
			view.Running++
		}
	}
	return view, nil
}

func subagentWaitSatisfied(view subagentWaitView) bool {
	if view.Total == 0 {
		return false
	}
	if view.Mode == "any" {
		return view.Completed > 0
	}
	return view.Completed == view.Total
}

func normalizeSubagentWaitJobIDs(jobID string, jobIDs []string) []string {
	out := make([]string, 0, len(jobIDs)+1)
	seen := make(map[string]struct{})
	appendID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	appendID(jobID)
	for _, id := range jobIDs {
		appendID(id)
	}
	return out
}

func normalizeSubagentWaitMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "any" {
		return "any"
	}
	return "all"
}

func normalizeSubagentWaitTimeoutMS(timeoutMS int) int {
	if timeoutMS <= 0 {
		return defaultSubagentWaitTimeoutMS
	}
	if timeoutMS > maxSubagentWaitTimeoutMS {
		return maxSubagentWaitTimeoutMS
	}
	return timeoutMS
}

func subagentStatusTerminal(status subagentJobStatus) bool {
	switch status {
	case subagentStatusCompleted, subagentStatusCanceled, subagentStatusInterrupted, subagentStatusTimeout, subagentStatusError:
		return true
	default:
		return false
	}
}

func previewSubagentResultForModel(result string) string {
	result = strings.TrimSpace(result)
	if len([]rune(result)) <= subagentResultPreviewLimit {
		return result
	}
	runes := []rune(result)
	return string(runes[:subagentResultPreviewLimit]) + "..."
}

func subagentResultDigest(result string) string {
	sum := sha256.Sum256([]byte(result))
	return fmt.Sprintf("%x", sum[:])
}

func formatSubagentJob(job *subagentJob, includeMessages bool) map[string]interface{} {
	_ = includeMessages
	if job == nil {
		return map[string]interface{}{}
	}
	out := map[string]interface{}{
		"job_id":          job.ID,
		"session_id":      job.SessionID,
		"parent_turn_id":  job.ParentTurnID,
		"agent_type":      job.AgentType,
		"role_id":         job.RoleID,
		"role_name":       job.RoleName,
		"package_name":    job.PackageName,
		"status":          job.Status,
		"result":          job.Result,
		"error":           job.Error,
		"created_at":      job.CreatedAt,
		"updated_at":      job.UpdatedAt,
		"started_at":      job.StartedAt,
		"finished_at":     job.FinishedAt,
		"write_scope":     append([]string{}, job.WriteScope...),
		"default_bundles": append([]string{}, job.DefaultBundles...),
		"tool_names":      append([]string{}, job.ToolNames...),
		"worktree_dir":    job.WorktreeDir,
		"isolation":       job.Isolation,
		"merge_status":    job.MergeStatus,
		"merged_at":       job.MergedAt,
	}
	out["progress"] = cloneSubagentProgress(job.Progress)
	return out
}

// RunSubagent runs a subagent with limited tools.
func (a *Agent) RunSubagent(ctx context.Context, prompt string, agentType string) (string, error) {
	toolNames := []string{"bash", "read_file"}
	if normalizeSubagentType(agentType) == "general-purpose" {
		toolNames = append(toolNames, "write_file", "edit_file")
	}

	result, err := a.runScopedSubagent(ctx, prompt, "You are a subagent. Be concise. Prefer workspace-relative file paths.", toolNames, 30)
	if err != nil && !errors.Is(err, conversation.ErrMaxTurnsReached) {
		return "", fmt.Errorf("subagent API error: %w", err)
	}

	if result == nil || result.LastAssistantText == "" {
		return "(subagent completed with no text output)", nil
	}
	return result.LastAssistantText, nil
}

func (a *Agent) runScopedSubagent(ctx context.Context, prompt, basePrompt string, toolNames []string, maxTurns int) (*conversation.Result, error) {
	messages := []protocol.Message{protocol.NewTextMessage(protocol.RoleUser, prompt)}
	prompts := conversation.PromptLayers{Base: strings.TrimSpace(basePrompt)}
	return conversation.Runner{
		Caller: a.client,
		BuildRequest: func(ctx context.Context) (protocol.Request, error) {
			_ = ctx
			return conversation.NewRequest(a.cfg.Model, a.cfg.MaxTokens, a.cfg.ReasoningEffort, prompts.Build(), messages, a.toolHandler.ActiveSchemas(toolNames...)), nil
		},
		AppendAssistant: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		AppendToolResults: func(msg protocol.Message) {
			messages = append(messages, msg)
		},
		ExecuteTool:      a.executeSubagentTool,
		ToolResultFilter: a.filterModelToolResult,
		MaxTurns:         maxTurns,
	}.Run(ctx)
}

func (a *Agent) reviewPermissionRequest(ctx context.Context, req tools.PermissionRequest) (tools.PermissionResult, error) {
	prompt := buildPermissionReviewPrompt(req)
	result, err := a.runScopedSubagent(ctx, prompt, "You are a security review subagent. Review one protected tool call from a remote session. You may use read_file when file context matters. Be conservative. Reply with exactly one line beginning with ALLOW:, DENY:, or MANUAL: followed by a short reason.", []string{"read_file"}, 8)
	if err != nil && !errors.Is(err, conversation.ErrMaxTurnsReached) {
		return tools.PermissionResult{}, err
	}
	if result == nil {
		return tools.PermissionResult{}, fmt.Errorf("permission review returned no result")
	}
	return parsePermissionReviewResult(result.LastAssistantText), nil
}

func buildPermissionReviewPrompt(req tools.PermissionRequest) string {
	lines := []string{
		"Review this protected tool call from a remote session.",
		"Decide whether it is safe enough to auto-approve right now.",
		"",
		fmt.Sprintf("Tool: %s", req.ToolName),
		fmt.Sprintf("Action: %s", req.Action),
		fmt.Sprintf("Source: %s", req.Source),
		fmt.Sprintf("Sender: %s", req.Sender),
		fmt.Sprintf("Mutation: %t", req.Mutation),
	}
	if command := strings.TrimSpace(req.Command); command != "" {
		lines = append(lines, "", "Command:", command)
	}
	if len(req.Paths) > 0 {
		lines = append(lines, "", "Paths:")
		lines = append(lines, req.Paths...)
	}
	if len(req.Input) > 0 {
		lines = append(lines, "", "Normalized input:")
		lines = append(lines, formatPermissionReviewInput(req.Input))
	}
	lines = append(lines,
		"",
		"Reply with exactly one line:",
		"ALLOW: <short reason>",
		"DENY: <short reason>",
		"MANUAL: <short reason>",
	)
	return strings.Join(lines, "\n")
}

func parsePermissionReviewResult(text string) tools.PermissionResult {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return tools.PermissionResult{Decision: tools.PermissionPending, Reason: "automatic review returned no decision"}
	}
	for _, line := range strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.Trim(line, "`"))
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "ALLOW:"):
			return tools.PermissionResult{Decision: tools.PermissionAllow, Reason: strings.TrimSpace(line[len("ALLOW:"):]), Scope: "review"}
		case strings.HasPrefix(strings.ToUpper(line), "DENY:"):
			return tools.PermissionResult{Decision: tools.PermissionDeny, Reason: strings.TrimSpace(line[len("DENY:"):]), Scope: "review"}
		case strings.HasPrefix(strings.ToUpper(line), "MANUAL:"):
			return tools.PermissionResult{Decision: tools.PermissionPending, Reason: strings.TrimSpace(line[len("MANUAL:"):]), Scope: "review"}
		}
	}
	return tools.PermissionResult{Decision: tools.PermissionPending, Reason: "automatic review returned an unrecognized decision"}
}

func formatPermissionReviewInput(input map[string]interface{}) string {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(data)
}

func (a *Agent) executeSubagentTool(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
	return executeSubagentToolWithHandlers(ctx, name, input, subagentToolHandlers{
		runBash: func(ctx context.Context, command string, allowUnlisted bool) (conversation.ToolExecutionResult, error) {
			input := map[string]interface{}{"command": command}
			if allowUnlisted {
				input["_allow_unlisted_commands"] = true
			}
			return a.handleToolResult(ctx, "bash", input)
		},
		readFile: func(ctx context.Context, path string, limit, offset, startLine int) (conversation.ToolExecutionResult, error) {
			input := map[string]interface{}{"path": path}
			if limit > 0 {
				input["limit"] = limit
			}
			if offset > 0 {
				input["offset"] = offset
			}
			if startLine > 0 {
				input["start_line"] = startLine
			}
			return a.handleToolResult(ctx, "read_file", input)
		},
		writeFile: func(ctx context.Context, path, content string) (conversation.ToolExecutionResult, error) {
			return a.handleToolResult(ctx, "write_file", map[string]interface{}{"path": path, "content": content})
		},
		editFile: func(ctx context.Context, path, oldText, newText string) (conversation.ToolExecutionResult, error) {
			return a.handleToolResult(ctx, "edit_file", map[string]interface{}{"path": path, "old_text": oldText, "new_text": newText})
		},
	})
}

// Run runs the agent loop.
func (a *Agent) Run(ctx context.Context) error {
	return a.RunWithOptions(ctx, RunOptions{})
}

func (a *Agent) messageState() ([]protocol.Message, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.CloneMessages(a.messages), a.historyVersion
}

func (a *Agent) captureMemoryCandidates() error {
	if a.memoryExt == nil {
		return nil
	}
	messages, _ := a.messageState()
	_, err := a.memoryExt.Capture(messages)
	return err
}

// CaptureInsightMemoryCandidates stores durable memory suggestions derived from an insights report.
func (a *Agent) CaptureInsightMemoryCandidates(report *insights.Report) error {
	if a.memoryExt == nil {
		return nil
	}
	_, err := a.memoryExt.CaptureInsightsReport(report)
	return err
}

// CaptureTimelineMemoryCandidates stores durable memory suggestions derived from runtime timeline events.
func (a *Agent) CaptureTimelineMemoryCandidates(items []events.Event) error {
	if a.memoryExt == nil {
		return nil
	}
	_, err := a.memoryExt.CaptureTimeline(items)
	return err
}

func (a *Agent) storeCompactedMessages(messages []protocol.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = protocol.CloneMessages(messages)
	a.transcriptRefs = mergeTranscriptRefs(a.transcriptRefs, extractTranscriptRefs(messages))
	a.historyVersion++
	a.lastCompactedVersion = a.historyVersion
}

func (a *Agent) maybeAutoCompact(ctx context.Context, history []protocol.Message, version int64, system string, estimate contextBudgetEstimate) ([]protocol.Message, bool, error) {
	if !shouldAutoCompact(estimate, a.cfg.CompressThreshold) {
		return history, false, nil
	}

	a.mu.Lock()
	if a.lastCompactedVersion == version {
		current := protocol.CloneMessages(a.messages)
		a.mu.Unlock()
		return current, false, nil
	}
	a.mu.Unlock()

	result, err := a.summarizer.SummarizeSession(ctx, compress.SessionSummaryRequest{
		System:               system,
		History:              protocol.CloneMessages(history),
		TokenBreakdown:       tokenBreakdownMap(estimate.Breakdown),
		RecentUserMessages:   recentPersistentUserMessages(history, 6),
		ContinuationSnapshot: a.continuationSnapshot(tools.SessionContextFromContext(ctx).SessionID, history),
	})
	if err != nil {
		return nil, false, err
	}
	compacted := result.Messages

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.historyVersion != version {
		return protocol.CloneMessages(a.messages), false, nil
	}
	a.messages = protocol.CloneMessages(compacted)
	a.transcriptRefs = mergeTranscriptRefs(a.transcriptRefs, extractTranscriptRefs(compacted))
	a.historyVersion++
	a.lastCompactedVersion = a.historyVersion
	return protocol.CloneMessages(a.messages), true, nil
}

func extractTranscriptRefs(messages []protocol.Message) []string {
	if len(messages) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(messages))
	refs := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.Metadata == nil {
			continue
		}
		ref := strings.TrimSpace(msg.Metadata.Transcript)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func mergeTranscriptRefs(existing, incoming []string) []string {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, ref := range existing {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		merged = append(merged, ref)
	}
	for _, ref := range incoming {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		merged = append(merged, ref)
	}
	return merged
}

func (a *Agent) resetIdle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.idleRequested = false
}

func (a *Agent) consumeIdleRequest() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	idle := a.idleRequested
	a.idleRequested = false
	return idle
}
