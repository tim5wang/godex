#!/usr/bin/env python3
"""
build_pi_tasks.py — turn swebench-frozen.jsonl into pi-friendly prompts.

Reuses the same story records that examples/evals/build_swebench_frozen.py
emits, and rewraps each one with godex's "Verdict: pass|fail|blocked|needs_fix"
contract. The contract is what makes a head-to-head comparison fair: both
godex longtask and pi are graded by grepping the same final marker line
out of the agent's output, so neither gets a structural advantage.

Option A pre-clone:
  Both godex and pi now run from a workspace-relative repo clone.
  Each pi prompt opens with an absolute `cd` instruction so the agent
  knows exactly which directory to operate in. run_pi_bench.sh does the
  actual clone before invoking pi.

Output: one JSON object per line:
    {
      "instance_id":      "django__django-12345",
      "prompt":           "<full prompt including the cd + verdict contract>",
      "repo":             "django/django",
      "base_commit":      "abc123",
      "tests":            ["tests/test_orm.py::test_filter_date", ...],
      "working_directory": "/abs/path/to/repos/django__django-12345"
    }

Usage:
    python build_pi_tasks.py \\
        --in  examples/evals/swebench-frozen.jsonl \\
        --workspace-dir /abs/path/to/pi-workspace \\
        --out examples/evals/pi-tasks.jsonl
"""
import argparse
import json
import os
import sys


# The "completion contract" must match godex's longtask prompt so both
# agents self-report the same way. The exact wording is copied verbatim
# from internal/agent/longtask_create.go:renderLongTaskStoryPrompt; if
# you change one, change the other.
PI_COMPLETION_CONTRACT = """\
Completion contract:
- Apply a minimal patch that resolves the issue at the stated base commit.
- Make every test listed under "Tests that must pass" pass; do not regress
  any test under "Tests that keep passing".
- Finish with an explicit line: Verdict: pass|fail|blocked|needs_fix
- Include a compact summary, changed files, validation run, and reusable learnings.
""".rstrip()


# Option A bootstrap. pi has bash tools, so we tell it to cd first
# thing. The first line is a single bash command the agent can run as
# its opening tool call. If the path doesn't exist, pi will see the
# error and can fall back to cloning itself.
PI_BOOTSTRAP_FMT = """\
Start every turn in the pre-cloned repo directory by running this bash
command first:

    cd {working_dir}

If the directory is empty or missing, clone the repo yourself:
    git clone https://github.com/{repo}.git {working_dir}
    cd {working_dir}
    git checkout {base_commit}

Do NOT skip this step. Edits and tool calls before the cd will operate
on the wrong working tree.
""".rstrip()


def render_prompt(story, working_dir_abs, repo, base_commit):
    # The prompt opens with the bootstrap block (where to cd), then the
    # godex-style envelope (problem statement, repo, commit, tests),
    # then the verdict contract. Mirrors renderLongTaskStoryPrompt
    # enough that verdict-grading works on both sides.
    desc = story.get("description") or ""
    title = story.get("title") or story.get("id") or ""
    bootstrap = PI_BOOTSTRAP_FMT.format(
        working_dir=working_dir_abs,
        repo=repo,
        base_commit=base_commit,
    )
    lines = [
        f"You are solving one SWE-bench instance end-to-end.",
        f"Work on this instance only; do not start other instances.",
        "",
        bootstrap,
        "",
        f"Story ID: {story.get('id', '')}",
        f"Title:    {title}",
        "",
        desc.strip(),
        "",
        PI_COMPLETION_CONTRACT,
    ]
    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--in", dest="inp", required=True,
                    help="frozen .jsonl from build_swebench_frozen.py")
    ap.add_argument("--out", required=True,
                    help="output pi-tasks .jsonl path")
    ap.add_argument("--limit", type=int, default=0,
                    help="emit only the first N tasks (0 = all)")
    ap.add_argument("--workspace-dir", default="",
                    help="absolute path to the pi workspace root. The "
                         "task's working_directory is computed as "
                         "<workspace-dir>/<working_directory-relative-from-frozen>. "
                         "Must match the --workspace-dir passed to "
                         "build_swebench_frozen.py so the two scripts "
                         "agree on where the clone lives.")
    args = ap.parse_args()

    if not os.path.isfile(args.inp):
        sys.stderr.write(f"error: input not found: {args.inp}\n")
        sys.exit(2)
    if not args.workspace_dir:
        sys.stderr.write(
            "warn: --workspace-dir not set; pi prompts will show "
            "<workspace>/... placeholders. run_pi_bench.sh will refuse "
            "to clone repos without an absolute path.\n"
        )

    workspace = os.path.abspath(args.workspace_dir) if args.workspace_dir else ""

    written = 0
    missing_wd = 0
    with open(args.inp) as src, open(args.out, "w") as dst:
        for line in src:
            line = line.strip()
            if not line:
                continue
            story = json.loads(line)
            # The frozen record carries the relative working_directory
            # (e.g. "repos/django__django-12345"). We compute the
            # absolute path here so the prompt is actionable.
            relpath = story.get("working_directory") or ""
            if not relpath:
                missing_wd += 1
            if workspace and relpath:
                working_dir_abs = os.path.join(workspace, relpath)
            else:
                working_dir_abs = f"<workspace>/{relpath}" if relpath else ""

            # Extract repo / base_commit / tests from the description so
            # the diff script can correlate by instance_id without
            # re-parsing prose. The description is the single source of
            # truth emitted by build_swebench_frozen.py.
            desc = story.get("description", "")
            repo = ""
            base_commit = ""
            tests = list(story.get("acceptance_criteria") or [])
            for ln in desc.splitlines():
                s = ln.strip()
                if s.startswith("Repo:"):
                    repo = s[len("Repo:"):].strip()
                elif s.startswith("Base commit:"):
                    base_commit = s[len("Base commit:"):].strip()
            task = {
                "instance_id": story.get("id", ""),
                # Pass repo + base_commit to the renderer so the
                # bootstrap block can include a self-clone fallback
                # with the right URL and ref.
                "prompt": render_prompt(story, working_dir_abs, repo, base_commit),
                "repo": repo,
                "base_commit": base_commit,
                "tests": tests,
                "working_directory": working_dir_abs,
            }
            dst.write(json.dumps(task) + "\n")
            written += 1
            if args.limit and written >= args.limit:
                break
    if missing_wd:
        sys.stderr.write(
            f"warn: {missing_wd}/{written} stories had no working_directory "
            f"field; their pi prompts will show <workspace>/... placeholders. "
            f"Re-run build_swebench_frozen.py to regenerate.\n"
        )
    sys.stderr.write(f"wrote {written} pi tasks to {args.out}\n")


if __name__ == "__main__":
    main()
