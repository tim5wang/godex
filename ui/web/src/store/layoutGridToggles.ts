import type { GridRatios, LayoutState } from "./layout";

// P4 / T13 (SPEC §3.2 + M0 doc §3.6): "double-click on a Splitter
// collapses the row to 0; double-click again restores it." The
// actual mouse handling lives on antd's <Splitter> (which does not
// expose an onDoubleClick event), so we expose the *policy* here
// as a pure function and let the renderer invoke it from a
// double-click handler attached to a label badge near the
// Splitter handle.
//
// Why a pure helper + store action instead of component-local
// state? Because the collapse must be persisted (P4 / T8) and
// must react to the cross-tab `storage` event (P4 / T8) so a
// double-click in one tab collapses the row in every tab. The
// store is the single source of truth; the helper is just a
// reducer over the current state.

export type GridRow = "top" | "bottom" | "left" | "right";

/** The default split we restore when a collapsed row is opened
 *  back up. Matches the SPEC §3.2 "top ~60% / bottom ~40%"
 *  default for the 2x2 grid. */
export const DEFAULT_RESTORED_SPLIT = 0.6;

/** Half-way point used as the "is this row open" predicate.
 *  Splits >= 0.5 are considered open; < 0.5 are considered
 *  collapsed. We do not compare against an exact 0 because the
 *  user may have dragged the handle to a small non-zero value. */
export const OPEN_THRESHOLD = 0.5;

/** Returns true when the row is currently considered open.
 *  `row` is one of "top" | "bottom" | "left" | "right" — the
 *  top/bottom rows are driven by `outerSplit`; the left/right
 *  columns are driven by `innerTopSplit` (used as a stand-in
 *  for the column split in this 2x2 grid where the left column
 *  and the right column are always split). The helper is
 *  generic so the same code path can be reused for future
 *  >2x2 grids.
 *
 *  "Open" here means *not at the fully-collapsed end* (i.e.
 *  the row still holds visible content). The top/bottom rows
 *  default to 0.6, the left/right columns default to 0.32; both
 *  are considered open. The only "collapsed" state is the
 *  exact 0 produced by a double-click toggle. The
 *  OPEN_THRESHOLD 0.5 export is kept for callers that want a
 *  broader "this row is more than half the screen" predicate. */
export function isRowOpen(ratios: GridRatios, row: GridRow): boolean {
  const v = pickRatio(ratios, row);
  // "Not at the fully collapsed end" — any non-zero ratio
  // counts as open. This handles the SPEC §3.2 left/right
  // default of 0.32 (files ~32%) which is < OPEN_THRESHOLD 0.5
  // but is still the canonical open state.
  return v > 0.01;
}

/** Map a row label to the GridRatios key that controls it. */
function pickRatio(ratios: GridRatios, row: GridRow): number {
  switch (row) {
    case "top":
    case "bottom":
      return ratios.outerSplit;
    case "left":
    case "right":
      // In the 2x2 grid the inner splits share the same value.
      // The renderer drives both top-left/top-right and bottom-
      // left/bottom-right with innerTopSplit, so a single
      // toggle covers the full left/right collapse.
      return ratios.innerTopSplit ?? 0.5;
  }
}

/** Compute the next split for a row given the current state.
 *  Pure function — no side effects, no store access. Returns
 *  a new `GridRatios` object so the caller can `set` it
 *  atomically. If the row is open, collapse to 0; if it is
 *  collapsed, restore to the default. */
export function toggleRowCollapse(
  ratios: GridRatios,
  row: GridRow,
  restoredSplit: number = DEFAULT_RESTORED_SPLIT,
): GridRatios {
  const open = isRowOpen(ratios, row);
  const next = clamp01(open ? 0 : restoredSplit);
  if (row === "top" || row === "bottom") {
    return { ...ratios, outerSplit: next };
  }
  return { ...ratios, innerTopSplit: next, innerBottomSplit: next };
}

/** Apply a row toggle to a full LayoutState (used by the
 *  integration test). The action is a no-op when the resulting
 *  state would be observably identical to the current one. */
export function applyRowToggle(state: LayoutState, row: GridRow): Partial<LayoutState> {
  const nextRatios = toggleRowCollapse(state.centerGridRatios, row);
  if (
    nextRatios.outerSplit === state.centerGridRatios.outerSplit &&
    nextRatios.innerTopSplit === state.centerGridRatios.innerTopSplit &&
    nextRatios.innerBottomSplit === state.centerGridRatios.innerBottomSplit
  ) {
    return state;
  }
  return { centerGridRatios: nextRatios };
}

function clamp01(v: number): number {
  if (Number.isNaN(v)) return 0;
  if (v < 0) return 0;
  if (v > 1) return 1;
  return v;
}
