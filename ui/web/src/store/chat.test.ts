import { describe, expect, it, beforeEach } from "vitest";
import {
  composeTranscriptArchives,
  groupFeedItemsIntoTurns,
  overlappingSnapshotMessageIndexes,
  snapshotToItems,
  transcriptArchiveRefs,
  useChatStore,
} from "./chat";
import type { ProtocolMessage, RuntimeEvent } from "../lib/types";

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

function toolFinished(turnId: string, id: string, name: string, timestamp: string, durationMs: number): RuntimeEvent {
  return {
    session_id: "s1",
    turn_id: turnId,
    type: "tool_call_finished",
    timestamp,
    payload: { id, name, duration_ms: durationMs, output: "ok" },
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

  it("records tool duration and surfaces loop guard recovery as a compact note", () => {
    const store = useChatStore.getState();
    store.setSession("s1", "k1");
    store.setRunningTurn("turn-3");

    store.handleEvent(toolStarted("turn-3", "call_1", "bash", "2026-08-07T01:00:03Z"));
    store.handleEvent(toolFinished("turn-3", "call_1", "bash", "2026-08-07T01:00:33Z", 30_000));
    store.handleEvent({
      session_id: "s1",
      turn_id: "turn-3",
      type: "runner_phase_changed",
      timestamp: "2026-08-07T01:00:34Z",
      payload: { phase: "recovery_attempted", message: "loop_guard_recovery: no_mutation_spiral detected" },
    } as RuntimeEvent);

    const state = useChatStore.getState();
    const tool = state.overlayItems.find((item) => item.kind === "tool");
    expect(tool?.startedAt).toBe("2026-08-07T01:00:03Z");
    expect(tool?.durationMs).toBe(30_000);
    const note = state.overlayItems.find((item) => item.kind === "background" && item.title === "Loop guard");
    expect(note?.body).toContain("loop_guard_recovery");
    expect(note?.status).toBe("recovered");
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

  it("renders compaction summaries separately from user messages and keeps the archive reference", () => {
    const store = useChatStore.getState();
    store.setSession("s-compacted", "k1");
    store.syncSnapshot(
      [
        { role: "user", content: [{ type: "text", text: "Earlier conversation" }] },
        {
          role: "user",
          content: [{ type: "text", text: "Long compaction summary" }],
          metadata: { kind: "summary", transcript: "transcript_before_compaction.json" },
        },
      ],
      false,
      "",
    );

    const items = useChatStore.getState().historyItems;
    expect(items.map((item) => item.kind)).toEqual(["user", "summary"]);
    expect(items[0].body).toBe("Earlier conversation");
    expect(items[1]).toMatchObject({
      title: "Context compacted",
      body: "Long compaction summary",
      transcriptRef: "transcript_before_compaction.json",
    });
    expect(groupFeedItemsIntoTurns(items).map((item) => item.kind)).toEqual(["user", "summary"]);
  });

  it("discovers older transcript archives across repeated compactions", () => {
    const summary = (transcript: string): ProtocolMessage => ({
      role: "user",
      content: [{ type: "text", text: "compacted" }],
      metadata: { kind: "summary", transcript },
    });

    expect(
      transcriptArchiveRefs(["t3"], {
        t3: [summary("t2")],
        t2: [summary("t1")],
        t1: [{ role: "user", content: [{ type: "text", text: "earliest message" }] }],
      }),
    ).toEqual(["t3", "t2", "t1"]);
  });

  it("stitches compaction archives by retained-tail overlap and keeps summary boundaries", () => {
    const user = (text: string) => ({ role: "user", content: [{ type: "text" as const, text }] });
    const summary = (text: string, transcript: string) => ({
      role: "user",
      content: [{ type: "text" as const, text }],
      metadata: { kind: "summary", transcript },
    });
    const pages = [
      { ref: "t1", messages: [user("m1"), user("m2")] },
      { ref: "t2", messages: [summary("s1", "t1"), user("m2"), user("m3")] },
      { ref: "t3", messages: [summary("s2", "t2"), user("m3"), user("m4")] },
    ];
    const archive = composeTranscriptArchives([...pages]);

    expect(archive.map(({ message }) => message.content[0]?.text)).toEqual(["m1", "m2", "s1", "m3", "s2", "m4"]);
    expect([...overlappingSnapshotMessageIndexes(archive, [
      summary("s3", "t3"),
      user("m3"),
      user("m4"),
      user("m5"),
    ])]).toEqual([1, 2]);

    const items = snapshotToItems(
      archive.map(({ message }) => message),
      {},
      { archiveOnly: true, idPrefix: "archive:", sourceIndexes: archive.map(({ sourceIndex }) => sourceIndex) },
    );
    expect(items.filter((item) => item.kind === "summary").map((item) => item.body)).toEqual(["s1", "s2"]);
    expect(items.every((item) => item.archiveOnly && item.messageIndex === undefined)).toBe(true);

    const expandedArchiveTool = snapshotToItems(
      [{ role: "assistant", content: [{ type: "tool_use", id: "call-1", name: "bash" }] }],
      { "archive:t1:tool:call-1": true },
      { archiveOnly: true, idPrefix: "archive:t1:" },
    );
    expect(expandedArchiveTool[0]).toMatchObject({ id: "archive:t1:tool:call-1", expanded: true, archiveOnly: true });
  });

  it("keeps archive turns separate from live history and disables historical fork indexes", () => {
    const turns = groupFeedItemsIntoTurns([
      {
        id: "archive:turn-a:text",
        kind: "assistant",
        title: "GoDex",
        body: "Archived answer",
        turnId: "turn-a",
        archiveOnly: true,
      },
      {
        id: "tool:live",
        kind: "tool",
        title: "bash",
        body: "",
        turnId: "turn-a",
      },
    ]);

    expect(turns).toHaveLength(2);
    expect(turns[0].id).not.toBe(turns[1].id);
    expect(turns[0]).toMatchObject({ archiveOnly: true, finalBody: "Archived answer" });
    expect(turns[0].forkMessageIndex).toBeUndefined();
    expect(turns[1].archiveOnly).toBeUndefined();
    expect(turns[1].segments?.map((segment) => segment.type)).toEqual(["tool"]);
  });

  it("matches transcript overlap after snapshot display strips reasoning metadata", () => {
    const archivedMessage = {
      role: "assistant",
      content: [{ type: "text", text: "Retained answer" }],
      metadata: { timestamp: "2026-09-01T12:00:00Z", reasoning_content: "private reasoning" },
    } as unknown as ProtocolMessage;
    const snapshotMessage = {
      role: "assistant",
      content: [{ type: "text", text: "Retained answer" }],
      metadata: { timestamp: "2026-09-01T12:00:00Z", reasoning_content: "" },
    } as unknown as ProtocolMessage;
    expect(
      overlappingSnapshotMessageIndexes([{ ref: "archive", sourceIndex: 3, message: archivedMessage }], [snapshotMessage]),
    ).toEqual(new Set([0]));
  });

  it("does not duplicate a successful /compact result, but still surfaces failures", () => {
    const store = useChatStore.getState();
    store.setSession("s-compact-command", "k1");
    store.addPendingSend({ id: "compact-1", kind: "command", commandName: "compact" });
    store.handleEvent({
      session_id: "s-compact-command",
      type: "command_completed",
      timestamp: "2026-08-07T01:00:00Z",
      payload: { name: "compact", output: "Long compaction summary" },
    } as RuntimeEvent);
    expect(useChatStore.getState().overlayItems.filter((item) => item.kind === "command")).toHaveLength(0);
    expect(useChatStore.getState().pendingSends).toHaveLength(0);

    store.handleEvent({
      session_id: "s-compact-command",
      type: "command_completed",
      timestamp: "2026-08-07T01:00:01Z",
      payload: { name: "compact", error: "compaction failed" },
    } as RuntimeEvent);
    expect(useChatStore.getState().overlayItems.find((item) => item.kind === "error")?.body).toBe("compaction failed");
  });

  it.each([
    { content: "exit status 1", is_error: true },
    { content: JSON.stringify({ status: "error", error: "legacy exit status 1", code: "tool_error" }) },
  ])("restores failed tool styling from persisted tool results ($is_error)", (result) => {
    const store = useChatStore.getState();
    store.setSession("s-failed-tool", "k1");
    store.syncSnapshot(
      [
        { role: "assistant", content: [{ type: "tool_use", id: "call-1", name: "bash", input: { command: "false" } }] },
        { role: "user", content: [{ type: "tool_result", tool_use_id: "call-1", ...result }] },
      ],
      false,
      "",
    );

    const tool = useChatStore.getState().historyItems.find((item) => item.kind === "tool");
    expect(tool?.status).toBe("failed");
    expect(tool?.error).toContain("exit status 1");
  });
});
