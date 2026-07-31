#!/usr/bin/env python3
"""sync_codex.py — 一键从 Codex 同步 provider 配置到 GoDex.

用法:
    python3 scripts/sync_codex.py              # 同步，输出变更
    python3 scripts/sync_codex.py --dry-run    # 仅预览，不写文件
    python3 scripts/sync_codex.py --force      # 跳过覆盖确认

读取:
    ~/.codex/config.toml                       → provider 定义
    ~/.codex/ais-switch-model-catalog.json     → 模型列表
写入:
    ~/.godex/godex.yaml                        → api.providers 增量合并
依赖:
    PyYAML (pip3 install pyyaml)
"""

import argparse
import json
import os
import re
import sys
from typing import Any, Dict, List

try:
    import yaml as yaml_mod
except ImportError:
    print("错误: 需要 PyYAML。请运行: pip3 install pyyaml", file=sys.stderr)
    sys.exit(1)

GOEXPECT_DIR = os.path.expanduser("~/.godex")
GOEXPECT_YAML = os.path.join(GOEXPECT_DIR, "godex.yaml")
CODEX_TOML = os.path.expanduser("~/.codex/config.toml")
CODEX_CATALOG = os.path.expanduser("~/.codex/ais-switch-model-catalog.json")


# ============================================================
#  Codex config.toml 解析 (简单行解析，不引入 tomllib)
# ============================================================

def parse_codex_toml(path: str) -> Dict[str, Dict[str, str]]:
    """解析 model_providers 段，返回 {provider_id: {字段: 值}}."""
    providers: Dict[str, Dict[str, str]] = {}
    current_section: str | None = None
    current_provider: str | None = None

    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            # [model_providers.custom]
            m = re.match(r"^\[model_providers\.([^\]]+)\]$", line)
            if m:
                current_section = "model_providers"
                current_provider = m.group(1)
                providers.setdefault(current_provider, {})
                continue
            # 嵌套子表 [model_providers.custom.env] — 跳过
            if re.match(r"^\[model_providers\.[^\]]+\.[^\]]+\]", line):
                current_section = None
                continue
            # 离开 model_providers 段
            if re.match(r"^\[", line):
                current_section = None
                continue
            # key = value
            m = re.match(r"^(\S+)\s*=\s*(.+)$", line)
            if m and current_section == "model_providers" and current_provider:
                key = m.group(1)
                val = m.group(2).strip().strip('"').strip("'")
                providers[current_provider][key] = val
    return providers


# ============================================================
#  AIS model catalog JSON 解析
# ============================================================

def parse_ais_catalog(path: str) -> List[Dict[str, str]]:
    """返回 [{id: ..., name: ...}]."""
    if not os.path.exists(path):
        print(f"[skip] AIS catalog not found: {path}", file=sys.stderr)
        return []

    with open(path) as f:
        data = json.load(f)

    items: List[Any]
    if isinstance(data, list):
        items = data
    elif isinstance(data, dict):
        items = data.get("models", [])
    else:
        return []

    models: List[Dict[str, str]] = []
    for entry in items:
        slug = (str(entry.get("slug") or entry.get("id") or "")).strip()
        if not slug:
            continue
        name = (str(entry.get("display_name") or entry.get("name") or slug)).strip()
        models.append({"id": slug, "name": name})
    return models


# ============================================================
#  Codex → GoDex 映射
# ============================================================

def codex_wire_to_godex_type(wire_api: str) -> str:
    wire = wire_api.strip().lower()
    if wire in ("anthropic", "anthropic_compatible"):
        return "anthropic_compatible"
    return "openai_compatible"


def normalize_base_url(raw: str) -> str:
    """剥离 Codex 路径后缀，回到 API host 根."""
    raw = raw.strip().rstrip("/")
    for suffix in ("/codex/v1", "/v1", "/responses", "/chat/completions"):
        while raw.endswith(suffix):
            raw = raw[: -len(suffix)].rstrip("/")
    return raw


def build_godex_providers(
    codex_providers: Dict[str, Dict[str, str]],
    ais_models: List[Dict[str, str]],
) -> Dict[str, Any]:
    """构建 GoDex 格式的 providers dict."""
    result: Dict[str, Any] = {}
    for pid, defn in codex_providers.items():
        base_url = normalize_base_url(defn.get("base_url", ""))
        wire_api = defn.get("wire_api", "")
        name = defn.get("name", pid)

        models: Dict[str, Any] = {}
        if ais_models:
            for m in ais_models:
                models[m["id"]] = {
                    "name": m["name"],
                    "model": m["id"],
                    "max_tokens": 4096,
                    "supports_streaming": True,
                }
        else:
            models["default"] = {
                "name": "default",
                "model": "default",
                "max_tokens": 4096,
                "supports_streaming": True,
            }

        env_var = pid.strip().upper().replace("-", "_") + "_API_KEY"
        result[f"codex-{pid}"] = {
            "name": name,
            "type": codex_wire_to_godex_type(wire_api),
            "base_url": base_url,
            "api_key_env": env_var,
            "models": models,
        }
    return result


# ============================================================
#  GoDex YAML 读写与合并
# ============================================================

class GodexConfig:
    """封装 ~/.godex/godex.yaml 的读写与 provider 合并."""

    def __init__(self, path: str):
        self.path = path
        self.data: Dict[str, Any] = {}
        if os.path.exists(path):
            with open(path) as f:
                self.data = yaml_mod.safe_load(f) or {}

    def providers(self) -> Dict[str, Any]:
        return self.data.setdefault("api", {}).setdefault("providers", {})

    def merge_providers(self, new_providers: Dict[str, Any]) -> int:
        """增量合并 new_providers 到现有 providers，返回新增数."""
        existing = self.providers()
        added = 0
        for pid, p in new_providers.items():
            if pid not in existing:
                added += 1
            existing[pid] = p
        return added

    def save(self, backup: bool = True) -> None:
        if backup and os.path.exists(self.path):
            bak = self.path + ".bak.sync-codex"
            with open(self.path) as f:
                bak_content = f.read()
            with open(bak, "w") as f:
                f.write(bak_content)
            print(f"已备份原配置到: {bak}")

        os.makedirs(os.path.dirname(self.path), exist_ok=True)
        # 使用 pyyaml 的 dump，保持可读性
        with open(self.path, "w") as f:
            yaml_mod.dump(
                self.data, f,
                default_flow_style=False,
                allow_unicode=True,
                sort_keys=False,
                indent=2,
            )


# ============================================================
#  主流程
# ============================================================

def main() -> None:
    parser = argparse.ArgumentParser(description="从 Codex 同步 provider 到 GoDex")
    parser.add_argument("--dry-run", action="store_true", help="仅预览，不写文件")
    parser.add_argument("--force", "-f", action="store_true", help="跳过覆盖确认")
    parser.add_argument("--godex-config", default=GOEXPECT_YAML,
                        help=f"GoDex 配置路径 (默认 {GOEXPECT_YAML})")
    parser.add_argument("--codex-config", default=CODEX_TOML,
                        help=f"Codex config.toml 路径 (默认 {CODEX_TOML})")
    parser.add_argument("--codex-catalog", default=CODEX_CATALOG,
                        help=f"AIS model catalog 路径 (默认 {CODEX_CATALOG})")
    args = parser.parse_args()

    # ---- 1. 读取 Codex ----
    if not os.path.exists(args.codex_config):
        print(f"错误: Codex config 不存在: {args.codex_config}", file=sys.stderr)
        sys.exit(1)

    codex_providers = parse_codex_toml(args.codex_config)
    if not codex_providers:
        print("未在 Codex config.toml 中找到 [model_providers] 定义", file=sys.stderr)
        sys.exit(1)

    ais_models = parse_ais_catalog(args.codex_catalog)
    new_providers = build_godex_providers(codex_providers, ais_models)

    # ---- 2. 预览 ----
    print(f"从 Codex 发现 {len(codex_providers)} 个 provider:")
    for pid, p in new_providers.items():
        models = p.get("models", {})
        print(f"  [{pid}] {p['name']} | {p['type']} | {p['base_url']} | {len(models)} models")
        for mid, m in models.items():
            print(f"      • {mid} → {m.get('name', mid)}")

    # ---- 3. 检查冲突 ----
    godex = GodexConfig(args.godex_config)
    existing = godex.providers()
    conflicts = [pid for pid in new_providers if pid in existing]
    if conflicts:
        print(f"\n以下 provider 已存在，将被覆盖: {', '.join(conflicts)}")

    if args.dry_run:
        print("\n--dry-run 模式 (未写入文件)")
        return

    # ---- 4. 确认 ----
    if not args.force:
        try:
            resp = input(f"\n写入 {args.godex_config}？[Y/n] ").strip().lower()
        except EOFError:
            # 非交互环境，默认跳过写入
            print("\n非交互环境，使用 --force 参数可跳过确认直接写入")
            print("已取消 (未写入)")
            return
        if resp and resp not in ("y", "yes"):
            print("已取消")
            return

    # ---- 5. 合并写入 ----
    added = godex.merge_providers(new_providers)
    try:
        godex.save(backup=True)
    except Exception as e:
        print(f"错误: 写入配置失败: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"✓ 已写入 {args.godex_config}")
    print(f"  新增 {added} 个, 更新 {len(new_providers) - added} 个 provider")
    print("  请确保 .env 中配置了对应的 API Key (如 CUSTOM_API_KEY)")


if __name__ == "__main__":
    main()
