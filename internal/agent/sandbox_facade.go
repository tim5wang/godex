package agent

import (
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"github.com/tim5wang/godex/internal/sandbox"
)

func localSandboxFromConfig(cfg *config.Config) sandbox.Sandbox {
	if cfg == nil {
		return sandbox.NewLocal(sandbox.LocalOptions{})
	}
	return sandbox.NewLocal(sandbox.LocalOptions{
		WorkspaceDir: cfg.WorkspaceDir,
		TempDir:      cfg.TempDir,
		Execution:    executionConfigFromRuntime(cfg.Tools.Execution),
	})
}

func (a *Agent) ensureSandbox() sandbox.Sandbox {
	if a == nil {
		return sandbox.NewLocal(sandbox.LocalOptions{})
	}
	if a.sandbox == nil {
		a.sandbox = localSandboxFromConfig(a.cfg)
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
func newWorkspaceFSForExecution(workspaceDir string, execution tooling.ExecutionConfig) workspacefs.FS {
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
	fs, _ := workspacefs.New(workspaceDir)
	return fs
}
