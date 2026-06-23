[Simplified Chinese](README.md) | [English](README.en.md)

# GoDex

<p align="center">
  <img src="ui/web/public/brand/godex-icon.jpg" alt="GoDex icon" width="160" />
</p>

GoDex is a local-first AI agent workspace. It connects CLI, TUI, Web, HTTP API, Feishu, Weixin, and other entry points to the same backend so chat, tool execution, file attachments, long-term memory, subagents, approvals, and run audits share one session runtime.

## Product Positioning

GoDex is built for teams and individuals who need AI agents to work inside real engineering workflows:

- Run inside local projects while keeping control over the workspace, configuration, and runtime state.
- Support long-running tasks, tool calls, subagents, and workflows instead of only single-turn chat.
- Configure, approve, trace, and review high-risk actions.
- Use the Web UI as the primary management surface while CLI, TUI, IM, and API channels share the same capabilities.

Good fits include:

- Understanding, modifying, testing, and assisting deployment work in codebases.
- Multi-channel team bots across Web, TUI, Feishu, and Weixin.
- Local agent runtimes that need long-term memory, historical recall, and auditable tool execution.
- Agent platform prototypes that bring packages, skills, automation, and subagents under unified governance.

## Core Features

- **Shared Session Runtime**: CLI, TUI, Web, HTTP API, and IM channels share sessions, timelines, attachments, permissions, and memory.
- **Web Workspace**: Draggable multi-panel grid layout (2×2 / 3×3), Chat, Terminal, Files, Automation, Nodes, Notes, Skills, Memory, Settings, approval panels, and subagent management — fully mobile-adaptive.
- **Multi-provider Management**: Anthropic-compatible providers, OpenAI-compatible providers, the OpenAI Codex provider, model policies, and dynamic Web Settings configuration.
- **Resilient Long Tasks**: Ralph-style LongTask story loop, auto-repair, validation artifacts, auto merge/commit, runner phase checkpoints, and in-flight follow-up/steering.
- **Context and Memory**: Model-assisted compression with pinned continuation snapshots, rule-based fallback, transcript archive, `history_search`, durable memory, candidate inbox, audit/restore, compact memory injection, and token estimation.
- **Agent Profile**: CLI/TUI/ACP default to the focused `coding` profile, while Web/IM default to the broader `general` profile; tool exposure can be overridden per entry point or command.
- **Tools and Safety**: merge, grep (ripgrep dual-backend), edit_file multi-edit, WorkspaceFS file boundaries, shell guard, manual/review/yolo approval modes, security profiles, and security audit.
- **Subagent and Workflow**: Durable subagent jobs, review/merge/cancel/resume, LongTask surfaces for Web/CLI/API, capability boundaries, isolated workspace strategies, and compact handoff.
- **Package and Skill Ecosystem**: Package manifests, role/command contracts, tool policies, quality diagnostics, smoke runs, reinstall tracking, and Claude Code import.
- **Automation and Channels**: Cron, Heartbeat, Feishu, Weixin, and OpenAI-compatible chat completions API; IM approval messages show the tool and key parameter summary.
- **Control Plane Foundation**: Lightweight Node Registry and read-only Nodes Dashboard for observing multiple GoDex runtimes.
- **Notes Workspace**: Local Markdown notes, search/tags, and saving agent output from Chat into notes.
- **Storage Governance**: Storage doctor plus browser cache, session checkpoint, artifact, and subagent garbage collection.
- **Terminal**: Real Go PTY backend + xterm.js frontend for a native shell experience.
- **Performance**: Anthropic-style cache_control breakpoints, prompt caching, and compaction optimizations.
- **Single-binary Web UI**: Web dist embedded in Go binary, cross-platform (Linux/macOS/Windows) single-file deployment.

## Quick Start

### Requirements

- Go `1.26+`
- Node.js + `pnpm`, only required when building the Web frontend
- At least one available LLM provider

### Run from Source

```bash
go mod download
pnpm -C ui/web install
pnpm -C ui/web build
go run ./cmd/godex serve --addr 127.0.0.1:8080
```

Open:

```text
http://127.0.0.1:8080
```

### Initialize a Project

```bash
go run ./cmd/godex setup --dir /path/to/project
```

On first startup, if the configuration file does not exist, GoDex creates a commented default configuration.

### Configure Models

The recommended path is to use Web `Settings` to manage providers, models, and key references. You can also log in from the CLI:

```bash
godex login openai --mode platform-api-key
godex login codex --mode codex-oauth
godex providers list
godex providers test <provider-id>
```

Global configuration and default runtime data live under `~/.godex`: providers, skills, sessions, memory, tmp/cache, logs, and related files are not written to the current project directory by default. Project directories only keep explicitly created project files such as `godex.yaml`, `.env.example`, and `AGENT.md`. For details, see [docs/user-guide.md](docs/user-guide.md).

## Common Entry Points

```bash
# Interactive CLI
go run ./cmd/godex

# One-off questions. CLI/TUI/ACP use the coding profile by default.
go run ./cmd/godex ask "Summarize the current repository structure"
go run ./cmd/godex ask --profile general "Help me plan a product proposal"

# Run a slash command
go run ./cmd/godex command /doctor

# Inspect and clean local runtime storage
go run ./cmd/godex doctor storage
go run ./cmd/godex gc --dry-run

# Full-screen TUI, also the default entry point
go run ./cmd/godex

# Web / HTTP / SSE / channel runtime
go run ./cmd/godex serve --addr 127.0.0.1:8080

# Import Claude Code ecosystem resources
go run ./cmd/godex import claude --source .claude --dry-run
```

For more commands, slash commands, and HTTP API details, see [docs/user-guide.md](docs/user-guide.md).

## Web Workspace

The Web UI is currently the most complete product entry point:

- **Chat**: Multi-entry sessions, attachments, approvals, model switching, Context & Recall, timeline, subagent progress, and saving agent output into Notes.
- **Settings**: Global/project configuration paths, providers/models, doctor, channel status, and security policies.
- **Nodes**: Read-only observation of local and manually/automatically registered GoDex runtimes.
- **Notes**: Local Markdown notes, tags, search, editing, and Chat integration.
- **Memory**: Durable memory, candidate inbox, suppression, audit diff, and restore/reapply.
- **Skills**: Package/skill management, quality diagnostics, smoke runs, and reinstall.
- **Automation**: Cron, Heartbeat, and run logs.

Build the frontend (outputs directly to Go embed directory):

```bash
pnpm -C ui/web build
```

The package development flow can use the example skill:

```text
examples/skills/package-developer
```

After installing this local skill from the Web `Skills` installer or Chat, load `package-developer` to get guidance for creating `godex.package.yaml`, running smoke tests, installing GitHub packages, reinstalling, and uninstalling.

## Agent Profile

`agent.profile` controls default agent behavior and does not replace `security.profile`. The default entry-point policy is:

- `acp`, `cli`, `tui`: `coding`, exposing only core coding, todo, `tool_exchange`, and essential session/compression/history tools by default.
- `web`, `weixin`, `feishu`: `general`, keeping the full workspace experience.

When the coding profile needs networking, browser, subagent, skill, memory, package, or related capabilities, the agent can use `tool_exchange` to enable the corresponding bundle on demand. CLI/TUI/ACP can temporarily override with `--profile general|coding`; Web `Settings` can also modify `agent.default_profiles.*`.

## Documentation

- [GoDex 2.0 Architecture SPEC](docs/SPEC.en.md): Agent/Sandbox, Orchestrator/Worker, Session Graph, and storage decoupling roadmap.
- [User Guide](docs/user-guide.md): Installation, configuration, providers, Web UI, tools, Memory, API, and release checks.
- [Project Structure](docs/project-structure.md): Directory responsibilities and refactoring boundaries.
- [Memory Design Principles](docs/memory-design-principles.md): Long-term memory, candidates, recall, and audit design.
- [Workflow Runtime](docs/workflow-runtime.md): Workflow/subagent runtime design.
- [Self-deployment Guide](docs/self-deploy.md): Deploying to a server and self-managed operations.
- [Capability Enhancement v2](docs/capability-enhencement-v2.md): App Shell, Node Registry, Notes, Claude import, and related phase plans and progress.
- [P0-P6 End-to-end Validation](docs/p0-p6-e2e-validation.md): Manual acceptance checklist.
- [High-ROI Roadmap](docs/high-roi-roadmap.md): Current capability baseline and future direction.

## Directory Structure

```text
cmd/godex/        CLI binary entry point
internal/app/     CLI, serve, and slash command assembly
internal/agent/   agent loop, context, turn runtime, subagents
internal/runtime/ HTTP/Web UI, IM channels, Cron, Heartbeat, REPL
internal/services/backend, commands, historysearch, noderegistry, sessionadmin
internal/tools/   bash/file/browser/web/memory/skill and other tools
internal/core/    config, conversation, compression, memory, notes, skill, media
ui/web/           React + Vite Web frontend
docs/             Product, architecture, validation, and deployment docs
```

For a fuller explanation, see [docs/project-structure.md](docs/project-structure.md).

## Release Check

Before release, run:

```bash
./scripts/release_check.sh
```

It runs Go tests, builds the Go binary, and builds the Web frontend. Locally, you can also run them separately:

```bash
go test ./...
pnpm -C ui/web build
git diff --check
```
