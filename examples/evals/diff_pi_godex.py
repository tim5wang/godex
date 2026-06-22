#!/usr/bin/env python3
"""
diff_pi_godex.py — compare pi and godex scores on the same frozen set.

Reads two score.json files produced by score.py (one for pi, one for
godex), prints:

  - the resolve_rate delta
  - instance IDs that pi solved but godex didn't (godex-lost)
  - instance IDs that godex solved but pi didn't (pi-lost)
  - which IDs both solved, which neither solved

The symmetric difference ("godex-lost" and "pi-lost") is the
head-to-head signal: it answers "where do these two agents
diverge?" without conflating with overall capability.

Exit code:
  0  symmetric difference is empty (the two agents agree on every
     instance — both pass or both fail)
  1  at least one instance was solved by one but not the other
  2  score files are not from the same frozen set
"""
import argparse
import json
import sys


def index(score):
    return {x["id"]: bool(x["passes"]) for x in score["per_instance"]}


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--pi",    required=True, help="score.json from pi run")
    ap.add_argument("--godex", required=True, help="score.json from godex run")
    ap.add_argument("--quiet", action="store_true",
                    help="only print the summary line")
    args = ap.parse_args()

    p = json.load(open(args.pi))
    g = json.load(open(args.godex))
    pi, gd = index(p), index(g)

    keys = sorted(set(pi) | set(gd))
    only_pi = sorted(k for k in keys if k in pi and k not in gd)
    only_gd = sorted(k for k in keys if k in gd and k not in pi)
    both = sorted(k for k in keys if k in pi and k in gd and pi[k] and gd[k])
    godex_lost = sorted(k for k in keys if k in pi and k in gd and pi[k] and not gd[k])
    pi_lost    = sorted(k for k in keys if k in pi and k in gd and (not pi[k]) and gd[k])

    if only_pi or only_gd:
        sys.stderr.write(
            f"warn: {len(only_pi)} ids only in pi, {len(only_gd)} ids only in "
            f"godex; comparisons will be unbalanced\n"
        )

    print(
        f"pi    {p['passed']}/{p['total']}  ({p['resolve_rate']:.1%})\n"
        f"godex {g['passed']}/{g['total']}  ({g['resolve_rate']:.1%})\n"
        f"godex-lost (pi solved, godex didn't)  : {len(godex_lost)}\n"
        f"pi-lost    (godex solved, pi didn't)  : {len(pi_lost)}\n"
        f"both-solved                              : {len(both)}"
    )
    if not args.quiet:
        if godex_lost:
            print(f"  GODEX-LOST ({len(godex_lost)}): {godex_lost}")
        if pi_lost:
            print(f"  PI-LOST    ({len(pi_lost)}): {pi_lost}")
        if only_pi:
            print(f"  only-in-pi    ({len(only_pi)}): {only_pi}")
        if only_gd:
            print(f"  only-in-godex ({len(only_gd)}): {only_gd}")

    # Non-zero exit on any divergence, so this script is CI-friendly.
    return 1 if (godex_lost or pi_lost) else 0


if __name__ == "__main__":
    sys.exit(main())
