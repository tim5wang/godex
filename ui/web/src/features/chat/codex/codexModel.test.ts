import { describe, expect, it } from "vitest";
import { codexFeedItems, emptyCodexConversation, hydrateCodex, mergeCodexTurns, reduceCodexEvent } from "./codexModel";
import { codexChatRoute, godexSessionDriver, isCodexChat, isCodexChatPath } from "../sessionDriver";
import type { CodexSnapshot, CodexTurn } from "./codexApi";

const turn = (id: string, message = id): CodexTurn => ({ id, status: "completed", items: [{ type: "agentMessage", id: `${id}-answer`, text: message }] });
const snapshot = (): CodexSnapshot => ({ thread: { id: "thread", preview: "test", cwd: "/workspace", createdAt: 0, updatedAt: 0, modelProvider: "native", status: { type: "idle" } }, turns: { data: [turn("new"), turn("old")], nextCursor: "earlier" }, approvals: [], sequence: 10 });

describe("Codex native conversation model", () => {
  it("keeps the original Chat route with a node-bound source identity", () => {
    expect(codexChatRoute("thread", "node/one")).toBe("/chat/codex/thread?codex_node=node%2Fone");
    expect(isCodexChat("codex")).toBe(true);
    expect(isCodexChat("web")).toBe(false);
    expect(isCodexChatPath("/chat/codex/thread")).toBe(true);
    expect(isCodexChatPath("/chat/codex-other/thread")).toBe(false);
  });
  it("leaves Godex submissions on the existing execution path", async () => {
    const submissions: string[] = [];
    const driver = godexSessionDriver(async ({ text }) => { submissions.push(text); }, async () => undefined);
    await driver.submit({ text: "unchanged", files: [] });
    expect(submissions).toEqual(["unchanged"]);
    expect(driver.capabilities.attachments).toBe(true);
  });
  it("hydrates newest-first pages chronologically and preserves earlier backscroll on reconnect", () => {
    let state = hydrateCodex(emptyCodexConversation(), snapshot());
    expect(state.turns.map((t) => t.id)).toEqual(["old", "new"]);
    state = { ...state, turns: mergeCodexTurns(state.turns, [turn("earliest")], true), cursor: null };
    state = hydrateCodex(state, snapshot());
    expect(state.turns.map((t) => t.id)).toEqual(["earliest", "old", "new"]);
    expect(state.cursor).toBe(null);
  });
  it("backscroll does not overwrite newer live data", () => {
    expect(mergeCodexTurns([turn("a", "new live")], [turn("a", "stale")], true)[0].items[0].text).toBe("new live");
  });
  it("upserts the same native item across deltas, completion and reconnect", () => {
    let state = hydrateCodex(emptyCodexConversation(), snapshot());
    state = reduceCodexEvent(state, { method: "item/agentMessage/delta", params: { threadId: "thread", turnId: "live", itemId: "answer", delta: "hel" } });
    state = reduceCodexEvent(state, { method: "item/agentMessage/delta", params: { threadId: "thread", turnId: "live", itemId: "answer", delta: "lo" } });
    state = reduceCodexEvent(state, { method: "item/completed", params: { threadId: "thread", turnId: "live", item: { type: "agentMessage", id: "answer", text: "hello" } } });
    state = reduceCodexEvent(state, { method: "turn/completed", params: { threadId: "thread", turn: { id: "live", status: "completed", items: [] } } });
    expect(state.turns.at(-1)?.items).toHaveLength(1);
    expect(state.turns.at(-1)?.items[0].text).toBe("hello");
    expect(reduceCodexEvent(state, { method: "item/agentMessage/delta", params: { threadId: "other", turnId: "live", itemId: "answer", delta: "wrong" } })).toBe(state);
  });
  it("removes approvals resolved on another native client", () => {
    let state = hydrateCodex(emptyCodexConversation(), snapshot());
    state = reduceCodexEvent(state, { method: "codex/approval", params: { id: "epoch:7", method: "item/commandExecution/requestApproval", params: { threadId: "thread" } } });
    expect(state.approvals).toHaveLength(1);
    state = reduceCodexEvent(state, { method: "serverRequest/resolved", params: { threadId: "thread", requestId: 7 } });
    expect(state.approvals).toHaveLength(0);
  });
  it("synchronizes model settings changed in the native client, including unknown effort values", () => {
    const state = hydrateCodex(emptyCodexConversation(), snapshot());
    const event = { method: "thread/settings/updated", params: { threadId: "thread", threadSettings: { model: "native-model-b", effort: "future-native-effort" } } };
    const updated = reduceCodexEvent(state, event);
    expect(updated.thread).toMatchObject({ model: "native-model-b", reasoningEffort: "future-native-effort" });
    expect(reduceCodexEvent(updated, { ...event, params: { ...event.params, threadId: "other" } })).toBe(updated);
    expect(reduceCodexEvent(updated, { ...event, params: { threadId: "thread", threadSettings: { model: "native-model-b", effort: null } } }).thread?.reasoningEffort).toBeNull();
  });
  it("localizes feed chrome without translating native content or breaking thinking grouping", () => {
    const turns: CodexTurn[] = [{ id: "t", status: "completed", items: [{ id: "u", type: "userMessage", text: "原样保留" }, { id: "r", type: "reasoning", summary: ["Published summary"] }, { id: "compact", type: "contextCompaction" }] }];
    const en = codexFeedItems(turns, new Set(), "en");
    const zh = codexFeedItems(turns, new Set(), "zh");
    expect(en[0]).toMatchObject({ title: "User", body: "原样保留" });
    expect(zh[0]).toMatchObject({ title: "用户", body: "原样保留" });
    expect(en.find((item) => item.kind === "summary")?.title).toBe("Codex context compaction");
    expect(zh.find((item) => item.kind === "summary")?.title).toBe("Codex 上下文压缩");
    expect(zh.find((item) => item.segments)?.segments?.[0].thinking).toBe(true);
  });
  it("normalizes native running tools and names grouped replies Codex", () => {
    const feed = codexFeedItems([{ id: "live", status: "inProgress", items: [{ id: "cmd", type: "commandExecution", status: "inProgress", command: "go test" }] }], new Set());
    expect(feed[0].title).toBe("Codex");
    expect(feed[0].segments?.[0].item?.status).toBe("running");
  });
  it("uses the shared grouped feed for messages and tools, with compaction as a system card", () => {
    const native: CodexTurn[] = [{ id: "t", status: "completed", items: [
      { id: "u", type: "userMessage", content: [{ type: "text", text: "inspect" }] },
      { id: "r", type: "reasoning", summary: ["Published summary only"], content: ["not a display summary"] },
      { id: "tool", type: "commandExecution", command: "go test", status: "completed", aggregatedOutput: "PASS", durationMs: 1200 },
      { id: "a", type: "agentMessage", text: "done" },
      { id: "compact", type: "contextCompaction" },
    ] }];
    const feed = codexFeedItems(native, new Set(["codex:t:tool"]));
    expect(feed[0].kind).toBe("user");
    const grouped = feed.find((item) => item.segments);
    expect(grouped?.segments?.some((segment) => segment.item?.output === "PASS")).toBe(true);
    expect(grouped?.segments?.some((segment) => segment.thinking)).toBe(true);
    expect(JSON.stringify(feed)).not.toContain("not a display summary");
    expect(feed.find((item) => item.id === "codex:t:compact")?.kind).toBe("summary");
  });
});
