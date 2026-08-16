# WASM test plugin

`plugin.wasm` is a Go 1.26 wasip1 module compiled from `plugin-src/main.go`
with `//go:wasmexport` (mailbox JSON ABI `godex:plugin@0.1`).

Rebuild:

```bash
cd internal/wasmrt/testdata/plugin-src
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../plugin.wasm main.go
```

The plugin exports `godex_abi_version`, `godex_request_buffer`,
`godex_tools_list`, and `godex_invoke`; it provides one tool `wasm_echo`.
