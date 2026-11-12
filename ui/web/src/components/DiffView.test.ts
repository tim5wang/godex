import { describe, expect, it } from "vitest";
import { parseUnifiedDiff } from "./DiffView";

describe("parseUnifiedDiff line numbers", () => {
  it("numbers pre- and post-image lines from the hunk header", () => {
    const diff = [
      "diff --git a/a.go b/a.go",
      "index 111..222 100644",
      "--- a/a.go",
      "+++ b/a.go",
      "@@ -10,3 +10,4 @@",
      " context",
      "-removed",
      "+added",
      "+added2",
      " tail",
    ].join("\n");

    const lines = parseUnifiedDiff(diff, 100);
    const context = lines.find((line) => line.text === " context");
    expect(context).toMatchObject({ kind: "context", oldLine: 10, newLine: 10 });

    const removed = lines.find((line) => line.text === "-removed");
    // A removed line only exists in the pre-image, so it carries no new-side number.
    expect(removed?.kind).toBe("del");
    expect(removed?.oldLine).toBe(11);
    expect(removed?.newLine).toBeUndefined();

    const added = lines.filter((line) => line.kind === "add");
    expect(added.map((line) => line.newLine)).toEqual([11, 12]);
    expect(added.every((line) => line.oldLine === undefined)).toBe(true);
    const tail = lines.find((line) => line.text === " tail");
    expect(tail).toMatchObject({ oldLine: 12, newLine: 13 });
  });

  it("resets the cursors at each hunk", () => {
    // A second hunk starts at an unrelated line, so continuing the first
    // hunk's counter would print wrong numbers for everything after it.
    const diff = [
      "@@ -1,2 +1,2 @@",
      "-one",
      "+one!",
      "@@ -50,2 +50,2 @@",
      " fifty",
      "-fifty-one",
      "+fifty-one!",
    ].join("\n");

    const lines = parseUnifiedDiff(diff, 100);
    const fifty = lines.find((line) => line.text === " fifty");
    expect(fifty).toMatchObject({ oldLine: 50, newLine: 50 });
    const removed = lines.find((line) => line.text === "-fifty-one");
    expect(removed).toMatchObject({ oldLine: 51 });
  });

  it("handles hunk headers without a count", () => {
    const lines = parseUnifiedDiff("@@ -5 +5 @@\n-a\n+b", 100);
    expect(lines.find((line) => line.text === "-a")).toMatchObject({ oldLine: 5 });
    expect(lines.find((line) => line.text === "+b")).toMatchObject({ newLine: 5 });
  });

  it("treats new-file and rename metadata as meta, not as content", () => {
    const diff = [
      "diff --git a/n.go b/n.go",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/n.go",
      "@@ -0,0 +1 @@",
      "+package main",
    ].join("\n");
    const lines = parseUnifiedDiff(diff, 100);
    expect(lines.find((line) => line.text === "new file mode 100644")?.kind).toBe("meta");
    expect(lines.find((line) => line.text === "+package main")).toMatchObject({ kind: "add", newLine: 1 });
  });

  it("stops at maxLines so a huge diff cannot block the UI", () => {
    const big = ["@@ -1,1000 +1,1000 @@", ...Array.from({ length: 1000 }, (_, i) => ` line ${i}`)].join("\n");
    expect(parseUnifiedDiff(big, 50)).toHaveLength(50);
  });
});
