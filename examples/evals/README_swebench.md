# SWE-bench regression sweep

This directory holds a **regression-only** SWE-bench-style benchmark for
godex. The goal is narrow and deliberately so:

> After changing godex, run a frozen subset of SWE-bench through godex
> and confirm the resolve rate did not drop relative to the last
> baseline. Optionally compare against `pi` on the same model and
> same task set.

It is **not** meant to compare godex against external models on a
public leaderboard. If you want that, run the official
[SWE-bench harness](https://github.com/SWE-bench/SWE-bench) outside
this repo.

## How it works (Option A: pre-clone enabled)

SWE-bench tasks ask the agent to fix a real GitHub issue in a real
repo at a specific commit. For an agent to actually work on one, it
needs:

1. The repo cloned at `base_commit` somewhere on disk.
2. Permission to edit files in that directory.
3. Knowledge of the absolute path (or a `cd` instruction) to find it.

**Option A** wires this up by adding a `pre-clone` step before either
godex or pi is invoked. Concretely:

- `build_swebench_frozen.py` emits, per story, a `working_directory`
  (workspace-relative path) and a `write_scope` (a single-element list
  granting write access to that path).
- `preclone_repos.py` reads the rendered spec and runs
  `git clone --depth 1 <repo_url>` + `git checkout <base_commit>` for
  every story, into the workspace's `repos/<instance_id>/`.
- `run_bench.sh` (godex) and `run_pi_bench.sh` (pi) both call
  `preclone_repos.py` before invoking their respective agents.

For **godex**, the `write_scope` field is what unlocks the subagent's
tools. godex's `internal/agent/subagent_jobs.go:narrowSubagentWriteTools`
strips `bash` / `write_file` / `edit_file` from any subagent with an
empty `write_scope`. Without pre-clone + a non-empty `write_scope`, the
subagent has only `read_file` and reports `Verdict: blocked` on every
story.

For **pi**, the prompt opens with an explicit `cd <abs path>` block,
and falls back to a self-clone if the directory is missing.

**Why this matters:** without Option A, both agents return 100%
`blocked` because there's no code on disk. With Option A, the agents
actually attempt the fix and self-report. The trade-off is that the
comparison now measures "agent skill at reading code and producing a
patch" rather than "agent skill at running tests in a clean sandbox" —
see [Caveats](#caveats).

## Files

| File | Purpose |
| --- | --- |
| `build_swebench_frozen.py` | Read `SWE-bench/SWE-bench` from HF and write `swebench-frozen.jsonl` (a hand-curated subset) |
| `swebench-frozen.jsonl` | The frozen subset in the JSON shape `godex longtask create --file` accepts |
| `build_pi_tasks.py` | Re-emit the frozen subset as a `pi`-friendly `.jsonl` (same prompts, same verdict contract) |
| `pi-tasks.jsonl` | Output of `build_pi_tasks.py`; consumed by `run_pi_bench.sh` |
| `preclone_repos.py` | Read a LongTaskArgs-shaped spec and `git clone` every story's repo @ base_commit into `<workspace>/repos/<id>` |
| `score.py` | Read the LongTaskView produced by `godex longtask run` (or the mirror produced by `run_pi_bench.sh`) and compute resolve_rate |
| `diff_score.py` | Compare two score.json files from the **same** agent, list regressions, exit non-zero on regression |
| `diff_pi_godex.py` | Compare pi vs godex score.json files, list where they diverge, exit non-zero on divergence |
| `run_bench.sh` | Wrapper: build → setup workspace → pre-clone → create → run → score, save everything under `results/<baseline>/` |
| `run_pi_bench.sh` | Wrapper: pre-clone → invoke `pi` once per task → capture verdicts → write a LongTaskView-shaped result.json |
| `diff_bench.sh` | Wrapper: diff two scored godex runs |
| `diff_pi_godex.sh` | Wrapper: score both sides and diff |
| `pyproject.toml` | uv-compatible dependency file (stdlib-only by default; `datasets` is an opt-in extra for `build_swebench_frozen.py`) |

## One-time setup

```bash
# 1. Install datasets (only needed to (re)build the frozen list).
pip install datasets
# Or with uv:
#   uv sync --extra frozen

# 2. Build the frozen subset. Pick a workspace root; both godex and
#    pi will pre-clone repos under <workspace>/repos/<instance_id>.
#    The path is embedded in every story's description so the agent
#    knows where to cd / find files.
mkdir -p .swebench-ws
./build_swebench_frozen.py \
    --out          swebench-frozen.jsonl \
    --workspace-dir "$(pwd)/.swebench-ws"

# 3. Build the pi-side task list with the SAME --workspace-dir.
#    run_pi_bench.sh sanity-checks that the absolute paths it sees
#    in pi-tasks.jsonl match PI_WORKSPACE, so the two scripts must
#    agree on the root.
PI_WORKSPACE="$(pwd)/.swebench-ws"
./build_pi_tasks.py \
    --in            swebench-frozen.jsonl \
    --out           pi-tasks.jsonl \
    --workspace-dir "${PI_WORKSPACE}"
```

The frozen list is at the top of `build_swebench_frozen.py`. Edit it
to retune; never remove an ID without bumping the file (or the run
history becomes un-comparable).

## Standard workflow

### A. Catch regressions in godex itself

```bash
# 1. Establish a baseline. --workspace-dir must match the one used
#    in build_swebench_frozen.py so pre-cloned repos land where the
#    story descriptions point.
./run_bench.sh v1.0

# 2. Iterate on godex
$EDITOR ../../internal/...

# 3. Re-run with the new baseline name
./run_bench.sh post-iter-1

# 4. Compare
./diff_bench.sh v1.0 post-iter-1
# sample output:
#   18/30 -> 19/30  (rate 60.0% -> 63.3%)  regressed=0 fixed=1
```

`diff_bench.sh` exits non-zero on regression — wire it into a CI gate
if you want hard enforcement.

#### Optional env overrides for `run_bench.sh`

| Var | Default | Effect |
| --- | --- | --- |
| `MAX_ITERATIONS` | 200 | Cap on the longtask run loop. Lower it for faster turnaround. |
| `WAIT_TIMEOUT_MS` | 60000 | Per-story subagent wait timeout. Lower it to fail fast on stuck stories. |
| `SKIP_PRECLONE` | 0 | Set to 1 to skip the `git clone` step (useful when re-running against an already-populated workspace). |
| `PI_OFFLINE` | 0 | Set to 1 to make `preclone_repos.py` fail fast on missing repos instead of hitting the network. |

### B. Compare godex against `pi` on the same model

The contract is the same on both sides: both agents are told to end
with `Verdict: pass|fail|blocked|needs_fix`, and `score.py` parses
the same field on both sides. So a difference in `resolve_rate`
reflects an agent-level difference, not a measurement difference.

```bash
# 1. Run godex (as in A)
./run_bench.sh godex-v1.0

# 2. Run pi with the same model on the same provider. PI_WORKSPACE
#    defaults to a per-baseline dir under results/<baseline>/, but
#    pass it explicitly to share clones with godex (saves bandwidth).
PI_BIN=pi \
PI_PROVIDER=minimax \
PI_MODEL=MiniMax-M3 \
PI_WORKSPACE="$(pwd)/.swebench-ws" \
    ./run_pi_bench.sh pi-v1.0

# 3. Diff
./diff_pi_godex.sh pi-v1.0 godex-v1.0
# sample output:
#   pi    12/30  (40.0%)
#   godex 18/30  (60.0%)
#   godex-lost (pi solved, godex didn't)  : 0
#   pi-lost    (godex solved, pi didn't)  : 6
#   both-solved                              : 12
#     PI-LOST    (6): ['django__django-10914', 'psf__requests-1724', ...]
```

`diff_pi_godex.sh` exits non-zero when the two agents diverge on at
least one instance — convenient for "no godex regression" gate plus a
soft warning when godex falls behind pi on specific tasks.

#### Required env for pi

| Var | Default | Notes |
| --- | --- | --- |
| `PI_BIN` | auto-detect (`pi` on PATH, then `~/.bun/bin/pi`, `~/.local/bin/pi`, `/usr/local/bin/pi`) | The path to the pi binary |
| `PI_PROVIDER` | `minimax` | Whatever you registered in your pi config |
| `PI_MODEL` | `MiniMax-M3` | Must match what godex is using for the head-to-head to be fair |
| `PI_TIMEOUT` | `1200` | Per-task wall-clock seconds (GNU `timeout`; macOS users need `brew install coreutils` for `gtimeout`) |
| `PI_WORKSPACE` | `results/<baseline>/pi-workspace` | Absolute path where pi-side clones land. Must match the `--workspace-dir` used for `build_pi_tasks.py`; `run_pi_bench.sh` refuses to run if it doesn't. |
| `MINIMAX_API_KEY` | (required) | Read by pi's provider config; godex reads it independently |

**Both agents must use the same model on the same provider.** The
scripts do not enforce this; verify by hand before drawing
conclusions.

#### Verdict-grading caveats

`score.py` trusts the agent's self-reported verdict, parsed by
grepping for `^Verdict: pass|fail|blocked|needs_fix` in stdout. This
is fast and uniform, but it is **not** the same as running the
FAIL_TO_PASS test suite:

- godex's longtask prompt enforces the contract; pi gets the same
  prompt prefix, but pi is a free-form agent and may emit the line
  in unexpected ways (lowercase, with bullets, inside a code block).
  `run_pi_bench.sh` greps case-insensitively and tolerates leading
  whitespace, which catches the common cases.
- A "Verdict: pass" from either agent means the agent **believes**
  it solved the task. It does not mean the FAIL_TO_PASS test suite
  actually passed. For objective grading, layer the official
  [SWE-bench Docker harness](https://github.com/SWE-bench/SWE-bench)
  on top — the per-task raw stdout is preserved in
  `results/<baseline>/raw/<id>.txt` so you can re-grade without
  re-running.

## Cost controls

- `--max-iterations` (default 200) caps the longtask loop. Lower it for
  faster turnaround when sweeping many candidates.
- `--wait-timeout-ms` (default 60000) caps each subagent's wait. Lower
  it to fail fast on stuck stories.
- Frozen subset size is the main cost knob — see `FROZEN_IDS` in the
  build script.

## What this is NOT

- **Not a leaderboard runner.** For that, use SWE-bench's own harness
  and submit predictions in the official format.
- **Not a Docker-graded scorer.** `score.py` trusts the agent's own
  verdict. If you need objective grading, layer a Docker harness on
  top: each story's `acceptance_criteria` is already the
  `FAIL_TO_PASS` test list, so wiring `swe-bench-eval` into
  `quality_checks` is a small extension.
- **Not a model benchmark.** The same workflow works for any model
  profile in your godex config; the runner uses whatever the default
  profile resolves to.
- **Not a fair head-to-head without caveats.** `pi` is a free-form
  CLI agent; godex is a structured longtask. The verdict-grading
  trick makes them mechanically comparable, but `pi` will see
  different tool affordances (interactive shell, file edit) than
  godex's subagent. Use the symmetric difference
  (`godex-lost` / `pi-lost`) as the signal, not the absolute
  resolve_rate.

## Caveats

These are the design decisions that make the comparison **possible**
but **not perfect**. Read this section before drawing conclusions.

### What this measures vs what it doesn't

| Claim | Confidence | Notes |
| --- | --- | --- |
| "Did godex stop solving tasks it used to solve?" | High | `diff_bench.sh` compares the same frozen set across two godex builds; the agent shape, prompt, and tools are identical between runs. |
| "Is godex better or worse than pi on this set?" | Medium | Same model, same task input, same verdict contract. But the agent shapes differ — see below. |
| "Does this set predict godex's score on full SWE-bench?" | Low | 30 tasks is a small sample; SWE-bench issues are heterogeneous. Use the absolute number with humility. |
| "Did godex's pass mean the test suite actually passed?" | No | `Verdict: pass` is the agent's self-report. No test is run. For objective grading, layer SWE-bench's Docker harness. |

### Asymmetries between godex and pi (Option A partially mitigates)

Even with pre-clone, the two agents do **not** see identical
environments:

- **Tool stack**: godex's `general-purpose` subagent gets
  `bash + read_file + write_file + edit_file` (when `write_scope` is
  set), but no `web_search` / `web_fetch` / skills by default. pi has
  whatever you registered in your pi config (which may include web
  search, skills, MCP servers, etc.). If you want a tighter
  comparison, configure pi without web/skills for the run.
- **Session shape**: godex's longtask has a structured, resumable
  subagent with handoffs and checkpoints; pi is a single-turn
  `--print` invocation. pi gets one shot. godex gets multiple if its
  first attempt fails validation.
- **Output format**: godex returns a structured LongTaskView JSON;
  pi returns free-form text we grep. Both are graded on the same
  `Verdict:` line, but pi may bury its verdict in a code block or
  surround it with commentary. The verdict-grader handles whitespace
  + case, but not every conceivable quirk.

**Practical advice**: read `diff_pi_godex.sh`'s output and look at
the **symmetric difference** (`godex-lost` and `pi-lost`), not the
absolute resolve_rate. A 60%/40% split is informative only insofar as
you then dig into *which tasks* each agent failed and *why* (raw
output lives under `results/<baseline>/raw/<id>.txt` for pi, and
under the per-story handoff JSONs for godex).

### Data quality caveats

- **Public SWE-bench changes**: the test split on Hugging Face gets
  new tasks over time. Re-running `build_swebench_frozen.py` against
  a stale `FROZEN_IDS` will hard-error on any ID that has been
  removed. This is intentional — it forces you to confirm the frozen
  list is still valid. If you need to retune, edit the list and bump
  the tag.
- **Per-instance FAIL_TO_PASS corruption**: some SWE-bench records
  have malformed `FAIL_TO_PASS` fields (e.g. a prose fragment instead
  of a test path). The build script only filters on truly-empty
  `FAIL_TO_PASS`; corrupted-but-non-empty values go through and the
  agent sees them. `score.py` doesn't validate test paths, just the
  verdict line, so this won't break scoring — but the agent may
  legitimately produce a `needs_fix` verdict on these.
- **Force-pushed base commits**: rare but possible.
  `preclone_repos.py` detects a stale checkout at the wrong SHA and
  re-clones; if the ref was deleted on the remote, the script
  hard-errors.

### Cost & wall-clock

A full 30-task sweep with `MAX_ITERATIONS=200` and a default 60s
`WAIT_TIMEOUT_MS` can take **hours per agent**. The bottleneck is
LLM latency × 30 tasks × N iterations per story. Plan accordingly:

- For a quick smoke test, set `MAX_ITERATIONS=5` and pick one or two
  tasks from the frozen list (you can hand-edit `swebench-frozen.jsonl`
  to a single line).
- For a tight loop, set `WAIT_TIMEOUT_MS=15000` and accept that
  some tasks may report `blocked` from the subagent hitting the
  timeout (rather than from a real failure).
- The pre-clone step itself is ~5–10 minutes for 30 small repos
  (`requests`, `pytest`, etc.) and longer for `tensorflow`-scale
  repos. Idempotent: re-runs skip repos already at the right SHA.
