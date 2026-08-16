#!/usr/bin/env bash
# Rebuild the TinyGo WASM test plugin used by internal/wasmrt tests.
#
# Requires: tinygo (>= 0.41), wasm-opt (binaryen >= 121), and a Go toolchain
# 1.19-1.23 for TinyGo's compatibility check.
#
#   brew install tinygo binaryen
#   ./examples/wasm-plugin-tinygo/rebuild-testdata.sh
#
# If your default Go is newer than TinyGo supports, set GOROOT_OVERRIDE to a
# cached 1.19-1.23 toolchain, e.g.:
#   GOROOT_OVERRIDE="$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.23.11.darwin-arm64" \
#     ./examples/wasm-plugin-tinygo/rebuild-testdata.sh
set -euo pipefail
cd "$(dirname "$0")"
TINYGO_BIN="${TINYGO_BIN:-tinygo}"
if [ -n "${GOROOT_OVERRIDE:-}" ]; then
  GOROOT="$GOROOT_OVERRIDE" PATH="$GOROOT_OVERRIDE/bin:$PATH" \
    "$TINYGO_BIN" build -o plugin.wasm -target=wasip1 -buildmode=c-shared .
else
  "$TINYGO_BIN" build -o plugin.wasm -target=wasip1 -buildmode=c-shared .
fi
cp plugin.wasm ../../internal/wasmrt/testdata/tinygo_plugin.wasm
rm -f plugin.wasm
echo "built: ../../internal/wasmrt/testdata/tinygo_plugin.wasm"
