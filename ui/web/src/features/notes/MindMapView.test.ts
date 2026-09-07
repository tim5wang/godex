import { describe, expect, it } from "vitest";
import { buildMarkdown, buildMindData, stripInlineMarkdown } from "./MindMapView";

describe("stripInlineMarkdown", () => {
  it("strips links, bold, inline code and strikethrough", () => {
    expect(stripInlineMarkdown("**bold** and [link](https://x) and `code`")).toBe("bold and link and code");
    expect(stripInlineMarkdown("~~gone~~ _em_")).toBe("gone em");
    expect(stripInlineMarkdown("![alt](img.png)")).toBe("alt");
  });
});

describe("buildMindData (Markdown → mind map)", () => {
  it("maps headings to nested nodes under the title root", () => {
    const data = buildMindData("My Note", "# A\n## B\n### C\n");
    const root = data.nodeData;
    expect(root.topic).toBe("My Note");
    expect(root.children?.map((c) => c.topic)).toEqual(["A"]);
    expect(root.children?.[0].children?.map((c) => c.topic)).toEqual(["B"]);
    expect(root.children?.[0].children?.[0].children?.map((c) => c.topic)).toEqual(["C"]);
  });

  it("hangs list items under the nearest heading", () => {
    const data = buildMindData("T", "# A\n- x\n- y\n## B\n- z\n");
    const a = data.nodeData.children![0];
    expect(a.topic).toBe("A");
    // B 是 A 下更深的标题，与 x/y 同为 A 的子节点
    expect(a.children?.map((c) => c.topic)).toEqual(["x", "y", "B"]);
    const b = a.children?.find((c) => c.topic === "B");
    expect(b?.children?.map((c) => c.topic)).toEqual(["z"]);
  });

  it("ignores paragraphs and code blocks (not mapped)", () => {
    const data = buildMindData("T", "intro paragraph\n# A\n```js\nconst a = 1;\n```\n");
    expect(data.nodeData.children?.map((c) => c.topic)).toEqual(["A"]);
  });

  it("supports indented nested lists", () => {
    const data = buildMindData("T", "- a\n  - b\n");
    const a = data.nodeData.children![0];
    expect(a.topic).toBe("a");
    expect(a.children?.map((c) => c.topic)).toEqual(["b"]);
  });
});

describe("buildMarkdown (mind map → Markdown)", () => {
  it("round-trips heading structure with list leaves", () => {
    const md = "# A\n- x\n- y\n## B\n- z\n";
    const md2 = buildMarkdown(buildMindData("T", md).nodeData);
    expect(md2).toBe(md);
  });

  it("omits the root title from the body", () => {
    const md = buildMarkdown(buildMindData("Root Title", "## Only Child\n").nodeData);
    expect(md).toBe("## Only Child\n");
  });

  it("flattens nodes deeper than level 6 into indented lists", () => {
    const md = buildMarkdown(buildMindData("T", "# a\n## b\n### c\n#### d\n##### e\n###### f\n####### g\n").nodeData);
    expect(md).toContain("###### f\n- g");
  });
});
