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
