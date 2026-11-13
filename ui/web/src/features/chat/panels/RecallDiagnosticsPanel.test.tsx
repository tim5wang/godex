import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { RecallDiagnosticsPanel } from "./RecallDiagnosticsPanel";

vi.mock("../../../i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));

describe("RecallDiagnosticsPanel", () => {
  it("labels live preview and bounded result counts", () => {
    const html = renderToStaticMarkup(<RecallDiagnosticsPanel mode="continuation" diagnostics={[{
      query: "work_context if_version", intent: "method", layer: "desktop", cache: "hit",
      candidates: 8, returned: 1, estimated_result_tokens: 20, elapsed_ms: 1.2,
      rejected: { missing_identifier_facet: 7 },
    }]} />);
    expect(html).toContain("chat.recallDiagnosticsPreview");
    expect(html).toContain("continuation");
    expect(html).toContain("desktop");
    expect(html).toContain("1/8");
  });

  it("does not fabricate diagnostics when the API has none", () => {
    expect(renderToStaticMarkup(<RecallDiagnosticsPanel diagnostics={[]} />)).toBe("");
  });
});
