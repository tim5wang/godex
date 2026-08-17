import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MarkdownRenderer } from "./MarkdownRenderer";

function render(content: string): string {
  return renderToStaticMarkup(<MarkdownRenderer content={content} />);
}

describe("MarkdownRenderer math", () => {
  it("renders inline math $...$ with KaTeX", () => {
    const html = render("Einstein's $E=mc^2$ is famous.");
    expect(html).toContain("katex");
    expect(html).toContain('class="katex"');
  });

  it("renders display math $$...$$", () => {
    const html = render("$$\n\\sum_{i=1}^{n} i = \\frac{n(n+1)}{2}\n$$");
    expect(html).toContain("katex-display");
    expect(html).toContain('class="katex"');
  });

  it("keeps KaTeX MathML output (sanitize schema allows it)", () => {
    const html = render("$x^2$");
    expect(html).toContain("<math");
    expect(html).toContain("<mrow");
  });

  it("keeps KaTeX inline styles (strut heights)", () => {
    const html = render("$x_i$");
    expect(html).toContain('style="height:');
    expect(html).toContain("vertical-align");
  });

  it("does not break regular markdown", () => {
    const html = render("# Title\n\n**bold** and `code`.");
    expect(html).toContain("<h1>");
    expect(html).toContain("<strong>");
    expect(html).toContain("<code>");
  });
});
