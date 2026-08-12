package agent

import (
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"github.com/tim5wang/godex/internal/sandbox"
)

func TestAgentHasDefaultLocalSandbox(t *testing.T) {
	a := newTestAgent(t, 4096)

	info := a.SandboxInfo()
	if info.ID == "" {
		t.Fatalf("expected sandbox id")
	}
	if info.Lifecycle != sandbox.LifecycleLocal {
		t.Fatalf("sandbox lifecycle %q, want %q", info.Lifecycle, sandbox.LifecycleLocal)
	}
	if info.WorkspaceDir != filepath.Clean(a.cfg.WorkspaceDir) {
		t.Fatalf("sandbox workspace %q, want %q", info.WorkspaceDir, filepath.Clean(a.cfg.WorkspaceDir))
	}
	if info.TempDir != filepath.Clean(a.cfg.TempDir) {
		t.Fatalf("sandbox temp dir %q, want %q", info.TempDir, filepath.Clean(a.cfg.TempDir))
	}
}

func TestAgentSandboxBindingClonesExecutionConfig(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.cfg.Tools.Execution.Mode = tooling.ExecutionModeDocker
	a.cfg.Tools.Execution.ShellAllowPatterns = []string{"go test"}
	a.sandbox = localSandboxFromConfig(a.cfg)

	binding := a.SandboxBinding()
	binding.Execution.ShellAllowPatterns[0] = "changed"

	fresh := a.SandboxBinding()
	if fresh.SandboxID != a.SandboxID() {
		t.Fatalf("binding sandbox id %q, want %q", fresh.SandboxID, a.SandboxID())
	}
	if fresh.Execution.ShellAllowPatterns[0] != "go test" {
		t.Fatalf("execution config was not cloned, got %#v", fresh.Execution.ShellAllowPatterns)
	}
}

func TestAgentRebuildSandboxPreservesIdentity(t *testing.T) {
	a := newTestAgent(t, 4096)
	before := a.SandboxInfo()

	after := a.RebuildSandbox()
	if after.ID != before.ID {
		t.Fatalf("rebuild changed sandbox id: %q -> %q", before.ID, after.ID)
	}
	if after.WorkspaceDir != before.WorkspaceDir {
		t.Fatalf("rebuild changed workspace: %q -> %q", before.WorkspaceDir, after.WorkspaceDir)
	}
}

func TestNewAgentWithDependenciesBuildsSandboxFallback(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.sandbox = nil

	if got := a.SandboxID(); got == "" {
		t.Fatalf("expected fallback sandbox id")
	}
	if a.sandbox == nil {
		t.Fatalf("expected fallback sandbox to be stored")
	}
}

func TestSandboxInfoReturnsCopy(t *testing.T) {
	a := newTestAgent(t, 4096)

	info := a.SandboxInfo()
	info.ID = "changed"

	if a.SandboxInfo().ID == "changed" {
		t.Fatalf("sandbox info was not returned as a copy")
	}
}

// fakeSandbox is a minimal Sandbox implementation used to prove the agent
// depends on the interface rather than the concrete local sandbox.
type fakeSandbox struct {
	id    string
	wsDir string
	scope scope.Id
}

func (f fakeSandbox) ID() string                       { return f.id }
func (f fakeSandbox) Lifecycle() sandbox.Lifecycle    { return "fake" }
func (f fakeSandbox) WorkspaceDir() string            { return f.wsDir }
func (f fakeSandbox) TempDir() string                 { return "" }
func (f fakeSandbox) ArtifactDir() string             { return "" }
func (f fakeSandbox) ScopeID() scope.Id               { return f.scope }
func (f fakeSandbox) ToolBinding() sandbox.ToolBinding {
	return sandbox.ToolBinding{SandboxID: f.id, WorkspaceDir: f.wsDir}
}
func (f fakeSandbox) Info() sandbox.Info {
	return sandbox.Info{ID: f.id, WorkspaceDir: f.wsDir, Lifecycle: "fake"}
}
func (f fakeSandbox) FileSystem() (workspacefs.FS, error) { return workspacefs.New(f.wsDir) }
func (f fakeSandbox) Rebuild() sandbox.Sandbox            { return f }

func TestAgentConsumesSandboxThroughInterface(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.sandbox = fakeSandbox{id: "sandbox:fake:abc", wsDir: filepath.Clean(a.cfg.WorkspaceDir)}

	if got := a.SandboxID(); got != "sandbox:fake:abc" {
		t.Fatalf("expected agent to use injected sandbox id, got %q", got)
	}
	binding := a.SandboxBinding()
	if binding.SandboxID != "sandbox:fake:abc" {
		t.Fatalf("expected agent binding to use injected sandbox, got %+v", binding)
	}
	info := a.SandboxInfo()
	if info.ID != "sandbox:fake:abc" {
		t.Fatalf("expected agent info to use injected sandbox, got %+v", info)
	}
}

func TestAgentRebuildSandboxThroughInterface(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.sandbox = fakeSandbox{id: "sandbox:fake:keep", wsDir: filepath.Clean(a.cfg.WorkspaceDir)}

	after := a.RebuildSandbox()
	if after.ID != "sandbox:fake:keep" {
		t.Fatalf("expected rebuild to preserve fake sandbox identity, got %+v", after)
	}
}
