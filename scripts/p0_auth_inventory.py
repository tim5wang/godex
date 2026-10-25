#!/usr/bin/env python3
"""P0 鉴权入口清点 —— 受保护/未受保护路由、鉴权族、执行入口子集。

用途：产出 docs/p0-principal-migration-inventory.md 的原始数据，使该清单
可被任何人复现（而非依赖一次性的人工 grep）。

口径说明（重要，否则数字对不上）：
  - 只统计 internal/runtime/httpapi/*.go，**排除 _test.go**（测试里的
    mux.Handle 不算真实注册面）。全仓库含测试约 300 处。
  - 包裹器常以**变量**形式在注册行**之前**定义，例如：
        auth := voiceQueryTokenAuth(protected, tokenProvider)
        mux.Handle("GET /v1/voice", auth(...))
    因此只看注册行本身会把它们误判为"无鉴权"（早期版本误报 40 条）。
    本脚本改为对每个注册做**显式归类**，并对未命中任何已知族的注册
    打印警告，避免静默漏报。

用法：
    python3 scripts/p0_auth_inventory.py            # 全部报告
    python3 scripts/p0_auth_inventory.py --exec     # 仅执行入口子集
"""
from __future__ import annotations

import argparse
import collections
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
HTTPAPI = ROOT / "internal" / "runtime" / "httpapi"

# mux.Handle("GET /path", <wrapper>(...))  —— 第三个捕获组是包裹器表达式首 token
REG = re.compile(
    r'mux\.Handle(?:Func)?\(\s*"([A-Z]+)\s+([^"]+)"\s*,\s*([A-Za-z_][A-Za-z0-9_]*)'
)

# 执行入口：决策⑨「超管独占 center_execution」的判定点。
# 锚定到**会话级**执行面：workflows / skills 的路由均已位于 /sessions/{id}/...
# 之下，不要单列这两个前缀——全局 GET /skills/catalog 属目录行而非执行入口，
# 早先把它们当作前缀会多算 1 处（67 而 66）。
EXEC_RE = re.compile(
    r"^/(sessions|turns"
    r"|v1/agent-steps|v1/chat/completions|v1/responses|v1/messages"
    r"|v1/models|v1/gateway)"
)


def collect() -> list[tuple[str, int, str, str, str]]:
    """返回 (file, line, method, route, wrapper) 列表。"""
    rows: list[tuple[str, int, str, str, str]] = []
    for path in sorted(HTTPAPI.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for i, line in enumerate(path.read_text(errors="replace").splitlines()):
            m = REG.search(line)
            if m:
                rows.append((path.name, i + 1, m.group(1), m.group(2), m.group(3)))
    return rows


def classify(f: str, route: str, wrapper: str) -> str:
    """把一个注册归入「受鉴权 / 无鉴权」的某一族。未识别则返回 ⚠️ 前缀值。"""
    # --- 无鉴权（8 处）---
    if wrapper == "tm":
        return "未受鉴权:terminal(缺陷)"
    if f == "routes_watch.go" and route in ("/watch/pair/start", "/watch/pair/token"):
        return "未受鉴权:watch 配对(有意匿名)"
    if f == "httpapi.go" and route == "/meta":
        return "未受鉴权:meta(公开元信息)"
    # --- 受鉴权（260 处）---
    if route in ("/v1/models", "/v1/usage/cache-stats"):
        return "受鉴权:inline gdx_ 代理键"
    if wrapper == "gunzipBody":
        return "受鉴权:gateway 双认证(gdx_|web token)"
    if wrapper == "protected":
        return "受鉴权:protected"
    if wrapper == "nodeProxyRead":
        return "受鉴权:nodeProxyRead"
    if wrapper == "read":
        return "受鉴权:watch read"
    if wrapper == "command":
        return "受鉴权:watch command"
    if wrapper == "approve":
        return "受鉴权:watch approve"
    if wrapper == "access":
        return "受鉴权:watch(read)|web token"
    if wrapper == "withBizKeyAuth":
        return "受鉴权:bizKey"
    if wrapper in ("auth", "previewAuth"):
        return "受鉴权:query-token"
    return f"⚠️未归类 wrapper={wrapper} route={route}"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--exec", action="store_true", help="仅列出执行入口子集")
    args = ap.parse_args()

    rows = collect()
    classified = [(r, classify(r[0], r[3], r[4])) for r in rows]

    if args.exec:
        print(f"=== 执行入口子集（决策⑨ 闸点）===")
        per_file: collections.Counter[str] = collections.Counter()
        for (f, ln, meth, route, _w), _c in classified:
            if EXEC_RE.search(route):
                per_file[f] += 1
                print(f"  {f}:{ln}  {meth:6s} {route}")
        print(f"  合计 {sum(per_file.values())} 处 / {len(per_file)} 文件")
        return 0

    print(f"=== 路由注册（非测试）: {len(rows)} 处 / "
          f"{len(set(r[0] for r in rows))} 文件 ===")

    counts = collections.Counter(c for _r, c in classified)
    authed = sum(v for k, v in counts.items() if k.startswith("受鉴权:"))
    unauth = sum(v for k, v in counts.items() if k.startswith("未受鉴权:"))
    unknown = {k: v for k, v in counts.items() if k.startswith("⚠️")}

    print("\n--- 受鉴权 ---")
    for k, v in sorted(((k, v) for k, v in counts.items()
                        if k.startswith("受鉴权:")), key=lambda x: -x[1]):
        print(f"  {k.split(':', 1)[1]:32s} {v}")
    print(f"  {'受鉴权合计':32s} {authed}")

    print("\n--- 未受鉴权 ---")
    for k, v in sorted(((k, v) for k, v in counts.items()
                        if k.startswith("未受鉴权:")), key=lambda x: -x[1]):
        print(f"  {k.split(':', 1)[1]:32s} {v}")
    print(f"  {'未受鉴权合计':32s} {unauth}")

    print(f"\n口径自检: {authed} + {unauth} = {authed + unauth} (应 = {len(rows)})")

    if unknown:
        print("\n⚠️ 存在未归类注册，请更新 classify()：", file=sys.stderr)
        for k, v in unknown.items():
            print(f"    {v} × {k}", file=sys.stderr)
        return 1

    n_exec = sum(1 for (f, ln, meth, route, _w), _c in classified
                 if EXEC_RE.search(route))
    n_files = len({f for (f, ln, meth, route, _w), _c in classified
                   if EXEC_RE.search(route)})
    print(f"\n执行入口子集（决策⑨ 闸点）: {n_exec} 处 / {n_files} 文件")
    print("  用 --exec 查看逐条")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
