import { describe, expect, it } from "vitest";
import type { FeedItem } from "../src/lib/types";
import { groupFeedItemsIntoTurns } from "../src/store/chat";

function makeItem(partial: Partial<FeedItem> & Pick<FeedItem, "id" | "kind">): FeedItem {
  return {
    title: partial.id,
    body: "",
    ...partial,
  } as FeedItem;
}

describe("groupFeedItemsIntoTurns", () => {
  it("merges same-turn assistant text and tool calls into one assistant item with ordered segments", () => {
    const items: FeedItem[] = [
      makeItem({ id: "user:t1", kind: "user", turnId: "t1", body: "do it", timestamp: "2026-07-31T10:00:00Z" }),
      makeItem({ id: "assistant:t1", kind: "assistant", turnId: "t1", body: "Let me check.", timestamp: "2026-07-31T10:00:01Z" }),
      makeItem({ id: "tool:1", kind: "tool", turnId: "t1", title: "bash", summary: "ls", status: "finished", timestamp: "2026-07-31T10:00:02Z" }),
      makeItem({ id: "assistant:t1#2", kind: "assistant", turnId: "t1", body: "Done listing.", timestamp: "2026-07-31T10:00:03Z" }),
    ];
    const grouped = groupFeedItemsIntoTurns(items);
    expect(grouped).toHaveLength(2);
    expect(grouped[0].id).toBe("user:t1");
    const turn = grouped[1];
    expect(turn.kind).toBe("assistant");
    expect(turn.segments?.map((s) => s.type)).toEqual(["text", "tool", "text"]);
    expect(turn.segments?.[0].text).toBe("Let me check.");
    expect(turn.segments?.[1].item?.title).toBe("bash");
    expect(turn.segments?.[2].text).toBe("Done listing.");
  });

  it("merges consecutive assistant/tool/todo items into one turn regardless of turnId", () => {
    const items: FeedItem[] = [
      makeItem({ id: "assistant:t1", kind: "assistant", turnId: "t1", body: "one", timestamp: "2026-07-31T10:00:01Z" }),
      makeItem({ id: "tool:t1", kind: "tool", turnId: "t1", title: "bash", timestamp: "2026-07-31T10:00:02Z" }),
      makeItem({ id: "assistant:t2", kind: "assistant", turnId: "t2", body: "two", timestamp: "2026-07-31T10:01:01Z" }),
    ];
    const grouped = groupFeedItemsIntoTurns(items);
    // Consecutive mergeable items form ONE assistant message (multi-message
    // history snapshots have distinct turnIds but are still a single turn).
    expect(grouped).toHaveLength(1);
    expect(grouped[0].segments?.map((s) => s.type)).toEqual(["text", "tool", "text"]);
    // finalBody = the LAST assistant text (the final result).
    expect(grouped[0].finalBody).toBe("two");
    expect(grouped[0].summary).toBe("two");
  });

  it("keeps items without turnId merging as one turn when consecutive", () => {
    const items: FeedItem[] = [
      makeItem({ id: "assistant:a", kind: "assistant", body: "hello", timestamp: "2026-07-31T10:00:01Z" }),
      makeItem({ id: "tool:x", kind: "tool", title: "bash", timestamp: "2026-07-31T10:00:02Z" }),
      makeItem({ id: "assistant:b", kind: "assistant", body: "world", timestamp: "2026-07-31T10:00:03Z" }),
    ];
    const grouped = groupFeedItemsIntoTurns(items);
    expect(grouped).toHaveLength(1);
    expect(grouped[0].segments?.map((s) => s.type)).toEqual(["text", "tool", "text"]);
    expect(grouped[0].finalBody).toBe("world");
  });

  it("keeps subagent/error/warning as standalone items even with turnId", () => {
    const items: FeedItem[] = [
      makeItem({ id: "assistant:t1", kind: "assistant", turnId: "t1", body: "spawning", timestamp: "2026-07-31T10:00:01Z" }),
      makeItem({ id: "subagent:j1", kind: "subagent", turnId: "t1", title: "sub", timestamp: "2026-07-31T10:00:02Z" }),
      makeItem({ id: "error:t1:x", kind: "error", turnId: "t1", body: "boom", timestamp: "2026-07-31T10:00:03Z" }),
    ];
    const grouped = groupFeedItemsIntoTurns(items);
    expect(grouped).toHaveLength(3);
    expect(grouped.map((i) => i.kind)).toEqual(["assistant", "subagent", "error"]);
  });

  it("merges todo updates into the turn as a todo segment", () => {
    const items: FeedItem[] = [
      makeItem({ id: "assistant:t1", kind: "assistant", turnId: "t1", body: "plan", timestamp: "2026-07-31T10:00:01Z" }),
      makeItem({ id: "todo:t1", kind: "todo", turnId: "t1", body: "- [ ] a", timestamp: "2026-07-31T10:00:02Z" }),
    ];
    const grouped = groupFeedItemsIntoTurns(items);
    expect(grouped).toHaveLength(1);
    expect(grouped[0].segments?.map((s) => s.type)).toEqual(["text", "todo"]);
  });

  it("starts a new group when a user message interrupts a turn", () => {
    const items: FeedItem[] = [
      makeItem({ id: "assistant:t1", kind: "assistant", turnId: "t1", body: "first", timestamp: "2026-07-31T10:00:01Z" }),
      makeItem({ id: "user:t2", kind: "user", turnId: "t2", body: "next", timestamp: "2026-07-31T10:00:02Z" }),
      makeItem({ id: "assistant:t1#b", kind: "assistant", turnId: "t1", body: "late", timestamp: "2026-07-31T10:00:03Z" }),
    ];
    const grouped = groupFeedItemsIntoTurns(items);
    // t1 assistant after the user message forms a NEW assistant bubble (don't reopen a closed group).
    expect(grouped).toHaveLength(3);
    expect(grouped.map((i) => i.kind)).toEqual(["assistant", "user", "assistant"]);
  });

  it("returns empty array for empty input", () => {
    expect(groupFeedItemsIntoTurns([])).toEqual([]);
  });
});
