#!/usr/bin/env python3
"""
score.py — turn a godex longtask view JSON into a SWE-bench-style score.

Reads the output of `godex longtask run <id>` (a LongTaskView), counts
how many stories ended with verdict=pass, and writes a compact score
file plus a one-line stdout summary.

This scorer relies on godex's own verdict, NOT on a separate SWE-bench
Docker harness. Rationale:

  - The point of the frozen sweep is to detect regressions in godex
    iteration-over-iteration. We are comparing godex to itself, not
    against external leaderboards.
  - Adding a Docker harness here would require installing
    swe-bench/SWE-bench (Python + Docker), doubling the run cost.
  - If a future change makes the per-story verdict untrustworthy, we
    can layer a Docker-graded scorer on top without touching this
    file's contract.

Usage:
    python score.py --result result.json --frozen swebench-frozen.jsonl \\
                    --out score.json
"""
import argparse
import json
import sys
from collections import Counter


def load_jsonl_ids(path):
    ids = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            ids.append(json.loads(line)["id"])
    return ids


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--result", required=True,
                    help="godex longtask view JSON (LongTaskView)")
    ap.add_argument("--frozen", required=True,
                    help="frozen .jsonl (story id order)")
    ap.add_argument("--out", required=True,
                    help="output score JSON path")
    args = ap.parse_args()

    with open(args.result) as f:
        view = json.load(f)
    frozen_ids = load_jsonl_ids(args.frozen)

    stories_by_id = {}
    for s in view.get("stories", []) or []:
        sid = s.get("id") or s.get("node_id") or ""
        if sid:
            stories_by_id[sid] = s

    verdicts = Counter()
    per_instance = []
    passed = 0
    missing = 0
    for iid in frozen_ids:
        s = stories_by_id.get(iid)
        if s is None:
            missing += 1
            verdicts["missing"] += 1
            per_instance.append({
                "id": iid,
                "status": "missing",
                "verdict": "",
                "passes": False,
            })
            continue
        verdict = s.get("verdict") or s.get("status") or "unknown"
        passes = bool(s.get("passes", False))
        # Override verdict when the task didn't actually run — the model
        # hit a rate-limit wall (MiniMax Token Plan, OpenAI 429, etc.).
        # "exhausted" is counted separately in the score; it does NOT
        # count as a regression (diff_score.py compares pass/fail, and
        # exhausted is neither). Without this, an exhausted run shows
        # up as 0/30 and the next run as 18/30, which triggers a false
        # regression signal.
        if _looks_exhausted(s):
            verdict = "exhausted"
            passes = False
        verdicts[verdict] += 1
        if passes:
            passed += 1
        per_instance.append({
            "id": iid,
            "status": s.get("status", ""),
            "verdict": s.get("verdict", ""),
            "passes": passes,
        })

    total = len(frozen_ids)
    score = {
        "total": total,
        "passed": passed,
        "missing": missing,
        "resolve_rate": (passed / total) if total else 0.0,
        "verdicts": dict(verdicts),
        "per_instance": per_instance,
    }
    with open(args.out, "w") as f:
        json.dump(score, f, indent=2)
    print(f"resolve_rate: {passed}/{total} "
          f"({score['resolve_rate']:.1%}) verdicts={dict(verdicts)}")
    return 0 if missing == 0 else 1


# Rate-limit / quota-exhaustion patterns. If any of these appear in the
# error field or the result preview, the task is scored as "exhausted"
# rather than "fail" or "blocked". The specific patterns below are
# known MiniMax Token Plan messages. Extend this list if your provider
# uses different wording.
_RATE_LIMIT_SUBSTRINGS = [
    "rate_limit_error",
    "Token Plan 用量上限",
    "Token Plan usage limit",
    "已达到 Token Plan",
    "请升级 Token Plan",
    "429",
]


def _looks_exhausted(story):
    """True when the story error or result_preview signals a rate-limit."""
    text = (story.get("error") or "") + " " + (story.get("result_preview") or "")
    return any(needle in text for needle in _RATE_LIMIT_SUBSTRINGS)


if __name__ == "__main__":
    sys.exit(main())
