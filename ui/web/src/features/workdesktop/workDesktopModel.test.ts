import { describe, expect, it } from "vitest";
import { createDesktopDemo, recallKnowledge, splitLines, updateStream } from "./workDesktopModel";
import { workDesktopMessages } from "./workDesktopMessages";

describe("work desktop prototype model", () => {
  it("has independent page data and stable shared resource references", () => {
    const first = createDesktopDemo("zh");
    const second = createDesktopDemo("zh");
    first.streams[0].summary = "edited";
    expect(second.streams[0].summary).not.toBe("edited");
    for (const stream of first.streams) {
      for (const id of stream.resourceIds) expect(first.resources.some((resource) => resource.id === id)).toBe(true);
      for (const id of stream.sourceIds) expect(first.sources.some((source) => source.id === id)).toBe(true);
    }
    expect(first.resources.filter((resource) => resource.id === "service")).toHaveLength(1);
    expect(first.streams.filter((stream) => stream.resourceIds.includes("service"))).toHaveLength(3);
  });

  it("keeps updates and delegation drafts on the owning stream", () => {
    const data = createDesktopDemo("en");
    const changed = updateStream(data, "speech", { summary: "saved", tasks: [...data.streams[0].tasks, { id: "draft-1", title: "Draft", status: "draft", acceptance: "Pass", checkpointRevision: 3 }] });
    expect(changed.streams[0].summary).toBe("saved");
    expect(changed.streams[1]).toBe(data.streams[1]);
    expect(changed.streams[2]).toBe(data.streams[2]);
    expect(data.streams[0].tasks).toHaveLength(2);
    expect(changed.streams[0].tasks.at(-1)?.id).toBe("draft-1");
    expect(updateStream(data, "speech", { id: "other" }).streams[0].id).toBe("speech");
  });

  it("limits recall to current or directly related streams and applies literal filtering", () => {
    const data = createDesktopDemo("en");
    expect(recallKnowledge(data, "speech", "current", "").every((item) => item.contextId === "speech")).toBe(true);
    expect(recallKnowledge(data, "speech", "related", "demo-id-v7").map((item) => item.id)).toEqual(["k-wer"]);
    expect(recallKnowledge(data, "speech", "all", " API ").map((item) => item.id)).toEqual(["k-contract"]);
    expect(recallKnowledge(data, "speech", "current", "[.*]")).toEqual([]);
    data.streams.push({ ...data.streams[0], id: "private" });
    data.knowledge.push({ ...data.knowledge[0], id: "unrelated", contextId: "private" });
    expect(recallKnowledge(data, "speech", "related", "").some((item) => item.id === "unrelated")).toBe(false);
  });

  it("keeps stale production observations distinct from confirmed offline evidence", () => {
    const data = createDesktopDemo("zh");
    const stale = data.knowledge.find((item) => item.id === "k-prod")!;
    expect(stale.status).toBe("stale");
    expect(stale.scope).toContain("human-report");
    for (const item of data.knowledge) expect(data.sources.some((source) => source.id === item.sourceId)).toBe(true);
  });

  it("normalizes checkpoint lists and has matching locale structures", () => {
    expect(splitLines(" A\n \nB \r\n")).toEqual(["A", "B"]);
    expect(Object.keys(workDesktopMessages.zh).sort()).toEqual(Object.keys(workDesktopMessages.en).sort());
    expect(createDesktopDemo("en").streams.map((item) => item.id)).toEqual(createDesktopDemo("zh").streams.map((item) => item.id));
  });
});
