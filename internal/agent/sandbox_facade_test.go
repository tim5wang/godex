package agent

import (
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
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
