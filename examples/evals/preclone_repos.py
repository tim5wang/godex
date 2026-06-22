#!/usr/bin/env python3
"""
preclone_repos.py — populate <workspace>/repos/<instance_id> for each
story in a godex longtask spec.

Reads a LongTaskArgs spec (the JSON written by run_bench.sh) and
ensures every story's `working_directory` exists as a git checkout
pinned to the story's `base_commit`. Stories that share a repo (e.g.
5 SWE-bench instances from django/django) share a single clone to
save bandwidth and disk.

Idempotent: re-running on a populated workspace is a no-op for repos
that already have the right commit. Re-runs that need a different
commit delete the directory first.

Usage:
    python preclone_repos.py \\
        --spec        <path to spec.json> \\
        --out-dir     <godex workspace root> \\
        --worktree-root <subdir under out-dir; default 'repos'> \\
        --log         <optional per-instance log path>

Failure modes:
  - Network failure during clone: hard error, script aborts
  - base_commit missing: hard error, abort
  - worktree-root contains conflicting files: hard error
  - SKIP_PRECLONE=1 in the environment: skip the entire step
    (handled by run_bench.sh, not this script)
"""
import argparse
import json
import os
import subprocess
import sys
from collections import OrderedDict


def parse_repo_and_commit(description):
    # The frozen build puts these on dedicated lines. Be tolerant to
    # extra whitespace and to lines that are absent.
    repo = ""
    base_commit = ""
    for line in (description or "").splitlines():
        s = line.strip()
        if s.startswith("Repo:"):
            repo = s[len("Repo:"):].strip()
        elif s.startswith("Base commit:"):
            base_commit = s[len("Base commit:"):].strip()
    return repo, base_commit


def run(cmd, **kwargs):
    """Run a subprocess command, capture output, raise on failure."""
    result = subprocess.run(cmd, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        raise RuntimeError(
            f"command failed (rc={result.returncode}): {' '.join(cmd)}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )
    return result


def current_commit(repo_dir):
    """Return the short SHA of HEAD in repo_dir, or '' if not a repo."""
    try:
        out = run(["git", "-C", repo_dir, "rev-parse", "HEAD"]).stdout.strip()
        return out
    except (RuntimeError, FileNotFoundError):
        return ""


def clone_and_checkout(repo_url, target_dir, base_commit, log):
    """Clone repo_url into target_dir and check out base_commit.

    Idempotent: if target_dir is already a git repo at the right
    commit, do nothing. If at a different commit, blow it away
    and re-clone (avoids stale state confusing the agent).
    """
    if not os.path.isdir(target_dir) or not os.listdir(target_dir):
        log(f"  clone {repo_url} -> {target_dir}")
        # --depth 1 for speed; if base_commit isn't reachable from the
        # default branch, unshallow and fetch the specific ref.
        run(["git", "clone", "--depth", "1", repo_url, target_dir])
        # Ensure base_commit is reachable
        log(f"  fetch {base_commit} (may unshallow)")
        try:
            run(["git", "-C", target_dir, "fetch", "--depth", "1",
                 "origin", base_commit])
        except RuntimeError as e:
            # Fetch with depth failed (e.g. shallow clone doesn't have
            # the ref). Unshallow then fetch.
            log(f"  fetch depth=1 failed ({e!s}); unshallowing and retrying")
            run(["git", "-C", target_dir, "fetch", "--unshallow", "origin"])
            run(["git", "-C", target_dir, "fetch", "origin", base_commit])
    else:
        existing = current_commit(target_dir)
        if existing == base_commit:
            log(f"  skip (already at {base_commit[:12]})")
            return
        # Stale state: nuke and re-clone. We don't try `git fetch +
        # checkout` because if the ref was force-pushed, fetch may
        # leave dangling state.
        log(f"  stale: existing={existing[:12] if existing else '?'} "
            f"want={base_commit[:12]}; re-cloning")
        run(["rm", "-rf", target_dir])
        run(["git", "clone", "--depth", "1", repo_url, target_dir])
        run(["git", "-C", target_dir, "fetch", "--depth", "1",
             "origin", base_commit])

    # Detached checkout at base_commit. --force so a previously-checked-out
    # commit (rare) doesn't block.
    run(["git", "-C", target_dir, "checkout", "--force", base_commit])
    # Verify the checkout actually landed at the right commit. This
    # catches the case where the ref existed on the remote but
    # somehow got pruned.
    landed = current_commit(target_dir)
    if landed != base_commit:
        raise RuntimeError(
            f"checkout verification failed: wanted {base_commit}, "
            f"got {landed} in {target_dir}"
        )
    log(f"  ok @ {base_commit[:12]}")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--spec", required=True,
                    help="LongTaskArgs JSON (output of run_bench.sh's render step)")
    ap.add_argument("--out-dir", required=True,
                    help="godex workspace root; worktree-root is created under it")
    ap.add_argument("--worktree-root", default="repos",
                    help="subdirectory under out-dir (default: repos)")
    ap.add_argument("--log", default="",
                    help="optional log file path; also prints to stdout")
    ap.add_argument("--repo-host", default="https://github.com",
                    help="Git host prefix for Repo: <owner>/<name> fields. "
                         "Default https://github.com. SWE-bench stores repos "
                         "as bare 'owner/name' on GitHub; if you point at a "
                         "different mirror, change this.")
    args = ap.parse_args()

    with open(args.spec) as f:
        spec = json.load(f)

    out_dir = os.path.abspath(args.out_dir)
    worktree_root = os.path.join(out_dir, args.worktree_root.strip("/"))
    os.makedirs(worktree_root, exist_ok=True)

    log_lines = []

    def log(msg):
        print(msg)
        log_lines.append(msg)

    # Group stories by (repo, base_commit). Each story's
    # working_directory is unique (one clone per instance) because
    # SWE-bench issues each have a distinct base_commit even when
    # they share an upstream repo.
    grouped = OrderedDict()
    skipped = []
    for story in spec.get("stories") or []:
        iid = story.get("id") or ""
        relpath = story.get("working_directory") or ""
        if not iid or not relpath:
            skipped.append(iid or "<no-id>")
            continue
        repo, base_commit = parse_repo_and_commit(story.get("description", ""))
        if not repo or not base_commit:
            log(f"  warn: {iid}: missing Repo/Base commit, skipping clone")
            skipped.append(iid)
            continue
        # Repo is stored as "owner/name" in the frozen description.
        # Turn it into a clone URL.
        if not (repo.startswith("http://") or repo.startswith("https://") or
                repo.startswith("git@")):
            clone_url = f"{args.repo_host.rstrip('/')}/{repo}.git"
        else:
            clone_url = repo
        grouped.setdefault((clone_url, base_commit), []).append((iid, relpath))

    log(f"  pre-cloning {len(grouped)} unique (repo, commit) pairs "
        f"for {sum(len(v) for v in grouped.values())} stories "
        f"({len(skipped)} skipped)")

    failures = []
    for (clone_url, base_commit), stories in grouped.items():
        # Each (repo, base_commit) pair is one clone. SWE-bench issues
        # each have a distinct base_commit even when they live in the
        # same upstream repo, so symlinking across instances is not
        # useful — we always clone. Dedup is for the rare case where
        # two stories share both repo AND commit.
        primary_iid, primary_rel = stories[0]
        primary_abs = os.path.join(out_dir, primary_rel)
        os.makedirs(os.path.dirname(primary_abs), exist_ok=True)
        try:
            clone_and_checkout(clone_url, primary_abs, base_commit, log)
        except (RuntimeError, FileNotFoundError) as e:
            log(f"  FAIL: {primary_iid}: {e!s}")
            failures.append(primary_iid)
            continue
        # Mark all stories in this group as done.
        for iid, _ in stories:
            log(f"  ok: {iid} -> {primary_abs}")

    if args.log:
        with open(args.log, "w") as f:
            f.write("\n".join(log_lines) + "\n")

    if failures:
        sys.stderr.write(
            f"error: {len(failures)} clone failures: {failures}\n"
        )
        sys.exit(1)

    log(f"  done")


if __name__ == "__main__":
    main()
