import { defaultSchema } from "rehype-sanitize";
import type { Options as SanitizeSchema } from "rehype-sanitize";

/**
 * MathML element names emitted by KaTeX (via rehype-katex). The default GitHub
 * sanitize schema allows none of them, so without extending it the accessible
 * MathML markup of every formula would be stripped.
 */
const MATHML_TAGS = [
  "math",
  "semantics",
  "annotation",
  "mrow",
  "mi",
  "mo",
  "mn",
  "mfrac",
  "msqrt",
  "mroot",
  "msup",
  "msub",
  "msubsup",
  "munder",
  "mover",
  "munderover",
  "mtable",
  "mtr",
  "mtd",
  "mspace",
  "mtext",
  "mpadded",
  "menclose",
  "mstyle",
  "mphantom",
  "mfenced",
  "merror",
  "mglyph",
] as const;

/** Attributes every KaTeX node needs: generated class names drive the CSS
 * layout and inline `style` carries strut heights / vertical alignment. */
const KATEX_ATTRS = ["className", "style"];

/**
 * Build the sanitize schema for the markdown renderer: the default GitHub
 * schema plus what KaTeX output needs to survive sanitization.
 *
 * Security notes:
 * - `className` is allowed on every element (KaTeX uses many generated classes
 *   such as `katex`, `base`, `strut`, `mord` on spans). Class names cannot
 *   execute code; the blast radius is limited to styling.
 * - Inline `style` is intentionally NOT allowed on `'*'` to keep the CSS
 *   injection surface from raw HTML small; it is only granted to the exact
 *   tags KaTeX emits (`span` + MathML).
 */
export function buildMarkdownSanitizeSchema(): SanitizeSchema {
  const attributes: SanitizeSchema["attributes"] = {
    ...defaultSchema.attributes,
    "*": [...(defaultSchema.attributes?.["*"] ?? []), "className"],
    span: [...KATEX_ATTRS, "ariaHidden"],
  };
  for (const tag of MATHML_TAGS) {
    attributes[tag] = [...KATEX_ATTRS];
  }
  attributes.math = [...KATEX_ATTRS, "xmlns"];
  attributes.annotation = [...KATEX_ATTRS, "encoding"];
  attributes.mtd = [...KATEX_ATTRS, "colSpan", "rowSpan"];

  return {
    ...defaultSchema,
    tagNames: [...(defaultSchema.tagNames ?? []), ...MATHML_TAGS],
    attributes,
  };
}
