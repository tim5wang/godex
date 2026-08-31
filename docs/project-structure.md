# Project Structure

> 状态：Active（项目结构规范）

GoDex uses a `cmd/` + `internal/` layout. New code should land in the narrowest layer that owns the behavior.

## Top Level

- `cmd/godex/`: binary entrypoint only.
- `internal/`: Go implementation packages.
- `ui/web/`: React/Vite Web UI. Route pages stay thin; feature implementation lives under `src/features/*`.
- `docs/`: design notes and roadmaps.
- `scripts/`: repeatable local build, smoke, and embedded UI sync helpers.
- `deploy/`: packaging and deployment artifacts.
- `examples/`: checked-in examples. Runtime user data does not live here.

## Internal Layers

- `internal/app`: CLI/serve assembly, lifecycle wiring, and command handlers.
- `internal/agent`: agent loop, context building, turn runtime, and durable subagent jobs.
- `internal/runtime`: external adapters such as HTTP/WebUI, IM channels, Cron, Heartbeat, and REPL.
- `internal/services`: reusable services behind adapters, such as backend, commands, history search, node registry, and session admin.
- `internal/contracts`: neutral shared wire contracts; `protocol` lives here so domain, core, and platform can depend on it without reversing layer direction.
- `internal/domain`: shared domain types that should not depend on runtime adapters or concrete tools.
- `internal/toolruntime`: typed tool framework, tool handler, permissions, interceptors, and tool execution context.
- `internal/tools`: concrete tools and tool bundles. Tool factories return `toolruntime.Tool`.
- `internal/platform`: infrastructure adapters and utilities for local repositories, filesystem, logging, workspace paths, text, browser helpers, and tooling definitions. JSON task/todo/message persistence lives in `platform/localstore` behind domain repository interfaces.
- `internal/core`: core product modules such as config, conversation, compression, memory, notes, skill, media, MCP, background tasks, insights, instructions, and teammate orchestration.
- `internal/tui`: Bubble Tea UI.
- `internal/uiassets`: embedded Web UI distribution used by the single binary.

## Runtime Data

The default local runtime root is `~/.godex` (`GODEX_HOME`). GoDex should not create a hidden `.godex/` directory in arbitrary project directories unless the user explicitly configures one.

- `~/.godex/sessions/`: session snapshots, turns, events, attachments, and subagent jobs.
- `~/.godex/tasks/` and `~/.godex/todos/`: task and todo state.
- `~/.godex/memory/`: durable memory markdown plus sidecar indexes.
- `~/.godex/notes/`: local Markdown notes managed by the Notes app and `/note` command.
- `~/.godex/skills/` and `~/.godex/packages/`: installed skills and packages.
- `~/.godex/tmp/`: temporary files, browser cache, large command-output spill files, and release build output.
- `~/.godex/channels/`, `~/.godex/cron/`, `~/.godex/heartbeat/`: adapter state.
- `~/.godex/control/`: lightweight node registry state. Node identity is stored in the configured state dir.

Top-level `skills/`, `temp/`, and `log/` are ignored only as protection for old local leftovers; new defaults should not create them.

## Web UI

`ui/web/src/app/appRegistry.tsx` is the executable source of truth for built-in Web apps and sidebar routes. `ui/web/src/pages/*` files are route wrappers only. Feature code belongs in the matching `features/*` vertical slice. As of 2026-08-31 the registry exposes Chat, Files, Automation, Nodes, Notes, Skills, Agent Templates, Memory, Settings, Business Agents, TaskBoard, and Usage.

`features/chat-v2` contains supporting state/components for the current Chat route; `/chat-v2` itself is only a compatibility redirect. `features/workflows` no longer owns a page: the removed Workflows product was superseded by Business Agents, and only the reusable `UiCardView` remains.

Shared UI primitives stay in `components/`; API/SSE/types/notification helpers stay in `lib/`; truly global state stays in `store/`.

## Embedded UI

Build the Web UI (Vite outputs directly to `internal/uiassets/embedded_dist`):

```bash
cd ui/web && pnpm build
```
