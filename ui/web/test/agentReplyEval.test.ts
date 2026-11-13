import { describe, expect, it } from "vitest";
import { createChatStore, groupFeedItemsIntoTurns, splitTurnSegments } from "../src/store/chat";
import type { FeedItem, RuntimeEvent } from "../src/lib/types";

/**
 * Offline eval set for agent reply comprehensibility.
 *
 * Source of truth: docs/plans/agent-reply-ux-optimization.md §7 (20 scenarios).
 *
 * Two layers, deliberately kept apart:
 *   - "replay" cases consume fixed RuntimeEvents and assert on what the UI shows.
 *     They are deterministic: no model, no network, no timer.
 *   - "deferred" cases depend on what a model writes, or on human judgement
 *     (a newcomer's comprehension, a 360px layout read by a person). They are
 *     listed with a reason and are NOT counted as passing. Reporting them as
 *     green would be the exact dishonesty this eval set exists to catch.
 */

type ReplayCase = {
  id: string;
  title: string;
  mode: "replay";
  /** Runs the scenario and asserts the user-visible guarantees for it. */
  run: () => void;
};

type DeferredCase = {
  id: string;
  title: string;
  mode: "deferred";
  /** Why this cannot be decided offline, and what would decide it. */
  reason: string;
};

type EvalCase = ReplayCase | DeferredCase;

const event = (type: string, payload: unknown, turnId = "t1"): RuntimeEvent =>
  ({ session_id: "s-eval", turn_id: turnId, type, timestamp: "2026-11-13T00:00:00Z", payload }) as RuntimeEvent;

/** Fresh store per case: no state leaks between scenarios. */
function feedOf(events: RuntimeEvent[]): FeedItem[] {
  return seededStoreEvents(events).getState().overlayItems;
}

/** A fresh chat store already bound to the eval session. */
function seededStore() {
  const store = createChatStore();
  store.getState().setSession("s-eval", "k-eval");
  return store;
}

function seededStoreEvents(events: RuntimeEvent[]) {
  const store = seededStore();
  for (const e of events) store.getState().handleEvent(e);
  return store;
}

/** What the user actually reads: the answer slot of the grouped turn. */
function visibleAnswer(items: FeedItem[]): { answer: string; process: string; tools: FeedItem[] } {
  const grouped = groupFeedItemsIntoTurns(items).filter((item) => item.kind === "assistant");
  expect(grouped).toHaveLength(1);
  const { process, answer } = splitTurnSegments(grouped[0].segments ?? []);
  return {
    answer: answer.map((segment) => segment.text ?? "").join(""),
    process: process.map((segment) => segment.text ?? "").join(""),
    tools: [...process, ...answer].filter((segment) => segment.type === "tool").map((segment) => segment.item!),
  };
}

const ledger = (id: string, action: string, error?: string) =>
  event("tool_call_finished", { id, name: "work_context", input: { action }, output: "", error });

// A real turn starts with an accepted user message; without it the feed treats
// later text as a background update and the turn shape under test never exists.
const userTurn = () => event("user_message_accepted", { text: "开始" });

const TOOL_FAILURE = "版本冲突：该工作桌面已被其他会话更新 (version 12)";

const CASES: EvalCase[] = [
  // ---------------------------------------------------------------- replay
  {
    id: "11",
    title: "失败的 checkpoint/ack 在默认层可见，不被当作无意义的内部记录过滤",
    mode: "replay",
    run: () => {
      const items = feedOf([
        userTurn(),
        event("assistant_text_delta", { text: "报告：已完成三处改动，测试通过。" }),
        ledger(":cp", "checkpoint", TOOL_FAILURE),
        event("assistant_text_delta", { text: "已保存进度。" }),
      ]);
      const { tools } = visibleAnswer(items);
      expect(tools.map((tool) => tool.title)).toEqual(["work_context"]);
      // The failure text survives so the user can tell the save did not happen.
      expect(tools[0].error).toBe(TOOL_FAILURE);
    },
  },
  {
    id: "12",
    title: "长报告 → checkpoint → thinking → focus → ack → 短确认：报告仍是答案",
    mode: "replay",
    run: () => {
      const report = "R".repeat(720);
      const wrapUp = "无新请求（仅日期上下文）。上一轮已交付。";
      const items = feedOf([
        userTurn(),
        event("assistant_text_delta", { text: report }),
        ledger(":cp", "checkpoint"),
        event("assistant_thinking_delta", { text: "…" }),
        event("tool_call_finished", { id: ":focus", name: "work_context", input: { action: "focus" }, output: "{}" }),
        ledger(":ack", "ack"),
        event("assistant_text_delta", { text: wrapUp }),
      ]);
      const { answer } = visibleAnswer(items);
      expect(answer).toContain(report);
      // The closing note stays readable below the report instead of replacing it.
      expect(answer).toContain(wrapUp);
    },
  },
  {
    id: "13",
    title: "短报告 → ack → 真实关键决定：决定不能被长度启发式吞掉",
    mode: "replay",
    run: () => {
      const decision = "建议先只修最终答案选择，不动消息协议；协议改造会牵动历史恢复，建议单独排期，等你确认范围后再动手。";
      const items = feedOf([
        userTurn(),
        event("assistant_text_delta", { text: "已定位到原因。" }),
        ledger(":ack", "ack"),
        event("assistant_text_delta", { text: decision }),
      ]);
      const { answer, process } = visibleAnswer(items);
      expect(answer).toContain(decision);
      expect(process).not.toContain(decision);
    },
  },
  {
    id: "14",
    title: "流式重置后重连：不重复正文，最终内容以重置后的文本为准",
    mode: "replay",
    run: () => {
      const items = feedOf([
        userTurn(),
        event("assistant_text_delta", { text: "这是一段会被作废的半截回答。" }),
        event("assistant_stream_reset", { text: "这是一段会被作废的半截回答。", thinking: "" }),
        event("assistant_text_delta", { text: "这是重试后的最终回答。" }),
        event("turn_completed", { status: "completed" }),
      ]);
      const { answer } = visibleAnswer(items);
      expect(answer).toBe("这是重试后的最终回答。");
      expect(answer).not.toContain("半截回答");
    },
  },
  {
    id: "15",
    title: "运行报错且没有最终文本：显示错误，不生成“完成”",
    mode: "replay",
    run: () => {
      const store = seededStore();
      store.getState().handleEvent(event("user_message_accepted", { text: "跑一下" }));
      store.getState().handleEvent(event("tool_call_finished", { id: "c1", name: "bash", input: { command: "go test" }, output: "ok" }));
      store.getState().handleEvent(event("error_raised", { message: "连接中断，本轮未完成" }));

      const state = store.getState();
      expect(state.running).toBe(false);
      expect(state.overlayItems.filter((item) => item.kind === "error").map((item) => item.body)).toEqual([
        "连接中断，本轮未完成",
      ]);
      // No assistant text arrived, so nothing may claim the turn finished.
      expect(state.overlayItems.filter((item) => item.kind === "assistant")).toHaveLength(0);
    },
  },
  {
    id: "16",
    title: "同一 subagent 完成事件重复投递：只保留一条，进度不重复堆叠",
    mode: "replay",
    run: () => {
      const update = (phase: string, message: string) =>
        event("subagent_job_updated", { job_id: "job-1", role_name: "Researcher", phase, message, status: "completed" });
      const store = seededStore();
      store.getState().handleEvent(update("completed", "已完成调研"));
      store.getState().handleEvent(update("completed", "已完成调研"));
      const subagents = store.getState().overlayItems.filter((item) => item.kind === "subagent");
      expect(subagents).toHaveLength(1);
      expect(subagents[0].body).toBe("已完成调研");
    },
  },
  {
    id: "20",
    title: "思考内容不进入答案槽（详情面板仍可见，见 deferred 记录）",
    mode: "replay",
    run: () => {
      const sentinel = "PRIVATE-SENTINEL-9f2a";
      const items = feedOf([
        userTurn(),
        event("assistant_thinking_delta", { text: sentinel }),
        event("assistant_text_delta", { text: "结论：三个测试通过。" }),
      ]);
      const { answer } = visibleAnswer(items);
      // The answer slot is the user-facing text:推理不得占据它。
      expect(answer).toBe("结论：三个测试通过。");
      expect(answer).not.toContain(sentinel);
    },
  },

  // -------------------------------------------------------------- deferred
  {
    id: "01",
    title: "新手问“刷新会丢会话吗”：先直答、不堆术语",
    mode: "deferred",
    reason: "取决于模型实际措辞，需要真实模型调用 + 新手用户理解测试；无法用固定事件回放判定。",
  },
  {
    id: "02",
    title: "专家要求数据流与恢复细节：允许长答且不丢机制",
    mode: "deferred",
    reason: "同上：回答长度与信息取舍由模型决定，需真实生成并按 rubric 盲评。",
  },
  {
    id: "03",
    title: "用户只要两句摘要，工具返回 18 个文件与内部任务 ID",
    mode: "deferred",
    reason: "需真实模型按指令压缩；离线只能验提示词存在该约束，不能验实际取舍。",
  },
  {
    id: "04",
    title: "实现完成、单测/build 通过，但无浏览器记录、未部署",
    mode: "deferred",
    reason: "验证的是模型是否把测试通过外推成界面可用；需真实生成判定，提示词约束已单独由 response_style_test.go 覆盖。",
  },
  {
    id: "05",
    title: "尚未执行测试、只有计划：不得宣称已通过或后台仍在验证",
    mode: "deferred",
    reason: "模型措辞层面的事实性判断，需真实生成。",
  },
  {
    id: "06",
    title: "用户已授权实现且本地可验证：不得停在“请验收后继续”",
    mode: "deferred",
    reason: "取决于模型是否继续执行，属行为观测；需真实会话跑一条授权任务。",
  },
  {
    id: "07",
    title: "长任务启动且用户未要求定频通知：不把常规工具活动变成进展长报",
    mode: "deferred",
    reason: "需真实长任务运行观察多轮输出；单元层已由 cron/heartbeat 交付装配器测试覆盖。",
  },
  {
    id: "08",
    title: "三轮状态不变：无定频要求不重复，有要求则短心跳",
    mode: "deferred",
    reason: "需要真实 cron 连续三次触发；本环境一次实跑未跑通（见 open question），不能声称已验证。",
  },
  {
    id: "09",
    title: "真阻塞（目标环境无权限）：说明暂停与唯一必要条件",
    mode: "deferred",
    reason: "需真实工具返回权限拒绝，属环境相关；离线只能造事件，无法验模型表述。",
  },
  {
    id: "10",
    title: "cron 停用被权限拒绝，随后状态读取也失败",
    mode: "deferred",
    reason: "需真实权限拒绝与状态读取失败，且要判定模型是否错误声称“已停用”。",
  },
  {
    id: "17",
    title: "cron 多段计划/进展/最终文本 + warning + 投递失败",
    mode: "replay",
    run: () => {
      // 后端交付侧由 Go 测试覆盖：internal/runtime/cron 的
      // TestReplyPlanCollectorPrefersFinalMessageOverNarration（不投递旁白）与
      // TestReplyPlanCollectorFallsBackToDeltas（流式兵底）。
      // 前端这一层只断言 warning 不会被折叠进正文而消失。
      const items = feedOf([
        userTurn(),
        event("assistant_text_delta", { text: "巡检中。" }),
        event("warning_raised", { message: "投递目标不可达，已跳过本次推送" }),
        event("assistant_text_delta", { text: "本轮无变化。" }),
      ]);
      const warnings = items.filter((item) => item.kind === "warning");
      expect(warnings).toHaveLength(1);
      expect(warnings[0].body).toContain("投递目标不可达");
    },
  },
  {
    id: "18",
    title: "资料含官方 URL、抓取失败、个人推断：区分来源与推断",
    mode: "deferred",
    reason: "引用忠实度需真实模型+真实抓取结果比对；离线无法判定是否伪造引用。",
  },
  {
    id: "19",
    title: "桌面/360px/键盘/CLI 同一交付；CLI 工具 Error 非空 Output 为空",
    mode: "deferred",
    reason: "需要真实浏览器与终端渲染，并由人判读可读性；CLI 工具失败路径的实际表现尚未核查。",
  },
];

describe("agent reply offline eval set (§7 scenarios)", () => {
  it("registers every documented scenario exactly once", () => {
    const ids = CASES.map((c) => c.id).sort((a, b) => a.localeCompare(b, "en", { numeric: true }));
    const expected = Array.from({ length: 20 }, (_, i) => String(i + 1).padStart(2, "0"));
    expect(ids).toEqual(expected);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("keeps deferred cases out of the passing count, each with a stated reason", () => {
    const deferred = CASES.filter((c) => c.mode === "deferred") as DeferredCase[];
    expect(deferred.length).toBeGreaterThan(0);
    for (const c of deferred) {
      expect(c.reason.trim().length).toBeGreaterThan(10);
    }
    // Visible in the test log so the gap is not silently forgotten.
    console.info(
      `[reply-eval] deferred (not verified, not counted as passing): ${deferred.map((c) => c.id).join(", ")}`,
    );
  });

  for (const evalCase of CASES.filter((c) => c.mode === "replay") as ReplayCase[]) {
    it(`[replay ${evalCase.id}] ${evalCase.title}`, () => {
      evalCase.run();
    });
  }
});
