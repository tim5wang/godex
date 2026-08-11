package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/instructions"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/core/media"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/core/notes"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/services/historysearch"
	"github.com/tim5wang/godex/internal/tools"
)

func buildDependencies(cfg *config.Config) dependencies {
	taskMgr := task.NewManager(cfg.TasksDir)
	msgBus := loadMessageBus(cfg.TeamDir)
	client := callerForConfigProfile(cfg, cfg.DefaultModelProfile())
	skillLoader := newSkillLoader(cfg, client)
	memoryMgr := memory.NewManager(cfg.MemoryDir)
	memoryExt := memory.NewExtractor(memoryMgr, cfg.TempDir)
	memoryStrategy := memory.NewStrategy(memory.StrategyOptions{
		Kind:    memory.ParseStrategyKind(cfg.Memory.Strategy),
		Extract: memoryExt,
		Consolidator: memory.NewConsolidator(memory.ConsolidatorOptions{
			Manager: memoryMgr,
			OneShot: memoryConsolidationOneShot(client, cfg),
			AfterN:  cfg.Memory.ConsolidateAfter,
		}),
	})
	compressor := compress.NewCompressor(cfg.TranscriptsDir)
	compressor.SetKeepRecent(cfg.Compaction.KeepRecentMessages)
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

	// Lightpanda integration: initialize binary and inject into search/fetch.
	if cfg.Tools.Lightpanda.Enabled {
		lpBin := tools.NewLightpandaBinary()
		if _, err := lpBin.EnsureBinary(context.Background(), cfg.Tools.Lightpanda.BinaryPath, cfg.TempDir, cfg.Tools.Lightpanda.AutoDownload); err == nil {
			lpSearcher := tools.NewLightpandaSearchProvider(
				lpBin,
				cfg.Tools.Lightpanda.SearchEngine,
				cfg.Tools.Lightpanda.SearchTemplate,
				cfg.Tools.Lightpanda.WaitNetworkMS,
				cfg.Tools.Lightpanda.ObeyRobots,
				cfg.Tools.Lightpanda.LogLevel,
			)
			webSearch.SetLightpandaSearcher(lpSearcher)
			webFetch.SetLightpandaFetcher(lpBin)
		}
	}

	return dependencies{
		taskMgr:      taskMgr,
		msgBus:       msgBus,
		client:       client,
		skillLoader:  skillLoader,
		instrLoader:  instructions.NewLoader(),
		memoryMgr:    memoryMgr,
		memoryExt:    memoryExt,
		memoryStrategy: memoryStrategy,
		notesMgr:     notes.NewManager(notesDirForConfig(cfg)),
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
		subagentJobs: newSubagentJobStoreWithLease(subagentJobsDir(cfg), cfg.StateDir),
		workflows:    newWorkflowStore(filepath.Join(cfg.StateDir, "workflows")),
		todoMgr:      todo.NewManager(cfg.TodosDir),
		sandbox:      localSandboxFromConfig(cfg),
	}
}

func apiRequestTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.APITimeoutSeconds <= 0 {
		return 600 * time.Second
	}
	return time.Duration(cfg.APITimeoutSeconds) * time.Second
}

// memoryConsolidationOneShot adapts the wired conversation caller into the
// one-shot LLM callback required by the consolidation strategy. It returns nil
// when the caller or model is unavailable so consolidation degrades gracefully.
func memoryConsolidationOneShot(client conversation.Caller, cfg *config.Config) func(ctx context.Context, prompt, input string) (string, error) {
	if client == nil || cfg == nil || strings.TrimSpace(cfg.Model) == "" {
		return nil
	}
	return func(ctx context.Context, prompt, input string) (string, error) {
		messages := []protocol.Message{
			protocol.NewTextMessage(protocol.RoleUser, prompt+"\n\n"+input),
		}
		req := conversation.NewRequest(cfg.Model, min(cfg.MaxTokens, 2048), "", "", messages, nil)
		resp, err := client.Call(ctx, req)
		if err != nil {
			return "", err
		}
		return protocol.BlocksText(resp.Content), nil
	}
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
	if deps.sandbox == nil {
		deps.sandbox = localSandboxFromConfig(cfg)
	}
	agent := &Agent{
		cfg:            cfg,
		toolHandler:    handler,
		todoMgr:        deps.todoMgr,
		skillLoader:    deps.skillLoader,
		instrLoader:    deps.instrLoader,
		memoryMgr:      deps.memoryMgr,
		memoryExt:      deps.memoryExt,
		memoryStrategy: deps.memoryStrategy,
		notesMgr:      deps.notesMgr,
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
		sandbox:        deps.sandbox,
		roleBundles:    newRoleBundleRegistry(),
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
	agent.workerRuntime = localGoDexWorkerRuntime{agent: agent}
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

func notesDirForConfig(cfg *config.Config) string {
	if strings.TrimSpace(cfg.HomeDir) != "" {
		return filepath.Join(cfg.HomeDir, "notes")
	}
	return filepath.Join(cfg.StateDir, "notes")
}
