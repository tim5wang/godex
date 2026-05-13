package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

func TestStableLocalIDIsStableOpaqueAndCleansPath(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	withNoise := "  " + filepath.Join(workspace, "nested", "..") + "  "

	first := StableLocalID(withNoise)
	second := StableLocalID(filepath.Clean(workspace))

	if first == "" {
		t.Fatal("expected non-empty stable local ID")
	}
	if !strings.HasPrefix(first, "sandbox:local:") {
		t.Fatalf("expected sandbox local prefix, got %q", first)
	}
	if first != second {
		t.Fatalf("expected clean path to produce stable ID, got %q and %q", first, second)
	}
	if len(strings.TrimPrefix(first, "sandbox:local:")) != 12 {
		t.Fatalf("expected 12 hex chars, got %q", first)
	}
}

func TestNewLocalCleansPathsDefaultsAndClonesExecution(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	artifact := filepath.Join(workspace, "artifacts", "..", "artifacts")
	opts := LocalOptions{
		WorkspaceDir: " " + filepath.Join(workspace, ".") + " ",
		ArtifactDir:  " " + artifact + " ",
		Execution: tooling.ExecutionConfig{
			Mode:               tooling.ExecutionModeDocker,
			ShellAllowPatterns: []string{"go test"},
			ShellDenyPatterns:  []string{"rm -rf"},
		},
	}

	sb := NewLocal(opts)
	opts.Execution.ShellAllowPatterns[0] = "mutated"
	opts.Execution.ShellDenyPatterns[0] = "mutated"

	if sb.Lifecycle() != LifecycleLocal {
		t.Fatalf("expected local lifecycle, got %q", sb.Lifecycle())
	}
	if sb.WorkspaceDir() != filepath.Clean(workspace) {
		t.Fatalf("unexpected workspace dir %q", sb.WorkspaceDir())
	}
	if sb.TempDir() != tooling.DefaultCommandOutputDir(filepath.Clean(workspace)) {
		t.Fatalf("unexpected temp dir %q", sb.TempDir())
	}
	if sb.ArtifactDir() != filepath.Clean(artifact) {
		t.Fatalf("unexpected artifact dir %q", sb.ArtifactDir())
	}

	binding := sb.ToolBinding()
	if binding.SandboxID != sb.ID() {
		t.Fatalf("binding sandbox ID %q does not match %q", binding.SandboxID, sb.ID())
	}
	if binding.Execution.ShellAllowPatterns[0] != "go test" {
		t.Fatalf("execution was not cloned on store: %#v", binding.Execution.ShellAllowPatterns)
	}
	if binding.Execution.ShellDenyPatterns[0] != "rm -rf" {
		t.Fatalf("execution was not cloned on store: %#v", binding.Execution.ShellDenyPatterns)
	}
}

func TestToolBindingReturnsExecutionClone(t *testing.T) {
	sb := NewLocal(LocalOptions{
		WorkspaceDir: t.TempDir(),
		Execution: tooling.ExecutionConfig{
			ShellAllowPatterns: []string{"go test"},
			ShellDenyPatterns:  []string{"rm -rf"},
		},
	})

	binding := sb.ToolBinding()
	binding.Execution.ShellAllowPatterns[0] = "mutated allow"
	binding.Execution.ShellDenyPatterns[0] = "mutated deny"

	again := sb.ToolBinding()
	if again.Execution.ShellAllowPatterns[0] != "go test" {
		t.Fatalf("allow patterns mutated through returned binding: %#v", again.Execution.ShellAllowPatterns)
	}
	if again.Execution.ShellDenyPatterns[0] != "rm -rf" {
		t.Fatalf("deny patterns mutated through returned binding: %#v", again.Execution.ShellDenyPatterns)
	}
}

func TestRebuildPreservesIDAndCopiesState(t *testing.T) {
	workspace := t.TempDir()
	sb := NewLocal(LocalOptions{
		ID:           "sandbox:local:custom",
		WorkspaceDir: workspace,
		TempDir:      filepath.Join(workspace, "tmp"),
		ArtifactDir:  filepath.Join(workspace, "artifacts"),
		Execution: tooling.ExecutionConfig{
			ShellAllowPatterns: []string{"go test"},
		},
	})

	rebuilt := sb.Rebuild()

	if rebuilt == sb {
		t.Fatal("expected rebuild to return a new sandbox")
	}
	if rebuilt.ID() != sb.ID() {
		t.Fatalf("expected rebuild to preserve ID, got %q want %q", rebuilt.ID(), sb.ID())
	}
	if rebuilt.ToolBinding().WorkspaceDir != sb.WorkspaceDir() {
		t.Fatalf("expected rebuild to preserve workspace dir")
	}
	binding := rebuilt.ToolBinding()
	binding.Execution.ShellAllowPatterns[0] = "mutated"
	if rebuilt.ToolBinding().Execution.ShellAllowPatterns[0] != "go test" {
		t.Fatalf("expected rebuilt execution to be cloned")
	}
}

func TestFileSystemReturnsWorkspaceView(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sb := NewLocal(LocalOptions{WorkspaceDir: workspace})

	root, err := sb.FileSystem()
	if err != nil {
		t.Fatalf("filesystem: %v", err)
	}
	defer root.Close()

	data, err := root.ReadFile("note.txt")
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected data %q", data)
	}
	if root.Dir() != filepath.Clean(workspace) {
		t.Fatalf("unexpected workspace root %q", root.Dir())
	}
}
