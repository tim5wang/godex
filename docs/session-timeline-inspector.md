# Session Timeline Inspector（会话时间线详情面板）— 阶段 1

> 中文为主，English summary at the end。
> 对标 DSH 的「轨迹视图」（ui-trajectory）"下探查看细节"能力。阶段 1 交付"每行可展开看详情"；阶段 2 再做分组折叠与概览时间线（见文末）。

## 数据（后端已实现）

### 新事件 `model_request_completed`

每次模型请求完成后发射（`internal/core/conversation/runner.go` 的 `OnModelRequest` 回调 → `internal/agent/runtime.go` → `internal/domain/events/events.go` 的 `ModelRequestPayload`）：

```json
{
  "type": "model_request_completed",
  "payload": {
    "model": "gpt-5.1-codex",
    "input_tokens": 20,
    "output_tokens": 5,
    "cache_read_tokens": 80,
    "cache_write_tokens": 0,
    "started_at": "...", "first_token_at": "...", "completed_at": "...",
    "duration_ms": 1234, "ttft_ms": 320,
    "stop_reason": "end_turn"
  }
}
```

- `ttft_ms` = first_token_at − started_at（首个流事件到达即视为首 token，codex 客户端在第一个 SSE 事件时触发 `OnStreamStarted`）。
- 该事件同时是**命中率观测**的入口：`cache_read_tokens / (input + cache_read)`。

### `assistant_message_completed` 携带 thinking

`TextPayload` 新增 `thinking` 字段（`internal/domain/events/events.go`）：`internal/agent/runtime.go` 用 `thinkingBuf` 累计本次模型调用的推理增量，消息完成时随事件下发，详情面板可展开查看完整思考链。

### 时间戳

`RuntimeEvent.timestamp` 仍是 ISO 字符串；前端用 `Date.parse` 换算（`timelineUtils` 的 `formatTimelineTime`）。工具耗时用已有的 `tool_call_*` 事件 `duration_ms`。

### 持久化

无需改动：`timeline.json` 每事件落盘（recorder 200 条窗口），`events.jsonl` 是崩溃恢复增量日志（turn 完成后轮转）。详情所需数据在窗口内齐全。

## UI（前端已实现）

`ui/web/src/features/chat/panels/EventDetailPanel.tsx`：

- 点击时间线任意行 → 右侧 Drawer（520px），标题含事件名/类型/turn。
- 两个标签页：
  - **Summary**：按事件类型渲染可读详情 ——
    - `tool_call_started/finished`：name、args（JSON）、output、error、duration、artifacts、recovery hint；
    - `assistant_message_completed`：答案（MarkdownRenderer）+ 可折叠 Thinking；
    - `model_request_completed`：模型、input/cached/output tokens（含命中率 %）、TTFT、总耗时、stop_reason；
    - `warning/error`：message/code/actor/recovery hint；
    - 其余类型回退到 JSON。
  - **Raw payload**：完整 JSON。
- `TimelinePanels.tsx`：行可点击（hover 高亮），turn/job tag 点击仍过滤（stopPropagation 隔离）。
- 时间线默认过滤新增 `model_request_completed`（每轮显示每次模型调用，含耗时与 token）。

## 阶段 2（未做，规划）

- 按 turn/step 分组 + 折叠（对应 DSH `TrajectoryTurnModel{groups{cells}}`）。
- 简化三 lane 概览时间线（sequence 模式，选中区间联动表格）。
- 虚拟化（>100 行启用 `@tanstack/react-virtual`）。
- system prompt / 上下文变更 diff（`structuredPatch` from `diff`）。

估算：阶段 1 前端约 1–1.5 人周（已实现），阶段 2 约 1 人周。DSH 的事件状态机层（绑定其类型化事件窗口）与 pan/zoom 交互整体跳过。

---

## English Summary

Stage 1 of a DSH-trajectory-style "drill into details" timeline:

**Backend**: new `model_request_completed` event (per-request usage + TTFT/duration, from `runner.OnModelRequest`), `assistant_message_completed` now carries full `thinking` text, no persistence changes needed.

**Frontend**: `EventDetailPanel.tsx` — clicking any timeline row opens a Drawer with Summary tabs (tool args/output/error/duration, assistant answer + thinking, model usage/token/cache-hit-rate/TTFT) and a Raw payload tab. `model_request_completed` added to default timeline filter.

**Stage 2 (planned, not done)**: turn/step grouping + collapse, simplified 3-lane overview timeline, virtualization, prompt diffs.
