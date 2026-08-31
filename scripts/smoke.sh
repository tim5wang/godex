#!/usr/bin/env bash
# Unified smoke-test entry point. Existing scenario scripts remain independently
# runnable; this wrapper standardizes discovery, dispatch, and optional JSON status.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
JSON=false

if [ "${1:-}" = "--json" ]; then
  JSON=true
  shift
fi

list_scenarios() {
  find "$ROOT_DIR/scripts" -maxdepth 1 -type f -name 'smoke_*.sh' -print \
    | sed 's#.*/smoke_##; s#\.sh$##' \
    | sort
}

scenario="${1:-list}"
if [ "$scenario" = "list" ] || [ "$scenario" = "--list" ]; then
  list_scenarios
  exit 0
fi
shift || true

run_scenario() {
  local name="$1"
  shift
  local script="$ROOT_DIR/scripts/smoke_${name}.sh"
  if [ ! -f "$script" ]; then
    echo "unknown smoke scenario: $name" >&2
    return 2
  fi
  bash "$script" "$@"
}

invoke_scenario() {
  if $JSON; then
    run_scenario "$@" >&2
  else
    run_scenario "$@"
  fi
}

status=0
if [ "$scenario" = "all" ]; then
  while IFS= read -r name; do
    invoke_scenario "$name" "$@" || status=$?
    [ "$status" -eq 0 ] || break
  done < <(list_scenarios)
else
  invoke_scenario "$scenario" "$@" || status=$?
fi

if $JSON; then
  result="passed"
  [ "$status" -eq 0 ] || result="failed"
  printf '{"smoke":"%s","status":"%s","exit_code":%d}\n' "$scenario" "$result" "$status"
fi
exit "$status"
