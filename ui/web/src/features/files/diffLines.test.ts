import { describe, expect, it } from "vitest";
import { computeLineDiff } from "./diffLines";

describe("computeLineDiff", () => {
  it("returns empty for identical inputs", () => {
    const out = computeLineDiff("a\nb\nc\n", "a\nb\nc\n");
    expect(out).toBe("a\nb\nc\n");
  });

  it("returns no prefix lines when both empty", () => {
    expect(computeLineDiff("", "")).toBe("");
  });

  it("marks an added line", () => {
    const out = computeLineDiff("a\nb\n", "a\nb\nc\n");
    expect(out.split("\n")).toContain("+c");
  });

  it("marks a removed line", () => {
    const out = computeLineDiff("a\nb\nc\n", "a\nb\n");
    expect(out.split("\n")).toContain("-c");
  });

  it("marks an edited line as remove+add", () => {
    const out = computeLineDiff("a\nb\nc\n", "a\nB\nc\n");
    const lines = out.split("\n");
    expect(lines).toContain("-b");
    expect(lines).toContain("+B");
  });

  it("keeps context lines unprefixed", () => {
    const out = computeLineDiff("keep\nx\n", "keep\ny\n");
    const lines = out.split("\n");
    expect(lines).toContain(" keep");
  });

  it("handles multi-line insert in the middle", () => {
    const out = computeLineDiff("1\n2\n5\n", "1\n2\n3\n4\n5\n");
    const lines = out.split("\n");
    expect(lines).toContain("+3");
    expect(lines).toContain("+4");
    expect(lines).toContain(" 5");
  });

  it("handles full replacement (no common lines)", () => {
    const out = computeLineDiff("old1\nold2\n", "new1\n");
    const lines = out.split("\n");
    expect(lines).toContain("-old1");
    expect(lines).toContain("-old2");
    expect(lines).toContain("+new1");
  });
});

import { countUnifiedDiffStats } from "../../components/ChangesCard";

describe("countUnifiedDiffStats", () => {
  it("counts added and deleted lines from a unified diff", () => {
    const diff = [
      "diff --git a/a.ts b/a.ts",
      "index 1234567..89abcde 100644",
      "--- a/a.ts",
      "+++ b/a.ts",
      "@@ -1,3 +1,4 @@",
      " context line",
      "-removed line",
      "+added line",
      "+another added",
      " context",
    ].join("\n");
    expect(countUnifiedDiffStats(diff)).toEqual({ added: 2, deleted: 1 });
  });

  it("ignores meta lines and file headers", () => {
    const diff = [
      "diff --git a/x b/x",
      "index 111..222 100644",
      "--- a/x",
      "+++ b/x",
      "@@ -0,0 +1,2 @@",
      "+new file line 1",
      "+new file line 2",
    ].join("\n");
    expect(countUnifiedDiffStats(diff)).toEqual({ added: 2, deleted: 0 });
  });

  it("handles empty diffs", () => {
    expect(countUnifiedDiffStats("")).toEqual({ added: 0, deleted: 0 });
  });
});
