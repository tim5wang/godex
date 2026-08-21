# GoDex WASM plugin - TODO tracker

A Go-toolchain implementation of the GoDex mailbox JSON ABI
`godex:plugin@0.1` that exercises all four plugin faces in one module:

- **tools** - `todo_scan`: scan one workspace file for TODO/FIXME/XXX/HACK
  comment lines (with line numbers)
- **KV persistence** - the last scan per file is stored in plugin KV
  (namespace `todo-tracker`), so repeat scans report `new` and `resolved`
  items across sessions
- **prompt contribution** - `godex_prompts_list` declares a background
  section telling the agent the tool exists
- **host calls** - `godex_workspace_read` (permission `read_file`) and
  `godex_kv_get/set` (permission `memory`)

## Build

```bash
cd examples/wasm-plugin-todo
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

`main_host.go` is a host-platform stub so the package stays buildable under
`go build ./...` on the host toolchain.

## Smoke test

```bash
go test ./examples/wasm-plugin-todo/
```

The test loads `plugin.wasm` through `internal/wasmrt` with stub host
callbacks and verifies tools list, prompt sections, scanning, and the
new/resolved diff end to end. It skips when `plugin.wasm` is absent.

## Install into GoDex

```json
// in a GoDex chat with the `packages` bundle enabled:
install_package {"source": "<repo>/examples/wasm-plugin-todo"}
```

Then ask the agent, e.g. *「用 todo_scan 扫一下 internal/agent/agent.go」*.
The tool registers automatically on install; the background prompt section
is injected into the session context.
