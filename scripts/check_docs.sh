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
  case "$doc" in
    docs/index.md|docs/guide/*.md|docs/develop/*.md|docs/reference/*.md|docs/operations/*.md) continue ;;
  esac
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
readme_sources=(README.md)
if [[ -f README.en.md ]]; then
  readme_sources+=(README.en.md)
fi
while IFS=$'\t' read -r source target; do
  case "$target" in
    http://*|https://*|/*) continue ;;
  esac
  resolved=$(dirname "$source")/$target
  if [[ ! -f "$resolved" ]]; then
    report_error "$source links to missing file $target"
  fi
done < <(perl -ne 'while (/\[[^\]]+\]\(([^)#]+\.md)/g) { print "$ARGV\t$1\n" }' "${readme_sources[@]}" $(find docs -path docs/node_modules -prune -o -path docs/.vitepress -prune -o -type f -name '*.md' -print | sort))

# VitePress navigation uses extensionless routes, so verify every local link in
# the site config resolves to a Markdown source. Static assets and external
# links are intentionally ignored here and are validated by the site build.
while IFS= read -r target; do
  case "$target" in
    http://*|https://*|mailto:*|\#*) continue ;;
  esac
  route=${target%%#*}
  route=${route#/}
  if [[ -z "$route" ]]; then
    resolved=docs/index.md
  elif [[ "$route" == */ ]]; then
    resolved="docs/${route}index.md"
  else
    resolved="docs/${route}.md"
  fi
  if [[ ! -f "$resolved" ]]; then
    report_error "docs/.vitepress/config.mts links to missing route $target"
  fi
done < <(grep -Eo "link: '[^']+'" docs/.vitepress/config.mts | sed -E "s/^link: '([^']+)'$/\\1/")

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
