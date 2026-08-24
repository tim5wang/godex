# Embed UI Demo（最小嵌入式 UI）

> 状态：Step 7 落地 —— 把 godex 的 Workflows 交互能力组件化，第三方 UI 可整体嵌入。
> 参考：`docs/workflows-integration-guide.md`（对接契约）+ `ui/web/src/features/workflows/components/UiCardView.tsx`（可复用组件）。

## 这是什么

一个最小可运行的嵌入式 UI 骨架：用自己的 React 页面，把 godex 当 agent 引擎，
实现「选剧本 → 跑 agent → 流式结果 + ui_card 表单/按钮卡片 + 审批按钮」，
**不依赖 godex 的 Workflows 板块页面**，只复用两样东西：

1. `UiCardView` 组件（渲染 agent 用 `ui_card` 工具产出的表单/按钮/卡片）
2. godex Web API（sessions / messages / events / permissions）

## 复用组件

```tsx
import { UiCardView, type UiCardData } from "godex/ui/src/features/workflows/components";
```

`UiCardView` 是纯展示组件：`card` 数据 + `onSubmitCard` 回调 + 可选 `labels`（默认英文），
不绑定 godex 的 i18n，可直接 drop 进任何 antd 项目。

## 核心接线（对照集成指南 §3）

```tsx
// 1. 建会话
const { session_id } = await fetch(`${API}/sessions`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify({ locator: { channel: "web", key: crypto.randomUUID() } }),
}).then((r) => r.json());

// 2. 发剧本消息（steering）
await fetch(`${API}/sessions/${session_id}/messages`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify({
    envelope: { source: "embed-demo", text: playbookMarkdown },
    queue_mode: "steering",
  }),
});

// 3. SSE 流式渲染 + 卡片 + 审批
const es = new EventSource(`${API}/sessions/${session_id}/events?replay=active`, {
  headers: { Authorization: `Bearer ${token}` },
});
es.onmessage = (event) => {
  const e = JSON.parse(event.data);
  if (e.type === "assistant_text_delta") appendText(e.payload.text);
  if (e.type === "tool_call_finished" && e.payload.name === "ui_card") {
    setCards((c) => [...c, JSON.parse(e.payload.output)]); // → <UiCardView card={...} onSubmitCard={...} />
  }
  if (e.type === "snapshot_ready") refreshPermissions(session_id); // → 审批按钮
  if (e.type === "turn_completed") setDone();
};
```

## 目录结构建议

```
embed-ui/
  App.tsx            ← 你的业务页面（物流看板 / 销售工作台 / 运营卡片）
  agentSession.ts    ← 上面这段接线（建会话/发消息/SSE/审批）
  index.tsx
```

## 与本板块的关系

- godex Workflows 板块（`/workflows`）是**完整参考实现**（Playbooks / Knowledge / Launch 三页签）。
- 本 demo 是**最小嵌入面**：只要 `UiCardView` + Web API 契约，不引入板块页面。
- 两者共用同一套数据（剧本 = notes[tag=workflow]，知识库 = memory）与协议（ui_card JSON）。

## 已组件化清单（Step 7）

| 组件/模块 | 位置 | 用途 |
|---|---|---|
| `UiCardView` | `ui/web/src/features/workflows/components/UiCardView.tsx` | 渲染 form/button_group/card，可嵌入 |
| 类型导出 | `ui/web/src/features/workflows/components/index.ts` | `UiCardData` / `UiCardField` / `UiCardAction` |
| 集成契约 | `docs/workflows-integration-guide.md` | 认证/会话/SSE/卡片/审批 全流程 |
