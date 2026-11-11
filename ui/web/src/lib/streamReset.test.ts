import { describe, expect, it } from "vitest";
import type { FeedItem, RuntimeEvent, SessionTimelineEntry } from "./types";
import { discardFailedStreamDeltas, resetStreamFeed } from "./streamReset";
import { useChatStore } from "../store/chat";
import { collectTextDeltas, collectThinkingDeltas } from "./timelineFeedUtils";

describe("failed streaming attempts", () => {
  it("orders reset markers after failed deltas within the same millisecond", () => {
    const entries: SessionTimelineEntry[] = [
      { type: "assistant_stream_reset", turn_id: "t", timestamp: "2026-11-11T01:00:00.1234569Z", payload: { text: "failed" } },
      { type: "assistant_text_delta", turn_id: "t", timestamp: "2026-11-11T01:00:00.1234561Z", payload: { text: "failed" } },
    ];
    expect(discardFailedStreamDeltas(entries)).toEqual([]);
  });
  it("removes a failed Unicode suffix while retaining a committed conclusion", () => {
    const items: FeedItem[] = [{ id: "a", kind: "assistant", title: "GoDex", body: "已验证。失败😀", turnId: "t" }];
    expect(resetStreamFeed(items, "t", "失败😀", "")[0].body).toBe("已验证。");
    expect(items[0].body).toBe("已验证。失败😀");
  });

  it("replaces failed live output and prevents it reappearing on reload", () => {
    const event = (type: RuntimeEvent["type"], time: number, payload: unknown): RuntimeEvent => ({ session_id: "s", turn_id: "t", type, timestamp: `2026-11-11T01:00:0${time}Z`, payload });
    const entries = [
      event("assistant_text_delta", 1, { text: "verified stage" }),
      event("assistant_message_completed", 2, { text: "verified stage" }),
      event("tool_call_started", 3, { id: "tool", name: "read_file" }),
      event("assistant_thinking_delta", 4, { text: "…failed reasoning" }),
      event("assistant_text_delta", 5, { text: "failed answer" }),
      event("assistant_stream_reset", 6, { text: "failed answer", thinking: "…failed reasoning" }),
      event("assistant_text_delta", 7, { text: "final answer" }),
    ];
    useChatStore.getState().reset();
    useChatStore.getState().setSession("s", "s");
    useChatStore.getState().setRunningTurn("t");
    for (const entry of entries) useChatStore.getState().handleEvent(entry);
    const bodies = useChatStore.getState().overlayItems.map((item) => item.body).join("|");
    expect(bodies).toContain("verified stage");
    expect(bodies).toContain("final answer");
    expect(bodies).not.toContain("failed");
    const persisted = entries as SessionTimelineEntry[];
    expect(collectTextDeltas(persisted).map((item) => item.body)).toEqual(["verified stage", "final answer"]);
    expect(collectThinkingDeltas(persisted)).toEqual([]);
    expect(discardFailedStreamDeltas(persisted).some((entry) => (entry.payload as { text?: string })?.text === "failed answer")).toBe(false);
  });
});
