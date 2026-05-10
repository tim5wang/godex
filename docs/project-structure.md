# Project Structure

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
- `internal/domain`: shared domain types that should not depend on runtime adapters or concrete tools.
- `internal/toolruntime`: typed tool framework, tool handler, permissions, interceptors, and tool execution context.
- `internal/tools`: concrete tools and tool bundles. Tool factories return `toolruntime.Tool`.
- `internal/platform`: small infrastructure utilities for filesystem, logging, workspace paths, text, browser helpers, and tooling definitions.
- `internal/core`: core product modules such as config, protocol, conversation, compression, memory, notes, skill, media, MCP, background tasks, insights, instructions, and teammate orchestration.
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

`ui/web/src/app/appRegistry.tsx` owns the built-in Web app registry and sidebar route declarations. `ui/web/src/pages/*` files are route wrappers only. Feature code belongs in:

- `features/chat`
- `features/automation`
- `features/nodes`
- `features/notes`
- `features/memory`
- `features/settings`
- `features/skills`

Shared UI primitives stay in `components/`; API/SSE/types/notification helpers stay in `lib/`; truly global state stays in `store/`.

## Embedded UI

Build and sync the Web UI with:

```bash
cd ui/web && pnpm build
cd ../..
./scripts/sync_embedded_web.sh
```

The sync target is `internal/uiassets/embedded_dist`.
