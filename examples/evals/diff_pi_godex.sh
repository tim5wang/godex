#!/usr/bin/env bash
# diff_pi_godex.sh — score both result.json files and diff them.
#
# Usage:
#   ./diff_pi_godex.sh <pi-baseline> <godex-baseline>
#
# Example:
#   ./run_bench.sh    godex-v1.0
#   ./run_pi_bench.sh pi-v1.0
#   ./diff_pi_godex.sh pi-v1.0 godex-v1.0
#
# Exit code:
#   0  both agents agree on every instance (same pass set)
#   1  at least one instance was solved by one but not the other
#   2  any prerequisite file is missing

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PI_BASELINE="${1:?usage: $0 <pi-baseline> <godex-baseline>}"
GODEX_BASELINE="${2:?usage: $0 <pi-baseline> <godex-baseline>}"

PI_RESULT="${HERE}/results/${PI_BASELINE}/result.json"
PI_SCORE="${HERE}/results/${PI_BASELINE}/score.json"
GODEX_RESULT="${HERE}/results/${GODEX_BASELINE}/result.json"
GODEX_SCORE="${HERE}/results/${GODEX_BASELINE}/score.json"
FROZEN="${HERE}/swebench-frozen.jsonl}"

for f in "${PI_RESULT}" "${GODEX_RESULT}" "${FROZEN}"; do
  if [[ ! -f "${f}" ]]; then
    echo "error: missing ${f}" >&2
    case "${f}" in
      "${PI_RESULT}")   echo "       run: ./run_pi_bench.sh ${PI_BASELINE}" >&2 ;;
      "${GODEX_RESULT}")echo "       run: ./run_bench.sh    ${GODEX_BASELINE}" >&2 ;;
      *)                echo "       run: ./build_swebench_frozen.py --out ${FROZEN}" >&2 ;;
    esac
    exit 2
  fi
done

echo "==> scoring pi (${PI_BASELINE})"
python3 "${HERE}/score.py" \
    --result "${PI_RESULT}" \
    --frozen "${FROZEN}" \
    --out   "${PI_SCORE}"

echo "==> scoring godex (${GODEX_BASELINE})"
python3 "${HERE}/score.py" \
    --result "${GODEX_RESULT}" \
    --frozen "${FROZEN}" \
    --out   "${GODEX_SCORE}"

echo "==> diff"
python3 "${HERE}/diff_pi_godex.py" \
    --pi    "${PI_SCORE}" \
    --godex "${GODEX_SCORE}"
