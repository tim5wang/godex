# GoDex Phase 1 Agent Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split `internal/agent/agent.go` into clearer Phase 1 subsystems while preserving all current CLI, Web, IM, tool, skill, package, permission, subagent, context, and compaction behavior.

**Architecture:** Keep `Agent` as the public facade and composition root for Phase 1. Move tightly related functions into focused files in `internal/agent`, without changing exported method names, tool names, tool schemas, default bundles, or storage formats.

**Tech Stack:** Go, existing `internal/agent` package tests, `go test ./internal/agent`, `go test ./...`.

---

## Scope

This plan implements only the first GoDex 2.0 migration step from `docs/architecture-v2-spec.md`: behavior-preserving `agent.go` decomposition. Sandbox, Worker Runtime Protocol, Session Graph, and Storage Backend abstraction remain separate future plans.

## Target File Structure

- Keep: `internal/agent/agent.go`
  - Owns `type Agent`, `type dependencies`, public constructor, small facade methods, and shared state fields.
- Create: `internal/agent/agent_wiring.go`
  - Owns dependency construction and helper constructors currently in `agent.go`.
- Create: `internal/agent/tool_registration.go`
  - Owns bundle constants, tool registration, and execution config projection.
- Create: `internal/agent/skill_facade.go`
  - Owns active skill state, skill install/list/load/expand/unload facade, and skill catalog decoration helpers.
- Create: `internal/agent/package_facade.go`
  - Owns package list/install/remove/prompts/commands/roles mapping helpers.
- Create: `internal/agent/session_state.go`
  - Owns message state mutation/access, pending resume state, idle state, transcript refs, current model, and manager accessors.
- Create: `internal/agent/context_facade.go`
  - Owns `InspectContext`, `CompactConversation`, and compaction storage bridge methods that currently sit in `agent.go`.
- Create: `internal/agent/permission_facade.go`
  - Owns permission policy projection, pending permission facade, and permission review subagent entrypoint.
- Create: `internal/agent/subagent_tool.go`
  - Owns the model-visible `task` tool schema, request/view structs, wait/list/log formatting, and legacy scoped subagent tool methods that still live in `agent.go`.
- Create: `internal/agent/tool_execution.go`
  - Owns `handleTool`, `handleToolResult`, `RunPackageSmokeCommand`, and model-output conversion helpers.
- Modify: existing `internal/agent/*_test.go`
  - Add focused guard tests before moving code when a boundary is not already covered.

## Behavior Invariants

- `New(cfg)` and `newAgentWithDependencies(cfg, deps)` keep the same call sites and semantics.
- `Agent.RegisterTools()` registers the same tool names with the same bundles and default active state.
- `ToolCatalog()`, `ActiveSkillNames()`, `PendingPermissions()`, `PendingResumeState()`, `TranscriptRefs()`, `HistorySearchRuntime()`, and manager accessors remain public methods on `Agent`.
- Tool execution continues to pass through the same permission interceptor and review callback.
- Skill/package facade methods remain methods on `Agent` so existing tools continue to compile.
- Durable subagent tool name remains `task`, and its JSON schema remains compatible.
- Existing JSON/file storage layout is unchanged.

---

### Task 1: Add Facade Guard Tests

**Files:**
- Create: `internal/agent/agent_facade_refactor_test.go`

- [ ] **Step 1: Add tests covering stable public facade behavior**

Create `internal/agent/agent_facade_refactor_test.go` with:

```go
package agent

import (
	"reflect"
	"sort"
	"testing"

	"github.com/tim5wang/godex/internal/tools"
)

func TestAgentRefactorKeepsDefaultToolCatalogShape(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.RegisterTools()

	catalog := a.ToolCatalog()
	if !containsString(catalog.ActiveBundles, bundleCoreCode) {
		t.Fatalf("expected default active bundle %q, got %+v", bundleCoreCode, catalog.ActiveBundles)
	}
	if !containsString(catalog.ActiveBundles, bundlePlanning) {
		t.Fatalf("expected default active bundle %q, got %+v", bundlePlanning, catalog.ActiveBundles)
	}
	if !containsString(catalog.AlwaysActiveTools, "tool_exchange") {
		t.Fatalf("expected tool_exchange to stay always active, got %+v", catalog.AlwaysActiveTools)
	}
	if !catalogContainsTool(catalog, bundleCoreCode, "bash") {
		t.Fatalf("expected bash in bundle %q, got %+v", bundleCoreCode, catalog.Bundles)
	}
	if !catalogContainsTool(catalog, bundleSubagent, "task") {
		t.Fatalf("expected task in bundle %q, got %+v", bundleSubagent, catalog.Bundles)
	}
}

func TestAgentRefactorKeepsSessionFacadeCopies(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.AddMessage("first")

	messages := a.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	messages = nil
	if got := len(a.GetMessages()); got != 1 {
		t.Fatalf("mutating returned slice must not mutate agent messages, got %d", got)
	}
}

func TestAgentRefactorKeepsActiveSkillNamesSorted(t *testing.T) {
	a := newTestAgent(t, 4096)
	a.mu.Lock()
	a.activeSkills["zeta"] = &activeSkillState{}
	a.activeSkills["alpha"] = &activeSkillState{}
	a.mu.Unlock()

	got := a.ActiveSkillNames()
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected sorted active skills %+v, got %+v", want, got)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func catalogContainsTool(catalog tools.ToolCatalog, bundleName, toolName string) bool {
	for _, item := range catalog.Bundles {
		if item.Name != bundleName {
			continue
		}
		toolNames := append([]string{}, item.Tools...)
		sort.Strings(toolNames)
		i := sort.SearchStrings(toolNames, toolName)
		return i < len(toolNames) && toolNames[i] == toolName
	}
	return false
}
```

- [ ] **Step 2: Run the new guard tests**

Run:

```bash
go test ./internal/agent -run 'TestAgentRefactorKeeps' -count=1
```

Expected: tests pass before any move. If a test fails because current behavior differs, update the assertion to current behavior before moving code.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/agent_facade_refactor_test.go
git commit -m "test(agent): lock facade behavior before refactor"
```

---

### Task 2: Extract Agent Wiring

**Files:**
- Create: `internal/agent/agent_wiring.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Move dependency construction functions**

Move these functions from `agent.go` into `agent_wiring.go` without changing their bodies:

```go
func buildDependencies(cfg *config.Config) dependencies
func apiRequestTimeout(cfg *config.Config) time.Duration
func callerForConfigProfile(cfg *config.Config, primary config.ModelProfileConfig) conversation.Caller
func newAgentWithDependencies(cfg *config.Config, deps dependencies) *Agent
func loadMessageBus(teamDir string) *message.Bus
func newSkillLoader(cfg *config.Config, client conversation.Caller) *skill.Loader
func newTeamManager(cfg *config.Config, taskMgr *task.Manager, msgBus *message.Bus, client conversation.Caller) *teammate.Manager
```

- [ ] **Step 2: Keep `New` in `agent.go`**

Leave this constructor in `agent.go`:

```go
// New creates a new agent.
func New(cfg *config.Config) *Agent {
	return newAgentWithDependencies(cfg, buildDependencies(cfg))
}
```

- [ ] **Step 3: Run wiring and runtime tests**

Run:

```bash
go test ./internal/agent -run 'TestAgentRefactorKeeps|TestRunWithOptions|TestBuildContext' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_wiring.go
git commit -m "refactor(agent): extract construction wiring"
```

---

### Task 3: Extract Tool Registration and Tool Execution

**Files:**
- Create: `internal/agent/tool_registration.go`
- Create: `internal/agent/tool_execution.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Move bundle constants and registration helpers**

Move these declarations into `tool_registration.go`:

```go
const (
	bundleCoreCode   = "core_code"
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
)

func (a *Agent) registerToolTo(handler *tools.ToolHandler, tool tools.Tool, meta tools.ToolMeta)
func (a *Agent) registerTool(tool tools.Tool, meta tools.ToolMeta)
func executionConfigFromRuntime(cfg config.ToolExecutionConfig) tooling.ExecutionConfig
func (a *Agent) RegisterTools()
func (a *Agent) registerToolsWith(handler *tools.ToolHandler)
```

- [ ] **Step 2: Move tool execution bridge**

Move these functions into `tool_execution.go`:

```go
func (a *Agent) handleTool(ctx context.Context, name string, input map[string]interface{}) (string, error)
func (a *Agent) handleToolResult(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error)
func (a *Agent) RunPackageSmokeCommand(ctx context.Context, runtimeCtx automation.SessionContext, command string) (tools.ToolResult, error)
func toolResultHasModelOutput(result tools.ToolResult) bool
```

- [ ] **Step 3: Run tool catalog and runtime tests**

Run:

```bash
go test ./internal/agent -run 'TestAgentRefactorKeepsDefaultToolCatalogShape|TestBuildContextExposesOnlyActiveToolSchemas|TestToolExchange|TestRunWithOptions' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/agent.go internal/agent/tool_registration.go internal/agent/tool_execution.go
git commit -m "refactor(agent): extract tool registration"
```

---

### Task 4: Extract Session State Facade

**Files:**
- Create: `internal/agent/session_state.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Move message and runtime state facade methods**

Move these methods into `session_state.go`:

```go
func (a *Agent) AddMessage(content string)
func (a *Agent) AddEnvelope(envelope message.Envelope)
func (a *Agent) AppendRuntimeFeedback(text string)
func (a *Agent) GetMessages() []protocol.Message
func (a *Agent) TranscriptRefs() []string
func (a *Agent) HistorySearchRuntime() tools.HistorySearchRuntime
func (a *Agent) ClearMessages()
func (a *Agent) TruncateMessages(count int)
func (a *Agent) SetIdle(idle bool)
func (a *Agent) TaskMgr() *task.Manager
func (a *Agent) TeamMgr() *teammate.Manager
func (a *Agent) ToolCatalog() tools.ToolCatalog
func (a *Agent) pendingResumeState() *PendingResumeState
func (a *Agent) PendingResumeState() *PendingResumeState
func (a *Agent) SetPendingResume(requestID string, priorMessageCount int, envelope message.Envelope, runtimeCtx automation.SessionContext, injections ...message.Envelope)
func (a *Agent) ClearPendingResume()
func (a *Agent) ActiveSkillNames() []string
func (a *Agent) MsgBus() *message.Bus
func (a *Agent) TodoMgr() *todo.Manager
func (a *Agent) MemoryMgr() *memory.Manager
func (a *Agent) CurrentModel() string
func (a *Agent) appendMessage(msg protocol.Message)
func (a *Agent) AppendAssistantText(text string, kind protocol.MessageKind)
func (a *Agent) AppendAssistantDelivery(text string, kind protocol.MessageKind, attachments []message.AttachmentRef)
func (a *Agent) messageState() ([]protocol.Message, int64)
func (a *Agent) resetIdle()
func (a *Agent) consumeIdleRequest() bool
```

- [ ] **Step 2: Run session and runtime tests**

Run:

```bash
go test ./internal/agent -run 'TestAgentRefactorKeepsSessionFacadeCopies|TestRunWithOptions|TestBuildContextCompactsPersistentHistoryButKeepsRuntimeMessages' -count=1
```

Expected: pass.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/agent.go internal/agent/session_state.go
git commit -m "refactor(agent): extract session state facade"
```

---

### Task 5: Extract Skill and Package Facades

**Files:**
- Create: `internal/agent/skill_facade.go`
- Create: `internal/agent/package_facade.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Move skill state and skill facade methods**

Move these declarations into `skill_facade.go`:

```go
type activeSkillState struct {
	catalog       skill.CatalogEntry
	core          string
	expanded      map[string]string
	expandedOrder []string
}

func (s *activeSkillState) loadedSections() []string
func (a *Agent) LoadSkill(name string) error
func (a *Agent) ActivateSkill(name string) (tools.SkillActivation, error)
func (a *Agent) InstallSkill(source, name string) (tools.SkillInstallResult, error)
func (a *Agent) NormalizeSkill(ctx context.Context, name string) (skill.CatalogEntry, error)
func (a *Agent) RemoveSkill(name string) (tools.SkillRemoveResult, error)
func (a *Agent) ExpandSkill(name string, sections []string) (tools.SkillExpansion, error)
func (a *Agent) ListSkills() ([]skill.CatalogEntry, error)
func (a *Agent) ListSkillSources() ([]tools.SkillSourceEntry, error)
func (a *Agent) SearchSkillSources(query string) ([]tools.SkillSourceEntry, error)
func (a *Agent) listSkillSources(query string) ([]tools.SkillSourceEntry, error)
func (a *Agent) GetSkill(name string) (skill.CatalogEntry, error)
func (a *Agent) catalogEntryWithSuiteMetadata(id string, fallback skill.CatalogEntry) skill.CatalogEntry
func (a *Agent) ActiveSkills() ([]tools.SkillActivation, error)
func (a *Agent) UnloadSkill(name string) (tools.SkillActivation, error)
func skillActivationResult(state *activeSkillState, status string) tools.SkillActivation
func decorateSkillSuiteMetadata(items []skill.CatalogEntry)
func splitSuiteSkillID(id string) (string, bool)
func (a *Agent) findActiveSkillKeyLocked(name string) string
func skillNotActiveError(name string) error
func (a *Agent) resolveSkillEntry(entry skill.CatalogEntry) skill.CatalogEntry
func cloneSkillInstallMemory(memory *skill.InstallMemory) *skill.InstallMemory
```

- [ ] **Step 2: Move package facade methods**

Move these functions into `package_facade.go`:

```go
func (a *Agent) ListPackages() ([]tools.PackageEntry, error)
func (a *Agent) InstallPackage(source string) (tools.PackageEntry, error)
func (a *Agent) RemovePackage(name string) (tools.PackageEntry, error)
func (a *Agent) ListPrompts(includeContent bool) ([]tools.PromptEntry, error)
func (a *Agent) ListPackageCommands(includeContent bool) ([]tools.PackageCommandEntry, error)
func (a *Agent) ListPackageRoles(includeContent bool) ([]tools.PackageRoleEntry, error)
func packageEntriesFromRegistry(items []pkgregistry.Entry) []tools.PackageEntry
func packageEntryFromRegistry(item pkgregistry.Entry) tools.PackageEntry
func packageAppFromRegistry(item pkgregistry.AppManifest) tools.PackageAppEntry
func packageSmokeTests(items []pkgregistry.SmokeTest) []tools.PackageSmokeTest
func packageResources(items pkgregistry.Resources) map[string][]string
```

- [ ] **Step 3: Run skill and package tests**

Run:

```bash
go test ./internal/agent -run 'TestAgentRefactorKeepsActiveSkillNamesSorted|TestBuildContextIncludesSkillCatalogPrompt|TestBuildContextExposesSkillManagementSchemasOnlyWhenNeeded|TestRunWithOptionsRestoresActiveSkills|TestPackage' -count=1
```

Expected: pass. If `-run TestPackage` does not match any tests, run `go test ./internal/agent -count=1` for this task.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/agent.go internal/agent/skill_facade.go internal/agent/package_facade.go
git commit -m "refactor(agent): extract skill and package facades"
```

---

### Task 6: Extract Context, Compaction, and Permission Facades

**Files:**
- Create: `internal/agent/context_facade.go`
- Create: `internal/agent/permission_facade.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Move context and compaction bridge methods**

Move these functions into `context_facade.go`:

```go
func (a *Agent) InspectContext(ctx context.Context, sessionID string) (tools.ContextInspection, error)
func (a *Agent) CompactConversation() (string, error)
func (a *Agent) captureMemoryCandidates() error
func (a *Agent) CaptureInsightMemoryCandidates(report *insights.Report) error
func (a *Agent) CaptureTimelineMemoryCandidates(items []events.Event) error
func (a *Agent) storeCompactedMessages(messages []protocol.Message)
func (a *Agent) maybeAutoCompact(ctx context.Context, history []protocol.Message, version int64, system string, estimate contextBudgetEstimate) ([]protocol.Message, bool, error)
```

- [ ] **Step 2: Move permission facade and review callback**

Move these declarations into `permission_facade.go`:

```go
func permissionPolicyFromConfig(cfg *config.Config) tools.PermissionPolicy
func (a *Agent) PendingPermissions(sessionID string) []tools.PendingPermission
func (a *Agent) ApprovePendingPermission(sessionID, requestID string, scope tools.PermissionGrantScope) (tools.PermissionResolution, error)
func (a *Agent) DenyPendingPermission(sessionID, requestID, reason string) (tools.PermissionResolution, error)
func (a *Agent) reviewPermissionRequest(ctx context.Context, req tools.PermissionRequest) (tools.PermissionResult, error)
```

- [ ] **Step 3: Run context and permission tests**

Run:

```bash
go test ./internal/agent -run 'TestInspectContext|TestBuildContext|TestSubagentPermission|TestRunWithOptionsEmitsLifecycleEvents' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/agent.go internal/agent/context_facade.go internal/agent/permission_facade.go
git commit -m "refactor(agent): extract context and permission facades"
```

---

### Task 7: Extract Subagent Tool Surface From `agent.go`

**Files:**
- Create: `internal/agent/subagent_tool.go`
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Move subagent tool schema and view types**

Move all declarations from `registerSubagentTool` through `subagentResultDigest` into `subagent_tool.go`, keeping function names unchanged:

```go
func (a *Agent) registerSubagentTool(handler *tools.ToolHandler)
type subagentArgs struct
type subagentBatchItem struct
type subagentLogsView struct
type subagentModelJobView struct
type subagentBatchView struct
type subagentBatchErrorView struct
type subagentWaitView struct
type subagentRunView struct
func newSubagentTool(agent *Agent) tools.Tool
func formatSubagentJobList(jobs []*subagentJob) []subagentModelJobView
func formatSubagentModelJob(job *subagentJob) subagentModelJobView
func formatSubagentLogs(job *subagentJob, limit int) subagentLogsView
func startSubagentBatch(ctx context.Context, agent *Agent, tasks []subagentBatchItem, wait bool, timeoutMS int) subagentBatchView
func (a *Agent) runDurableSubagentSync(ctx context.Context, req durableSubagentStartRequest, timeoutMS int) (subagentRunView, error)
func waitSubagents(ctx context.Context, agent *Agent, req subagentWaitRequest) (subagentWaitView, error)
func snapshotSubagentWait(agent *Agent, jobIDs []string, mode string, timeoutMS int) (subagentWaitView, error)
func subagentWaitSatisfied(view subagentWaitView) bool
func subagentStatusTerminal(status subagentJobStatus) bool
func subagentResultPreview(result string) string
func subagentResultDigest(result string) string
```

- [ ] **Step 2: Move legacy scoped subagent helpers if still in `agent.go`**

Move these related functions into `subagent_tool.go` as a second block:

```go
func formatSubagentJob(job *subagentJob, includeMessages bool) map[string]interface{}
func (a *Agent) RunSubagent(ctx context.Context, prompt string, agentType string) (string, error)
func (a *Agent) runScopedSubagent(ctx context.Context, prompt, basePrompt string, toolNames []string, maxTurns int) (*conversation.Result, error)
func (a *Agent) executeSubagentTool(ctx context.Context, name string, input map[string]interface{}) (conversation.ToolExecutionResult, error)
```

- [ ] **Step 3: Run subagent tests**

Run:

```bash
go test ./internal/agent -run 'TestSubagent|TestWorkflow|TestLongTask|TestAgentRefactorKeepsDefaultToolCatalogShape' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/agent.go internal/agent/subagent_tool.go
git commit -m "refactor(agent): extract subagent tool surface"
```

---

### Task 8: Clean Imports and Verify `agent.go` Is a Thin Facade

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: created files from previous tasks

- [ ] **Step 1: Format all changed Go files**

Run:

```bash
gofmt -w internal/agent/agent.go internal/agent/agent_wiring.go internal/agent/tool_registration.go internal/agent/tool_execution.go internal/agent/session_state.go internal/agent/skill_facade.go internal/agent/package_facade.go internal/agent/context_facade.go internal/agent/permission_facade.go internal/agent/subagent_tool.go internal/agent/agent_facade_refactor_test.go
```

Expected: command exits with status 0.

- [ ] **Step 2: Check remaining `agent.go` responsibilities**

Run:

```bash
rg -n '^func |^func \(a \*Agent\)|^type activeSkillState|^type subagent|^const \(' internal/agent/agent.go
```

Expected: `agent.go` still contains `type Agent`, `type dependencies`, `New`, and only small facade glue that genuinely needs to live beside the struct.

- [ ] **Step 3: Run package-level tests**

Run:

```bash
go test ./internal/agent -count=1
```

Expected: pass.

- [ ] **Step 4: Run repository tests**

Run:

```bash
go test ./...
```

Expected: pass.

- [ ] **Step 5: Check generated diff cleanliness**

Run:

```bash
git diff --check
```

Expected: no whitespace errors.

- [ ] **Step 6: Commit**

```bash
git add internal/agent docs/superpowers/plans/2026-05-13-godex-phase1-agent-refactor.md
git commit -m "refactor(agent): complete phase 1 facade split"
```

---

## Self-Review Notes

- Spec coverage: this plan covers only `docs/architecture-v2-spec.md` Phase 1. It intentionally does not implement Sandbox Boundary, Worker Runtime Protocol, Session Graph, Storage Backend Abstraction, Orchestration DSL, Run Model, Knowledge, Gallery, or Inbound MCP.
- Placeholder scan: no step depends on unspecified future behavior; each move names concrete functions and files.
- Type consistency: all functions remain in package `agent`, so unexported helper names remain callable across the new files.

## Execution Options

1. **Subagent-Driven (recommended)** - Dispatch a fresh worker per task, review between tasks, and keep each commit small.
2. **Inline Execution** - Execute tasks in this session using the plan as a checklist, with verification checkpoints after each task.
