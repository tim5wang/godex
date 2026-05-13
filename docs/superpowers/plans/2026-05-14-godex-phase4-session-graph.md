# Phase 4 Session Graph Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a compatibility Session Graph layer that records branchable session context without replacing the existing linear session runtime.

**Architecture:** Add `internal/sessiongraph` as a small graph model and JSON store. Keep current `state.json`, `manifest.json`, `checkpoint.json`, timeline, turns, and queue files as the source of compatibility. Backend session flow appends graph metadata for mainline checkpoints, forks, and worker review/merge records.

**Tech Stack:** Go, existing JSON atomic writes, existing backend service tests, no new external dependencies.

---

## Scope

- Keep current CLI/Web/HTTP/channel behavior and session IDs unchanged.
- Store graph metadata as `graph.json` inside each existing session directory.
- Do not introduce storage backend abstraction in this phase.
- Do not implement distributed graph coordination.

## Tasks

### Task 1: Session Graph Package

**Files:**
- Create: `internal/sessiongraph/graph.go`
- Create: `internal/sessiongraph/json_store.go`
- Create: `internal/sessiongraph/graph_test.go`

- [ ] Define opaque string IDs for `NodeID` and `BranchID`.
- [ ] Define `SessionGraph`, `BranchHead`, `GraphNode`, `CheckpointRecord`, and `MergeRecord`.
- [ ] Implement `EnsureMainBranch`, `AppendNode`, `CloneBranch`, `RollbackBranch`, `MergeBranch`, `Head`, and `Nodes`.
- [ ] Implement JSON load/save against one `graph.json` path.
- [ ] Tests cover empty graph initialization, append, clone, rollback, merge record, and JSON round trip.

### Task 2: Backend Session Graph Wiring

**Files:**
- Modify: `internal/services/backend/backend.go`
- Modify: `internal/services/backend/backend_test.go`

- [ ] Load or initialize `graph.json` when opening a session.
- [ ] Persist graph metadata after mainline session checkpoints.
- [ ] Keep old sessions without `graph.json` readable and initialize `branch:main`.
- [ ] Keep legacy `state.json` and checkpoint reads unchanged.
- [ ] Add tests for opening legacy sessions, mainline graph head advancement, and checkpoint node metadata.

### Task 3: Fork And Worker Branch Metadata

**Files:**
- Modify: `internal/services/backend/backend.go`
- Modify: `internal/services/backend/backend_test.go`
- Modify: `internal/agent/subagent_jobs.go`
- Modify: `internal/agent/subagent_jobs_test.go`

- [ ] `ForkSession` clones from the source main branch head and records branch metadata.
- [ ] Durable subagent jobs persist optional `source_branch_id` and `source_node_id`.
- [ ] Worker review/merge operations append merge records when session graph context is present.
- [ ] Tests cover fork branch metadata and worker branch fields round trip.

### Task 4: Review, Verify, Commit

- [ ] Run `go test ./internal/sessiongraph ./internal/services/backend ./internal/agent -count=1`.
- [ ] Run `go test ./...`.
- [ ] Run `git diff --check`.
- [ ] Fix review findings.
- [ ] Commit as `feat(session): add session graph compatibility layer`.

## Acceptance Criteria

- Existing sessions can be read without migration failure.
- Mainline chat still behaves as a normal linear conversation.
- Worker explorations can carry cloned branch metadata.
- Existing public APIs, tool schemas, and default storage layout remain compatible.
