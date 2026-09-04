import { describe, expect, it } from "vitest";
import type { SessionTimelineEntry } from "./types";
import { groupTimelineTurns, flattenTimelineEvents, timelineEventLane, pendingSendsForFeed, collectToolCalls, mergeChronologicalFeedItems, alignAssistantTextTurnIds, collectThinkingDeltas, collectTextDeltas } from "./timelineUtils";
import type { PendingSend } from "../store/chat";

/**
 * NOTE: groupTimelineTurns consumes a NEWEST-FIRST list (as served by the
 * timeline API / recorder fallback) and reverses it internally, so all test
 * inputs below are ordered newest-first.
 */
function ev(type: SessionTimelineEntry["type"], turnId: string | undefined, ts: string, payload: Record<string, unknown> = {}): SessionTimelineEntry {
  return { type, turn_id: turnId, timestamp: ts, payload };
}

describe("groupTimelineTurns", () => {
  it("returns [] for empty input", () => {
    expect(groupTimelineTurns([])).toEqual([]);
  });

  it("groups one turn into Message + Step N, preserving event order", () => {
    // newest-first input of one turn: turn_completed ... user_message
    const items = [
      ev("turn_completed", "turn-1", "2026-01-01T00:00:06Z", { status: "completed" }),
      ev("assistant_message_completed", "turn-1", "2026-01-01T00:00:05Z", { text: "done" }),
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:04Z", { name: "bash", duration_ms: 900 }),
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:03Z", { name: "bash" }),
      ev("model_request_completed", "turn-1", "2026-01-01T00:00:02Z", { model: "gpt-x", input_tokens: 10 }),
      ev("user_message_accepted", "turn-1", "2026-01-01T00:00:00Z", { text: "hi" }),
    ];
    const groups = groupTimelineTurns(items);
    expect(groups).toHaveLength(1);
    const turn = groups[0];
    expect(turn.turnId).toBe("turn-1");
    expect(turn.eventCount).toBe(6);
    expect(turn.tools).toEqual([{ name: "bash", count: 1 }]);
    expect(turn.steps.map((s) => s.label)).toEqual(["Message", "Step 1"]);
    expect(turn.steps[0].events.map((e) => e.type)).toEqual(["user_message_accepted"]);
    expect(turn.steps[1].events.map((e) => e.type)).toEqual([
      "model_request_completed",
      "tool_call_started",
      "tool_call_finished",
      "assistant_message_completed",
      "turn_completed",
    ]);
    // chronological flatten is oldest-first
    expect(flattenTimelineEvents(groups).map((e) => e.type)).toEqual([...items].reverse().map((e) => e.type));
  });

  it("opens a new Step N per model request", () => {
    // newest-first: second model request + its tool first
    const items = [
      ev("tool_call_finished", "turn-9", "2026-01-01T00:00:04Z", { name: "ls" }),
      ev("model_request_completed", "turn-9", "2026-01-01T00:00:03Z", { model: "b" }),
      ev("tool_call_finished", "turn-9", "2026-01-01T00:00:02Z", { name: "sh" }),
      ev("model_request_completed", "turn-9", "2026-01-01T00:00:01Z", { model: "a" }),
    ];
    const groups = groupTimelineTurns(items);
    expect(groups[0].steps.map((s) => s.label)).toEqual(["Step 1", "Step 2"]);
    expect(groups[0].tools).toEqual([
      { name: "sh", count: 1 },
      { name: "ls", count: 1 },
    ]);
  });

  it("emits turns newest-first (input is newest-first) with chronological inner order", () => {
    const items = [
      ev("turn_completed", "turn-2", "2026-01-01T00:10:02Z", {}),
      ev("model_request_completed", "turn-2", "2026-01-01T00:10:01Z", {}),
      ev("user_message_accepted", "turn-2", "2026-01-01T00:10:00Z", { text: "second" }),
      ev("turn_completed", "turn-1", "2026-01-01T00:00:02Z", {}),
      ev("model_request_completed", "turn-1", "2026-01-01T00:00:01Z", {}),
      ev("user_message_accepted", "turn-1", "2026-01-01T00:00:00Z", { text: "first" }),
    ];
    const groups = groupTimelineTurns(items);
    expect(groups.map((g) => g.turnId)).toEqual(["turn-2", "turn-1"]);
    expect(flattenTimelineEvents(groups).map((e) => e.turn_id)).toEqual(["turn-1", "turn-1", "turn-1", "turn-2", "turn-2", "turn-2"]);
  });

  it("buckets events without a turn under 'No turn'", () => {
    const items = [
      ev("turn_completed", "turn-1", "2026-01-01T00:00:01Z", {}),
      ev("warning_raised", undefined, "2026-01-01T00:00:00Z", { message: "boom" }),
    ];
    const groups = groupTimelineTurns(items);
    expect(groups).toHaveLength(2);
    // newest-first: the newer turn-1 group first, the older "No turn" group last
    expect(groups[0].turnId).toBe("turn-1");
    expect(groups[1].turnId).toBeNull();
    expect(groups[1].label).toBe("No turn");
    expect(groups[1].steps[0].events.map((e) => e.type)).toEqual(["warning_raised"]);
  });

  it("routes tool events without a preceding model request into a Message step", () => {
    const items = [
      ev("model_request_completed", "turn-5", "2026-01-01T00:00:02Z", {}),
      ev("tool_call_finished", "turn-5", "2026-01-01T00:00:01Z", { name: "glob" }),
      ev("tool_call_started", "turn-5", "2026-01-01T00:00:00Z", { name: "glob" }),
    ];
    const groups = groupTimelineTurns(items);
    expect(groups[0].steps.map((s) => s.label)).toEqual(["Message", "Step 1"]);
  });
});

describe("timelineEventLane", () => {
  it("classifies lanes", () => {
    expect(timelineEventLane(ev("user_message_accepted", undefined, "t"))).toBe("input");
    expect(timelineEventLane(ev("message_injected", undefined, "t"))).toBe("input");
    expect(timelineEventLane(ev("model_request_completed", undefined, "t"))).toBe("model");
    expect(timelineEventLane(ev("assistant_message_completed", undefined, "t"))).toBe("model");
    expect(timelineEventLane(ev("tool_call_started", undefined, "t"))).toBe("tool");
    expect(timelineEventLane(ev("tool_call_finished", undefined, "t"))).toBe("tool");
    expect(timelineEventLane(ev("error_raised", undefined, "t"))).toBe("other");
  });
});

describe("pendingSendsForFeed", () => {
  const userSend: PendingSend = { id: "user:1", kind: "user", text: "hello", sender: "You" };
  const cmdSend: PendingSend = { id: "cmd:1", kind: "command", commandName: "compact" };

  it("drops queued user messages (only shown once truly sent)", () => {
    expect(pendingSendsForFeed([userSend])).toEqual([]);
    expect(pendingSendsForFeed([userSend, cmdSend])).toEqual([cmdSend]);
  });

  it("keeps command placeholders for running feedback", () => {
    expect(pendingSendsForFeed([cmdSend])).toEqual([cmdSend]);
  });

  it("returns [] for empty input", () => {
    expect(pendingSendsForFeed([])).toEqual([]);
  });
});

describe("collectToolCalls", () => {
  it("returns [] for empty input", () => {
    expect(collectToolCalls([])).toEqual([]);
  });

  it("pairs started+finished into one finished tool item keyed by id", () => {
    const items = [
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:01Z", { id: "tool-1", name: "bash", input: { command: "ls" } }),
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:02Z", { id: "tool-1", name: "bash", input: { command: "ls" } }),
    ];
    const tools = collectToolCalls(items);
    expect(tools).toHaveLength(1);
    expect(tools[0].id).toBe("tool:tool-1");
    expect(tools[0].kind).toBe("tool");
    expect(tools[0].title).toBe("bash");
    expect(tools[0].status).toBe("finished");
    expect(tools[0].turnId).toBe("turn-1");
  });

  it("leaves an unfinished tool as running", () => {
    const items = [
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:01Z", { id: "tool-2", name: "read_file", input: { path: "/a.txt" } }),
    ];
    const tools = collectToolCalls(items);
    expect(tools).toHaveLength(1);
    expect(tools[0].status).toBe("running");
  });

  it("falls back to turnId:name key when id is missing", () => {
    const items = [
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:01Z", { name: "bash" }),
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:02Z", { name: "bash" }),
    ];
    const tools = collectToolCalls(items);
    expect(tools).toHaveLength(1);
    expect(tools[0].id).toBe("tool:turn-1:bash");
    expect(tools[0].status).toBe("finished");
  });

  it("sorts by timestamp ascending", () => {
    const items = [
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:05Z", { id: "b" }),
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:01Z", { id: "a" }),
    ];
    const tools = collectToolCalls(items);
    expect(tools.map((t) => t.id)).toEqual(["tool:a", "tool:b"]);
  });

  it("re-entry: finished-only events still rebuild tool rows (pi-acp evicts started from the 1000-event window)", () => {
    // pi-acp turns emit hundreds of text/thinking deltas; the timeline is
    // capped at TIMELINE_WINDOW_LIMIT, so the earliest tool_call_started
    // events can roll out while their tool_call_finished stays. The row must
    // still be rebuilt from the finished event alone.
    const items = [
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:02Z", { id: "tool-1", name: "bash", input: { command: "npm run build" } }),
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:03Z", { id: "tool-2", name: "glob", input: {} }),
    ];
    const tools = collectToolCalls(items);
    expect(tools).toHaveLength(2);
    expect(tools[0].id).toBe("tool:tool-1");
    expect(tools[0].status).toBe("finished");
    expect(tools[0].input).toEqual({ command: "npm run build" });
    expect(tools[1].id).toBe("tool:tool-2");
    expect(tools[1].status).toBe("finished");
  });
});

describe("mergeChronologicalFeedItems", () => {
  const textItem = (id: string, timestamp: string | undefined, messageIndex: number, turnId = "") => ({
    id,
    kind: "assistant" as const,
    title: "GoDex",
    body: `text-${id}`,
    timestamp,
    messageIndex,
    turnId: turnId || undefined,
  });

  it("interleaves history and overlay by timestamp when present", () => {
    const history = [textItem("h1", "2026-01-01T00:00:01Z", 0)];
    const tools = [
      { ...textItem("t1", "2026-01-01T00:00:02Z", 5), kind: "tool" as const, title: "bash", summary: "", input: {}, status: "finished" as const, expanded: false },
    ];
    const merged = mergeChronologicalFeedItems(history, tools);
    expect(merged.map((m) => m.id)).toEqual(["h1", "t1"]);
  });

  it("keeps missing-timestamp items anchored to their message index instead of clumping at the end", () => {
    // History text has no timestamp (old message), overlay tool has a
    // timestamp. The tool must NOT be pushed after the text purely because of
    // array order: both belong to the same message index.
    const history = [textItem("h1", undefined, 2)];
    const tools = [
      { ...textItem("t1", "2026-01-01T00:00:02Z", 2), kind: "tool" as const, title: "bash", summary: "", input: {}, status: "finished" as const, expanded: false },
    ];
    const merged = mergeChronologicalFeedItems(history, tools);
    expect(merged.map((m) => m.id)).toEqual(["h1", "t1"]);
  });

  it("re-entry: thinking deltas rebuild and interleave with tools by timestamp", () => {
    // Timeline served by the backend after my fix now includes
    // assistant_thinking_delta events persisted between tool calls.
    const timeline = [
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:01Z", { id: "c1", name: "bash", input: {} }),
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:02Z", { id: "c1", name: "bash", input: {} }),
      ev("assistant_thinking_delta", "turn-1", "2026-01-01T00:00:03Z", { text: "the first result suggests" }),
      ev("tool_call_started", "turn-1", "2026-01-01T00:00:04Z", { id: "c2", name: "glob", input: {} }),
      ev("tool_call_finished", "turn-1", "2026-01-01T00:00:05Z", { id: "c2", name: "glob", input: {} }),
      ev("assistant_thinking_delta", "turn-1", "2026-01-01T00:00:06Z", { text: " I should inspect the handler" }),
    ];
    const rebuiltTools = collectToolCalls(timeline);
    const rebuiltThinking = collectThinkingDeltas(timeline);
    const merged = mergeChronologicalFeedItems([], [...rebuiltTools, ...rebuiltThinking]);
    // collectToolCalls merges started+finished into one tool item per call:
    // tool1, thinking1, tool2, thinking2 — the thinking↔tool alternation the
    // user sees live is reconstructed on re-entry.
    expect(merged.map((m) => m.kind)).toEqual(["tool", "background", "tool", "background"]);
    const thinking = merged.filter((m) => m.kind === "background");
    expect(thinking.map((m) => m.title)).toEqual(["Thinking…", "Thinking…"]);
    // The two segments are NOT merged into one bubble: a tool sits between them.
    expect(thinking[0].body).toBe("the first result suggests");
    expect(thinking[1].body).toBe("I should inspect the handler");
    // Chronological order restored: second thinking comes after the second tool.
    expect(merged[merged.length - 1].timestamp).toBe("2026-01-01T00:00:06Z");
  });
});

describe("alignAssistantTextTurnIds", () => {
  const hist = (id: string, body: string, turnId: string) => ({
    id,
    kind: "assistant" as const,
    title: "GoDex",
    body,
    turnId,
  });
  const amc = (turnId: string, text: string): SessionTimelineEntry =>
    ({ type: "assistant_message_completed", turn_id: turnId, timestamp: "2026-01-01T00:00:00Z", payload: { text } }) as SessionTimelineEntry;

  it("replaces an assistant message's synthetic msg-N turnId with the timeline's real turn id (same text)", () => {
    const history = [
      hist("message:3:assistant", "good, implementing fixes", "msg-3"),
    ];
    const timeline = [amc("turn-abc-123", "good, implementing fixes")];
    const aligned = alignAssistantTextTurnIds(history, timeline);
    expect(aligned[0].turnId).toBe("turn-abc-123");
  });

  it("leaves messages with no matching timeline event unchanged", () => {
    const history = [hist("message:1:assistant", "Let me look", "msg-1")];
    // timeline has a DIFFERENT assistant message; no text match.
    const timeline = [amc("turn-abc-123", "unrelated text")];
    const aligned = alignAssistantTextTurnIds(history, timeline);
    expect(aligned[0].turnId).toBe("msg-1");
    expect(aligned[0].body).toBe("Let me look");
  });

  it("leaves tool/todo/user items untouched and preserves order", () => {
    const history = [
      hist("message:3:assistant", "implementing", "msg-3"),
      { id: "message:2:user", kind: "user" as const, title: "You", body: "go", turnId: undefined },
    ];
    const timeline = [amc("turn-abc-123", "implementing")];
    const aligned = alignAssistantTextTurnIds(history, timeline);
    expect(aligned[0].turnId).toBe("turn-abc-123");
    expect(aligned[1].kind).toBe("user");
    expect(aligned[1].turnId).toBeUndefined();
  });
});

describe("collectThinkingDeltas", () => {
  const thinking = (turnId: string, ts: string, text: string): SessionTimelineEntry =>
    ({ type: "assistant_thinking_delta", turn_id: turnId, timestamp: ts, payload: { text } }) as SessionTimelineEntry;
  const tool = (turnId: string, ts: string, name: string): SessionTimelineEntry =>
    ({ type: "tool_call_started", turn_id: turnId, timestamp: ts, payload: { id: `c-${ts}`, name } }) as SessionTimelineEntry;

  it("returns [] for empty input", () => {
    expect(collectThinkingDeltas([])).toEqual([]);
  });

  it("merges consecutive same-turn thinking deltas into one bubble", () => {
    const items = [
      thinking("t1", "2026-01-01T00:00:01Z", "Let me think"),
      thinking("t1", "2026-01-01T00:00:02Z", " about the plan"),
    ];
    const out = collectThinkingDeltas(items);
    expect(out).toHaveLength(1);
    expect(out[0].kind).toBe("background");
    expect(out[0].title).toBe("Thinking…");
    expect(out[0].turnId).toBe("t1");
    expect(out[0].body).toBe("Let me think about the plan");
  });

  it("starts a new bubble after a tool call (tool↔thinking alternation)", () => {
    const items = [
      thinking("t1", "2026-01-01T00:00:01Z", "First thought"),
      tool("t1", "2026-01-01T00:00:02Z", "bash"),
      thinking("t1", "2026-01-01T00:00:03Z", "Second thought"),
    ];
    const out = collectThinkingDeltas(items);
    expect(out).toHaveLength(2);
    expect(out[0].body).toBe("First thought");
    expect(out[1].body).toBe("Second thought");
  });

  it("keeps different turns separate and sorts by timestamp", () => {
    const items = [
      thinking("t2", "2026-01-01T00:00:05Z", "later turn"),
      thinking("t1", "2026-01-01T00:00:01Z", "earlier turn"),
    ];
    const out = collectThinkingDeltas(items);
    expect(out.map((i) => i.turnId)).toEqual(["t1", "t2"]);
  });
});

describe("collectTextDeltas", () => {
  const text = (turnId: string, ts: string, body: string): SessionTimelineEntry =>
    ({ type: "assistant_text_delta", turn_id: turnId, timestamp: ts, payload: { text: body } }) as SessionTimelineEntry;
  const tool = (turnId: string, ts: string, name: string): SessionTimelineEntry =>
    ({ type: "tool_call_started", turn_id: turnId, timestamp: ts, payload: { id: `c-${ts}`, name } }) as SessionTimelineEntry;

  it("returns [] for empty input", () => {
    expect(collectTextDeltas([])).toEqual([]);
  });

  it("merges consecutive same-turn text deltas into one assistant segment", () => {
    const items = [
      text("t1", "2026-01-01T00:00:01Z", "收到两个问题。先诊断"),
      text("t1", "2026-01-01T00:00:02Z", " i18n 键为什么没解析"),
    ];
    const out = collectTextDeltas(items);
    expect(out).toHaveLength(1);
    expect(out[0].kind).toBe("assistant");
    expect(out[0].title).toBe("GoDex");
    expect(out[0].turnId).toBe("t1");
    expect(out[0].body).toBe("收到两个问题。先诊断 i18n 键为什么没解析");
  });

  it("starts a new segment after a tool call so process text lands before its tool", () => {
    const items = [
      text("t1", "2026-01-01T00:00:01Z", "先检查 i18n 插入位置"),
      tool("t1", "2026-01-01T00:00:02Z", "grep"),
      text("t1", "2026-01-01T00:00:03Z", "需要确认 processSummary 路径"),
      tool("t1", "2026-01-01T00:00:04Z", "python"),
      text("t1", "2026-01-01T00:00:05Z", "找到根因"),
    ];
    const out = collectTextDeltas(items);
    expect(out).toHaveLength(3);
    expect(out[0].body).toBe("先检查 i18n 插入位置");
    expect(out[1].body).toBe("需要确认 processSummary 路径");
    expect(out[2].body).toBe("找到根因");
  });

  it("keeps different turns separate and sorts by timestamp", () => {
    const items = [
      text("t2", "2026-01-01T00:00:05Z", "later turn"),
      text("t1", "2026-01-01T00:00:01Z", "earlier turn"),
    ];
    const out = collectTextDeltas(items);
    expect(out.map((i) => i.turnId)).toEqual(["t1", "t2"]);
  });
});
