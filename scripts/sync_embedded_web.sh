#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src_dir="$repo_root/ui/web/dist"
dst_dir="$repo_root/internal/uiassets/embedded_dist"

if [[ ! -f "$src_dir/index.html" ]]; then
  echo "missing built web UI at $src_dir; run 'cd ui/web && pnpm build' first" >&2
  exit 1
fi

rm -rf "$dst_dir"
mkdir -p "$dst_dir"
cp -R "$src_dir"/. "$dst_dir"/

echo "synced embedded web UI from $src_dir to $dst_dir"
