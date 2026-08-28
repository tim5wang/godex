package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
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

// builtinPluginOwner* identify the owning dynamic component of builtin tools
// that are registered through the pluginrt ownership model. UnregisterOwner
// with one of these ids cleanly removes the whole builtin group.
const (
	builtinPluginOwnerMCP = "godex:builtin:mcp"
)

// registerToolTo registers a tool and returns its reversible registration
// handle. Dynamic components (plugins, packages, MCP bridges) can keep the
// handle and call Dispose to unload the tool cleanly.
func (a *Agent) registerToolTo(handler *tools.ToolHandler, tool tools.Tool, meta tools.ToolMeta) *tools.Registration {
	return handler.RegisterWithMeta(tool, meta)
}

// registerOwnedTool registers a tool owned by the named dynamic component
// (e.g. a plugin or package id). The returned handle is reversible, and
// UnregisterOwner on the handler removes every tool of that owner.
func (a *Agent) registerOwnedTool(handler *tools.ToolHandler, owner string, tool tools.Tool, meta tools.ToolMeta) (*tools.Registration, error) {
	return handler.RegisterOwned(owner, tool, meta)
}

// registerMCPServerTools discovers and registers one first-class tool per
// declared tool of every configured MCP server (stdio or streamable-http).
// Each server owns its tools (owner "mcp:<server>"), so unregistering one
// server's tools never affects another. Server-tool discovery failures are
// non-fatal: the generic list_mcp_tools/call_mcp_tool bridge remains available
// as a fallback.
func (a *Agent) registerMCPServerTools(handler *tools.ToolHandler) {
	if a.mcpMgr == nil {
		return
	}
	for _, serverName := range a.mcpMgr.ListToolServers() {
		owner := "mcp:" + serverName
		decls, err := a.mcpMgr.ListServerTools(context.Background(), serverName)
		if err != nil {
			// Keep going; other servers may still register.
			continue
		}
		for _, decl := range decls {
			tool, toolErr := tools.NewMCPServerTool(a.mcpMgr, serverName, decl)
			if toolErr != nil {
				continue
			}
			_, _ = a.registerOwnedTool(handler, owner, tool, tools.ToolMeta{
				Bundle:  bundleMCP,
				Summary: "first-class tool from MCP server " + serverName,
			})
		}
	}
}

func (a *Agent) registerTool(tool tools.Tool, meta tools.ToolMeta) *tools.Registration {
	return a.registerToolTo(a.toolHandler, tool, meta)
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
	if err := a.ActivateInstalledPackageRuntimes(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to activate one or more package runtimes: %v\n", err)
	}
}

func (a *Agent) registerToolsWith(handler *tools.ToolHandler) {
	binding := a.SandboxBinding()
	workspaceDir := binding.WorkspaceDir
	tempDir := binding.TempDir
	execution := binding.Execution

	// Create a unified workspacefs.FS backed by the sandbox. For local
	// sessions, read_file and attach_file additionally get read-only access
	// to this session's own uploaded attachments. Writes through the same FS
	// remain workspace-bound, and other sessions' attachment dirs are not
	// exposed.
	var readAllowlist []string
	if sessionID := strings.TrimSpace(a.sessionID); sessionID != "" && a.cfg != nil && strings.TrimSpace(a.cfg.SessionsDir) != "" {
		readAllowlist = append(readAllowlist, filepath.Join(a.cfg.SessionsDir, sessionID, "attachments"))
	}
	fileToolFS := newWorkspaceFSForExecution(workspaceDir, execution, readAllowlist...)
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
	a.registerOwnedTool(handler, builtinPluginOwnerMCP, tools.NewListMCPResourcesTool(a.mcpMgr), tools.ToolMeta{
		Bundle:  bundleMCP,
		Summary: "configured MCP resource servers",
	})
	a.registerOwnedTool(handler, builtinPluginOwnerMCP, tools.NewReadMCPResourceTool(a.mcpMgr), tools.ToolMeta{
		Bundle:  bundleMCP,
		Summary: "configured MCP resource servers",
	})
	a.registerOwnedTool(handler, builtinPluginOwnerMCP, tools.NewListMCPToolsTool(a.mcpMgr), tools.ToolMeta{
		Bundle:        bundleMCP,
		Summary:       "discover MCP tools (bridge)",
		DefaultActive: true,
	})
	a.registerOwnedTool(handler, builtinPluginOwnerMCP, tools.NewCallMCPToolTool(a.mcpMgr), tools.ToolMeta{
		Bundle:        bundleMCP,
		Summary:       "call MCP tools (bridge)",
		DefaultActive: true,
	})
	a.registerOwnedTool(handler, builtinPluginOwnerMCP, tools.NewListMCPPromptsTool(a.mcpMgr), tools.ToolMeta{
		Bundle:  bundleMCP,
		Summary: "configured stdio MCP prompt servers",
	})
	a.registerOwnedTool(handler, builtinPluginOwnerMCP, tools.NewGetMCPPromptTool(a.mcpMgr), tools.ToolMeta{
		Bundle:  bundleMCP,
		Summary: "configured stdio MCP prompt servers",
	})
	// §5.2 dynamic per-server registration: each configured stdio MCP server's
	// tools appear directly in the catalog (namespaced <server>__<tool>) with
	// owner mcp:<server>, so unload/reload of one server never touches others.
	a.registerMCPServerTools(handler)
	a.registerToolTo(handler, tools.NewLSPTool(workspaceDir), tools.ToolMeta{
		Bundle:        bundleLSP,
		Summary:       "LSP code intelligence (definitions, references, hover, diagnostics, completions)",
		DefaultActive: true,
	})
	a.registerToolTo(handler, tools.NewUICardTool(), tools.ToolMeta{
		Bundle:        bundleCoreCode,
		Summary:       "structured interactive cards (form / button group / markdown) for rich chat UI",
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
	if a.taskboard != nil {
		a.registerToolTo(handler, taskboard.NewTaskboardToolWithExecutor(a.taskboard, a.taskboardExec), tools.ToolMeta{
			Bundle:        bundleTaskBoard,
			Summary:       "cross-session project task board (claim/execute/accept cards, dispatch to execution sessions)",
			DefaultActive: true,
		})
	}
	if a.cfg.Tools.Execution.ScopeWrite {
		handler.AddBeforeInterceptorsForTools(
			[]string{"write_file", "edit_file"},
			NewScopeWriteInterceptor(workspaceDir),
		)
	}
	handler.ActivateDefaults()
}
