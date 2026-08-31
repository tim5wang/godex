# GoDex Phase 2 Sandbox Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce an explicit local sandbox boundary so workspace path, temp/artifact policy, execution config, and worker sandbox references are modeled separately from Agent identity while preserving current local behavior.

**Architecture:** Add a narrow `internal/sandbox` package that describes local sandbox metadata and tool bindings. Attach one default local sandbox to each `Agent`, route existing tool registration through the sandbox binding, propagate sandbox IDs through tool contexts, and persist sandbox IDs on durable subagent jobs without changing storage layout compatibility or tool schemas.

**Tech Stack:** Go, existing `internal/agent`, `internal/tools`, `internal/toolruntime`, `internal/platform/workspacefs`, `internal/platform/tooling`, `go test ./internal/sandbox ./internal/toolruntime ./internal/tools ./internal/agent`, `go test ./...`.

---

## Scope

This plan implements only Phase 2 from `docs/architecture-v2-spec.md`: explicit local sandbox boundary.

Included:

- Stable opaque sandbox ID for the current local workspace.
- Local sandbox lifecycle metadata and rebuild operation.
- Workspace filesystem view through existing `workspacefs.FS`.
- Tool runtime binding for workspace directory, temp directory, artifact directory, execution config, and sandbox ID.
- Agent-owned default local sandbox.
- Tool registration that uses sandbox binding instead of directly reading `cfg.WorkspaceDir` and `cfg.TempDir`.
- Tool execution context carries `sandbox_id`.
- Durable subagent jobs persist and expose `sandbox_id`.

Excluded:

- Remote sandbox execution.
- Disposable sandbox lifecycle creation.
- Storage backend migration.
- Session Graph branch model.
- Worker Runtime Protocol from Phase 3.
- Product UI pages for Sandbox Inspector.

## Target File Structure

- Create: `internal/sandbox/sandbox.go`
  - Owns local sandbox object, ID generation, lifecycle metadata, workspace/temp/artifact policy, file-system access, and tool binding.
- Create: `internal/sandbox/sandbox_test.go`
  - Unit tests for ID stability, path normalization, tool binding clones, and rebuild preserving ID.
- Modify: `internal/agent/agent.go`
  - Add `sandbox *sandbox.Sandbox` field and dependency field.
- Modify: `internal/agent/agent_wiring.go`
  - Build default local sandbox from config and provide fallback when tests construct dependencies manually.
- Create: `internal/agent/sandbox_facade.go`
  - Exposes `SandboxID`, `SandboxBinding`, `SandboxInfo`, and local sandbox construction helper.
- Create: `internal/agent/sandbox_facade_test.go`
  - Tests that Agent has a default local sandbox and that rebuild keeps identity stable.
- Modify: `internal/agent/tool_registration.go`
  - Use `a.SandboxBinding()` for workspace/temp/execution when registering workspace-sensitive tools.
- Create: `internal/agent/tool_sandbox_test.go`
  - Verifies workspace tools are bound to `Agent` sandbox rather than direct config paths.
- Modify: `internal/toolruntime/runtime_context.go`
  - Add `WithSandboxID` and `SandboxIDFromContext`.
- Modify: `internal/tools/toolruntime_aliases.go`
  - Re-export sandbox context helpers through the `tools` package.
- Modify: `internal/agent/tool_execution.go`
  - Annotate tool execution contexts with the agent sandbox ID.
- Create: `internal/toolruntime/sandbox_context_test.go`
  - Tests sandbox ID context helper behavior.
- Modify: `internal/agent/subagent_jobs.go`
  - Persist `SandboxID` on durable subagent jobs and public job views.
- Modify: `internal/agent/subagent_tool.go`
  - Include `sandbox_id` in compact model-visible subagent job views and legacy `formatSubagentJob`.
- Create or modify: `internal/agent/subagent_jobs_test.go`
  - Add focused tests that subagent jobs expose sandbox IDs.
- Modify: `docs/architecture-v2-spec.md`
  - Add a short Phase 2 implementation note after Phase 2 acceptance criteria.
- Modify: `docs/architecture-v2-spec.en.md`
  - Mirror the Phase 2 implementation note.

## Behavior Invariants

- Current local workspace behavior remains default.
- Public tool names and input schemas remain unchanged.
- `cfg.WorkspaceDir`, `cfg.TempDir`, and `cfg.Tools.Execution` still determine the default local tool runtime behavior.
- Existing JSON subagent jobs without `sandbox_id` still load successfully.
- Sandbox IDs are opaque strings; code must not parse an ID prefix for behavior.
- Rebuilding a local sandbox from the same options preserves `agent_id`, `session_id`, `branch_id`, and sandbox ID.
- Subagent isolated workspaces continue to use existing worktree/snapshot logic.

---

### Task 1: Add Local Sandbox Model

**Files:**
- Create: `internal/sandbox/sandbox.go`
- Create: `internal/sandbox/sandbox_test.go`

- [ ] **Step 1: Write sandbox model tests**

Create `internal/sandbox/sandbox_test.go`:

```go
package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

func TestStableLocalIDIsOpaqueAndStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	first := StableLocalID(dir)
	second := StableLocalID(filepath.Clean(dir))

	if first == "" {
		t.Fatalf("expected non-empty sandbox id")
	}
	if first != second {
		t.Fatalf("expected stable id, got %q and %q", first, second)
	}
	if !strings.HasPrefix(first, "sandbox:local:") {
		t.Fatalf("expected debuggable local prefix, got %q", first)
	}
}

func TestLocalSandboxBindingClonesExecution(t *testing.T) {
	workspace := t.TempDir()
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	sb := NewLocal(LocalOptions{
		WorkspaceDir: workspace,
		TempDir:      tempDir,
		Execution: tooling.ExecutionConfig{
			Mode:               tooling.ExecutionModeDocker,
			DockerImage:        "golang:1.26",
			ShellAllowPatterns: []string{"go test"},
		},
	})

	binding := sb.ToolBinding()
	binding.Execution.ShellAllowPatterns[0] = "changed"

	fresh := sb.ToolBinding()
	if fresh.SandboxID != sb.ID() {
		t.Fatalf("binding sandbox id %q, want %q", fresh.SandboxID, sb.ID())
	}
	if fresh.WorkspaceDir != filepath.Clean(workspace) {
		t.Fatalf("binding workspace %q, want %q", fresh.WorkspaceDir, filepath.Clean(workspace))
	}
	if fresh.TempDir != filepath.Clean(tempDir) {
		t.Fatalf("binding temp dir %q, want %q", fresh.TempDir, filepath.Clean(tempDir))
	}
	if got := fresh.Execution.ShellAllowPatterns[0]; got != "go test" {
		t.Fatalf("execution config was not cloned, got %q", got)
	}
}

func TestLocalSandboxRebuildPreservesID(t *testing.T) {
	workspace := t.TempDir()
	sb := NewLocal(LocalOptions{WorkspaceDir: workspace})

	rebuilt := sb.Rebuild()
	if rebuilt.ID() != sb.ID() {
		t.Fatalf("rebuild changed id: %q -> %q", sb.ID(), rebuilt.ID())
	}
	if rebuilt.WorkspaceDir() != sb.WorkspaceDir() {
		t.Fatalf("rebuild changed workspace: %q -> %q", sb.WorkspaceDir(), rebuilt.WorkspaceDir())
	}
}

func TestLocalSandboxFileSystemUsesWorkspaceView(t *testing.T) {
	workspace := t.TempDir()
	sb := NewLocal(LocalOptions{WorkspaceDir: workspace})

	fs, err := sb.FileSystem()
	if err != nil {
		t.Fatalf("open filesystem: %v", err)
	}
	defer fs.Close()

	if fs.Dir() != filepath.Clean(workspace) {
		t.Fatalf("filesystem dir %q, want %q", fs.Dir(), filepath.Clean(workspace))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/sandbox -count=1
```

Expected: fail because package `internal/sandbox` does not exist.

- [ ] **Step 3: Implement local sandbox model**

Create `internal/sandbox/sandbox.go`:

```go
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

type Lifecycle string

const (
	LifecycleLocal Lifecycle = "local"
)

type LocalOptions struct {
	ID           string
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

type Sandbox struct {
	id           string
	lifecycle    Lifecycle
	workspaceDir string
	tempDir      string
	artifactDir  string
	execution    tooling.ExecutionConfig
}

func StableLocalID(workspaceDir string) string {
	clean := cleanPath(workspaceDir)
	sum := sha256.Sum256([]byte(clean))
	return "sandbox:local:" + hex.EncodeToString(sum[:])[:12]
}

func NewLocal(opts LocalOptions) *Sandbox {
	workspace := cleanPath(opts.WorkspaceDir)
	tempDir := cleanPath(opts.TempDir)
	if tempDir == "" && workspace != "" {
		tempDir = tooling.DefaultCommandOutputDir(workspace)
	}
	artifactDir := cleanPath(opts.ArtifactDir)
	if artifactDir == "" {
		artifactDir = tempDir
	}
	lifecycle := opts.Lifecycle
	if lifecycle == "" {
		lifecycle = LifecycleLocal
	}
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = StableLocalID(workspace)
	}
	return &Sandbox{
		id:           id,
		lifecycle:    lifecycle,
		workspaceDir: workspace,
		tempDir:      tempDir,
		artifactDir:  artifactDir,
		execution:    cloneExecution(opts.Execution),
	}
}

func (s *Sandbox) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *Sandbox) Lifecycle() Lifecycle {
	if s == nil {
		return ""
	}
	return s.lifecycle
}

func (s *Sandbox) WorkspaceDir() string {
	if s == nil {
		return ""
	}
	return s.workspaceDir
}

func (s *Sandbox) TempDir() string {
	if s == nil {
		return ""
	}
	return s.tempDir
}

func (s *Sandbox) ArtifactDir() string {
	if s == nil {
		return ""
	}
	return s.artifactDir
}

func (s *Sandbox) Execution() tooling.ExecutionConfig {
	if s == nil {
		return tooling.ExecutionConfig{}
	}
	return cloneExecution(s.execution)
}

func (s *Sandbox) ToolBinding() ToolBinding {
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

func (s *Sandbox) Info() Info {
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

func (s *Sandbox) FileSystem() (*workspacefs.FS, error) {
	if s == nil {
		return workspacefs.New("")
	}
	return workspacefs.New(s.workspaceDir)
}

func (s *Sandbox) Rebuild() *Sandbox {
	if s == nil {
		return nil
	}
	return NewLocal(LocalOptions{
		ID:           s.id,
		WorkspaceDir: s.workspaceDir,
		TempDir:      s.tempDir,
		ArtifactDir:  s.artifactDir,
		Execution:    cloneExecution(s.execution),
		Lifecycle:    s.lifecycle,
	})
}

func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if abs, err := filepath.Abs(value); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(value)
}

func cloneExecution(cfg tooling.ExecutionConfig) tooling.ExecutionConfig {
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
```

- [ ] **Step 4: Run sandbox tests**

Run:

```bash
go test ./internal/sandbox -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go
git commit -m "feat(sandbox): add local sandbox model"
```

---

### Task 2: Attach Default Local Sandbox To Agent

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/agent_wiring.go`
- Create: `internal/agent/sandbox_facade.go`
- Create: `internal/agent/sandbox_facade_test.go`

- [ ] **Step 1: Write Agent sandbox tests**

Create `internal/agent/sandbox_facade_test.go`:

```go
package agent

import (
	"path/filepath"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
	"github.com/tim5wang/godex/internal/sandbox"
)

func TestAgentHasDefaultLocalSandbox(t *testing.T) {
	a := newTestAgent(t, 4096)

	if a.SandboxID() == "" {
		t.Fatalf("expected sandbox id")
	}
	binding := a.SandboxBinding()
	if binding.SandboxID != a.SandboxID() {
		t.Fatalf("binding sandbox id %q, want %q", binding.SandboxID, a.SandboxID())
	}
	if binding.WorkspaceDir != filepath.Clean(a.cfg.WorkspaceDir) {
		t.Fatalf("binding workspace %q, want %q", binding.WorkspaceDir, filepath.Clean(a.cfg.WorkspaceDir))
	}
	if binding.TempDir != filepath.Clean(a.cfg.TempDir) {
		t.Fatalf("binding temp dir %q, want %q", binding.TempDir, filepath.Clean(a.cfg.TempDir))
	}
}

func TestAgentSandboxRebuildPreservesID(t *testing.T) {
	a := newTestAgent(t, 4096)
	before := a.SandboxID()
	rebuilt := a.sandbox.Rebuild()

	if rebuilt.ID() != before {
		t.Fatalf("rebuild changed sandbox id: %q -> %q", before, rebuilt.ID())
	}
}

func TestNewAgentWithDependenciesCreatesSandboxFallback(t *testing.T) {
	a := newTestAgent(t, 4096)
	deps := buildDependencies(a.cfg)
	deps.sandbox = nil

	rebuilt := newAgentWithDependencies(a.cfg, deps)
	if rebuilt.SandboxID() == "" {
		t.Fatalf("expected fallback sandbox id")
	}
	if rebuilt.SandboxBinding().Execution.Mode != tooling.ExecutionModeLocal {
		t.Fatalf("expected default local execution mode, got %q", rebuilt.SandboxBinding().Execution.Mode)
	}
}

func TestAgentSandboxInfoReturnsCopy(t *testing.T) {
	a := newTestAgent(t, 4096)
	info := a.SandboxInfo()
	info.ID = "changed"

	fresh := a.SandboxInfo()
	if fresh.ID != a.SandboxID() {
		t.Fatalf("sandbox info must be a value copy, got %q want %q", fresh.ID, a.SandboxID())
	}
	if fresh.Lifecycle != sandbox.LifecycleLocal {
		t.Fatalf("expected local lifecycle, got %q", fresh.Lifecycle)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run 'TestAgent.*Sandbox|TestNewAgentWithDependenciesCreatesSandboxFallback' -count=1
```

Expected: fail because `Agent` has no sandbox facade yet.

- [ ] **Step 3: Add sandbox field and dependency**

Modify `internal/agent/agent.go` imports and structs:

```go
import (
	"context"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/core/background"
	"github.com/tim5wang/godex/internal/core/compress"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/instructions"
	"github.com/tim5wang/godex/internal/core/mcp"
	"github.com/tim5wang/godex/internal/core/media"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/contracts/protocol"
	"github.com/tim5wang/godex/internal/core/skill"
	"github.com/tim5wang/godex/internal/core/teammate"
	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/domain/task"
	"github.com/tim5wang/godex/internal/domain/todo"
	"github.com/tim5wang/godex/internal/sandbox"
	"github.com/tim5wang/godex/internal/services/historysearch"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/tools"
)
```

Add field to `Agent`:

```go
sandbox *sandbox.Sandbox
```

Add field to `dependencies`:

```go
sandbox *sandbox.Sandbox
```

- [ ] **Step 4: Build local sandbox in wiring**

Modify `internal/agent/agent_wiring.go`.

In `buildDependencies`, add:

```go
	localSandbox := localSandboxFromConfig(cfg)
```

In returned `dependencies`, add:

```go
		sandbox:      localSandbox,
```

In `newAgentWithDependencies`, before constructing `agent`, add:

```go
	if deps.sandbox == nil {
		deps.sandbox = localSandboxFromConfig(cfg)
	}
```

In `Agent` literal, add:

```go
		sandbox:        deps.sandbox,
```

- [ ] **Step 5: Add sandbox facade**

Create `internal/agent/sandbox_facade.go`:

```go
package agent

import (
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/tooling"
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
		Lifecycle:    sandbox.LifecycleLocal,
	})
}

func (a *Agent) SandboxID() string {
	if a == nil || a.sandbox == nil {
		return ""
	}
	return a.sandbox.ID()
}

func (a *Agent) SandboxBinding() sandbox.ToolBinding {
	if a == nil || a.sandbox == nil {
		return sandbox.NewLocal(sandbox.LocalOptions{
			Execution: tooling.ExecutionConfig{},
		}).ToolBinding()
	}
	return a.sandbox.ToolBinding()
}

func (a *Agent) SandboxInfo() sandbox.Info {
	if a == nil || a.sandbox == nil {
		return sandbox.Info{}
	}
	return a.sandbox.Info()
}
```

- [ ] **Step 6: Run Agent sandbox tests**

Run:

```bash
go test ./internal/agent -run 'TestAgent.*Sandbox|TestNewAgentWithDependenciesCreatesSandboxFallback' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_wiring.go internal/agent/sandbox_facade.go internal/agent/sandbox_facade_test.go
git commit -m "feat(agent): attach default local sandbox"
```

---

### Task 3: Route Tool Registration Through Sandbox Binding

**Files:**
- Modify: `internal/agent/tool_registration.go`
- Create: `internal/agent/tool_sandbox_test.go`

- [ ] **Step 1: Write sandbox-bound tool registration test**

Create `internal/agent/tool_sandbox_test.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/sandbox"
)

func TestWorkspaceToolsUseAgentSandboxBinding(t *testing.T) {
	a := newTestAgent(t, 4096)
	otherWorkspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherWorkspace, "sandbox.txt"), []byte("from sandbox"), 0644); err != nil {
		t.Fatalf("write sandbox file: %v", err)
	}

	a.sandbox = sandbox.NewLocal(sandbox.LocalOptions{
		WorkspaceDir: otherWorkspace,
		TempDir:      a.cfg.TempDir,
		Execution:    executionConfigFromRuntime(a.cfg.Tools.Execution),
	})
	a.RegisterTools()

	output, err := a.handleTool(context.Background(), "read_file", map[string]interface{}{"path": "sandbox.txt"})
	if err != nil {
		t.Fatalf("read sandbox file: %v", err)
	}
	if !strings.Contains(output, "from sandbox") {
		t.Fatalf("expected sandbox workspace output, got %q", output)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/agent -run TestWorkspaceToolsUseAgentSandboxBinding -count=1
```

Expected: fail because tools still read from `cfg.WorkspaceDir`.

- [ ] **Step 3: Use sandbox binding in registration**

Modify `internal/agent/tool_registration.go`.

At the start of `registerToolsWith`, replace:

```go
	execution := executionConfigFromRuntime(a.cfg.Tools.Execution)
```

with:

```go
	binding := a.SandboxBinding()
	execution := binding.Execution
	workspaceDir := binding.WorkspaceDir
	tempDir := binding.TempDir
```

Replace workspace-sensitive constructors:

```go
tools.NewBashToolWithExecution(workspaceDir, tempDir, execution)
tools.NewGlobTool(workspaceDir, a.cfg.Tools.Glob.DefaultMaxResults)
tools.NewReadFileTool(workspaceDir)
tools.NewWriteFileTool(workspaceDir)
tools.NewEditFileTool(workspaceDir)
tools.NewAttachFileTool(workspaceDir)
tools.NewBrowserTool(a.browser, workspaceDir)
tools.NewDesktopTool(tools.NewDesktopService(tempDir))
tools.NewBackgroundRunToolWithExecution(a.bgMgr, workspaceDir, tempDir, execution)
tools.NewACPAgentTool(a.cfg.ACP.Agents, workspaceDir)
```

Do not change non-workspace tools.

- [ ] **Step 4: Run tool registration tests**

Run:

```bash
go test ./internal/agent -run 'TestWorkspaceToolsUseAgentSandboxBinding|TestAgentRefactorKeepsDefaultToolCatalogShape|TestBuildContextExposesOnlyActiveToolSchemas|TestRunWithOptions' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/tool_registration.go internal/agent/tool_sandbox_test.go
git commit -m "refactor(agent): bind workspace tools through sandbox"
```

---

### Task 4: Propagate Sandbox ID Through Tool Runtime Context

**Files:**
- Modify: `internal/toolruntime/runtime_context.go`
- Modify: `internal/tools/toolruntime_aliases.go`
- Modify: `internal/agent/tool_execution.go`
- Create: `internal/toolruntime/sandbox_context_test.go`
- Modify: `internal/agent/tool_sandbox_test.go`

- [ ] **Step 1: Write toolruntime sandbox context tests**

Create `internal/toolruntime/sandbox_context_test.go`:

```go
package toolruntime

import (
	"context"
	"testing"
)

func TestSandboxIDContextRoundTrip(t *testing.T) {
	ctx := WithSandboxID(context.Background(), "sandbox:local:test")
	if got := SandboxIDFromContext(ctx); got != "sandbox:local:test" {
		t.Fatalf("sandbox id %q", got)
	}
}

func TestWithSandboxIDIgnoresBlankID(t *testing.T) {
	ctx := WithSandboxID(context.Background(), "")
	if got := SandboxIDFromContext(ctx); got != "" {
		t.Fatalf("blank sandbox id should not be stored, got %q", got)
	}
}
```

- [ ] **Step 2: Extend agent sandbox tool test**

Append to `internal/agent/tool_sandbox_test.go`:

```go
func TestToolExecutionContextIncludesSandboxID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	seenSandboxID := ""
	a.registerTool(tools.NewTypedTool(tools.NewToolSpec("capture_sandbox", "Capture sandbox id", map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}, nil), func(ctx context.Context, args struct{}) (tools.ToolResult, error) {
		seenSandboxID = tools.SandboxIDFromContext(ctx)
		return tools.ToolResult{Text: "ok"}, nil
	}), tools.ToolMeta{AlwaysActive: true})

	if _, err := a.handleTool(context.Background(), "capture_sandbox", map[string]interface{}{}); err != nil {
		t.Fatalf("handle tool: %v", err)
	}
	if seenSandboxID != a.SandboxID() {
		t.Fatalf("tool context sandbox id %q, want %q", seenSandboxID, a.SandboxID())
	}
}
```

Add `github.com/tim5wang/godex/internal/tools` to imports in `tool_sandbox_test.go`.

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/toolruntime -run TestSandboxIDContext -count=1
go test ./internal/agent -run TestToolExecutionContextIncludesSandboxID -count=1
```

Expected: fail because sandbox context helpers are not implemented.

- [ ] **Step 4: Add toolruntime context helpers**

Modify `internal/toolruntime/runtime_context.go`.

Add key type:

```go
type sandboxIDKey struct{}
```

Add functions:

```go
func WithSandboxID(ctx context.Context, sandboxID string) context.Context {
	if sandboxID == "" {
		return ctx
	}
	return context.WithValue(ctx, sandboxIDKey{}, sandboxID)
}

func SandboxIDFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(sandboxIDKey{}).(string); ok {
		return value
	}
	return ""
}
```

- [ ] **Step 5: Re-export helpers through tools package**

Modify `internal/tools/toolruntime_aliases.go`:

```go
func WithSandboxID(ctx context.Context, sandboxID string) context.Context {
	return toolruntime.WithSandboxID(ctx, sandboxID)
}

func SandboxIDFromContext(ctx context.Context) string {
	return toolruntime.SandboxIDFromContext(ctx)
}
```

- [ ] **Step 6: Annotate tool execution context**

Modify `internal/agent/tool_execution.go`.

At the top of `handleToolResult`, before `a.toolHandler.HandleResult`, add:

```go
	ctx = tools.WithSandboxID(ctx, a.SandboxID())
```

Full function start:

```go
func (a *Agent) handleToolResult(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error) {
	ctx = tools.WithSandboxID(ctx, a.SandboxID())
	result, err := a.toolHandler.HandleResult(ctx, name, input)
```

- [ ] **Step 7: Run runtime context tests**

Run:

```bash
go test ./internal/toolruntime -run TestSandboxIDContext -count=1
go test ./internal/agent -run 'TestToolExecutionContextIncludesSandboxID|TestRunWithOptions' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/toolruntime/runtime_context.go internal/tools/toolruntime_aliases.go internal/agent/tool_execution.go internal/toolruntime/sandbox_context_test.go internal/agent/tool_sandbox_test.go
git commit -m "feat(toolruntime): carry sandbox id in tool context"
```

---

### Task 5: Add Sandbox ID To Durable Subagent Jobs

**Files:**
- Modify: `internal/agent/subagent_jobs.go`
- Modify: `internal/agent/subagent_tool.go`
- Modify: `internal/agent/subagent_jobs_test.go`

- [ ] **Step 1: Write durable subagent sandbox tests**

Append to `internal/agent/subagent_jobs_test.go`:

```go
func TestDurableSubagentRecordsSandboxID(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()
	a.client = repeatedTextCaller("done")

	job, err := a.StartDurableSubagentWithContext(context.Background(), "inspect sandbox id", "Explore", nil)
	if err != nil {
		t.Fatalf("start subagent: %v", err)
	}
	waitForSubagentTerminal(t, a, job.ID)

	stored, err := a.subagentJobs.Get(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.SandboxID == "" {
		t.Fatalf("expected stored sandbox id")
	}
	if stored.SandboxID == a.SandboxID() {
		t.Fatalf("expected worker sandbox id to reference prepared workspace, got parent sandbox id %q", stored.SandboxID)
	}

	view := durableSubagentJobView(stored)
	if view.SandboxID != stored.SandboxID {
		t.Fatalf("view sandbox id %q, want %q", view.SandboxID, stored.SandboxID)
	}
}

func TestSubagentModelViewIncludesSandboxID(t *testing.T) {
	job := &subagentJob{
		ID:        "job-1",
		SandboxID: "sandbox:local:worker",
		Status:    subagentStatusCompleted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	view := formatSubagentModelJob(job)
	if view.SandboxID != "sandbox:local:worker" {
		t.Fatalf("model view sandbox id %q", view.SandboxID)
	}
}
```

If `waitForSubagentTerminal` is not available in the test file, add this helper near other test helpers:

```go
func waitForSubagentTerminal(t *testing.T, a *Agent, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := a.subagentJobs.Get(id)
		if err == nil && subagentStatusTerminal(job.Status) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := a.subagentJobs.Get(id)
	if err != nil {
		t.Fatalf("get subagent after wait: %v", err)
	}
	t.Fatalf("subagent did not finish, status=%s error=%s", job.Status, job.Error)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run 'TestDurableSubagentRecordsSandboxID|TestSubagentModelViewIncludesSandboxID' -count=1
```

Expected: fail because `SandboxID` fields do not exist.

- [ ] **Step 3: Add sandbox fields to durable job types**

Modify `internal/agent/subagent_jobs.go`.

Add to `subagentJob`:

```go
	SandboxID string `json:"sandbox_id,omitempty"`
```

Add to `DurableSubagentJobView`:

```go
	SandboxID string `json:"sandbox_id,omitempty"`
```

Add to `subagentStartOptions`:

```go
	SandboxID string
```

In `StartWithOptions`, where the `subagentJob` is constructed, set:

```go
		SandboxID: strings.TrimSpace(opts.SandboxID),
```

- [ ] **Step 4: Set parent sandbox ID at start**

Modify `startDurableSubagentWithContext` in `internal/agent/subagent_jobs.go`.

Add to `subagentStartOptions` literal:

```go
		SandboxID:      a.SandboxID(),
```

- [ ] **Step 5: Update sandbox ID after worker workspace preparation**

Modify `subagentJobStore.SetWorkspace` signature:

```go
func (s *subagentJobStore) SetWorkspace(id, worktreeDir, baselineDir, isolation, gitBranch, workspaceOrigin, sandboxID string) (*subagentJob, error)
```

Inside it, after setting `WorkspaceOrigin`, add:

```go
	job.SandboxID = strings.TrimSpace(sandboxID)
```

Import `github.com/tim5wang/godex/internal/sandbox` in `internal/agent/subagent_jobs.go`, then update the three workspace preparation return sites exactly:

```go
return a.subagentJobs.SetWorkspace(job.ID, workspace, "", subagentIsolationSharedReadOnly, "", "shared_workspace", sandbox.StableLocalID(workspace))
```

```go
return a.subagentJobs.SetWorkspace(job.ID, workspace, baselineDir, subagentIsolationSharedApproval, "", "non_git_shared_with_approval", sandbox.StableLocalID(workspace))
```

```go
return a.subagentJobs.SetWorkspace(job.ID, worktreeDir, baselineDir, isolation, gitBranch, origin, sandbox.StableLocalID(worktreeDir))
```

- [ ] **Step 6: Add sandbox ID to public and model views**

Modify `durableSubagentJobView` in `internal/agent/subagent_jobs.go` to set:

```go
		SandboxID: job.SandboxID,
```

Modify `subagentModelJobView` in `internal/agent/subagent_tool.go`:

```go
	SandboxID string `json:"sandbox_id,omitempty"`
```

Modify `formatSubagentModelJob`:

```go
		SandboxID: job.SandboxID,
```

Modify `formatSubagentJob` map:

```go
		"sandbox_id": job.SandboxID,
```

- [ ] **Step 7: Run durable subagent sandbox tests**

Run:

```bash
go test ./internal/agent -run 'TestDurableSubagentRecordsSandboxID|TestSubagentModelViewIncludesSandboxID|TestSubagent|TestWorkflow|TestLongTask' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/subagent_jobs.go internal/agent/subagent_tool.go internal/agent/subagent_jobs_test.go
git commit -m "feat(agent): record sandbox id on subagent jobs"
```

---

### Task 6: Add Sandbox Binding Compatibility Guards

**Files:**
- Create: `internal/agent/sandbox_compat_test.go`
- Modify: `internal/agent/context_facade.go`

- [ ] **Step 1: Write context inspection sandbox guard test**

Create `internal/agent/sandbox_compat_test.go`:

```go
package agent

import (
	"context"
	"testing"
)

func TestInspectContextDoesNotRequireSandboxInspectorSurface(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	inspection, err := a.InspectContext(context.Background(), "session-sandbox")
	if err != nil {
		t.Fatalf("inspect context: %v", err)
	}
	if inspection.SessionID != "session-sandbox" {
		t.Fatalf("session id %q", inspection.SessionID)
	}
	if a.SandboxID() == "" {
		t.Fatalf("expected agent sandbox id")
	}
}
```

- [ ] **Step 2: Run compatibility guard**

Run:

```bash
go test ./internal/agent -run TestInspectContextDoesNotRequireSandboxInspectorSurface -count=1
```

Expected: pass. This test confirms Phase 2 does not require new user-visible context inspector schema changes.

- [ ] **Step 3: Decide whether `context_facade.go` changes are needed**

If `context_facade.go` was not modified by this task, do not edit it. This task is allowed to be test-only because Phase 2 intentionally avoids changing `tools.ContextInspection`.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/sandbox_compat_test.go
git commit -m "test(agent): guard sandbox compatibility surface"
```

---

### Task 7: Document Phase 2 Local Sandbox Boundary

**Files:**
- Modify: `docs/architecture-v2-spec.md`
- Modify: `docs/architecture-v2-spec.en.md`

- [ ] **Step 1: Update Chinese SPEC Phase 2 section**

In `docs/architecture-v2-spec.md`, after the Phase 2 acceptance criteria, add:

```markdown
Phase 2 首次落地只要求 local sandbox boundary：

- Agent 持有一个默认 local sandbox，并通过 `sandbox_id` 引用。
- 现有 workspace、temp dir、artifact spill 和 execution config 由 sandbox binding 提供。
- 当前 local workspace 行为保持默认，不引入 remote/disposable runtime。
- Durable subagent job 持久化 `sandbox_id`，用于后续 Worker Runtime Protocol 统一引用。
- Sandbox ID 是 API 边界上的不透明字符串，不能通过拆分前缀驱动行为。
```

- [ ] **Step 2: Update English SPEC Phase 2 section**

In `docs/architecture-v2-spec.en.md`, after the Phase 2 acceptance criteria, add:

```markdown
The first Phase 2 implementation only requires a local sandbox boundary:

- Agent owns one default local sandbox and references it by `sandbox_id`.
- Existing workspace, temp directory, artifact spill, and execution config come from the sandbox binding.
- Current local workspace behavior remains the default; remote and disposable runtimes are not introduced yet.
- Durable subagent jobs persist `sandbox_id` so the later Worker Runtime Protocol can reference sandboxes uniformly.
- Sandbox IDs are opaque API-boundary strings and must not be parsed for behavior.
```

- [ ] **Step 3: Commit docs**

```bash
git add docs/architecture-v2-spec.md docs/architecture-v2-spec.en.md
git commit -m "docs(spec): define phase 2 local sandbox boundary"
```

---

### Task 8: Final Verification

**Files:**
- Modify only if `gofmt` changes a touched file.

- [ ] **Step 1: Format changed Go files**

Run:

```bash
gofmt -w internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go internal/agent/agent.go internal/agent/agent_wiring.go internal/agent/sandbox_facade.go internal/agent/sandbox_facade_test.go internal/agent/tool_registration.go internal/agent/tool_sandbox_test.go internal/toolruntime/runtime_context.go internal/toolruntime/sandbox_context_test.go internal/tools/toolruntime_aliases.go internal/agent/tool_execution.go internal/agent/subagent_jobs.go internal/agent/subagent_tool.go internal/agent/subagent_jobs_test.go internal/agent/sandbox_compat_test.go
```

Expected: command exits with status 0.

- [ ] **Step 2: Run targeted packages**

Run:

```bash
go test ./internal/sandbox ./internal/toolruntime ./internal/tools ./internal/agent -count=1
```

Expected: pass.

- [ ] **Step 3: Run full repository tests**

Run:

```bash
go test ./...
```

Expected: pass.

- [ ] **Step 4: Check diff cleanliness**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors. `git status --short` is empty if `gofmt` did not change files after prior commits.

- [ ] **Step 5: Commit final formatting if needed**

If `git status --short` shows changed files after `gofmt`, commit them:

```bash
git add internal/sandbox internal/agent internal/toolruntime internal/tools docs/architecture-v2-spec.md docs/architecture-v2-spec.en.md
git commit -m "chore: finalize phase 2 sandbox boundary"
```

If `git status --short` is empty, do not create an empty commit.

---

## Self-Review Notes

- Spec coverage: covers Phase 2 acceptance criteria: explicit sandbox identity/lifecycle, workspace filesystem view, tool runtime binding, artifact/temp policy, local implementation, default local behavior, worker job sandbox references, and rebuild preserving sandbox ID.
- Scope boundary: remote/disposable sandbox runtime, full Worker Runtime Protocol, Session Graph, and storage backend abstraction remain outside this plan.
- Type consistency: `sandbox.ToolBinding`, `sandbox.Info`, `Agent.SandboxBinding`, `Agent.SandboxInfo`, `tools.WithSandboxID`, and `tools.SandboxIDFromContext` are consistently named across tasks.

## Execution Options

1. **Subagent-Driven (recommended)** - Dispatch a fresh worker per task, review between tasks, and keep each commit small.
2. **Inline Execution** - Execute tasks in this session using the plan as a checklist, with verification checkpoints after each task.
