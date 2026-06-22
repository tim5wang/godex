#!/usr/bin/env bash
# run_pi_bench.sh — run the same SWE-bench frozen subset through `pi`
# and produce a result.json whose story records match godex's
# LongTaskView shape, so score.py + diff_score.py work uniformly on
# either side.
#
# Usage:
#   PI_BIN=pi PI_WORKSPACE=/path/to/pi-workspace \\
#       ./run_pi_bench.sh <baseline-name> [extra pi flags...]
#
# Examples:
#   PI_BIN=pi ./run_pi_bench.sh pi-v1.0
#   PI_BIN=pi ./run_pi_bench.sh pi-v1.1 --thinking low
#
# Environment:
#   PI_BIN         path to the pi binary (default: pi on PATH; .bun/bin/pi
#                  is auto-detected if it exists)
#   PI_PROVIDER    provider name (default: minimax)
#   PI_MODEL       model pattern (default: MiniMax-M3)
#   PI_TIMEOUT     per-task wall-clock timeout in seconds (default: 1200)
#   PI_WORKSPACE   absolute path where pi-side repo clones live. Must
#                  match the --workspace-dir passed to build_pi_tasks.py
#                  so the absolute paths in the prompts resolve.
#                  Default: examples/evals/results/<baseline>/pi-workspace
#   SKIP_PRECLONE  if 1, skip the git clone step (you must have populated
#                  the workspace manually; useful for re-runs).
#
# Outputs under examples/evals/results/<baseline>/:
#   - pi-workspace/repos/    pre-cloned repos (keyed by instance_id)
#   - pre-clone.log          per-instance clone/fetch log
#   - pi.log                 full stdout of all pi invocations, in order
#   - raw/<id>.txt           per-task raw stdout (so you can inspect a single run)
#   - result.json            LongTaskView-shaped JSON, ready for score.py
#
# Verdict grading: we look for a line matching
#   ^[[:space:]]*Verdict:[[:space:]]*(pass|fail|blocked|needs_fix)
# in pi's stdout. Missing verdict counts as "blocked" (conservative).
# This matches godex longtask's contract exactly.
#
# Note on `timeout`: this script uses GNU coreutils' `timeout`. On
# macOS, install coreutils (`brew install coreutils`) and either
# PATH-prioritize `gtimeout` or symlink `timeout` to `gtimeout`.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

BASELINE="${1:?usage: PI_BIN=pi PI_WORKSPACE=/abs/path $0 <baseline-name> [extra pi flags...]}"
shift || true

TASKS="${HERE}/pi-tasks.jsonl"
OUT_DIR="${HERE}/results/${BASELINE}"
RAW_DIR="${OUT_DIR}/raw"
RESULT="${OUT_DIR}/result.json"
LOG="${OUT_DIR}/pi.log"
# Default pi workspace to a per-baseline dir. Pass PI_WORKSPACE to
# share clones across baselines (faster re-runs) or to point at a
# pre-populated directory.
PI_WORKSPACE="${PI_WORKSPACE:-${OUT_DIR}/pi-workspace}"

PI_BIN="${PI_BIN:-}"
if [[ -z "${PI_BIN}" ]]; then
  for candidate in pi "$HOME/.bun/bin/pi" "$HOME/.local/bin/pi" "/usr/local/bin/pi"; do
    if command -v "${candidate}" >/dev/null 2>&1; then
      PI_BIN="${candidate}"
      break
    fi
  done
fi
if [[ -z "${PI_BIN}" ]] || ! command -v "${PI_BIN}" >/dev/null 2>&1; then
  echo "error: pi binary not found; set PI_BIN or install pi" >&2
  exit 2
fi

PI_PROVIDER="${PI_PROVIDER:-minimax}"
PI_MODEL="${PI_MODEL:-MiniMax-M3}"
PI_TIMEOUT="${PI_TIMEOUT:-1200}"

if [[ ! -f "${TASKS}" ]]; then
  echo "error: ${TASKS} not found" >&2
  echo "       run: python ${HERE}/build_pi_tasks.py --in ${HERE}/swebench-frozen.jsonl --out ${TASKS}" >&2
  exit 2
fi

mkdir -p "${RAW_DIR}"
: > "${LOG}"

echo "==> [${BASELINE}] pi binary: ${PI_BIN}"
echo "==> [${BASELINE}] provider/model: ${PI_PROVIDER} / ${PI_MODEL}"
echo "==> [${BASELINE}] tasks: ${TASKS}"
echo "==> [${BASELINE}] pi workspace: ${PI_WORKSPACE}"

# Sanity: the absolute working_directory in the FIRST task's prompt
# must live under PI_WORKSPACE. If not, the user passed the wrong
# PI_WORKSPACE and the agent will cd to a directory we never cloned.
FIRST_WD=$(head -1 "${TASKS}" | python3 -c "import json,sys; print(json.loads(sys.stdin.read()).get('working_directory',''))")
if [[ -n "${FIRST_WD}" && "${FIRST_WD}" != "${PI_WORKSPACE}"/* ]]; then
  echo "error: first task's working_directory is ${FIRST_WD}" >&2
  echo "       but PI_WORKSPACE is ${PI_WORKSPACE}" >&2
  echo "       these must share a prefix. Re-run build_pi_tasks.py with" >&2
  echo "       --workspace-dir=${PI_WORKSPACE} and regenerate pi-tasks.jsonl." >&2
  exit 2
fi

echo "==> [${BASELINE}] pre-clone repos @ base_commit"
if [[ "${SKIP_PRECLONE:-0}" == "1" ]]; then
  echo "  SKIP_PRECLONE=1, assuming ${PI_WORKSPACE}/repos/ is already populated"
else
  # Build a synthetic LongTaskArgs-shaped spec from pi-tasks.jsonl so
  # preclone_repos.py can be reused. The synthetic spec is also saved
  # to OUT_DIR/spec-from-pi.jsonl for inspection.
  python3 - "${TASKS}" "${OUT_DIR}/spec-from-pi.json" <<'PY'
import json, sys
src, dst = sys.argv[1], sys.argv[2]
stories = []
with open(src) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        t = json.loads(line)
        # Convert pi task → LongTaskArgs story shape. The description
        # is reconstructed because preclone_repos.py parses Repo/ and
        # Base commit: from it.
        repo = t.get("repo", "")
        # Strip owner from tests if tests contain path-like strings
        # (they don't matter here; preclone_repos only looks at description).
        desc_lines = [f"Repo: {repo}"]
        if t.get("base_commit"):
            desc_lines.append(f"Base commit: {t['base_commit']}")
        # The repo path relative to PI_WORKSPACE. We synthesize it
        # from the working_directory basename.
        wd = t.get("working_directory", "")
        rel = wd.split("/")[-1] if wd else f"repos/{t['instance_id']}"
        stories.append({
            "id": t["instance_id"],
            "description": "\n".join(desc_lines),
            "working_directory": f"repos/{t['instance_id']}",
        })
with open(dst, "w") as f:
    json.dump({"stories": stories}, f, indent=2)
print(f"  wrote {len(stories)} stories to {dst}")
PY
  python3 "${HERE}/preclone_repos.py" \
      --spec          "${OUT_DIR}/spec-from-pi.json" \
      --out-dir       "${PI_WORKSPACE}" \
      --worktree-root "repos" \
      --log           "${OUT_DIR}/pre-clone.log"
fi

# Append a one-line system-prompt nudge that re-states the verdict
# contract — defense in depth in case the prompt template changes.
SYSTEM_PROMPT="You are a precise SWE-bench solver. End your final answer with a single line of the form: Verdict: pass|fail|blocked|needs_fix"

declare -a STORIES=()
COUNT=$(wc -l < "${TASKS}" | tr -d ' ')
INDEX=0
PASS_COUNT=0
FAIL_COUNT=0
BLOCKED_COUNT=0
NEEDS_FIX_COUNT=0
MISSING_COUNT=0

while IFS= read -r line; do
  [[ -z "${line}" ]] && continue
  INDEX=$((INDEX + 1))
  iid=$(printf '%s' "${line}" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['instance_id'])")
  prompt=$(printf '%s' "${line}" | python3 -c "import json,sys; print(json.loads(sys.stdin.read())['prompt'])")
  raw_path="${RAW_DIR}/${iid}.txt"
  echo "  [${INDEX}/${COUNT}] ${iid}"

  # Run pi with the prompt on stdin via -c. --no-session keeps the
  # session dir clean; --print exits after one turn.
  set +e
  timeout "${PI_TIMEOUT}" "${PI_BIN}" \
      --provider "${PI_PROVIDER}" \
      --model    "${PI_MODEL}" \
      --print \
      --no-session \
      --append-system-prompt "${SYSTEM_PROMPT}" \
      --append-system-prompt "${prompt}" \
      > "${raw_path}" 2>&1
  rc=$?
  set -e

  # Tee the per-task output into the combined log.
  {
    echo "===== ${iid} (rc=${rc}) ====="
    cat "${raw_path}"
    echo
  } >> "${LOG}"

  # Extract verdict.
  verdict=$(grep -E '^[[:space:]]*Verdict:[[:space:]]*(pass|fail|blocked|needs_fix)' \
              "${raw_path}" | tail -1 | sed -E 's/^[[:space:]]*Verdict:[[:space:]]*//' | tr '[:upper:]' '[:lower:]')
  if [[ -z "${verdict}" ]]; then
    verdict="blocked"
    MISSING_COUNT=$((MISSING_COUNT + 1))
  fi

  case "${verdict}" in
    pass)     PASS_COUNT=$((PASS_COUNT + 1));     passes=true ;;
    fail)     FAIL_COUNT=$((FAIL_COUNT + 1));     passes=false ;;
    blocked)  BLOCKED_COUNT=$((BLOCKED_COUNT + 1)); passes=false ;;
    needs_fix) NEEDS_FIX_COUNT=$((NEEDS_FIX_COUNT + 1)); passes=false ;;
    *)        BLOCKED_COUNT=$((BLOCKED_COUNT + 1)); passes=false ;;
  esac

  # Emit a story record in LongTaskView shape. Keep field names
  # identical to godex's so score.py can index both uniformly.
  python3 - "${iid}" "${verdict}" "${passes}" "${rc}" "${raw_path}" \
    >> "${RESULT}.tmp" <<'PY'
import json, sys
iid, verdict, passes, rc, raw = sys.argv[1], sys.argv[2], sys.argv[3] == "true", int(sys.argv[4]), sys.argv[5]
# Read tail of raw as the "result_preview"; cap at 4 KiB so the JSON
# doesn't grow unbounded.
try:
    with open(raw) as f:
        f.seek(0, 2); size = f.tell()
        f.seek(max(0, size - 4096))
        tail = f.read()
except OSError:
    tail = ""
story = {
    "id": iid,
    "status": "completed" if verdict in ("pass", "fail", "needs_fix") else "blocked",
    "verdict": verdict,
    "passes": passes,
    "result_preview": tail,
    "error": "" if verdict == "pass" else f"verdict={verdict} rc={rc}",
}
print(json.dumps(story))
PY

  echo "       verdict=${verdict}"
done < "${TASKS}"

# Wrap the per-line story records in a LongTaskView-shaped envelope.
python3 - "${BASELINE}" "${RESULT}.tmp" "${COUNT}" "${RESULT}" <<'PY'
import json, sys
baseline, tmp, total_s, out_path = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
stories = []
with open(tmp) as f:
    for line in f:
        line = line.strip()
        if line:
            stories.append(json.loads(line))
total = int(total_s)
view = {
    "longtask_id": f"pi-{baseline}",
    "workflow_id": f"pi-{baseline}",
    "total": total,
    "pending": 0,
    "running": 0,
    "completed": sum(1 for s in stories if s["status"] == "completed"),
    "failed": sum(1 for s in stories if s["status"] != "completed"),
    "stories": stories,
}
with open(out_path, "w") as f:
    json.dump(view, f, indent=2)
PY
rm -f "${RESULT}.tmp"

echo "==> [${BASELINE}] pass=${PASS_COUNT} fail=${FAIL_COUNT} blocked=${BLOCKED_COUNT} needs_fix=${NEEDS_FIX_COUNT} missing_verdict=${MISSING_COUNT}"
echo "==> [${BASELINE}] wrote ${RESULT}"
