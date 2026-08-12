package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/scope"
	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

type Lifecycle string

const LifecycleLocal Lifecycle = "local"

type LocalOptions struct {
	ID           string
	Scope        scope.Id
	WorkspaceDir string
	TempDir      string
	ArtifactDir  string
	Execution    tooling.ExecutionConfig
	Lifecycle    Lifecycle
}

type ToolBinding struct {
	SandboxID    string
	WorkspaceDir string
	TempDir      string
	ArtifactDir  string
	Execution    tooling.ExecutionConfig
}

type Info struct {
	ID           string    `json:"sandbox_id"`
	Lifecycle    Lifecycle `json:"lifecycle"`
	WorkspaceDir string    `json:"workspace_dir"`
	TempDir      string    `json:"temp_dir,omitempty"`
	ArtifactDir  string    `json:"artifact_dir,omitempty"`
}

// Sandbox abstracts the execution environment an agent runs in, mirroring the
// reference Sandbox interface (temp/qm/src/sandbox/sandbox.ts) trimmed to the
// capabilities the Go agent consumes. Concrete implementations (see
// LocalSandbox) provide identity, workspace/temp/artifact layout, tool binding,
// a file-system view and rebuild semantics.
type Sandbox interface {
	ID() string
	Lifecycle() Lifecycle
	WorkspaceDir() string
	TempDir() string
	ArtifactDir() string
	// ScopeID returns the scope this sandbox is bound to (roadmap 6.2), or
	// "" when unspecified (org/shared layer).
	ScopeID() scope.Id
	ToolBinding() ToolBinding
	Info() Info
	FileSystem() (workspacefs.FS, error)
	// Rebuild returns a fresh sandbox preserving identity and configuration.
	Rebuild() Sandbox
}

// LocalSandbox is the default local-filesystem Sandbox implementation.
type LocalSandbox struct {
	id           string
	scope        scope.Id
	lifecycle    Lifecycle
	workspaceDir string
	tempDir      string
	artifactDir  string
	execution    tooling.ExecutionConfig
}

var _ Sandbox = (*LocalSandbox)(nil)

func StableLocalID(workspaceDir string) string {
	workspaceDir = filepath.Clean(strings.TrimSpace(workspaceDir))
	sum := sha256.Sum256([]byte(workspaceDir))
	return "sandbox:local:" + hex.EncodeToString(sum[:])[:12]
}

func NewLocal(opts LocalOptions) *LocalSandbox {
	workspaceDir := cleanPath(opts.WorkspaceDir)
	tempDir := cleanPath(opts.TempDir)
	artifactDir := cleanPath(opts.ArtifactDir)
	// Roadmap 6.2: when a session/personal scope is set, temp and artifact
	// dirs default into a per-scope subdirectory so concurrent sessions never
	// share scratch state. Org/unspecified scopes keep the shared defaults.
	scopeKey := ""
	if kind, _, ok := scope.Parse(opts.Scope); ok && kind != scope.KindOrg {
		scopeKey = scope.StorageKey(opts.Scope)
	}
	if tempDir == "" && workspaceDir != "" {
		if scopeKey != "" {
			tempDir = filepath.Join(workspaceDir, ".godex", "tmp", scopeKey)
		} else {
			tempDir = tooling.DefaultCommandOutputDir(workspaceDir)
		}
	}
	if artifactDir == "" && workspaceDir != "" && scopeKey != "" {
		artifactDir = filepath.Join(workspaceDir, ".godex", "artifacts", scopeKey)
	}
	lifecycle := opts.Lifecycle
	if lifecycle == "" {
		lifecycle = LifecycleLocal
	}
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = StableLocalID(workspaceDir)
	}
	return &LocalSandbox{
		id:           id,
		scope:        opts.Scope,
		lifecycle:    lifecycle,
		workspaceDir: workspaceDir,
		tempDir:      tempDir,
		artifactDir:  artifactDir,
		execution:    cloneExecution(opts.Execution),
	}
}

func (s *LocalSandbox) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *LocalSandbox) Lifecycle() Lifecycle {
	if s == nil {
		return ""
	}
	return s.lifecycle
}

func (s *LocalSandbox) WorkspaceDir() string {
	if s == nil {
		return ""
	}
	return s.workspaceDir
}

func (s *LocalSandbox) TempDir() string {
	if s == nil {
		return ""
	}
	return s.tempDir
}

func (s *LocalSandbox) ArtifactDir() string {
	if s == nil {
		return ""
	}
	return s.artifactDir
}

// ScopeID returns the scope this sandbox is bound to, or "" when unspecified.
func (s *LocalSandbox) ScopeID() scope.Id {
	if s == nil {
		return ""
	}
	return s.scope
}

func (s *LocalSandbox) ToolBinding() ToolBinding {
	if s == nil {
		return ToolBinding{}
	}
	return ToolBinding{
		SandboxID:    s.id,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
		Execution:    cloneExecution(s.execution),
	}
}

func (s *LocalSandbox) Info() Info {
	if s == nil {
		return Info{}
	}
	return Info{
		ID:           s.id,
		Lifecycle:    s.lifecycle,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
	}
}

func (s *LocalSandbox) FileSystem() (workspacefs.FS, error) {
	if s == nil {
		return workspacefs.New("")
	}
	return workspacefs.New(s.workspaceDir)
}

func (s *LocalSandbox) Rebuild() Sandbox {
	if s == nil {
		return nil
	}
	return NewLocal(LocalOptions{
		ID:           s.id,
		Scope:        s.scope,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
		Execution:    cloneExecution(s.execution),
		Lifecycle:    s.lifecycle,
	})
}

func cloneExecution(cfg tooling.ExecutionConfig) tooling.ExecutionConfig {
	cfg.SSHOptions = append([]string{}, cfg.SSHOptions...)
	cfg.ShellAllowPatterns = append([]string{}, cfg.ShellAllowPatterns...)
	cfg.ShellDenyPatterns = append([]string{}, cfg.ShellDenyPatterns...)
	return cfg
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
