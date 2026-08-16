# GoDex WASM plugin — TinyGo example

A TinyGo implementation of the GoDex mailbox JSON ABI `godex:plugin@0.1`,
ABI-equivalent to the Rust example (`../wasm-plugin-rust`). TinyGo produces
much smaller modules than the Go standard toolchain (~128 KiB vs ~3 MB),
which suits constrained plugin payloads.

It demonstrates the four P4 plugin faces:

- **tools** — `godex_tools_list` / `godex_invoke` (`tiny_echo`, `tiny_ping`)
- **prompt contribution** — `godex_prompts_list`
- **tool policy** — `godex_policy` (denies `tiny_secret`)
- **ABI/identity** — `godex_abi_version`, `godex_request_buffer`

## Build

```bash
cd examples/wasm-plugin-tinygo
tinygo build -o plugin.wasm -target=wasip1 -buildmode=c-shared .
cp plugin.wasm ../../internal/wasmrt/testdata/tinygo_plugin.wasm
```

`-buildmode=c-shared` is required so the module exports `_initialize` (the
Go-toolchain-style reactor start function); the default command buildmode
exports `_start`, which `internal/wasmrt` also handles, but the c-shared form
matches the Rust/Go examples.

`internal/wasmrt` integration-tests the compiled module end to end
(`TestTinyGoCompiledPluginEndToEnd`).

## Requirements

- tinygo >= 0.41 (supports the Go 1.19-1.23 toolchains it checks against)
- wasm-opt from binaryen (only needed when not using `-opt=0`)
- a Go 1.19-1.23 toolchain for TinyGo's compatibility check (TinyGo 0.41
  rejects newer Go toolchains)

If your default Go is newer, set `GOROOT` to a cached 1.23 toolchain:

```bash
GOROOT="$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.23.11.darwin-arm64" \
  tinygo build -o plugin.wasm -target=wasip1 -buildmode=c-shared .
```
