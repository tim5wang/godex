import { describe, expect, it } from "vitest";
import { buildMarkdownSanitizeSchema } from "./markdownSanitize";

describe("buildMarkdownSanitizeSchema", () => {
  const schema = buildMarkdownSanitizeSchema();

  it("keeps the default GitHub tags", () => {
    for (const tag of ["a", "code", "pre", "table", "blockquote", "div", "span"]) {
      expect(schema.tagNames).toContain(tag);
    }
  });

  it("allows the MathML tags KaTeX emits", () => {
    for (const tag of [
      "math",
      "semantics",
      "annotation",
      "mrow",
      "mi",
      "mo",
      "mn",
      "mfrac",
      "msqrt",
      "msup",
      "msub",
      "munderover",
      "mtable",
      "mtr",
      "mtd",
      "mspace",
      "mtext",
    ]) {
      expect(schema.tagNames).toContain(tag);
    }
  });

  it("allows className on all elements", () => {
    expect(schema.attributes?.["*"]).toContain("className");
  });

  it("allows inline style on KaTeX tags (span and MathML)", () => {
    expect(schema.attributes?.span).toContain("style");
    expect(schema.attributes?.math).toContain("style");
    expect(schema.attributes?.mtd).toContain("style");
  });

  it("keeps required KaTeX attributes", () => {
    expect(schema.attributes?.span).toContain("ariaHidden");
    expect(schema.attributes?.math).toContain("xmlns");
    expect(schema.attributes?.annotation).toContain("encoding");
    expect(schema.attributes?.mtd).toContain("colSpan");
    expect(schema.attributes?.mtd).toContain("rowSpan");
  });
});
