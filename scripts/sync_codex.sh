#!/usr/bin/env bash
# sync_codex.sh — 一键从 Codex 同步 provider 配置到 GoDex (shell 版本)
#
# 用法:
#   ./scripts/sync_codex.sh           # 交互确认
#   ./scripts/sync_codex.sh --yes     # 自动确认
#   ./scripts/sync_codex.sh --dry-run # 仅预览
#
# 依赖: godex 二进制已编译，且在 PATH 中

set -euo pipefail

GODEX_BIN="${GODEX_BIN:-godex}"
YES="${1:-}"

die() { echo "错误: $*" >&2; exit 1; }

# 检查 Codex config 是否存在
CODEX_TOML="${HOME}/.codex/config.toml"
if [[ ! -f "$CODEX_TOML" ]]; then
  die "Codex config 不存在: $CODEX_TOML"
fi

# 检查 godex 二进制
if ! command -v "$GODEX_BIN" &>/dev/null; then
  # 尝试在项目根目录找
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
  GODEX_BIN="$PROJECT_ROOT/godex"
  if [[ ! -x "$GODEX_BIN" ]]; then
    die "未找到 godex 二进制，请先 go build ./cmd/godex/"
  fi
fi

# 预览: 调用 GoDex 内部配置导入逻辑
echo "=== Codex 配置预览 ==="
echo ""

# 解析 Codex TOML (纯 Bash)
extract_codex_providers() {
  local current_provider=""
  local in_providers=0
  while IFS= read -r line; do
    line="${line%%#*}"  # 去注释
    line="${line## }"; line="${line%% }"  # trim
    [[ -z "$line" ]] && continue

    # [model_providers]
    if [[ "$line" =~ ^\[model_providers\]$ ]]; then
      in_providers=1
      continue
    fi
    # [model_providers.custom]
    if [[ "$line" =~ ^\[model_providers\.([^\]]+)\]$ ]]; then
      in_providers=1
      current_provider="${BASH_REMATCH[1]}"
      echo "PROVIDER $current_provider"
      continue
    fi
    # 离开段
    if [[ $in_providers -eq 1 ]] && [[ "$line" =~ ^\[.*\]$ ]] && [[ ! "$line" =~ model_providers ]]; then
      in_providers=0
      continue
    fi
    # key = value
    if [[ $in_providers -eq 1 ]] && [[ "$line" =~ ^([a-zA-Z_][a-zA-Z0-9_]*)\ *=\ *(.+)$ ]]; then
      key="${BASH_REMATCH[1]}"
      val="${BASH_REMATCH[2]}"
      val="${val#\"}"; val="${val%\"}"  # 去引号
      val="${val#\'}"; val="${val%\'}"
      echo "  $key = $val"
    fi
  done < "$CODEX_TOML"
}

echo "Codex provider(s):"
extract_codex_providers
echo ""

# AIS 模型目录
CODEX_CATALOG="${HOME}/.codex/ais-switch-model-catalog.json"
if [[ -f "$CODEX_CATALOG" ]]; then
  echo "AIS 模型目录:"
  # 用 python3 解析 JSON，或 jq
  if command -v python3 &>/dev/null; then
    python3 -c "
import json, sys
with open('$CODEX_CATALOG') as f:
    data = json.load(f)
items = data if isinstance(data, list) else data.get('models', [])
for m in items:
    slug = m.get('slug', m.get('id', '?'))
    name = m.get('display_name', slug)
    print(f'  • {slug} ({name})')
"
  elif command -v jq &>/dev/null; then
    jq -r 'if type == "array" then .[] else .models[] end | .slug + " (" + .display_name + ")"' "$CODEX_CATALOG"
  fi
  echo ""
fi

# 如果是 dry-run 模式退出
if [[ "$YES" == "--dry-run" ]]; then
  echo "(dry-run: 未写入)"
  exit 0
fi

# 执行导入
echo "=== 开始同步 ==="
if [[ "$YES" == "--yes" || "$YES" == "-y" ]]; then
  # 自动确认
  echo "i"$'\n'"y"$'\n'"q"$'\n' | "$GODEX_BIN" config
else
  echo "启动交互式配置向导，请选择 [i] Import from Codex..."
  "$GODEX_BIN" config
fi

echo ""
echo "同步完成。运行 'godex config' 查看或 'godex doctor' 诊断。"
