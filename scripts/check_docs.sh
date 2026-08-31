#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

errors=0

report_error() {
  printf 'docs-check: %s\n' "$*" >&2
  errors=$((errors + 1))
}

assert_implementation_fact() {
  local source_path=$1
  local doc=$2
  local fact=$3
  if [[ -e "$source_path" ]] && ! grep -Fq "$fact" "$doc"; then
    report_error "$doc is missing implementation fact '$fact' backed by $source_path"
  fi
}

# Every top-level document is part of the curated index. Historical detailed
# plans under docs/superpowers are intentionally represented by their folder,
# rather than by one index row per execution note.
while IFS= read -r doc; do
  name=${doc#docs/}
  if ! grep -Fq "./$name" docs/README.md; then
    report_error "docs/README.md does not index $doc"
  fi
done < <(find docs -mindepth 1 -maxdepth 1 -type f -name '*.md' ! -name README.md | sort)

# Status belongs near the title so a document cannot be mistaken for a current
# contract after its implementation state changes.
while IFS= read -r doc; do
  status_line=$(sed -n '1,8p' "$doc" | grep -Em1 '状态[：:]|Status[：:]' || true)
  if [[ -z "$status_line" ]]; then
    report_error "$doc has no status marker in its first 8 lines"
  elif ! grep -Eq '(Active|Implemented|Partial|Planned|Draft|Superseded|Historical|Analysis|分析报告)' <<<"$status_line"; then
    report_error "$doc uses an unrecognized status: $status_line"
  fi
done < <(find docs -mindepth 1 -maxdepth 1 -type f -name '*.md' ! -name README.md | sort)

# Extract relative Markdown links from the main READMEs and all documentation.
# Anchors are deliberately stripped: this gate checks file ownership; heading
# anchors remain a Markdown-renderer concern.
while IFS=$'\t' read -r source target; do
  case "$target" in
    http://*|https://*|/*) continue ;;
  esac
  resolved=$(dirname "$source")/$target
  if [[ ! -f "$resolved" ]]; then
    report_error "$source links to missing file $target"
  fi
done < <(perl -ne 'while (/\[[^\]]+\]\(([^)#]+\.md)/g) { print "$ARGV\t$1\n" }' README.md README.en.md $(find docs -type f -name '*.md' | sort))

# Keep a small set of high-value implementation facts tied to source paths.
# This does not attempt to prove all prose, but prevents completed migrations
# from regressing to the specific stale conclusions found by the audit.
assert_implementation_fact internal/platform/localstore/localstore.go docs/feature-implementation-matrix.md '`platform/localstore`'
assert_implementation_fact internal/core/toolfilter/toolfilter.go docs/feature-implementation-matrix.md '`core/toolfilter`'
assert_implementation_fact internal/platform/fsutil/size.go docs/code-and-docs-review-2026-08-31.md '`fsutil.DirSizeBestEffort`'

if (( errors > 0 )); then
  printf 'docs-check: failed with %d error(s)\n' "$errors" >&2
  exit 1
fi

printf 'docs-check: ok\n'
