# P3 前置完善：四个根基问题方案（2026-09-23 定稿，方案 A 全采纳）

> 背景：用户提出 4 个 P3 前的根基问题（画布↔JSON 同步 / 执行过程不可见+运行语义 /
> 无节点库 / 运行时统一上下文+事件流+流式）。调研实证见 .godex/tmp/p3_root_issues_research.md。
> 本文是落地设计，交付顺序：Phase 1（①）→ Phase 2（②）→ Phase 3（③）→ Phase 4（④）。

## ① 画布 ↔ Flow JSON 双向同步

现状问题（实证）：
- 画布保存 = 生成新版本（单向）；JSON tab 是**只读**且显示 `versions[0]`（**最旧**版本，bug，FlowsPage.tsx:588）
- 无双向同步：画布改不写 JSON、JSON 改不刷画布

设计：
- JSON tab 改**可编辑 TextArea**（受控，初始值 = 最新带 definition 的版本；修 versions[0] bug）
- 「应用到画布」按钮：`JSON.parse` → 前端基础校验（nodes/edges 数组）→ `flowSpecToWorkflow(def)` → 触发画布重建（canvasKey 变化 remount）
- 「从画布获取」按钮：`document.toJSON()` → `workflowToFlowSpec` → 刷新 JSON 文本
- 画布「保存为新版本」成功后 → JSON tab 自动刷新为新版本定义
- 冲突语义：JSON 编辑与画布编辑**不做实时互相覆盖**（避免丢改动），只在显式操作（应用/获取/保存）时同步

改动文件：
- `FlowsPage.tsx`（DetailDrawer JSON tab：可编辑 + 应用/获取按钮 + versions[0] 修复）
- `FlowGramFlowEditor.tsx`（暴露 applyJSON / getJSON / 保存回调通知）

## ② 调试 vs 生产（画布运行改调试，生产走 gateway）

现状问题（实证）：
- 画布「运行」= 直接 CreateFlowRun+StartFlowRun（生产语义混淆）
- 事件已落盘 events.jsonl + flowRunEvents API + DetailDrawer 轮询存在，但**主画布无运行态展示**
- 生产网关已存在：`POST /v1/gateway/{route}`（biz key 认证 + 幂等 + wait_ms）

设计：
- 画布工具栏「运行」→ 改为「**调试**」：
  - 调试面板（抽屉）：选版本（默认 latest draft）+ 测试 inputs（JSON 编辑器）→ 启动 run
  - 主画布实时事件高亮：节点边框状态色（pending/running/completed/error）+ 连线 flowing 动画（flowgram isFlowingLine）+ 事件列表面板（轮询 flowRunEvents）
- 生产入口：DetailDrawer「接入生产」→ 展示 gateway 调用示例（curl POST /v1/gateway/{route} + BizKey + wait_ms），引导外部客户端接入；画布不再承担生产运行
- 保留 publish/版本管理/运行历史

改动文件：
- `FlowGramFlowEditor.tsx`（调试按钮 + 事件高亮 + 事件面板；复用 FlowGramCanvas 的事件渲染思路）
- `FlowsPage.tsx`（调试面板状态 + 生产接入引导 UI）

## ③ 节点库（function 节点 + JS/WASM + 保存复用）

现状问题（实证）：
- 只有六类固定逻辑节点（step/llm/decision/human/branch/loop）；无代码型节点、无节点库
- WASM 运行时已有（internal/wasmrt，wazero，ABI godex:plugin@0.1）；JS 运行时无（go.mod 无 goja）

设计：
- 第七类节点 `kind=function`（代码节点）：
  ```json
  {
    "id": "vad_split",
    "kind": "function",
    "title": "VAD 切分",
    "function": {
      "runtime": "js" | "wasm",
      "source": "<js 源码>" | "ref": "<wasm 插件 id>",
      "handler": "export function handle(ctx, event) { return [ ...events ] }",
      "input_schema": { "...json schema..." },
      "output_schema": { "...json schema..." }
    }
  }
  ```
- 后端：
  - function 节点编译进事件流运行时（见 ④）；单次场景由 workflow 编排层桥接
  - 节点库 store：`~/.godex/state/node-library/{id}/definition.json`（v1 单版本；含 handler 源码/ref + schema）
  - REST：GET/POST/PUT/DELETE /v1/node-library
- 前端：
  - 节点库面板（列出库节点 → 点/拖插入画布）
  - 画布 function 节点表单：runtime 选择 + 源码/ref 编辑 + schema + 「保存到节点库」
  - 音频转写示例库节点：vad_split / asr_transcribe / quality_check / classify / llm_rewrite
- JS 运行时：引 goja（纯 Go 无 CGO，sandbox：无网络/fs，仅 ctx 读写 + log + 有限 utils）
- WASM：复用 wasmrt（wazero）

## ④ 事件流运行时（双层，统一上下文 + 流式）

现状问题（实证）：
- 现在全部走 durable workflow（internal/agent/workflow.go 1930 行）：step→subagent_task、llm→llm_task、decision→低成本模型、human→human task、branch→同步网关
- 节点间靠 handoff 文本 + DependsOn，**无统一 json/map 上下文**、**无流式**（一次性跑完）
- 编译链：Flow → Compiled → compileFlowToWorkflowInputs → durable workflow

设计（双层运行时）：
- **编排层（保留 durable workflow）**：step/llm/decision/human/branch/loop 继续走子 agent 编排（handoff/重试/恢复/human task），不动
- **计算层（新增事件流运行时）**承载 function 节点：
  - 统一上下文 `ctx = { inputs, vars, node_outputs, state }`（json/map）
  - 节点 = 纯函数 `handler(ctx, event) -> Event[]`（可多次输入/多次输出）
  - 边 = 转换/派生（可选 `transform(ctx, event) -> Event`）
  - 事件不可变追加（event sourcing，落盘 events.jsonl 兼容）；支持流式（VAD 输出 N 段 → ASR 每段一次调用 → 合并节点）
- 桥接：
  - workflow 内 function 节点：单次执行（ctx = 上游变量渲染，跑 handler，输出写回 node outputs）
  - 纯函数流式图（全部 function 节点）：直接事件流运行时执行，不建子 agent
- 执行入口：
  - 单次：走现有 /v1/flow-runs（CreateFlowRun/StartFlowRun 扩展 function 节点执行）
  - 流式：/v1/flow-runs/{id}/stream（SSE 事件流）
- 函数定义复用 godex 已引入的 WASM（wazero）+ 新增 JS（goja），DSL 暂不做（YAGNI，JS 足够表达）

## 交付顺序与验收

| Phase | 内容 | 验收 |
|---|---|---|
| 1（本次） | ① JSON 双向同步 + versions[0] bug | JSON 可编辑、应用/获取双向生效、保存后刷新 |
| 2 | ② 调试模式 + 生产 gateway 引导 | 画布调试单步+事件高亮；生产示例可见 |
| 3 | ③ function 节点 + 节点库（WASM 先行，JS 引入 goja） | 库节点拖入画布、保存/复用、音频转写示例 |
| 4 | ④ 事件流运行时 + SSE 流式 | 纯函数图事件流执行、流式音频/辅助场景 |

## 明确不做（本次）
- 实时 live 双向同步（画布改即改 JSON）——flowgram re-import 重挂载问题，收益低
- 全新 DSL——JS 足够表达，YAGNI
- 重写 1930 行 durable workflow——双层运行时桥接，不推翻
