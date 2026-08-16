# GoDex WASM plugin — Rust example

A zero-dependency Rust implementation of the GoDex mailbox JSON ABI
`godex:plugin@0.1` (see `internal/wasmrt/wasmrt.go`). It demonstrates all four
P4 plugin faces:

- **tools** — `godex_tools_list` / `godex_invoke` (`rust_echo`, `rust_ping`)
- **prompt contribution** — `godex_prompts_list`
- **tool policy** — `godex_policy` (denies `rust_secret` with an explicit
  decision, allows everything else)
- **ABI/identity** — `godex_abi_version`, `godex_request_buffer`

## Build

```bash
rustup target add wasm32-wasip1
cd examples/wasm-plugin-rust
cargo build --release --target wasm32-wasip1
cp target/wasm32-wasip1/release/godex_hello_plugin.wasm ../hello-plugin.wasm
```

The example uses **no external crates** (hand-rolled minimal JSON parsing) so
it compiles offline. To refresh the Go test fixture:

```bash
./examples/wasm-plugin-rust/rebuild-testdata.sh
```

`internal/wasmrt` integration-tests the compiled module end to end
(`TestRustCompiledPluginEndToEnd`), verifying tool calls, prompt sections, and
policy decisions all round-trip through the wazero host.

## Load it in GoDex

Either load the module directly with `internal/wasmrt`, or ship it as a
package:

```yaml
# godex.package.yaml
name: rust-hello
version: 0.1.0
runtime:
  kind: wasm
  module: plugin.wasm
  abi: godex:plugin@0.1
provides:
  - godex:rust-hello@1
```

## Security boundary

Only the `godex:host` calls are available: `godex_log`, `godex_kv_get`,
`godex_kv_set`, `godex_workspace_read`. Full WASI filesystem/network/shell/env
are intentionally **not** exposed, matching the P4 MVP scope.
