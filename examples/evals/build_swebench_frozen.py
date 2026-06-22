#!/usr/bin/env python3
"""
build_swebench_frozen.py — produce examples/evals/swebench-frozen.jsonl

Reads SWE-bench (princeton-nlp/SWE-bench) from Hugging Face, picks a
hand-curated frozen subset, and writes one JSON object per line in the
shape consumed by `godex longtask create --file`.

Why a frozen subset (and not the full 2,294-task test split)?
  1. Determinism — the public SWE-bench test split changes as new tasks
     are added. Running "regression" against a moving target is useless.
  2. Cost — a 30-task sweep fits in an afternoon; the full test split
     takes days of LLM time.
  3. Diff stability — when we say "v1.1 regressed on
     psf__requests-9999", that statement must mean the same thing a
     month from now.

Two output modes:
  - Default (no --workspace-dir): the JSON's working_directory is a
    workspace-relative path ("repos/<id>"), portable across machines.
    The description shows a "<workspace>/repos/<id>" placeholder.
  - With --workspace-dir: an absolute path is embedded in the
    description so the agent knows exactly where to find the repo
    clone.

Option A fields:
  - working_directory: relative path under godex workspace where the
    pre-clone of this instance's repo lives.
  - write_scope: list of workspace-relative paths the subagent is
    allowed to write. Non-empty is REQUIRED: godex's
    narrowSubagentWriteTools strips bash/write_file/edit_file from
    any subagent with empty WriteScope. See
    internal/agent/subagent_jobs.go:3270.

Usage:
    pip install datasets
    python examples/evals/build_swebench_frozen.py \\
        --workspace-dir /abs/path/to/godex/workspace \\
        --out examples/evals/swebench-frozen.jsonl

The frozen list lives at the top of main(). Edit it to retune.
"""
import argparse
import json
import os
import sys

# Hand-curated frozen subset. To regenerate: pick ~30 instances spanning
# at least 5 repos and 3 difficulty bands. Keep IDs stable across
# releases; never remove an ID without bumping the file under a new
# tag (see --tag below).
#
# Validation: each ID was confirmed to exist in
# SWE-bench/SWE-bench:test as of 2026-06-21. If you regenerate this
# list, run build_swebench_frozen.py once to confirm; missing IDs
# produce a hard error and an empty jsonl.
FROZEN_IDS = [
    # django (5 of 850) — varied numbers, easy to medium
    "django__django-10087",
    "django__django-11039",
    "django__django-11808",
    "django__django-13343",
    "django__django-13710",
    # requests (5 of 44) — small repo, fast runs
    "psf__requests-1142",
    "psf__requests-1327",
    "psf__requests-1724",
    "psf__requests-1921",
    "psf__requests-2148",
    # pytest (5 of 119) — test infra, useful probe
    "pytest-dev__pytest-9780",
    "pytest-dev__pytest-9911",
    "pytest-dev__pytest-9956",
    "pytest-dev__pytest-10051",
    "pytest-dev__pytest-10115",
    # scikit-learn (5 of 229) — ML, mid-size
    "scikit-learn__scikit-learn-9274",
    "scikit-learn__scikit-learn-9288",
    "scikit-learn__scikit-learn-9304",
    "scikit-learn__scikit-learn-10198",
    "scikit-learn__scikit-learn-10297",
    # sphinx (5 of 187) — docs/build
    "sphinx-doc__sphinx-8595",
    "sphinx-doc__sphinx-10021",
    "sphinx-doc__sphinx-10325",
    "sphinx-doc__sphinx-10466",
    "sphinx-doc__sphinx-10673",
    # matplotlib (5 of 184) — visualization, heavier
    "matplotlib__matplotlib-13859",
    "matplotlib__matplotlib-13980",
    "matplotlib__matplotlib-25332",
    "matplotlib__matplotlib-26466",
    "matplotlib__matplotlib-26532",
]


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--out", required=True, help="output .jsonl path")
    ap.add_argument("--tag", default="frozen-v1",
                    help="tag written into each story as a marker")
    ap.add_argument("--dataset",
                    default="SWE-bench/SWE-bench",
                    help="HF dataset name; default is SWE-bench/SWE-bench")
    ap.add_argument("--split", default="test",
                    help="dataset split; 'dev' is fine for offline previews")
    ap.add_argument("--workspace-dir", default="",
                    help="absolute path to the godex workspace root. "
                         "Used to compute the absolute Working directory "
                         "injected into each story's description. If empty, "
                         "the working_directory in the JSON stays relative "
                         "(\"repos/<id>\") and the description shows a "
                         "placeholder.")
    ap.add_argument("--repos-subdir", default="repos",
                    help="subdirectory of the workspace where per-instance "
                         "repo clones live. Default: 'repos'.")
    args = ap.parse_args()

    workspace = os.path.abspath(args.workspace_dir) if args.workspace_dir else ""
    repos_subdir = args.repos_subdir.strip("/") or "repos"

    try:
        from datasets import load_dataset
    except ImportError:
        sys.stderr.write(
            "error: 'datasets' is not installed.\n"
            "       run: pip install datasets\n"
        )
        sys.exit(2)

    sys.stderr.write(f"loading {args.dataset}/{args.split}...\n")
    ds = load_dataset(args.dataset, split=args.split)
    by_id = {x["instance_id"]: x for x in ds}

    missing = [i for i in FROZEN_IDS if i not in by_id]
    if missing:
        sys.stderr.write(
            f"error: {len(missing)} frozen IDs not found in "
            f"{args.dataset}/{args.split}:\n  "
            + "\n  ".join(missing) + "\n"
        )
        sys.exit(1)

    out_path = os.path.abspath(args.out)
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    written = 0
    with open(out_path, "w") as f:
        for iid in FROZEN_IDS:
            x = by_id[iid]
            try:
                f2p = json.loads(x.get("FAIL_TO_PASS") or "[]")
            except json.JSONDecodeError:
                sys.stderr.write(f"warn: {iid}: bad FAIL_TO_PASS, skipping\n")
                continue
            # Filter only on truly empty FAIL_TO_PASS. SWE-bench has
            # some data-quality issues (e.g. FAIL_TO_PASS is a prose
            # fragment, not a test path) — those still go into the
            # frozen set; the agent will see the odd test name in the
            # prompt and can self-report a real verdict. score.py
            # doesn't validate the test path, it just keys on the
            # verdict line.
            if not f2p:
                sys.stderr.write(f"warn: {iid}: empty FAIL_TO_PASS, skipping\n")
                continue

            env = x.get("environment_setup_commit", "")
            version = x.get("version", "")
            repo_relpath = f"{repos_subdir}/{x['instance_id']}"
            # Absolute path for the prompt; relative path for the JSON's
            # working_directory / write_scope so the artifact is portable
            # across machines (a baseline generated on a dev box can be
            # diffed against one generated on a CI runner).
            if workspace:
                working_dir_abs = os.path.join(workspace, repo_relpath)
            else:
                working_dir_abs = f"<workspace>/{repo_relpath}"

            desc_lines = [
                x["problem_statement"].strip(),
                "",
                f"Working directory: {working_dir_abs}",
                f"Repo: {x['repo']}",
                f"Base commit: {x['base_commit']}",
            ]
            if version:
                desc_lines.append(f"Version: {version}")
            if env:
                desc_lines.append(f"Environment setup commit: {env}")
            desc_lines.append("")
            desc_lines.append("Tests that must pass after your fix:")
            for t in f2p:
                desc_lines.append(f"  - {t}")

            story = {
                "id": x["instance_id"],
                "title": f"{x['instance_id']} ({x['repo']})",
                "description": "\n".join(desc_lines),
                "acceptance_criteria": list(f2p),
                "priority": 1,
                "agent_type": "general-purpose",
                # Option A fields: written so the run scripts know
                #   (a) where to pre-clone the repo, and
                #   (b) what WriteScope to set on the story so the
                #       subagent's bash/write_file/edit_file tools
                #       are unlocked (godex strips them when
                #       WriteScope is empty; see
                #       subagent_jobs.go:narrowSubagentWriteTools).
                "working_directory": repo_relpath,
                "write_scope": [repo_relpath],
            }
            f.write(json.dumps(story) + "\n")
            written += 1

    sys.stderr.write(
        f"wrote {written} stories to {out_path} (tag={args.tag})\n"
    )


if __name__ == "__main__":
    main()
