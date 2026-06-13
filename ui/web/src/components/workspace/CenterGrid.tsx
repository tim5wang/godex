import type { ReactNode } from "react";
import { Splitter } from "antd";
import {
  DEFAULT_GRID_OCCUPANCY,
  type GridOccupancy,
  type GridPresetId,
  type PanelKey,
} from "../../store/layout";

// P2 / T6b (SPEC §3.2 + §4.1): the chat workspace renders a 2x2 grid of
// cells whose contents are determined by the layout store's
// centerGridPreset + centerGrid occupancy. The grid is purely presentational
// — it does not own state and does not know about specific panels. Callers
// pass a `renderSlot` function that maps a `PanelKey` (or null) to a
// ReactNode. The 5 presets map onto the same 4 cells; the "topFull" and
// "bottomFull" shapes are encoded by putting the same panel in both cells
// of a row (the inner Splitter collapse-to-0 is what visually merges them).
//
// The grid is intentionally read-only here: actions like movePanelToGrid /
// setGridPreset / setGridRatio are dispatched by the layout store, not by
// this component. That keeps the renderer easy to unit-test (no
// dispatching, no store reading required to assert shape) and easy to swap
// for a different splitter implementation later.
//
// Two layers of <Splitter>:
//   * outer  — vertical,  separates the top row from the bottom row
//              (driven by `outerSplit`, default 0.6 = top 60 / bottom 40).
//   * inner  — horizontal, splits the top row (topLeft / topRight) and
//              splits the bottom row (bottomLeft / bottomRight) when the
//              corresponding cells hold different panels.
//
// When the outer split is exactly 0 or 1, the corresponding row collapses
// (SPEC §3.2 double-click collapse). The renderSlot callback still gets
// called for both rows so the dropped cell can still receive its panel
// when the row reopens (the Splitter keeps the cell mounted, just at 0
// height).

export type CenterGridRenderSlot = (panel: PanelKey | null) => ReactNode;

export type CenterGridProps = {
  preset: GridPresetId;
  occupancy?: GridOccupancy;
  outerSplit?: number;
  innerTopSplit?: number;
  innerBottomSplit?: number;
  renderSlot: CenterGridRenderSlot;
};

/** Resolve the effective occupancy for a preset. Falls back to the
 *  store-level catalogue so callers do not have to import the store. */
function resolveOccupancy(preset: GridPresetId, occupancy?: GridOccupancy): GridOccupancy {
  if (occupancy) return occupancy;
  return DEFAULT_GRID_OCCUPANCY[preset];
}

export function CenterGrid(props: CenterGridProps) {
  const occ = resolveOccupancy(props.preset, props.occupancy);
  const outer = clamp01(props.outerSplit ?? 0.6);
  const innerTop = clamp01(props.innerTopSplit ?? 0.5);
  const innerBottom = clamp01(props.innerBottomSplit ?? 0.5);

  return (
    <div
      data-testid="center-grid"
      data-preset={props.preset}
      className="center-grid"
      style={{ height: "100%", minHeight: 0 }}
    >
      <Splitter
        layout="vertical"
        style={{ height: "100%" }}
        data-testid="center-grid-outer-splitter"
      >
        <Splitter.Panel
          defaultSize={`${Math.round(outer * 100)}%`}
          min="0%"
          data-testid="center-grid-top-row"
        >
          {renderTopRow(occ.topLeft, occ.topRight, innerTop, props.renderSlot)}
        </Splitter.Panel>
        <Splitter.Panel
          min="0%"
          data-testid="center-grid-bottom-row"
        >
          {renderBottomRow(occ.bottomLeft, occ.bottomRight, innerBottom, props.renderSlot)}
        </Splitter.Panel>
      </Splitter>
    </div>
  );
}

function renderTopRow(
  topLeft: PanelKey | null,
  topRight: PanelKey | null,
  innerTopSplit: number,
  renderSlot: CenterGridRenderSlot,
): ReactNode {
  // If both cells hold the same panel (e.g. "topChat_bottomTerminal" with
  // topLeft=topRight=chat) we render a single full-width cell — the
  // visual effect of a full-width top row.
  if (topLeft && topLeft === topRight) {
    return (
      <div data-testid="center-grid-top-full" data-panel={topLeft} style={{ height: "100%" }}>
        {renderSlot(topLeft)}
      </div>
    );
  }
  return (
    <Splitter
      layout="horizontal"
      style={{ height: "100%" }}
      data-testid="center-grid-top-splitter"
    >
      <Splitter.Panel
        defaultSize={`${Math.round(innerTopSplit * 100)}%`}
        min="0%"
        data-testid="center-grid-top-left"
        data-panel={topLeft ?? ""}
      >
        {renderSlot(topLeft)}
      </Splitter.Panel>
      <Splitter.Panel
        min="0%"
        data-testid="center-grid-top-right"
        data-panel={topRight ?? ""}
      >
        {renderSlot(topRight)}
      </Splitter.Panel>
    </Splitter>
  );
}

function renderBottomRow(
  bottomLeft: PanelKey | null,
  bottomRight: PanelKey | null,
  innerBottomSplit: number,
  renderSlot: CenterGridRenderSlot,
): ReactNode {
  if (bottomLeft && bottomLeft === bottomRight) {
    return (
      <div data-testid="center-grid-bottom-full" data-panel={bottomLeft} style={{ height: "100%" }}>
        {renderSlot(bottomLeft)}
      </div>
    );
  }
  return (
    <Splitter
      layout="horizontal"
      style={{ height: "100%" }}
      data-testid="center-grid-bottom-splitter"
    >
      <Splitter.Panel
        defaultSize={`${Math.round(innerBottomSplit * 100)}%`}
        min="0%"
        data-testid="center-grid-bottom-left"
        data-panel={bottomLeft ?? ""}
      >
        {renderSlot(bottomLeft)}
      </Splitter.Panel>
      <Splitter.Panel
        min="0%"
        data-testid="center-grid-bottom-right"
        data-panel={bottomRight ?? ""}
      >
        {renderSlot(bottomRight)}
      </Splitter.Panel>
    </Splitter>
  );
}

function clamp01(v: number): number {
  if (Number.isNaN(v)) return 0;
  if (v < 0) return 0;
  if (v > 1) return 1;
  return v;
}

// Pure helper (exported for tests) — encodes the shape of a preset into a
// label of which cells are merged into a "full" row. The render layer
// uses this so test assertions can describe the rendered DOM without
// walking the tree.
export type PresetShape =
  | "topFull-bottomFull"
  | "topSplit-bottomFull"
  | "topFull-bottomSplit"
  | "topSplit-bottomSplit"
  | "topSplit-bottomSplit-symmetric";

export function presetShape(
  occ: GridOccupancy,
): PresetShape {
  const topFull = occ.topLeft !== null && occ.topLeft === occ.topRight;
  const bottomFull = occ.bottomLeft !== null && occ.bottomLeft === occ.bottomRight;
  if (topFull && bottomFull) return "topFull-bottomFull";
  if (topFull && !bottomFull) return "topFull-bottomSplit";
  if (!topFull && bottomFull) return "topSplit-bottomFull";
  return "topSplit-bottomSplit";
}
