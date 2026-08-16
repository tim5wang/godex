#!/usr/bin/env bash
# Rebuild the Rust WASM test plugin used by internal/wasmrt tests.
#
# This uses a sandbox-local rustup/cargo home so the build works even when
# ~/.rustup is read-only (CI/sandbox). It needs the wasm32-wasip1 target:
#
#   rustup target add wasm32-wasip1
#
# Then run this script; it emits internal/wasmrt/testdata/rust_plugin.wasm.
set -euo pipefail

cd "$(dirname "$0")/../wasm-plugin-rust"

RUSTUP_HOME="${RUSTUP_HOME:-$PWD/../../.rust-home}"
CARGO_HOME="${CARGO_HOME:-$PWD/../../.cargo-home}"

# If the wasm target is not present in the sandbox home, add it.
if ! RUSTUP_HOME="$RUSTUP_HOME" rustup target list --installed 2>/dev/null | grep -q wasm32-wasip1; then
  RUSTUP_HOME="$RUSTUP_HOME" CARGO_HOME="$CARGO_HOME" rustup target add wasm32-wasip1 >/dev/null 2>&1 || true
fi

OUT="$PWD/target/wasm32-wasip1/release/godex_hello_plugin.wasm"
DEST="$PWD/../../wasmrt/testdata/rust_plugin.wasm"

if [ -f "$OUT" ] && [ -n "${RUST_CACHE:-}" ]; then
  cp "$OUT" "$DEST"
  echo "cached: $DEST"
  exit 0
fi

RUSTUP_HOME="$RUSTUP_HOME" CARGO_HOME="$CARGO_HOME" \
  cargo build --release --target wasm32-wasip1

cp "$OUT" "$DEST"
echo "built: $DEST"
