[简体中文](SPEC.md) | [English](SPEC.en.md)

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

## Concept Boundaries And IDs

GoDex 2.0 must make runtime objects explicit. An implementation should not use one struct to mean agent, session, sandbox, worker, and storage row at the same time.

### Concept Boundary Table

| Concept | Answers | Owns | Does Not Own |
|---------|---------|------|--------------|
| **Agent Identity** | Who am I? What policy do I follow? | profile, role, prompt strategy, model policy, capability policy, delegation policy | workspace files, process state, session messages, persistent storage layout |
| **Agent Instance** | Which live agent process is acting now? | runtime bindings to a session, sandbox, model caller, and tool exposure state | durable identity definition, storage backend implementation |
| **Orchestrator** | Who plans and assigns work? | job planning, worker assignment, review/merge decisions, mainline context hygiene | worker filesystem, low-level tool execution |
| **Worker** | Who executes one assigned job? | job-local model turns, progress, artifacts, diffs, result summaries | main session authority, global memory policy, unrelated branches |
| **Sandbox** | Where can work happen? | workspace view, environment variables, command policy, temp/artifact roots, lifecycle | agent identity, session graph, storage schema |
| **Tool Runtime** | How do hands execute actions? | concrete tool handlers, permission checks, tool bundle activation, adapters to shell/file/browser/web/MCP | orchestration policy, session branching, long-term identity |
| **Session Graph** | What happened and what context branch are we on? | session nodes, branches, messages, events, checkpoints, merge records | physical storage details, process lifecycle |
| **Storage Backend** | Where is state persisted? | JSON/SQLite/DB/cloud implementation, transactions, indexes, migrations | agent behavior, sandbox policy, orchestration decisions |
| **Artifact** | What concrete output was produced? | file/blob metadata, digest, producer reference, retention policy | session graph topology, worker lifecycle |

### Stable IDs

IDs should be opaque strings at API boundaries. Prefixes are recommended for debugging and migration safety, but code must not parse IDs by splitting on prefixes except in compatibility adapters.

| ID | Example | Scope | Meaning |
|----|---------|-------|---------|
| `agent_id` | `agent:lead` | Durable config scope | Stable identity definition for an agent or role. |
| `agent_instance_id` | `agent-inst:01J...` | Runtime process scope | One live attachment of an agent identity to a session/sandbox. |
| `session_id` | `session:web:42d...` | Durable storage scope | Logical user/workflow session. |
| `branch_id` | `branch:main` | Session scope | Named or generated branch inside one session graph. |
| `node_id` | `node:01J...` | Session graph scope | Immutable point in the context graph. |
| `sandbox_id` | `sandbox:local:repo` | Runtime/storage scope | Re-creatable execution environment. |
| `tool_runtime_id` | `tools:local:default` | Sandbox scope | Concrete tool handler set bound to a sandbox. |
| `worker_id` | `worker:godex:01J...` | Orchestration scope | Worker runtime endpoint or live worker. |
| `job_id` | `job:subagent:01J...` | Orchestration/session scope | One delegated unit of work. |
| `artifact_id` | `artifact:01J...` | Storage scope | Persisted file/blob/diff/result reference. |
| `store_id` | `store:sqlite:local` | Deployment scope | Storage backend instance. |

### Ownership And Reference Rules

- An `Agent Identity` can have many `Agent Instance` records over time.
- An `Agent Instance` binds exactly one `Agent Identity` to one active `Session Graph` branch and zero or one active `Sandbox`.
- An `Orchestrator` is an agent instance with authority to create jobs and merge worker output into a branch.
- A `Worker` executes one or more jobs, but each job must name its `session_id`, source `branch_id` or `node_id`, and assigned `sandbox_id`.
- A `Sandbox` may be reused by multiple jobs only when its lifecycle policy allows reuse. Disposable sandboxes should be recreated per job or per branch.
- A `Tool Runtime` is bound to a sandbox. Tool permissions and bundle activation belong to this binding, not to the durable agent identity.
- A `Session Graph` references artifacts by `artifact_id`; it does not store large artifact payloads inline.
- A `Storage Backend` persists records for all durable concepts, but it does not define runtime behavior. Runtime behavior belongs to agent, worker, sandbox, and tool policies.
- Cross-object references must use IDs, not in-memory pointers, at storage and API boundaries.

### Minimal Relationship Model

```text
Agent Identity
  -> Agent Instance
      -> active Session Graph branch
      -> optional Sandbox
          -> Tool Runtime

Orchestrator Agent Instance
  -> creates Job
      -> assigned Worker
      -> assigned Sandbox
      -> source Session node/branch
      -> output Artifacts
      -> merge record back into Session Graph

Storage Backend
  -> persists Agent Identity, Session Graph, Job, Sandbox metadata, Artifact metadata, Permission records
```

### Boundary Invariants

- Recreating a sandbox must not change `agent_id`, `session_id`, or `branch_id`.
- Cloning a session branch must not clone process state; it creates new graph nodes and references existing artifacts unless copy-on-write is requested.
- Moving from JSON to SQLite must not change public IDs.
- A worker failure must not corrupt the orchestrator branch. Failed work remains attached to its job and branch until explicitly merged or discarded.
- Tool availability changes are runtime state. They can be checkpointed for replay, but they are not part of durable Agent Identity.

## Identity, Authority, And Capability Policy

GoDex 2.0 must distinguish human/channel identity from agent identity. Approval and capability decisions should be explicit records, not hidden side effects of a tool call.

### Identity Types

- **UserIdentity**: the human or service account that initiated or owns a request. It should include stable user ID, display label, trust level, and optional organization/team scope.
- **ChannelIdentity**: the entry surface that delivered a request, such as CLI, Web, TUI, Feishu, Weixin, HTTP API, ACP, cron, or heartbeat. It should include channel type, channel account, tenant/chat/user identifiers, and auth state.
- **AgentIdentity**: the durable agent role or profile that acts on behalf of the user.
- **AgentInstance**: the live runtime binding of an agent identity to one session branch and optional sandbox.

### Approval Authority

Approval authority is the right to allow a protected action. It is not the same as being the message sender.

Approval records should capture:

- Who requested the action: `agent_instance_id`, `session_id`, `branch_id`, and `job_id` if delegated.
- Who or what originated the request: `user_identity` and `channel_identity`.
- Who approved or denied it: approving `user_identity`, channel, timestamp, scope, and reason.
- What was approved: tool name, normalized input summary, affected paths/commands, sandbox, and capability.
- Approval scope: once, session, branch, sandbox, job, or configured policy.

The orchestrator may request approval for a worker, but the approval record must name the worker job and sandbox that will consume the permission.

### Capability Downgrade

Capability downgrade is required when a requested action cannot safely or fully run under current identity, channel, sandbox, or policy.

Examples:

- A remote IM channel requests a mutating shell command, so the tool is downgraded to approval-required.
- A worker asks for `web` but the orchestrator has not enabled or approved web tools, so the job is blocked with a capability-required error.
- A sandbox is read-only, so write tools are hidden or rejected.
- A model/provider lacks image understanding, so image analysis is downgraded to attachment metadata only.

Capability downgrade results must be model-visible and machine-readable:

- `status`: `allowed`, `downgraded`, `blocked`, or `requires_approval`
- `missing_capabilities`
- `available_alternatives`
- `approval_hint`
- `retry_policy`

Downgrade must be preferred over silent failure or pretending a capability exists.

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

## Runtime Protocols

Runtime communication should be event-driven and replayable. Events, artifacts, changesets, failures, and retries must have stable IDs so sessions can be rebuilt and worker outputs can be audited.

### Event Model

Every significant runtime transition should emit an event:

- user message received
- agent turn started/completed
- tool call started/completed/failed
- approval requested/resolved
- worker job created/started/progress/completed/failed
- sandbox created/reused/reset/destroyed
- artifact created/attached
- branch created/merged/rolled back
- compaction checkpoint created

Events should include:

- `event_id`, `event_type`, `created_at`
- `session_id`, `branch_id`, and `node_id` when applicable
- `agent_instance_id`, `worker_id`, `job_id`, `sandbox_id` when applicable
- causality fields: `parent_event_id`, `request_id`, `idempotency_key`
- structured payload plus redacted model-visible summary

### Artifact Model

Artifacts are durable outputs produced by tools, workers, sandboxes, or model turns.

Artifact metadata should include:

- `artifact_id`, `kind`, `name`, `mime_type`, `size_bytes`, `sha256`
- producer references: `session_id`, `branch_id`, `job_id`, `tool_call_id`, `sandbox_id`
- storage reference: path, object key, or external URI
- retention class and expiration policy
- visibility: model-visible summary, user-visible attachment, internal-only, or secret

Large artifacts should be referenced by ID in session nodes instead of embedded in messages.

### Changeset Model

Changesets represent proposed or applied modifications to a workspace or session graph.

Changeset metadata should include:

- `changeset_id`, producer job/worker/sandbox
- base reference: git commit, file snapshot, session node, or artifact digest
- changed files or graph nodes
- summary and risk notes
- validation results
- merge status: proposed, applied, rejected, conflicted, reverted

Worker writes should flow back through changesets rather than directly mutating the orchestrator's trusted branch without review.

### Failure Model

Failures must be explicit and resumable where possible.

Failure records should classify:

- provider failure
- tool failure
- permission denial
- capability missing
- sandbox failure
- storage failure
- validation failure
- timeout/cancellation
- merge conflict

Each failure should include:

- retryability: no, retry-same, retry-new-sandbox, retry-after-approval, retry-after-capability-change
- user/action hint
- affected IDs
- partial artifacts or progress that remain valid

### Idempotency

Runtime requests that can be retried must accept an `idempotency_key`.

Idempotency applies to:

- creating jobs
- approving permissions
- creating artifacts
- applying changesets
- creating branches
- starting migrations

Repeated requests with the same key should return the original result or a safe conflict response. They must not create duplicate jobs, duplicate approvals, or duplicate applied changes.

## Engineering Execution

### Testing Strategy

Each refactor phase must have behavior-preserving tests before structural changes.

Required test layers:

- Unit tests for extracted components.
- Contract tests for stores, worker runtime, sandbox runtime, and tool runtime.
- Golden compatibility tests for current tool schemas and model-visible outputs where stability matters.
- Migration tests from existing JSON/file sessions.
- End-to-end smoke tests for CLI, Web, IM channel runtime, subagent, approval, and service install.

### Compatibility Matrix

Every phase should update a compatibility matrix covering:

- Entry surfaces: CLI, TUI, Web, HTTP API, Feishu, Weixin, ACP, cron, heartbeat.
- Storage backends: current JSON/files, future SQLite, future DB/cloud.
- Sandbox modes: current local workspace, future disposable local, future remote.
- Worker runtimes: current durable subagent, future local worker, future remote/external worker.
- Tool bundles: core code, web, browser, MCP, desktop, background, package, skill, memory, automation.

The matrix should mark each combination as supported, degraded, unsupported, or planned.

### Refactor Guardrails

- Keep public tool names stable unless a migration SPEC explicitly changes them.
- Keep current config defaults working.
- Do not move storage formats without a reader for old state.
- Do not change user-visible behavior in a file split unless the test names the behavior change.
- Prefer adapters over rewrites when crossing old and new boundaries.
- Keep `agent.go` as a facade during Phase 1 so downstream code can migrate gradually.
- New interfaces must have one current implementation before adding future-only abstraction layers.

### API Stability

Stable API surfaces include:

- CLI command behavior and flags.
- HTTP API routes and SSE event semantics.
- Tool names, required input fields, and structured result field names.
- Session IDs and channel session addressing.
- Service install/start/status/logs commands.

Breaking changes require:

- migration note
- compatibility adapter or versioned route
- test coverage for old and new behavior

## Operations And Product Experience

### Doctor

`godex doctor` should evolve into a multi-layer diagnostic surface:

- identity and provider readiness
- channel auth
- storage backend health
- sandbox health and cleanup
- tool runtime capability checks
- worker queue/job health
- session graph integrity
- artifact retention pressure
- prefix cache/context diagnostics

Doctor output should be usable both by humans and automation.

### Migration UX

Migrations should be explicit and reversible where practical.

Expected UX:

- `godex migrate plan` shows source backend, target backend, affected records, risk, and estimated size.
- `godex migrate run --dry-run` validates without modifying durable state.
- `godex migrate run` writes a migration checkpoint.
- `godex migrate rollback` works when the migration is marked rollback-safe.
- Web Settings should show migration status and last checkpoint.

### Observability

GoDex should expose operational visibility for:

- active sessions, branches, workers, sandboxes, and jobs
- event stream lag
- model request count and token estimates
- tool call count, latency, and failure rate
- memory and storage pressure
- restart count and watchdog status
- approval queue age

Local deployments should get logs and doctor summaries. Larger deployments should be able to export metrics/events.

### Retention

Retention should be policy-driven:

- session event retention
- transcript archive retention
- artifact retention by kind and visibility
- sandbox workspace retention
- worker job retention
- approval/audit retention

Deletion must respect references. A retained session node may keep artifact metadata while allowing large artifact payload cleanup if the payload is no longer needed.

### Performance Targets

Initial targets:

- Local service should remain usable under a 300 MiB memory budget with conservative GC settings.
- Context build should keep stable prompt prefixes cache-friendly.
- Session graph lookup for active branch context should be fast enough for interactive chat.
- Worker job status and logs should be cheap to inspect without loading full transcripts.
- Storage backends should support bounded cleanup and indexing for long-running installations.

## Product Surfaces

GoDex 2.0 concepts need visible surfaces, not only internal APIs.

- **Branch Inspector**: view session branches, current head, checkpoints, rollback points, and merge records.
- **Worker Inspector**: view worker jobs, assigned role, sandbox, progress, failures, artifacts, and merge status.
- **Sandbox Inspector**: view sandbox lifecycle, workspace path, command policy, tool runtime, resource usage, and cleanup action.
- **Artifact Inspector**: view artifact metadata, preview, producer, retention policy, and references.
- **Context Inspector**: show current prompt breakdown, prefix-cache hashes, dynamic runtime sections, memory layers, branch source, and tool schema exposure.
- **Approval Surface**: show requester, channel identity, agent/worker/job/sandbox IDs, normalized action, scope, and downgrade reason.

These surfaces should exist in Web first and expose enough API shape for CLI inspection commands.

## Orchestration DSL, Run Model, And Ecosystem Interfaces

GoDex 2.0 should not only provide internal runtime objects. It should also provide importable, exportable, auditable descriptions. Coze's agent/bot product model, n8n's workflow/run model, and Dify's app/workflow DSL all point to the same lesson: platform capabilities need to be declared, reused, published, and tracked at runtime.

### Orchestration DSL

The Orchestration DSL describes reusable agent orchestration units. It can start as an extension of package, skill, and workflow manifests, then stabilize into an independent schema.

The DSL should describe:

- metadata: name, version, description, author, trust, compatibility.
- agents: orchestrator role, worker roles, model policy, prompt strategy.
- inputs: user input, files, parameters, secret references, channel constraints.
- jobs: worker task template, required bundles, required tools, sandbox policy, write scope, timeout.
- control flow: sequence, parallel, fan-out/fan-in, approval gate, retry, conditional branch.
- memory/knowledge: memory layers, knowledge sources, RAG policy.
- outputs: artifacts, changesets, summary, delivery target, merge policy.
- validation: smoke command, test command, quality gate, manual review requirement.

The first phase does not need a complete graphical DSL. The minimum target is to let current package commands, workflows, long tasks, and subagent jobs gradually map to one shared concept set.

### Run Model

The Run Model describes how one orchestration execution is tracked. It complements `session_id`, `branch_id`, and `job_id` with runtime execution dimensions.

Core IDs:

- `run_id`: one orchestrated execution, possibly containing multiple jobs.
- `step_id`: one deterministic step inside a run.
- `attempt_id`: one attempt for a step or job.
- `tool_call_id`: one tool invocation.
- `changeset_id`: one proposed or applied modification.
- `artifact_id`: one durable output.

Relationship rules:

- One `run_id` belongs to one `session_id` and one source `branch_id`.
- One run may create multiple jobs, and each job may have multiple attempts.
- Retry must create a new `attempt_id` while preserving the original `job_id`.
- Tool calls, artifacts, failures, and changesets must trace back to `run_id` and `attempt_id`.
- Run completion is not the same as changeset merge. Merge is a separate review/apply result.

### Knowledge vs Memory

GoDex 2.0 should clearly separate knowledge from memory.

- **Knowledge**: external or project information sources, usually from docs, code, web pages, databases, or uploaded files. It emphasizes retrieval, citation, and updates.
- **Memory**: long-term preferences, facts, workflows, risks, lessons, and user agreements learned during runtime. It emphasizes cross-session behavior improvement and context compression.
- **Session Context**: short-term context built for the current branch and task, including messages, runtime state, selected memory, and selected knowledge snippets.
- **Artifact**: concrete output produced by a tool or worker. It does not automatically become knowledge or memory.

Rules:

- Knowledge can be retrieved by a RAG pipeline, but it should not be written automatically into durable memory.
- Memory candidates must pass extraction, deduplication, audit, or user-confirmation policy.
- Workers may read knowledge, but memory writes should be decided by the orchestrator or memory policy.
- Context Inspector should show memory layers and knowledge snippets separately to avoid source confusion.

### Template And Package Gallery

GoDex 2.0 should treat roles, workflows, sandbox policies, tool policies, knowledge bindings, and validation gates as reusable assets.

Package/Gallery assets can include:

- Agent role template.
- Worker role template.
- Orchestration DSL template.
- Sandbox policy template.
- Tool policy template.
- Knowledge connector template.
- Validation/smoke template.
- Web surface extension metadata.

Installing a package should not grant high-risk capabilities by default. Packages should declare requested capabilities, doctor/security review should show risk, and users or policy should decide the enabled scope.

### MCP Inbound And Outbound

MCP should support both inbound and outbound use.

- **Outbound MCP**: GoDex acts as an MCP client, calls external MCP resources/tools, and includes them in the tool runtime.
- **Inbound MCP**: GoDex exposes its own capabilities as an MCP server so other agents/runtimes can call GoDex jobs, workflows, knowledge, artifacts, or context inspection.

Inbound MCP should initially expose low-risk, auditable capabilities:

- list available workflows/packages/roles
- start job with idempotency key
- inspect job/run status
- read artifact metadata or approved artifact payload
- inspect context summary

High-risk capabilities such as applying changesets, shell execution, memory mutation, and approval resolution must go through capability policy and approval authority.

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

Implementation note:

- The current implementation adds an `internal/sandbox` local sandbox model and attaches one default local sandbox to each `Agent`.
- Workspace-sensitive tools obtain workspace, temp, and execution config through the sandbox tool binding while public tool names and schemas stay unchanged.
- Tool execution context carries `sandbox_id`, and durable subagent jobs persist and expose the worker sandbox ID through API/model views.
- Phase 2 does not introduce remote/disposable sandbox runtime and does not expand the Context Inspector schema; those remain later-phase work.

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

Implementation note:

- The current implementation adds an `internal/workerruntime` contract package for job request, progress event, result/artifact, capability, review, and merge contracts.
- Durable subagent start/resume/cancel/review/merge execute through the local GoDex worker runtime adapter, while the default behavior remains the current local durable subagent.
- Durable subagent records, API/model views, and events expose `worker_id` and continue to expose the Phase 2 `sandbox_id`.
- Phase 3 does not implement remote transport, distributed scheduling, or Session Graph branch handoff; those remain later-phase work.

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
