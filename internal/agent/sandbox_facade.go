package agent

import (
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/sandbox"
)

func localSandboxFromConfig(cfg *config.Config) *sandbox.Sandbox {
	if cfg == nil {
		return sandbox.NewLocal(sandbox.LocalOptions{})
	}
	return sandbox.NewLocal(sandbox.LocalOptions{
		WorkspaceDir: cfg.WorkspaceDir,
		TempDir:      cfg.TempDir,
		Execution:    executionConfigFromRuntime(cfg.Tools.Execution),
	})
}

func (a *Agent) ensureSandbox() *sandbox.Sandbox {
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
