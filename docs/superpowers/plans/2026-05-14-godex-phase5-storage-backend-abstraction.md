# Phase 5 Storage Backend Abstraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a session storage repository abstraction with JSON compatibility and opt-in SQLite backend.

**Architecture:** Introduce `internal/sessionstore` as the storage boundary for session durable files. Keep JSON/file as the default backend and preserve the current session directory layout. Add a SQLite backend for opt-in local reliability and backend diagnostics/export-import primitives without changing CLI/Web/HTTP/channel entrypoints.

**Tech Stack:** Go, `database/sql`, existing `modernc.org/sqlite`, existing JSON atomic write helpers.

---

## Scope

- JSON remains default.
- SQLite is enabled only with `storage.session_backend: sqlite` or `GODEX_STORAGE_SESSION_BACKEND=sqlite`.
- Existing session IDs, public APIs, tool schemas, and channel entrypoints remain unchanged.
- No cloud/object/server database backend in this phase.

## Tasks

### Task 1: Session Store Package

**Files:**
- Create: `internal/sessionstore/store.go`
- Create: `internal/sessionstore/json_store.go`
- Create: `internal/sessionstore/sqlite_store.go`
- Create: `internal/sessionstore/store_test.go`

- [ ] Define `SessionData`, `CheckpointData`, `Diagnostics`, and `Store`.
- [ ] Implement JSON backend preserving existing `manifest.json`, `state.json`, `timeline.json`, `turns.json`, `turn_queue.json`, `checkpoint.json`, `events.jsonl`, and `graph.json`.
- [ ] Implement SQLite backend with schema versioning and CRUD for session blobs.
- [ ] Implement single-session export/import between stores.
- [ ] Tests cover JSON compatibility, SQLite restart restore, import/export, list/delete, and diagnostics.

### Task 2: Config

**Files:**
- Modify: `internal/core/config/types.go`
- Modify: `internal/core/config/defaults.go`
- Modify: `internal/core/config/schema.go`
- Modify: `internal/core/config/manager.go`
- Modify: `internal/core/config/template.go`
- Modify: `internal/core/config/config.go`
- Modify: `internal/core/config/config_test.go`

- [ ] Add `storage.session_backend`, default `json`.
- [ ] Add `storage.sqlite_path`, default empty.
- [ ] Add env vars `GODEX_STORAGE_SESSION_BACKEND` and `GODEX_STORAGE_SQLITE_PATH`.
- [ ] Ensure dirs creates the SQLite parent directory when an explicit path is configured.
- [ ] Tests cover defaults, env, update manager, and template output.

### Task 3: Backend Integration And Diagnostics

**Files:**
- Modify: `internal/services/backend/backend.go`
- Modify: `internal/services/backend/backend_test.go`
- Modify storage doctor/diagnostic code where current storage health is rendered.

- [ ] Backend constructs a `sessionstore.Store` from config.
- [ ] JSON backend remains the default and keeps existing tests passing.
- [ ] SQLite backend can open, persist, close/recreate service, and restore manifest/state/timeline/turns/queue/graph.
- [ ] Add diagnostics surface for active backend, sqlite path, schema version, and health.
- [ ] Add minimal single-session JSON<->SQLite import/export helpers.

### Task 4: Review, Verify, Commit

- [ ] Run `go test ./internal/sessionstore ./internal/services/backend ./internal/core/config -count=1`.
- [ ] Run `go test ./...`.
- [ ] Run `git diff --check`.
- [ ] Fix review findings.
- [ ] Commit as `feat(storage): add session store backends`.

## Acceptance Criteria

- Existing JSON/file state remains supported and default.
- New storage-aware code uses session store interfaces for session durable state.
- SQLite can be enabled without changing CLI/Web/HTTP/channel entrypoints.
- Storage diagnostics identify the active session backend and health.
