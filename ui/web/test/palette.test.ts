import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { godexPalette, godexStatus, godexSurfaces } from "../src/theme/palette";

// The palette module duplicates values that also live in stylesFoundation.css:
// antd's ConfigProvider needs literals, while the CSS needs custom properties.
// Duplication is only safe if something fails when the two drift, which is what this
// test is for — a silent divergence shows up as two different dark greys or teal
// chrome next to a blue control: exactly the split P0 set out to fix.
//
// Why this lives in test/ rather than beside src/theme/palette.ts: it reads a CSS
// file from disk, and the app tsconfig typechecks `src/` without @types/node, so a
// node:fs import there fails `pnpm typecheck`. Same reason as
// test/accountSurfacesI18n.test.ts.

const foundationCss = readFileSync(fileURLToPath(new URL("../src/stylesFoundation.css", import.meta.url)), "utf8");

/** Read one custom property from the first matching block. */
function readToken(css: string, name: string, block: "root" | "darkRoot"): string | undefined {
  const darkStart = css.indexOf("@media (prefers-color-scheme: dark)");
  const scoped = block === "root" ? css.slice(css.indexOf(":root {"), darkStart) : css.slice(darkStart);
  const match = scoped.match(new RegExp(`${name}\\s*:\\s*([^;]+);`));
  return match?.[1].trim();
}

const surfaceTokens = [
  ["--godex-bg", "bg"],
  ["--godex-panel", "panel"],
  ["--godex-panel-muted", "panelMuted"],
  ["--godex-border", "border"],
  ["--godex-text", "text"],
  ["--godex-muted", "muted"],
] as const;

describe("theme palette matches stylesFoundation.css", () => {
  it("reads the stylesheet it is asserting against", () => {
    // Guard against the regexes below silently matching nothing.
    expect(foundationCss.length).toBeGreaterThan(1000);
    expect(foundationCss).toContain("@media (prefers-color-scheme: dark)");
  });

  it.each(surfaceTokens)("light %s matches godexSurfaces.light.%s", (token, key) => {
    expect(readToken(foundationCss, token, "root")).toBe(godexSurfaces.light[key]);
  });

  it.each(surfaceTokens)("dark %s matches godexSurfaces.dark.%s", (token, key) => {
    expect(readToken(foundationCss, token, "darkRoot")).toBe(godexSurfaces.dark[key]);
  });

  it("brand accent matches --godex-accent", () => {
    expect(readToken(foundationCss, "--godex-accent", "root")).toBe(godexPalette.accent);
  });

  it("text-level status colours match --godex-warning / --godex-error", () => {
    expect(readToken(foundationCss, "--godex-warning", "root")).toBe(godexStatus.warning);
    expect(readToken(foundationCss, "--godex-error", "root")).toBe(godexStatus.error);
  });

  it("keeps status text darker than the chart palette, so errors stay readable", () => {
    // #fa8c16 / #ff4d4f are chart fills; as body text they fail contrast. Guard the
    // distinction instead of letting one constant quietly absorb the other.
    expect(godexStatus.warning).not.toBe(godexPalette.warning);
    expect(godexStatus.error).not.toBe(godexPalette.danger);
  });
});
