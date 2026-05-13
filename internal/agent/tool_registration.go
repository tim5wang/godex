package agent

import (
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/tools/teamtools"
)

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
