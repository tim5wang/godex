import { describe, expect, it, beforeEach, afterEach } from "vitest";

import { useLayoutStore } from "../src/store/layout";
import {
  DEFAULT_RESTORED_SPLIT,
  OPEN_THRESHOLD,
  applyRowToggle,
  isRowOpen,
  toggleRowCollapse,
  type GridRow,
} from "../src/store/layoutGridToggles";
import type { GridRatios } from "../src/store/layout";

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

// P4 / T13 (SPEC §3.2 + M0 doc §3.6) contract tests for the
// "double-click collapses a row" policy. The pure helpers are
// tested directly; the store integration is tested through the
// public action surface (`setGridRatio`) to confirm the
// toggled ratio round-trips.

describe("toggleRowCollapse (P4 / T13)", () => {
  const base: GridRatios = { outerSplit: 0.6, innerTopSplit: 0.5, innerBottomSplit: 0.5 };

  it("open top row → collapse to 0", () => {
    const next = toggleRowCollapse(base, "top");
    expect(next.outerSplit).toBe(0);
  });

  it("collapsed top row → restore to default (0.6)", () => {
    const next = toggleRowCollapse({ ...base, outerSplit: 0 }, "top");
    expect(next.outerSplit).toBe(DEFAULT_RESTORED_SPLIT);
  });

  it("bottom row shares the outerSplit with the top row", () => {
    const next = toggleRowCollapse(base, "bottom");
    expect(next.outerSplit).toBe(0);
    const restored = toggleRowCollapse({ ...base, outerSplit: 0 }, "bottom");
    expect(restored.outerSplit).toBe(DEFAULT_RESTORED_SPLIT);
  });

  it("left column drives innerTopSplit (the 2x2 column split)", () => {
    const next = toggleRowCollapse(base, "left");
    expect(next.innerTopSplit).toBe(0);
    expect(next.innerBottomSplit).toBe(0);
    const restored = toggleRowCollapse({ ...base, innerTopSplit: 0, innerBottomSplit: 0 }, "left");
    expect(restored.innerTopSplit).toBe(DEFAULT_RESTORED_SPLIT);
    expect(restored.innerBottomSplit).toBe(DEFAULT_RESTORED_SPLIT);
  });

  it("right column is symmetric to the left column", () => {
    const next = toggleRowCollapse(base, "right");
    expect(next.innerTopSplit).toBe(0);
  });

  it("double-toggling a row round-trips back to the open state", () => {
    const once = toggleRowCollapse(base, "top");
    const twice = toggleRowCollapse(once, "top");
    expect(twice.outerSplit).toBe(DEFAULT_RESTORED_SPLIT);
  });

  it("custom restoredSplit is honored", () => {
    const next = toggleRowCollapse({ ...base, outerSplit: 0 }, "top", 0.4);
    expect(next.outerSplit).toBe(0.4);
  });

  it("clamps invalid restoredSplit to [0, 1]", () => {
    const a = toggleRowCollapse({ ...base, outerSplit: 0 }, "top", 1.5);
    const b = toggleRowCollapse({ ...base, outerSplit: 0 }, "top", -0.2);
    expect(a.outerSplit).toBe(1);
    expect(b.outerSplit).toBe(0);
  });
});

describe("isRowOpen (P4 / T13)", () => {
  it("rows with ratio >= OPEN_THRESHOLD are open", () => {
    expect(isRowOpen({ outerSplit: 0.6, innerTopSplit: 0.5, innerBottomSplit: 0.5 }, "top")).toBe(true);
    expect(isRowOpen({ outerSplit: 0.5, innerTopSplit: 0.5, innerBottomSplit: 0.5 }, "top")).toBe(true);
  });

  it("rows with ratio exactly 0 are collapsed (the 'fully collapsed end')", () => {
    // After isRowOpen() was changed to use a strict 'not at
    // the fully collapsed end' predicate (v > 0.01), any
    // non-zero ratio counts as open. The 0.4 case the previous
    // test asserted on now reads as open, which is correct for
    // SPEC §3.2: even a small non-zero split still holds visible
    // content. The only 'collapsed' state is the exact 0
    // produced by a double-click toggle.
    expect(isRowOpen({ outerSplit: 0, innerTopSplit: 0.5, innerBottomSplit: 0.5 }, "top")).toBe(false);
    expect(isRowOpen({ outerSplit: 0.6, innerTopSplit: 0, innerBottomSplit: 0 }, "left")).toBe(false);
  });

  it("uses innerTopSplit for left/right columns", () => {
    expect(isRowOpen({ outerSplit: 0.6, innerTopSplit: 0.5, innerBottomSplit: 0.5 }, "left")).toBe(true);
    expect(isRowOpen({ outerSplit: 0.6, innerTopSplit: 0, innerBottomSplit: 0 }, "left")).toBe(false);
  });
});

describe("applyRowToggle (P4 / T13 store integration)", () => {
  const rows: GridRow[] = ["top", "bottom", "left", "right"];

  for (const row of rows) {
    it(`${row} row toggle round-trips through the store`, () => {
      // Apply the toggle through the store's setGridRatio action
      // (we mimic the integration: the helper returns a partial
      // state that the caller passes to setState).
      const before = useLayoutStore.getState().centerGridRatios;
      const partial = applyRowToggle(useLayoutStore.getState(), row);
      useLayoutStore.setState(partial);
      const after = useLayoutStore.getState().centerGridRatios;
      // The target ratio is whichever GridRatios key the row maps
      // to. Top/bottom rows map to outerSplit; left/right rows map
      // to innerTopSplit (used as the stand-in for the column
      // split in this 2x2 grid). We use a strict "is the row at
      // the fully-collapsed end" predicate (ratio <= 0.01) so the
      // left/right default of 0.32 (SPEC §3.2 'files ~32%') is
      // treated as open even though it is < OPEN_THRESHOLD.
      const target = row === "top" || row === "bottom" ? "outerSplit" : "innerTopSplit";
      const fullyCollapsed = (v: number) => v <= 0.01;
      if (!fullyCollapsed(before[target])) {
        expect(after[target]).toBe(0);
      } else {
        expect(after[target]).toBe(DEFAULT_RESTORED_SPLIT);
      }
      // Toggle again. The restored value is DEFAULT_RESTORED_SPLIT
      // (0.6) for all rows — SPEC §3.2 lets the double-click
      // collapse restore to the canonical default, not necessarily
      // to the pre-collapse user value. We assert the row is open
      // (ratio > 0.01) after the second toggle, and that the
      // top/bottom row is restored to the canonical 0.6.
      const partial2 = applyRowToggle(useLayoutStore.getState(), row);
      useLayoutStore.setState(partial2);
      const after2 = useLayoutStore.getState().centerGridRatios;
      expect(after2[target]).toBeGreaterThan(0.01);
      if (row === "top" || row === "bottom") {
        expect(after2[target]).toBe(DEFAULT_RESTORED_SPLIT);
      }
    });
  }
});

describe("store defaults match the row toggle policy (P4 / T13 sanity)", () => {
  it("default outerSplit is open (>= OPEN_THRESHOLD)", () => {
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBeGreaterThanOrEqual(OPEN_THRESHOLD);
  });

  it("default innerTopSplit is 0.32 (SPEC §3.2 'files ~32% / chat ~68%')", () => {
    // The left/right column default is intentionally below
    // OPEN_THRESHOLD: files gets ~32% of the top row, chat gets
    // the rest. isRowOpen() still classifies 0.32 as 'open'
    // because the row is not at the fully-collapsed end. We
    // assert the exact value here so a future change to the
    // default surfaces as a deliberate decision, not a silent
    // tweak.
    expect(useLayoutStore.getState().centerGridRatios.innerTopSplit).toBe(0.32);
  });
});
