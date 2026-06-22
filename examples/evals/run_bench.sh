#!/usr/bin/env bash
# run_bench.sh — run godex against the frozen SWE-bench sweep and score it.
#
# Usage:
#   ./examples/evals/run_bench.sh <baseline-name> [extra godex longtask run flags...]
#
# Examples:
#   ./examples/evals/run_bench.sh v1.0
#   ./examples/evals/run_bench.sh post-fix-orm --max-iterations=20
#
# What this script does (Option A — pre-clone enabled):
#   1. (re)build godex so the binary reflects the current working tree
#   2. create a per-baseline workspace via `godex setup`
#   3. render a LongTaskArgs spec from examples/evals/swebench-frozen.jsonl
#   4. PRE-CLONE each story's repo @ base_commit into <workspace>/repos/<id>
#      (so the subagent has a real codebase to operate on; otherwise it
#      reports "workspace is empty" and every story returns Verdict: blocked)
#   5. godex longtask create
#   6. godex longtask run (synchronous)
#   7. score the final LongTaskView into a compact score.json
#
# Outputs land under examples/evals/results/<baseline-name>/:
#   - workspace/         per-baseline godex workspace (godex.yaml + .godex state)
#   - workspace/repos/   pre-cloned repos keyed by instance_id
#   - pre-clone.log      per-instance clone/fetch log
#   - spec.json          the LongTaskArgs we sent to `godex longtask create`
#   - create.json        the view returned by `godex longtask create`
#   - result.json        the view returned by `godex longtask run`
#   - score.json         score.py output (resolve_rate + per_instance)
#
# Exit code: 0 if score.py reports no missing stories. Non-zero if the
# longtask or scoring failed. (Regressions vs. a previous baseline are
# NOT caught here — use diff_bench.sh for that.)
#
# Required env:
#   MAX_ITERATIONS    cap on the longtask run loop (default 200)
#   WAIT_TIMEOUT_MS   per-story subagent wait timeout (default 60000)
#   SKIP_PRECLONE     if set to 1, skip the git clone step (useful for
#                     re-runs against an already-cloned workspace; you
#                     must have populated <workspace>/repos/ manually)
#   PI_OFFLINE        if 1, refuse to hit the network; pre-clone fails
#                     fast if a repo is missing.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/../.." && pwd)"

BASELINE="${1:?usage: $0 <baseline-name> [extra godex longtask run flags...]}"
shift || true

FROZEN="${HERE}/swebench-frozen.jsonl"
OUT_DIR="${HERE}/results/${BASELINE}"
SPEC="${OUT_DIR}/spec.json"
CREATE_VIEW="${OUT_DIR}/create.json"
RESULT="${OUT_DIR}/result.json"
SCORE="${OUT_DIR}/score.json"
GODEX_BIN="${OUT_DIR}/godex"
# Use a per-baseline workspace so concurrent runs don't trample each
# other's longtask state, sessions, or memos.
WORKSPACE="${OUT_DIR}/workspace"
CONFIG_PATH="${WORKSPACE}/godex.yaml"

if [[ ! -f "${FROZEN}" ]]; then
  echo "error: ${FROZEN} not found" >&2
  echo "       run: python ${HERE}/build_swebench_frozen.py --out ${FROZEN}" >&2
  exit 2
fi

mkdir -p "${OUT_DIR}"

echo "==> [${BASELINE}] build godex"
(cd "${ROOT}" && go build -o "${GODEX_BIN}" ./cmd/godex/)

echo "==> [${BASELINE}] prepare workspace (godex.yaml + dirs)"
mkdir -p "${WORKSPACE}"
if [[ ! -f "${CONFIG_PATH}" ]]; then
  "${GODEX_BIN}" setup --dir "${WORKSPACE}" >/dev/null
fi

echo "==> [${BASELINE}] render LongTaskArgs spec"
python3 - "$@" <<'PY' "${FROZEN}" "${SPEC}" "${BASELINE}" "${ROOT}"
import json, os, sys
frozen, spec_path, baseline, root = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
with open(frozen) as f:
    stories = [json.loads(line) for line in f if line.strip()]
spec = {
    "longtask_id":  f"swebench-{baseline}",
    "workflow_id":  f"swebench-{baseline}",
    "project":      "swebench-frozen",
    "branch_name":  f"swebench/eval-{baseline}",
    "description":  f"Regression sweep over the frozen SWE-bench subset ({len(stories)} instances).",
    "quality_checks": [],
    "validation_timeout_ms": 60000,
    "merge_policy": "review_only",
    "commit_policy": "none",
    "stories":      stories,
}
with open(spec_path, "w") as f:
    json.dump(spec, f, indent=2)
print(f"  wrote {len(stories)} stories to {spec_path}")
PY

echo "==> [${BASELINE}] pre-clone repos @ base_commit"
if [[ "${SKIP_PRECLONE:-0}" == "1" ]]; then
  echo "  SKIP_PRECLONE=1, assuming ${WORKSPACE}/repos/ is already populated"
else
  python3 "${HERE}/preclone_repos.py" \
      --spec   "${SPEC}" \
      --out-dir "${WORKSPACE}" \
      --log     "${OUT_DIR}/pre-clone.log" \
      --worktree-root "${WORKSPACE}/repos"
fi

echo "==> [${BASELINE}] godex longtask create"
"${GODEX_BIN}" --config "${CONFIG_PATH}" longtask create \
    --file "${SPEC}" \
    > "${CREATE_VIEW}"

WORKFLOW_ID=$(python3 -c "import json; print(json.load(open('${CREATE_VIEW}'))['workflow_id'])")
echo "  workflow_id: ${WORKFLOW_ID}"

echo "==> [${BASELINE}] godex longtask run (synchronous)"
# Default is sync (blocking); --max-iterations caps the loop so a wedged
# run can't burn the budget. --no-stop-on-failure keeps going past a
# blocked story so we get a result on every instance, not just the first.
"${GODEX_BIN}" --config "${CONFIG_PATH}" longtask run "${WORKFLOW_ID}" \
    --no-stop-on-failure \
    --max-iterations "${MAX_ITERATIONS:-200}" \
    --wait-timeout-ms "${WAIT_TIMEOUT_MS:-60000}" \
    "$@" \
    > "${RESULT}"

echo "==> [${BASELINE}] score"
python3 "${HERE}/score.py" \
    --result "${RESULT}" \
    --frozen "${FROZEN}" \
    --out   "${SCORE}"
