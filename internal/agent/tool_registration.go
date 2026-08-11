package agent

import (
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/tools"
	"github.com/tim5wang/godex/internal/tools/teamtools"
)

const (
	bundleCoreCode   = "core_code"
	bundleWriting    = "writing"
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
	bundleLSP        = "lsp"
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
	binding := a.SandboxBinding()
	workspaceDir := binding.WorkspaceDir
	tempDir := binding.TempDir
	execution := binding.Execution

	// Create a unified workspacefs.FS backed by the sandbox.  For local
	// mode this is an os.Root-backed FS; for SSH/Docker mode afero-backed.
	fileToolFS := newWorkspaceFSForExecution(workspaceDir, execution)
	// Attach the FS to the file executor so ReadFileLines/WriteFile/EditFile
	// use the correct backend.
	fileExecutor := tooling.NewWorkspaceExecutorWithTempDirAndExecution(workspaceDir, tempDir, execution)
	fileExecutor.SetFS(fileToolFS)

	a.registerToolTo(handler, tools.NewBashToolWithExecution(workspaceDir, tempDir, execution), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewGlobToolWithFS(fileToolFS, workspaceDir, a.cfg.Tools.Glob.DefaultMaxResults), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewReadFileToolWithExecutor(fileExecutor), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewWriteFileToolWithExecutor(fileExecutor), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewEditFileToolWithExecutor(fileExecutor), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewAttachFileToolWithFS(fileToolFS, workspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewGrepToolWithFS(fileToolFS, workspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewFindToolWithFS(fileToolFS, workspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewLsToolWithFS(fileToolFS, workspaceDir), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "workspace shell commands and code file access",
		DefaultActive: true,
	})

	a.registerToolTo(handler, tools.NewTaskTool(a.taskMgr), tools.ToolMeta{
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

	a.registerToolTo(handler, tools.NewMemoryTool(a.memoryMgr), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewSkillTool(a), tools.ToolMeta{AlwaysActive: true})
	a.registerToolTo(handler, tools.NewListPackagesTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewInstallPackageTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewRemovePackageTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewListPromptsTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package and prompt ecosystem"})
	a.registerToolTo(handler, tools.NewListPackageCommandsTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package command declarations"})
	a.registerToolTo(handler, tools.NewListPackageRolesTool(a), tools.ToolMeta{Bundle: bundlePackages, Summary: "declaration-only package subagent role declarations"})
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
			Bundle:        bundleWeb,
			Summary:       "current information lookup and page fetching",
			DefaultActive: true,
		})
	}
	if a.webFetch != nil && a.cfg.Tools.WebFetch.Enabled {
		a.registerToolTo(handler, tools.NewWebFetchTool(a.webFetch), tools.ToolMeta{
			Bundle:        bundleWeb,
			Summary:       "current information lookup and page fetching",
			DefaultActive: true,
		})
	}
	if a.browser != nil && a.cfg.Tools.Browser.Enabled {
		a.registerToolTo(handler, tools.NewBrowserTool(a.browser, workspaceDir), tools.ToolMeta{
			Bundle:  bundleBrowser,
			Summary: "interactive browser automation for dynamic pages",
		})
	}
	a.registerToolTo(handler, tools.NewDesktopTool(tools.NewDesktopService(tempDir)), tools.ToolMeta{
		Bundle:  bundleDesktop,
		Summary: "local desktop screenshots, clipboard, keyboard, mouse, and window inspection",
	})

	a.registerToolTo(handler, tools.NewBackgroundTool(a.bgMgr, workspaceDir, tempDir, execution), tools.ToolMeta{
		Bundle:  bundleBackground,
		Summary: "long-running command execution and status checks",
	})
	a.registerToolTo(handler, newWorkflowTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "isolated delegated exploration or implementation work",
	})
	a.registerToolTo(handler, newAgentGraphTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "dynamic agent graph DAG abstraction over durable workflow nodes",
	})
	a.registerToolTo(handler, newLongTaskTool(a), tools.ToolMeta{
		Bundle:  bundleSubagent,
		Summary: "Ralph-style prioritized long task orchestration over durable workflow nodes",
	})
	a.registerSubagentTool(handler)
	a.registerToolTo(handler, tools.NewACPAgentTool(a.cfg.ACP.Agents, workspaceDir), tools.ToolMeta{
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
	a.registerToolTo(handler, tools.NewLSPTool(workspaceDir), tools.ToolMeta{
		Bundle:        bundleLSP,
		Summary:       "LSP code intelligence (definitions, references, hover, diagnostics, completions)",
		DefaultActive: true,
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
