# Session Timeline Inspector（会话时间线详情面板）

> 中文为主，English summary at the end。
> 对标 DSH 的「轨迹视图」（ui-trajectory）"下探查看细节"能力。阶段 1：每行可展开看详情；阶段 2：turn/step 分组折叠 + 三 lane 概览时间线 + 窗口化虚拟化（均已实现）。

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

## 阶段 2（已实现）

- **turn/step 分组 + 折叠**（`TimelineGroupedList.tsx`）：事件按 turn 分组（新 turn 在上），turn 内按 `Message` / `Step N` 分段（每个 `model_request_completed` 开启新 Step）；turn 头显示事件数/工具直方图/时间范围，step 头显示耗时与工具列表；turn 与 step 均可折叠（折叠后显示汇总行）。
- **三 lane 概览时间线**（`TimelineOverview.tsx`）：Chrome Network 式 sequence 概览条 —— 每个事件一个等宽色块（input=蓝 / model=绿 / tool=橙 / other=灰），turn 边界用间隙分隔；点击色块跳转并选中对应行（自动展开所在 turn/step），tooltip 显示事件名与摘要。
- **窗口化虚拟化**（`useWindowedRows`，TimelineGroupedList 内）：行数 >150 时启用，滚动容器 + 前缀和二分定位 + overscan（依赖受限无法安装 @tanstack/react-virtual，手写实现；分页本身已限制每页规模）。
- **选中联动**：概览点击 / 事件行点击 → 高亮 + 滚动到行 + 打开详情 Drawer。
- **单测**：`ui/web/src/lib/timelineUtils.test.ts`（`groupTimelineTurns` / `flattenTimelineEvents` / `timelineEventLane`，7 个用例，全部通过）。

估算：阶段 1 前端约 1–1.5 人周（已实现），阶段 2 约 1 人周（已实现）。DSH 的事件状态机层（绑定其类型化事件窗口）、pan/zoom 全交互时间线、增量搜索索引未做（跳过项见上文）。

---

## English Summary

DSH-trajectory-style session timeline, both stages implemented:

**Stage 1 — drill into details**: new `model_request_completed` event (per-request usage + TTFT/duration from `runner.OnModelRequest`); `assistant_message_completed` carries full `thinking` text; `EventDetailPanel.tsx` opens on row click with Summary tabs (tool args/output/error/duration, assistant answer + thinking, model usage/token/cache-hit-rate/TTFT) and Raw payload; `model_request_completed` added to the default filter.

**Stage 2 — grouped timeline**: `groupTimelineTurns` (pure, unit-tested) groups events into turns → `Message` / `Step N`; `TimelineGroupedList.tsx` renders collapsible turn/step headers (event count, tool histogram, duration) with windowed virtualization (>150 rows, hand-rolled since @tanstack/react-virtual was not installable offline); `TimelineOverview.tsx` is a Chrome-Network-style 3-lane sequence strip (input/model/tool colors, turn gaps, click-to-jump with auto-expand); selection highlights + scrolls the row and opens the detail drawer.

Not ported (DSH-specific): typed event state machines, pan/zoom interactions, incremental search index, prompt diffs.
