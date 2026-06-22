#!/usr/bin/env python3
"""
diff_score.py — compare two score.json files and report regressions.

Reads two score files produced by score.py, prints:
  - the resolve_rate delta
  - instance IDs that flipped from pass→fail (regressed)
  - instance IDs that flipped from fail→pass (fixed)
  - any IDs that were missing in either side

Exits non-zero if there are regressions. Designed to be called from CI
or a git pre-push hook.

Usage:
    python diff_score.py --a results/v1.0/score.json \\
                         --b results/post-iter/score.json
"""
import argparse
import json
import sys


def index(score):
    return {x["id"]: bool(x["passes"]) for x in score["per_instance"]}


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--a", required=True, help="baseline score.json")
    ap.add_argument("--b", required=True, help="candidate score.json")
    ap.add_argument("--quiet", action="store_true",
                    help="only print the summary line")
    args = ap.parse_args()

    a = json.load(open(args.a))
    b = json.load(open(args.b))
    ai, bi = index(a), index(b)
    keys = sorted(set(ai) | set(bi))

    regressed, fixed, only_in_a, only_in_b = [], [], [], []
    for k in keys:
        if k in ai and k not in bi:
            only_in_b.append(k)
        elif k in bi and k not in ai:
            only_in_a.append(k)
        elif ai[k] and not bi[k]:
            regressed.append(k)
        elif (not ai[k]) and bi[k]:
            fixed.append(k)

    print(
        f"{a['passed']}/{a['total']} -> {b['passed']}/{b['total']}  "
        f"(rate {a['resolve_rate']:.1%} -> {b['resolve_rate']:.1%})  "
        f"regressed={len(regressed)} fixed={len(fixed)}"
    )
    if not args.quiet:
        if regressed:
            print(f"  REGRESSED ({len(regressed)}): {regressed}")
        if fixed:
            print(f"  fixed ({len(fixed)}): {fixed}")
        if only_in_a:
            print(f"  only-in-baseline ({len(only_in_a)}): {only_in_a}")
        if only_in_b:
            print(f"  only-in-candidate ({len(only_in_b)}): {only_in_b}")

    return 1 if regressed or only_in_a or only_in_b else 0


if __name__ == "__main__":
    sys.exit(main())
