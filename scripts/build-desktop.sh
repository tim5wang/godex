#!/usr/bin/env bash
# Build the godex desktop shell with the embedded Go godex binary (self-hosted
# mode): go build → sidecar → Tauri shell binary → optional .app bundle.
#
# Usage:
#   scripts/build-desktop.sh              # debug shell binary only
#   scripts/build-desktop.sh --release    # release shell binary only
#   scripts/build-desktop.sh --bundle     # shell binary + packaged .app
#   scripts/build-desktop.sh --release --bundle
#
# The sidecar and the bundle target share one triple (rustc host by default;
# override with TARGET_TRIPLE, e.g. x86_64-apple-darwin for an Intel build).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TAURI_DIR="$ROOT/desktop/src-tauri"

MODE="${1:-}"
BUNDLE=0
for arg in "$@"; do
  case "$arg" in
    --release) MODE="--release" ;;
    --bundle) BUNDLE=1 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

# Target triple shared by the sidecar name and `tauri build --target`.
TRIPLE="${TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}"
SIDECAR="$TAURI_DIR/binaries/godex-$TRIPLE"

# Derive GOOS/GOARCH from the triple for the Go sidecar build.
case "$TRIPLE" in
  aarch64-apple-darwin)      GOOS=darwin;  GOARCH=arm64 ;;
  x86_64-apple-darwin)       GOOS=darwin;  GOARCH=amd64 ;;
  aarch64-unknown-linux-*)   GOOS=linux;   GOARCH=arm64 ;;
  x86_64-unknown-linux-*)    GOOS=linux;   GOARCH=amd64 ;;
  x86_64-pc-windows-msvc)    GOOS=windows; GOARCH=amd64 ;;
  aarch64-pc-windows-msvc)   GOOS=windows; GOARCH=arm64 ;;
  *) echo "ERROR: unsupported triple $TRIPLE" >&2; exit 2 ;;
esac

echo "==> Building embedded godex binary ($TRIPLE)"
mkdir -p "$(dirname "$SIDECAR")"
CGO_ENABLED="${CGO_ENABLED:-0}" GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w" -o "$SIDECAR" ./cmd/godex
echo "    sidecar: $SIDECAR ($(du -h "$SIDECAR" | cut -f1))"

echo "==> Building Tauri shell"
cd "$TAURI_DIR"
if [ "$MODE" = "--release" ]; then
  cargo build --release
  SHELL_BIN="$TAURI_DIR/target/release/godex-desktop"
else
  cargo build
  SHELL_BIN="$TAURI_DIR/target/debug/godex-desktop"
fi
echo "    shell binary: $SHELL_BIN"

if [ "$BUNDLE" = "1" ]; then
  # Tauri CLI is required for bundling (.app/.dmg). Prefer a globally
  # installed `tauri`, otherwise fall back to `npx @tauri-apps/cli` (no
  # global install needed; downloads on first run).
  if command -v tauri >/dev/null 2>&1; then
    TAURI_CMD="tauri"
  elif command -v npx >/dev/null 2>&1; then
    TAURI_CMD="npx --yes @tauri-apps/cli"
  else
    echo "ERROR: Tauri CLI not found. Install it with one of:" >&2
    echo "  npm install -g @tauri-apps/cli" >&2
    echo "  # or (Rust, slower)" >&2
    echo "  cargo install tauri-cli --locked" >&2
    exit 1
  fi

  # macOS bundling needs a full icon set (.icns etc.); generate from the
  # placeholder PNG. Idempotent — re-running is safe after swapping art.
  if [ ! -f "$TAURI_DIR/icons/icon.icns" ]; then
    echo "==> Generating platform icons (tauri icon)"
    $TAURI_CMD icon "$TAURI_DIR/icons/icon.png"
  fi

  echo "==> Bundling .app (tauri build --target $TRIPLE)"
  # Explicit --target keeps externalBin resolution and the produced binary
  # on the SAME triple, regardless of the Tauri CLI's own arch (e.g. a
  # Rosetta x64 npx CLI would otherwise look for an x86_64 sidecar while
  # cargo compiled for the host aarch64).
  $TAURI_CMD build --target "$TRIPLE"
  echo "==> Bundle done — see $TAURI_DIR/target/$TRIPLE/release/bundle/"
else
  echo "==> Done (shell binary only)"
  echo "    To package the .app run: scripts/build-desktop.sh --bundle"
fi
