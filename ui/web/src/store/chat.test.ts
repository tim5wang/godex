import { describe, expect, it, beforeEach } from "vitest";
import { useChatStore } from "./chat";
import type { RuntimeEvent } from "../lib/types";

function delta(turnId: string, text: string, timestamp: string): RuntimeEvent {
  return {
    session_id: "s1",
    turn_id: turnId,
    type: "assistant_text_delta",
    timestamp,
    payload: { text },
  } as RuntimeEvent;
}

function toolStarted(turnId: string, id: string, name: string, timestamp: string): RuntimeEvent {
  return {
    session_id: "s1",
    turn_id: turnId,
    type: "tool_call_started",
    timestamp,
    payload: { id, name, input: { command: "ls" } },
  } as RuntimeEvent;
}

describe("chat store live interleaving", () => {
  beforeEach(() => {
    useChatStore.getState().reset();
  });

  it("splits streaming assistant text into segments when tools run in between", () => {
    const store = useChatStore.getState();
    store.setSession("s1", "k1");
    store.setRunningTurn("turn-1");

    store.handleEvent(delta("turn-1", "planning…", "2026-08-07T01:00:01Z"));
    store.handleEvent(delta("turn-1", " more", "2026-08-07T01:00:02Z"));
    store.handleEvent(toolStarted("turn-1", "call_1", "bash", "2026-08-07T01:00:03Z"));
    store.handleEvent(delta("turn-1", "after tool output…", "2026-08-07T01:00:04Z"));

    const state = useChatStore.getState();
    const assistantItems = state.overlayItems.filter((item) => item.kind === "assistant");
    expect(assistantItems.length).toBe(2);
    // First segment accumulates pre-tool text; the second starts after the tool.
    expect(assistantItems[0].body).toBe("planning… more");
    expect(assistantItems[1].body).toBe("after tool output…");
    // The feed order must interleave text -> tool -> text.
    const ordered = [...state.overlayItems].sort((a, b) => (a.timestamp ?? "").localeCompare(b.timestamp ?? ""));
    expect(ordered.map((item) => item.kind)).toEqual(["assistant", "tool", "assistant"]);
  });

  it("keeps appending to one segment when no tool runs", () => {
    const store = useChatStore.getState();
    store.setSession("s1", "k1");
    store.setRunningTurn("turn-2");
    store.handleEvent(delta("turn-2", "a", "2026-08-07T01:00:01Z"));
    store.handleEvent(delta("turn-2", "b", "2026-08-07T01:00:02Z"));
    store.handleEvent(delta("turn-2", "c", "2026-08-07T01:00:03Z"));

    const assistantItems = useChatStore.getState().overlayItems.filter((item) => item.kind === "assistant");
    expect(assistantItems.length).toBe(1);
    expect(assistantItems[0].body).toBe("abc");
  });
});

describe("chat store null snapshot safety", () => {
  beforeEach(() => {
    useChatStore.getState().reset();
  });

  it("syncSnapshot tolerates a null message list (empty/new session)", () => {
    const store = useChatStore.getState();
    store.setSession("s-null", "k1");
    // Backend emits messages: null for a fresh session; the relay may too.
    store.syncSnapshot(null as never, false, "");
    expect(useChatStore.getState().historyItems).toEqual([]);
    expect(useChatStore.getState().status).not.toContain("crashed");
  });

  it("snapshotToItems skips messages with null content blocks", () => {
    const store = useChatStore.getState();
    store.setSession("s-nullcontent", "k1");
    store.syncSnapshot(
      [
        { role: "user", content: null, metadata: { text: "hello" } },
        { role: "assistant", content: null, metadata: { text: "reply" } },
      ] as never,
      false,
      "",
    );
    const items = useChatStore.getState().historyItems;
    expect(items.filter((item) => item.kind === "user" || item.kind === "assistant").length).toBe(2);
  });
});
