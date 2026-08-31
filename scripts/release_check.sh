#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "[release-check] documentation contracts"
./scripts/check_docs.sh

echo "[release-check] go test ./..."
go test ./...

echo "[release-check] go build"
GODEX_HOME_DIR="${GODEX_HOME:-$HOME/.godex}"
RELEASE_BIN="$GODEX_HOME_DIR/tmp/release/godex"
mkdir -p "$(dirname "$RELEASE_BIN")"
go build -ldflags "-s -w" -o "$RELEASE_BIN" ./cmd/godex

if command -v pnpm >/dev/null 2>&1; then
  echo "[release-check] ui/web pnpm build"
  (cd ui/web && pnpm build)
else
  echo "[release-check] skip ui/web build (pnpm not found)"
fi

if [[ "${GODEX_BROWSER_SMOKE:-0}" == "1" ]]; then
  echo "[release-check] browser smoke"
  go test ./internal/tools -run TestBrowserServiceSmoke -count=1
fi

if [[ "${GODEX_LONGTASK_SMOKE:-0}" == "1" ]]; then
  echo "[release-check] longtask smoke"
  ./scripts/longtask_smoke.sh
fi

if [[ "${GODEX_EVAL_SMOKE:-0}" == "1" ]]; then
  echo "[release-check] eval smoke"
  "$RELEASE_BIN" eval run --suite examples/evals/smoke.yaml --out "$GODEX_HOME_DIR/evals/runs"
fi

echo "[release-check] done"
