# GoDex Workflow Runtime

> 状态：Active（durable workflow runtime 权威说明）
> 修订日志：2026-08-11 动态并行 DAG（2.1）+ 重启恢复（2.2）+ 上下文预算（2.3）落地

This document tracks the durable workflow runtime used for long-running,
multi-agent work.

## Node Handoff Artifacts

Workflow nodes write an immutable handoff artifact when they reach a terminal
state. The workflow status view stays compact and only exposes the artifact
reference, digest, verdict, and bounded result preview.

Artifacts are stored under:

```text
~/.godex/workflows/{workflowID}/handoffs/{nodeID}/{attempt}.json
```

The v1 artifact schema is:

```json
{
  "workflow_id": "wf_review",
  "node_id": "test",
  "attempt": 1,
  "job_id": "subagent_...",
  "status": "completed",
  "verdict": "pass",
  "summary": "bounded handoff summary",
  "result_preview": "bounded model-visible result",
  "error": "",
  "changed_files": [],
  "artifact_refs": [],
  "created_at": "2026-04-27T00:00:00Z",
  "digest": "sha256..."
}
```

Verdicts are parsed from `Verdict: pass|fail|blocked|needs_fix` or a JSON
`verdict` field. If no explicit verdict is present, completed nodes default to
`pass`, failed nodes to `fail`, and canceled nodes to `blocked`.

## Dependency Handoff Injection

Nodes with dependencies receive bounded handoff context by default. The injected
context includes dependency node id, status, verdict, summary, changed files, and
optional artifact references. It does not include raw subagent transcripts,
complete logs, or full diffs.

Node fields:

```json
{
  "depends_on": ["dev"],
  "handoff_policy": "summary",
  "handoff_from": ["dev"],
  "handoff_max_bytes": 8000,
  "preview_merge": true
}
```

`handoff_policy` may be `none`, `summary`, `summary_artifacts`, or `selected`.
The default is `summary` when a node has dependencies. `handoff_max_bytes`
defaults to 8000 and is capped at 32000.

## Preview Merge

When `preview_merge` is enabled, completed dependency job changes are applied to
the downstream subagent's isolated workspace before it starts. The main
workspace is not modified. If the downstream job has a `write_scope`, dependency
changes are also applied to its merge baseline so review output contains only
the downstream node's own delta.

## Dynamic Append Nodes

The workflow tool supports `action: "append_node"` for adding pending nodes to
an existing workflow. Appends are validated against the full DAG and are capped
at 64 total nodes.

```json
{
  "action": "append_node",
  "workflow_id": "wf_review",
  "idempotency_key": "test-fix-1",
  "parent_node_id": "test",
  "reason": "test node returned Verdict: needs_fix",
  "nodes": [
    {
      "id": "fix_1",
      "kind": "fix",
      "prompt": "Fix the bug reported by the test node.",
      "depends_on": ["test"]
    }
  ]
}
```

If the same `idempotency_key` is replayed, the existing appended node ids are
returned and no duplicate nodes are created. Each append is recorded in
`events.jsonl` with its parent node and reason.

## Conditional Append Edges

Edges can append a node template when a terminal source node matches a
structured condition. Conditions only inspect `status` and `verdict`; arbitrary
expressions and model-evaluated conditions are intentionally unsupported.

```json
{
  "edges": [
    {
      "id": "test-failed",
      "from_kind": "test",
      "when": {"verdict": "needs_fix"},
      "iteration_key": "repair-loop",
      "max_iterations": 3,
      "append": {
        "id": "fix_{iteration}",
        "kind": "fix",
        "prompt": "Fix the issue reported by {source}.",
        "depends_on": ["{source}"]
      }
    }
  ]
}
```

Each edge/source pair is processed once, including after restart. `{source}` and
`{iteration}` placeholders are expanded in the appended node template. When the
iteration cap is reached, GoDex records an `edge_iteration_cap` event and marks
the workflow as `error` instead of appending another node.

## LongTask Story Layer

The `longtask` tool is a Ralph-style layer on top of the durable workflow
runtime. It compiles prioritized user stories into sequential workflow nodes
with `kind: "story"`. Each story node is still executed as a fresh durable
subagent, so workflow handoff, preview merge, cancellation, wait, and bounded
status views continue to apply.

LongTask specs are stored beside the compiled workflow:

```text
~/.godex/workflows/{workflowID}/longtask.json
```

Minimal create input:

```json
{
  "action": "create",
  "longtask_id": "lt_checkout",
  "project": "Shop",
  "branch_name": "longtask/checkout",
  "description": "Improve checkout",
  "quality_checks": ["go test ./...", "git diff --check"],
  "merge_policy": "auto_merge",
  "commit_policy": "auto_commit",
  "stories": [
    {
      "id": "US-001",
      "title": "Add backend API",
      "description": "Implement the checkout API endpoint.",
      "acceptance_criteria": ["API tests pass"],
      "priority": 1,
      "write_scope": ["internal/checkout"]
    }
  ]
}
```

Supported actions:

- `create`: persist the LongTask spec and compile stories into workflow nodes.
- `status`: return story-level status derived from the underlying workflow.
- `start`: start ready story nodes through workflow scheduling.
- `wait`: wait for running story subagent jobs.
- `cancel`: cancel one story node or pending/running workflow nodes.
- `complete_story`: manually complete a story with a result containing an
  optional `Verdict:` line.
- `finalize_story`: run deterministic runtime validation for a completed story
  that reported `Verdict: pass`.
- `run`: automatically loop through `start -> wait -> finalize_story` until all
  stories pass, a story blocks/fails, no progress is possible, or
  `max_iterations` is reached. Optional inputs include `max_iterations`,
  `wait_timeout_ms`, `auto_repair`, and `max_repair_attempts`.

Validation artifacts are stored under:

```text
~/.godex/workflows/{workflowID}/validations/{nodeID}/{attempt}.json
```

A validation artifact records each configured quality check command, bounded
output preview, status, error, and duration. If no `quality_checks` are
configured, a completed story with `Verdict: pass` is marked with
`validation_status: "skipped"`.

Validation runs in the completed story subagent worktree when one
exists (so checks can validate the isolated changes before they are
merged), and otherwise falls back to the host workspace. In either
case the quality check is executed through the agent's runtime
`Tools.Execution` sandbox (host / docker / ssh per the runtime
config), **not** the subagent's inner sandbox. This is the same
sandbox the subagent itself runs in, so a `docker` runtime puts
both the subagent and the validation commands in the same docker
sandbox; an `ssh` runtime forwards both to the same SSH target.
A story with no `JobID` (i.e. one that was created without
running a subagent first) runs its checks against the host
workspace inside the runtime sandbox.

The distinction matters for two reasons:

1. The validation workspace is **not** the subagent's
   private sandbox. The subagent and the validation share the
   same executor. Configuring the runtime to use a stricter
   sandbox (e.g. `mode: docker` with a read-only root
   filesystem) tightens both at once.
2. The validation workspace is **not** the host filesystem
   outside any sandbox either. The runtime `Tools.Execution`
   config is the boundary; running with the default
   `mode: host` is equivalent to running the quality check
   on the operator's machine.

## LongTask Repair, Merge, and Commit

`run` can optionally enable a repair loop:

```json
{
  "action": "run",
  "workflow_id": "lt_checkout",
  "auto_repair": true,
  "max_repair_attempts": 2
}
```

When a story blocks or validation fails, GoDex appends a deterministic repair
node such as `US-001_repair_1`, injects the failed node handoff, validation
artifact reference, failure reason, and previous result preview, then rewires
downstream pending stories to depend on the repair node. Appends use a stable
idempotency key, so restarting the run does not duplicate repair nodes.

After validation `pass` or `skipped`, `finalize_story` applies the configured
merge/commit policy:

- `merge_policy: "auto_merge"`: review and merge the story subagent's
  `write_scope` changes into the main workspace.
- `merge_policy: "review_only"`: write review metadata but require a manual
  subagent merge before the story can pass.
- `commit_policy: "auto_commit"`: in a Git repo, commit only the files merged
  from the story with message
  `longtask(<id>): complete <storyID> <title>`.
- `commit_policy: "none"`: skip committing after merge.

Commit artifacts are stored under:

```text
~/.godex/workflows/{workflowID}/commits/{nodeID}/{attempt}.json
```

Each artifact records validation-adjacent finalization data: merge status,
commit status, changed files, commit hash/message when available, and blocking
errors such as conflicts or failed commits. Non-Git repositories skip commit
with `commit_status: "skipped_non_git"` while still allowing a successful merge
to complete the story.

A story `passes` only when the compiled node is `completed`, its normalized
verdict is `pass`, and runtime validation is either `pass` or `skipped`. If
`finalize_story` runs quality checks and any check fails, GoDex writes the
validation artifact and marks the story/workflow as `error`. If merge or commit
fails, the story is also marked `error` and can be repaired or handled manually.

## LongTask Product Surface

LongTask can be driven through the tool, HTTP API, CLI, and Web inspector.

CLI:

```text
godex longtask list --session local:default
godex longtask create --file prd.json --session local:default
godex longtask run lt_checkout --auto-repair --max-repair-attempts 2
godex longtask status lt_checkout
godex longtask finalize lt_checkout --node US-001
```

HTTP:

```text
GET  /api/sessions/{id}/longtasks
POST /api/sessions/{id}/longtasks
GET  /api/sessions/{id}/longtasks/{workflowID}
POST /api/sessions/{id}/longtasks/{workflowID}/run
POST /api/sessions/{id}/longtasks/{workflowID}/cancel
POST /api/sessions/{id}/longtasks/{workflowID}/finalize
```

The Web Chat inspector includes a LongTasks tab with story progress,
validation/merge/commit status, repair attempts, and run/finalize/cancel
controls.

## Continuation Compaction

Manual `/compact` and automatic compaction now prepend a deterministic
`Pinned continuation state` section to the summary. The snapshot includes recent
user goals, current todos, active LongTasks/workflows, active subagents, pending
approvals/blockers, and recent touched files/validation commands. This keeps
the next turn on task without immediately spending context on transcript
searches. The transcript is still archived and should be searched only when
exact older details are needed.
