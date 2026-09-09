// godex-feature: sandbox
// Agent Identity / Sandbox 解耦：Sandbox 接口 + LocalSandbox，scope 感知的执行环境。
// 入口：内部（工具执行绑定）
// 文档：docs/architecture-v2-spec.md、README 核心特性
package agent

import (
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"github.com/tim5wang/godex/internal/sandbox"
)

// sandboxFromConfig picks the sandbox implementation from the session config:
// the default local sandbox, or a RemoteSandbox (docs/remote-sandbox-design.md
// M1) when tools.execution.mode=relay and a relay node is configured. Relay
// center/token default to the control section so a joined node needs no extra
// fields.
func sandboxFromConfig(cfg *config.Config) sandbox.Sandbox {
	if cfg == nil {
		return sandbox.NewLocal(sandbox.LocalOptions{})
	}
	exec := executionConfigFromRuntime(cfg.Tools.Execution)
	if exec.Mode == tooling.ExecutionModeRelay && exec.RelayNode != "" {
		center := exec.RelayCenter
		if center == "" {
			center = cfg.Control.CenterURL
		}
		token := exec.RelayToken
		if token == "" {
			token = cfg.Control.CenterToken
		}
		return sandbox.NewRemote(sandbox.RemoteOptions{
			WorkspaceDir: cfg.WorkspaceDir,
			TempDir:      cfg.TempDir,
			CenterURL:    center,
			NodeID:       exec.RelayNode,
			Token:        token,
			Execution:    exec,
		})
	}
	return sandbox.NewLocal(sandbox.LocalOptions{
		WorkspaceDir: cfg.WorkspaceDir,
		TempDir:      cfg.TempDir,
		Execution:    exec,
	})
}

func (a *Agent) ensureSandbox() sandbox.Sandbox {
	if a == nil {
		return sandbox.NewLocal(sandbox.LocalOptions{})
	}
	if a.sandbox == nil {
		a.sandbox = sandboxFromConfig(a.cfg)
	}
	return a.sandbox
}

func (a *Agent) SandboxID() string {
	return a.ensureSandbox().ID()
}

func (a *Agent) SandboxBinding() sandbox.ToolBinding {
	return a.ensureSandbox().ToolBinding()
}

func (a *Agent) SandboxInfo() sandbox.Info {
	return a.ensureSandbox().Info()
}

// SandboxScope returns the scope the agent's sandbox is bound to (roadmap
// 6.2), or "" when unspecified (shared org layer).
func (a *Agent) SandboxScope() scope.Id {
	return a.ensureSandbox().ScopeID()
}

func (a *Agent) RebuildSandbox() sandbox.Info {
	if a == nil {
		return sandbox.Info{}
	}
	a.sandbox = a.ensureSandbox().Rebuild()
	return a.sandbox.Info()
}

// newWorkspaceFSForExecution creates a workspacefs.FS for the given execution
// mode.  For local mode the OS-backed FS is returned; for SSH mode an afero
// SFTP-backed FS is returned.  The caller receives a nil FS when the workspace
// directory is empty or SSH client creation fails (tools gracefully degrade).
func newWorkspaceFSForExecution(workspaceDir string, execution tooling.ExecutionConfig, readAllowlist ...string) workspacefs.FS {
	if execution.Mode == tooling.ExecutionModeSSH {
		fs, err := workspacefs.NewSSHFS(workspacefs.SSHConfig{
			Target:     execution.SSHTarget,
			Workspace:  execution.SSHWorkspace,
			SSHOptions: execution.SSHOptions,
		})
		if err != nil {
			return nil
		}
		return fs
	}
	fs, _ := workspacefs.New(workspaceDir, readAllowlist...)
	return fs
}
