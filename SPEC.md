# GoDex 2.0 Architecture SPEC

## Summary

GoDex 1.0 has grown from a local-first agent workbench into a shared runtime that can serve CLI, TUI, Web, HTTP API, Feishu, Weixin, tools, memory, subagents, automation, and approval flows. The next architecture step is GoDex 2.0: move from a single large in-process agent toward an agent runtime platform that separates identity, orchestration, worker execution, sandbox environments, session memory, and storage.

This SPEC defines the long-term architecture direction and the migration path. It is not a single big-bang rewrite. Each phase must preserve current 1.0 behavior while extracting clearer boundaries.

## Current 1.0 Baseline

GoDex 1.0 already provides:

- Shared session runtime across CLI, TUI, Web, HTTP API, Feishu, and Weixin.
- Tool execution with approval, shell/file/browser/web/memory/skill/package/MCP surfaces.
- Durable memory, history recall, compaction, transcript archive, and context inspection.
- Durable subagent jobs, workflow/longtask orchestration, review/merge/cancel/resume.
- Web workbench for chat, settings, memory, automation, nodes, notes, skills, and context diagnostics.
- Local-first deployment with single binary Web UI embedding and self-managed service install.

The main architectural pressure is that `internal/agent/agent.go` still acts as a large composition root, session state holder, tool registry, skill/package facade, permission facade, subagent tool controller, and compaction bridge. That shape works for 1.0, but it makes sandbox replacement, worker runtime replacement, session branching, and storage backend replacement harder than necessary.

## 2.0 Principles

### 1. Agent Identity Must Be Separate From Sandbox Execution

An Agent answers: who am I, what can I do, and how do I work?

A Sandbox answers: where do I work, what files and tools can I touch, and how can this environment be recreated?

GoDex 2.0 treats these as separate concepts:

- **Agent Identity**: name, profile, role, model policy, capability policy, prompt strategy, delegation strategy.
- **Sandbox**: workspace, filesystem view, process environment, command policy, tool runtime, artifacts, lifecycle.
- **Tool Runtime**: concrete hands for bash, file IO, browser, web, MCP, desktop, and external systems.

If a sandbox becomes polluted or broken, the Agent identity should be able to attach to a fresh sandbox without losing the high-level session, memory, or orchestration state.

### 2. Orchestrator Agent Must Be Separate From Worker Agents

The main Agent should stay smart and context-clean. It should plan, delegate, monitor, review, and merge.

Worker Agents should do heavy or dirty work inside bounded sandboxes. A worker can be:

- A GoDex role running in the same process.
- A GoDex worker running in another sandbox, host, or node.
- Another agent runtime exposed through a compatible worker protocol.

The orchestrator assigns work through structured jobs: prompt, role, required tools, required bundles, sandbox policy, write scope, expected output, timeout, and merge policy. Workers return progress, artifacts, diffs, summaries, errors, and completion state.

### 3. Session Memory Must Become A Branchable Tree

The current session is mostly linear history plus compaction and transcript references. GoDex 2.0 needs a session memory tree:

- Branch from any stable point.
- Clone a session for worker exploration.
- Roll back to an earlier node.
- Merge worker results, summaries, diffs, and decisions back into the main branch.
- Rebuild context from events, checkpoints, summaries, artifacts, and memory references.

This should feel closer to a versioned context graph than a single mutable `messages` slice.

### 4. Session Must Be Separate From Storage Backend

Session state should not assume one storage medium. GoDex 2.0 must define storage interfaces that can be backed by:

- JSON files for simple local deployments.
- SQLite for reliable single-machine indexing and transactions.
- Server database storage for multi-node or team deployments.
- Cloud/object storage for large artifacts, transcripts, and long-term archives.

Runtime code should depend on repositories/stores, not direct file layouts, except inside storage backend implementations.

## Target Layered Architecture

### Entry Layer

CLI, TUI, Web, HTTP API, Feishu, Weixin, ACP, and future integrations should only translate external events into runtime requests. They should not own agent internals.

Responsibilities:

- Authenticate and normalize incoming requests.
- Attach channel/session metadata.
- Stream runtime events back to users.
- Surface approvals, attachments, and artifacts.

### Orchestration Layer

The orchestrator owns planning, delegation, progress monitoring, and result integration. It should be able to run without directly knowing how a sandbox executes bash or how a worker stores its local files.

Responsibilities:

- Build high-level plans.
- Decide when to call tools directly and when to delegate.
- Create worker jobs.
- Monitor worker progress.
- Review, merge, reject, or retry worker outputs.
- Keep the main context compact.

### Agent Identity Layer

This layer defines the stable agent brain.

Responsibilities:

- Agent profile and role.
- System prompt strategy.
- Capability policy.
- Tool exposure strategy.
- Delegation policy.
- Model selection and fallback strategy.

This layer should be portable across sandboxes.

### Session Graph Layer

This layer owns conversation and memory topology.

Responsibilities:

- Persistent messages and runtime events.
- Ephemeral context layers.
- Branch, clone, rollback, and merge.
- Compaction checkpoints.
- Transcript and artifact references.
- Context rebuild for model requests.

### Worker Runtime Layer

This layer runs concrete delegated jobs.

Responsibilities:

- Accept structured job requests.
- Attach identity/role to sandbox.
- Execute model turns and tools.
- Emit progress events.
- Return result summaries, artifacts, diffs, and errors.
- Support cancellation, resume, timeout, review, and merge hooks.

### Sandbox And Tool Runtime Layer

This layer owns execution environments and hands.

Responsibilities:

- Workspace and filesystem isolation.
- Command execution policy.
- Tool bundle availability.
- Artifact capture.
- Environment reset/rebuild.
- Remote or local placement.

### Storage Layer

This layer persists sessions, memory, artifacts, jobs, permissions, timelines, and runtime state through interfaces.

Responsibilities:

- JSON backend for 1.0 compatibility.
- SQLite backend for local 2.0 reliability.
- Future DB/cloud backend support.
- Migration and export/import paths.

## Migration Plan

### Phase 1: Behavior-Preserving Agent Refactor

Goal: reduce `agent.go` coupling without changing user-visible behavior.

Extract clear modules:

- Agent construction and dependency wiring.
- Tool registry and bundle registration.
- Session transcript state.
- Skill/session activation facade.
- Package registry facade.
- Permission review facade.
- Subagent tool controller.
- Context inspection and compaction bridge.

Acceptance criteria:

- `go test ./...` passes.
- Existing CLI/Web/IM/subagent flows keep working.
- Public tool names and tool schemas remain compatible.
- `agent.go` becomes a thin composition root and facade.

### Phase 2: Sandbox Boundary

Goal: make workspace/tools execution attachable and replaceable.

Introduce explicit sandbox abstractions:

- Sandbox identity and lifecycle.
- Workspace filesystem view.
- Tool runtime binding.
- Artifact and temp storage policy.
- Local sandbox implementation matching current behavior.

Acceptance criteria:

- Existing local workspace behavior remains default.
- Worker jobs can reference a sandbox by ID.
- A sandbox can be recreated without changing Agent identity.

### Phase 3: Worker Runtime Protocol

Goal: make workers first-class runtimes instead of only a tool implementation detail.

Introduce structured worker interfaces:

- Job request.
- Progress event.
- Result and artifact contract.
- Capability inheritance.
- Review/merge contract.

Acceptance criteria:

- Current durable subagent jobs are implemented through the worker runtime interface.
- The orchestrator can dispatch to local GoDex workers through the same contract future remote workers will use.

### Phase 4: Session Graph

Goal: replace linear session mutation with branchable context state.

Introduce:

- Session node IDs.
- Branch heads.
- Clone/rollback/merge operations.
- Compaction checkpoints as graph nodes.
- Worker branch handoff and merge.

Acceptance criteria:

- Existing sessions can be read and migrated.
- Mainline chat still behaves like a normal linear conversation.
- Worker explorations can happen on cloned branches.

### Phase 5: Storage Backend Abstraction

Goal: make session state independent from storage media.

Introduce repository interfaces and backend implementations:

- JSON backend for compatibility.
- SQLite backend for local reliability.
- Export/import tools.
- Storage capability diagnostics.

Acceptance criteria:

- Current JSON/file state remains supported.
- New code uses store interfaces rather than direct paths.
- SQLite can be enabled without changing entry/channel code.

## Compatibility Rules

- Keep current 1.0 commands, HTTP APIs, Web flows, tool names, and default local storage working during migration.
- Do not require distributed infrastructure for local users.
- Do not move all state at once; add adapters and migration paths.
- Keep worker and sandbox features optional until the local implementation is stable.
- Preserve current approval and security behavior unless a SPEC explicitly changes it.

## Non-Goals For The First Refactor

- No immediate distributed cluster implementation.
- No immediate cloud storage requirement.
- No replacement of all current tools.
- No forced SQLite migration.
- No behavior changes to normal chat, Web UI, IM channels, or CLI unless required by an explicit compatibility bug fix.

## First SPEC Target

The first implementation SPEC should focus on Phase 1 only: behavior-preserving decomposition of `internal/agent/agent.go`.

Recommended deliverables:

- `agent.go` remains the public facade and composition root.
- New files split responsibilities by subsystem.
- Existing tests are moved only when it improves ownership clarity.
- New tests assert behavior compatibility rather than internal file layout.

