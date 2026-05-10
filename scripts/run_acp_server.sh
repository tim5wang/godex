#!/usr/bin/env bash
set -euo pipefail

# run_acp_server.sh — Build (if needed) and launch godex as an ACP stdio agent.
#
# Usage:
#   ./scripts/run_acp_server.sh               # build + run
#   ./scripts/run_acp_server.sh --skip-build   # run existing binary
#   GODEX_BIN=/path/to/godex ./scripts/run_acp_server.sh --skip-build

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

SKIP_BUILD=0
GODEX_BIN="${GODEX_BIN:-$ROOT_DIR/godex}"

for arg in "$@"; do
  case "$arg" in
    --skip-build) SKIP_BUILD=1 ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

if [[ "$SKIP_BUILD" -eq 0 ]]; then
  echo "[acp] building godex ..." >&2
  go build -o "$GODEX_BIN" ./cmd/godex
fi

if [[ ! -x "$GODEX_BIN" ]]; then
  echo "[acp] binary not found at $GODEX_BIN" >&2
  exit 1
fi

exec "$GODEX_BIN" acp-server
