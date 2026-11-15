/**
 * Single source of truth for GoDex UI colours.
 *
 * Before this module the same palette was copy-pasted into tsx files (charts,
 * mermaid classDefs, flow nodes, usage cards), while antd was configured with a
 * *different* primary. The visible result was two competing accents: brand teal
 * (#0f766e) for chrome and antd's old default blue (#1677ff) for everything that
 * had been written against a stock antd palette.
 *
 * Rules:
 *  - `accent*` are UI chrome (links, selection, brand marks). They must match
 *    --godex-accent.
 *  - `success` / `warning` / `danger` / `purple` are *semantic status* colours and
 *    deliberately keep the antd palette values so charts and status badges stay
 *    distinguishable from the brand accent.
 *  - These are literals, not CSS variables: mermaid template strings and recharts
 *    `fill`/`stroke` props need real values.
 */
export const godexPalette = {
  /** Brand accent. Mirrors --godex-accent in stylesFoundation.css. */
  accent: "#0f766e",
  /** Secondary accent (information/charts that need to differ from brand). */
  info: "#2563eb",
  success: "#52c41a",
  warning: "#fa8c16",
  danger: "#ff4d4f",
  purple: "#722ed1",
} as const;

/**
 * Text-level status colours for chrome (form errors, warning banners).
 *
 * Deliberately darker than the chart palette above: #fa8c16 / #ff4d4f work as chart
 * fills and badges but fail contrast as body text on a light panel. They mirror
 * --godex-warning / --godex-error, which is what antd's tokens used before this
 * refactor — pointing them at the bright chart values was a regression.
 */
export const godexStatus = {
  warning: "#b45309",
  error: "#b42318",
} as const;

/**
 * Surface tokens, duplicated from the --godex-* variables in stylesFoundation.css.
 *
 * They are repeated here because antd's ConfigProvider takes literal token values:
 * switching on `darkAlgorithm` alone makes antd render its own dark greys while our
 * CSS panels stay on the --godex-* palette, which is the split this file exists to
 * close. Keep the two lists in step —
 * ui/web/src/theme/palette.test.ts asserts they match the CSS.
 */
export const godexSurfaces = {
  light: {
    bg: "#f6f8fa",
    panel: "#ffffff",
    panelMuted: "#f3f6f7",
    border: "#d9e2e7",
    text: "#172026",
    muted: "#64747d",
  },
  dark: {
    bg: "#101518",
    panel: "#161d21",
    panelMuted: "#1e272c",
    border: "#2b3940",
    text: "#e7eef2",
    muted: "#9aa9b1",
  },
} as const;

export type GodexPalette = typeof godexPalette;
