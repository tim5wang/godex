# Agent Step Platform — TypeScript SDK（Phase B）

> 状态：Phase B 落地 ｜ 位置：`ui/web/src/lib/agent-step/`
> 定位：自包含、零依赖的 TS 客户端，让任意业务前端把 godex 当 agent 运行时——`createStep` 跑一个业务环节，拿到结构化结果；`ui_card` 卡片数据直接复用 `UiCardView` 渲染。
> 后端契约：`docs/agent-step-platform-details.md` §2。

## 安装/引入

SDK 是**纯 TS、零运行时依赖**的单个模块，直接复制 `client.ts` + `index.ts` 进你的项目即可；也可从 godex 仓库按需引用：

```ts
import { createStepClient } from "./lib/agent-step"; // 或 godex 的 ui/web/src/lib/agent-step
```

不需要 React、不需要 antd——`UiCardData` 只是类型，渲染层（`UiCardView`）在 godex UI 里，业务方用自己的组件渲染卡片 JSON。

## 快速开始

```ts
import { createStepClient, type UiCardData } from "./lib/agent-step";

const step = createStepClient({
  baseUrl: "https://godex.claw.carc.top", // godex 服务地址
  apiKey: "biz_xxx",                      // 业务系统 key（/v1/biz/keys 创建）
});

// 跑一个业务环节（同步；超时后自动轮询直至完成）
const result = await step.createStep({
  prompt: "分析订单 ORD-1234 的延迟原因并给出恢复方案",
  inputs: { order_id: "ORD-1234" },                       // 业务上下文（受控注入）
  context: { recall: ["sales_crm", "godex://memory"] },   // 知识库召回
  tools: { mcp: ["crm/*"], sandbox: ["read_file"] },      // 工具白名单（key 为上限）
  structured_output: {
    schema: {
      type: "object",
      properties: {
        cause: { type: "string" },
        recovery: { type: "string" },
      },
      required: ["cause", "recovery"],
    },
  },
  timeout_seconds: 120,
});

console.log(result.output);   // { cause: "...", recovery: "..." }
console.log(result.text);     // 人类可读结果
console.log(result.tools_used); // [{name:"crm__get_order",kind:"mcp"},...]
```

## API

### `createStepClient({ baseUrl, apiKey, fetch?, pollIntervalMs? })`
构造客户端。`fetch` 可注入（测试/代理），`pollIntervalMs` 默认 1s。

### `client.createStep(req, signal?) → Promise<StepResult>`
跑一个同步 agent 环节。收到 408（同步超时）时自动开始轮询 `GET /v1/agent-steps/{id}` 直到离开 running 态。传 `AbortSignal` 可取消等待。

### `client.getStep(stepId, signal?) → Promise<StepResult>`
查询某 step 的终态（超时后追查用）。

### `client.cancelStep(stepId, signal?) → Promise<void>`
中止某 step 的活跃 turn（无活跃 turn 抛 `StepAPIError` 409）。

### `client.streamEvents(stepId, onEvent, signal?) → Promise<AbortController>`
SSE 订阅 step 底层会话事件（`assistant_text_delta` / `tool_call_finished` / `turn_completed` ...）。返回的 controller 用于中止。

### 错误
非 2xx 抛 `StepAPIError`，带统一信封：

```ts
catch (err) {
  if (err instanceof StepAPIError) {
    console.log(err.status, err.code, err.message); // 422, "invalid_output", "..."
    console.log(err.step_id, err.session_id);
  }
}
```

错误码：`invalid_request` / `unauthorized` / `step_failed` / `step_timeout`(408) / `invalid_output`(422) / `step_not_running`(409) / ...

## 渲染 ui_card（可选）

agent 用 `ui_card` 工具产出的表单/按钮/卡片，会以 JSON 形式出现在结果/事件里。godex 的 `UiCardView` 组件（`ui/web/src/features/workflows/components/UiCardView.tsx`）负责渲染，业务方若用 React 可直接复用；否则按 `UiCardData` 类型自行渲染：

```ts
import type { UiCardData } from "./lib/agent-step";

const card: UiCardData = { kind: "form", title: "补充信息", fields: [{ name: "priority", type: "select", options: [...] }] };
```

## 测试

```bash
cd ui/web
npx vitest run src/lib/agent-step/client.test.ts
```

## 与 Phase A 的关系

| 层 | 位置 | 职责 |
|---|---|---|
| HTTP API | Go：`internal/runtime/httpapi/routes_steps.go` | `POST /v1/agent-steps` + 追踪端点 |
| TS SDK | `ui/web/src/lib/agent-step/` | 封装成 `createStep` / `getStep` / `cancelStep` / `streamEvents` |
| 嵌入组件 | Phase C（后续） | 基于 SDK 封装 `<godex-step />` Web Component |
