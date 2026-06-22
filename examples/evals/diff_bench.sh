#!/usr/bin/env bash
# diff_bench.sh — compare two scored runs and flag regressions.
#
# Usage:
#   ./examples/evals/diff_bench.sh <baseline-a> <baseline-b>
#
# Example:
#   ./examples/evals/run_bench.sh v1.0
#   # ... edit godex, rebuild ...
#   ./examples/evals/run_bench.sh post-iter-1
#   ./examples/evals/diff_bench.sh v1.0 post-iter-1
#
# Exit code:
#   0  no regressions (resolve_rate ≥ baseline, no missing IDs)
#   1  regressions detected (diff_score.py found regressed IDs)
#   2  script misuse (missing argument, missing score.json)
#
# Designed to be safe to run in a git pre-push hook or a CI step.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

A="${1:?usage: $0 <baseline-a> <baseline-b>}"
B="${2:?usage: $0 <baseline-a> <baseline-b>}"

SCORE_A="${HERE}/results/${A}/score.json"
SCORE_B="${HERE}/results/${B}/score.json"

if [[ ! -f "${SCORE_A}" ]]; then
  echo "error: ${SCORE_A} not found; run ./run_bench.sh ${A} first" >&2
  exit 2
fi
if [[ ! -f "${SCORE_B}" ]]; then
  echo "error: ${SCORE_B} not found; run ./run_bench.sh ${B} first" >&2
  exit 2
fi

python3 "${HERE}/diff_score.py" --a "${SCORE_A}" --b "${SCORE_B}"
