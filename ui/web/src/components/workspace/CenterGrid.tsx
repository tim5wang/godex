import type { ReactNode } from "react";
import { Button, Dropdown, Splitter, type MenuProps } from "antd";
import { MoreOutlined } from "@ant-design/icons";
import {
  DEFAULT_GRID_OCCUPANCY,
  DEFAULT_GRID_RATIOS,
  type GridOccupancy,
  type GridPresetId,
  type GridRatios,
  type GridSlot,
  type PanelKey,
} from "../../store/layout";

// P2 / T6b (SPEC §3.2 + §4.1) — v2.0 upgrade (M1+ candidate C):
// CenterGrid now supports both 2×2 (legacy 5 presets) and 3×3
// (3 new presets: grid3x3_*). The grid is purely presentational —
// it does not own state and does not know about specific panels.
// Callers pass a `renderSlot` function that maps a `PanelKey` (or
// null) to a ReactNode.
//
// When `occ.rows === 3`, the grid renders 3 vertical rows each
// with 3 horizontal columns using nested Splitter components.
// Full-row spanning is optimized: if all 3 cells in a row hold
// the same panel, they collapse into a single full-width cell.

export type CenterGridRenderSlot = (panel: PanelKey | null) => ReactNode;
export type CenterGridCellSlot = Exclude<GridSlot, "topFull" | "bottomFull">;
export type CenterGridPanelMoveAction = "move" | "swap";

export type PanelMoveMenuItem = {
  slot: CenterGridCellSlot;
  action: CenterGridPanelMoveAction;
  label: string;
};

export type CenterGridProps = {
  preset: GridPresetId;
  occupancy?: GridOccupancy;
  outerSplit?: number;
  innerTopSplit?: number;
  innerBottomSplit?: number;
  /** 3×3 ratios (row/col splits) */
  row0Split?: number;
  row1Split?: number;
  col0Split?: number;
  col1Split?: number;
  renderSlot: CenterGridRenderSlot;
  onPanelMove?: (panel: PanelKey, from: CenterGridCellSlot, to: CenterGridCellSlot, action: CenterGridPanelMoveAction) => void;
};

function is3x3(occ: GridOccupancy): boolean {
  return occ.rows === 3 && occ.cols === 3;
}

function resolveOccupancy(preset: GridPresetId, occupancy?: GridOccupancy): GridOccupancy {
  if (occupancy) return occupancy;
  return DEFAULT_GRID_OCCUPANCY[preset];
}

function resolveRatios(props: CenterGridProps): GridRatios {
  return {
    outerSplit: props.outerSplit ?? DEFAULT_GRID_RATIOS.outerSplit,
    innerTopSplit: props.innerTopSplit ?? DEFAULT_GRID_RATIOS.innerTopSplit,
    innerBottomSplit: props.innerBottomSplit ?? DEFAULT_GRID_RATIOS.innerBottomSplit,
    row0Split: props.row0Split ?? DEFAULT_GRID_RATIOS.row0Split,
    row1Split: props.row1Split ?? DEFAULT_GRID_RATIOS.row1Split,
    col0Split: props.col0Split ?? DEFAULT_GRID_RATIOS.col0Split,
    col1Split: props.col1Split ?? DEFAULT_GRID_RATIOS.col1Split,
  };
}

export function CenterGrid(props: CenterGridProps) {
  const occ = resolveOccupancy(props.preset, props.occupancy);
  const ratios = resolveRatios(props);

  if (is3x3(occ)) {
    return <Grid3x3 occ={occ} ratios={ratios} renderSlot={props.renderSlot} preset={props.preset} onPanelMove={props.onPanelMove} />;
  }
  return <Grid2x2 occ={occ} ratios={ratios} renderSlot={props.renderSlot} preset={props.preset} onPanelMove={props.onPanelMove} />;
}

// ---- 2×2 grid (legacy) ----

function Grid2x2({
  occ, ratios, renderSlot, preset, onPanelMove,
}: {
  occ: GridOccupancy;
  ratios: GridRatios;
  renderSlot: CenterGridRenderSlot;
  preset: GridPresetId;
  onPanelMove?: CenterGridProps["onPanelMove"];
}) {
  const outer = clamp01(ratios.outerSplit);
  const innerTop = clamp01(ratios.innerTopSplit ?? 0.5);
  const innerBottom = clamp01(ratios.innerBottomSplit ?? 0.5);

  return (
    <div
      data-testid="center-grid"
      data-preset={preset}
      data-rows="2"
      className="center-grid"
      style={{ height: "100%", minHeight: 0 }}
    >
      <Splitter layout="vertical" style={{ height: "100%" }}>
        <Splitter.Panel
          defaultSize={`${Math.round(outer * 100)}%`}
          min="0%"
          data-testid="center-grid-top-row"
        >
          {render2x2Row(occ, occ.topLeft, occ.topRight, innerTop, renderSlot, "top", onPanelMove)}
        </Splitter.Panel>
        <Splitter.Panel min="0%" data-testid="center-grid-bottom-row">
          {render2x2Row(occ, occ.bottomLeft, occ.bottomRight, innerBottom, renderSlot, "bottom", onPanelMove)}
        </Splitter.Panel>
      </Splitter>
    </div>
  );
}

function render2x2Row(
  occ: GridOccupancy,
  left: PanelKey | null,
  right: PanelKey | null,
  split: number,
  renderSlot: CenterGridRenderSlot,
  row: "top" | "bottom",
  onPanelMove?: CenterGridProps["onPanelMove"],
): ReactNode {
  if (left && left === right) {
    const testId = row === "top" ? "center-grid-top-full" : "center-grid-bottom-full";
    const slot = row === "top" ? "topLeft" : "bottomLeft";
    return (
      <div data-testid={testId} data-panel={left} style={{ height: "100%" }}>
        <SlotFrame panel={left} slot={slot} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(left)}
        </SlotFrame>
      </div>
    );
  }
  const testId = row === "top" ? "center-grid-top-splitter" : "center-grid-bottom-splitter";
  const leftTestId = row === "top" ? "center-grid-top-left" : "center-grid-bottom-left";
  const rightTestId = row === "top" ? "center-grid-top-right" : "center-grid-bottom-right";
  return (
    <Splitter layout="horizontal" style={{ height: "100%" }} data-testid={testId}>
      <Splitter.Panel
        defaultSize={`${Math.round(split * 100)}%`}
        min="0%"
        data-testid={leftTestId}
        data-panel={left ?? ""}
      >
        <SlotFrame panel={left} slot={row === "top" ? "topLeft" : "bottomLeft"} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(left)}
        </SlotFrame>
      </Splitter.Panel>
      <Splitter.Panel min="0%" data-testid={rightTestId} data-panel={right ?? ""}>
        <SlotFrame panel={right} slot={row === "top" ? "topRight" : "bottomRight"} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(right)}
        </SlotFrame>
      </Splitter.Panel>
    </Splitter>
  );
}

// ---- 3×3 grid (v2.0) ----

function Grid3x3({
  occ, ratios, renderSlot, preset, onPanelMove,
}: {
  occ: GridOccupancy;
  ratios: GridRatios;
  renderSlot: CenterGridRenderSlot;
  preset: GridPresetId;
  onPanelMove?: CenterGridProps["onPanelMove"];
}) {
  const row0 = clamp01(ratios.row0Split ?? 0.33);
  const row1 = clamp01(ratios.row1Split ?? 0.34);
  // row2 gets the remainder (1 - row0 - row1)
  const row2Percent = Math.max(0, Math.round((1 - row0 - row1) * 100));
  const col0 = clamp01(ratios.col0Split ?? 0.33);
  const col1 = clamp01(ratios.col1Split ?? 0.34);
  const col2Percent = Math.max(0, Math.round((1 - col0 - col1) * 100));

  const rows: Array<[PanelKey | null, PanelKey | null, PanelKey | null]> = [
    [occ.r0c0 ?? null, occ.r0c1 ?? null, occ.r0c2 ?? null],
    [occ.r1c0 ?? null, occ.r1c1 ?? null, occ.r1c2 ?? null],
    [occ.r2c0 ?? null, occ.r2c1 ?? null, occ.r2c2 ?? null],
  ];

  return (
    <div
      data-testid="center-grid"
      data-preset={preset}
      data-rows="3"
      className="center-grid"
      style={{ height: "100%", minHeight: 0 }}
    >
      <Splitter layout="vertical" style={{ height: "100%" }}>
        <Splitter.Panel
          defaultSize={`${Math.round(row0 * 100)}%`}
          min="0%"
          data-testid="center-grid-row-0"
        >
          {render3x3Row(occ, rows[0], col0, col1, col2Percent, renderSlot, 0, onPanelMove)}
        </Splitter.Panel>
        <Splitter.Panel
          defaultSize={`${Math.round(row1 * 100)}%`}
          min="0%"
          data-testid="center-grid-row-1"
        >
          {render3x3Row(occ, rows[1], col0, col1, col2Percent, renderSlot, 1, onPanelMove)}
        </Splitter.Panel>
        <Splitter.Panel
          defaultSize={`${row2Percent}%`}
          min="0%"
          data-testid="center-grid-row-2"
        >
          {render3x3Row(occ, rows[2], col0, col1, col2Percent, renderSlot, 2, onPanelMove)}
        </Splitter.Panel>
      </Splitter>
    </div>
  );
}

function render3x3Row(
  occ: GridOccupancy,
  cells: [PanelKey | null, PanelKey | null, PanelKey | null],
  col0: number,
  col1: number,
  col2Percent: number,
  renderSlot: CenterGridRenderSlot,
  rowIdx: number,
  onPanelMove?: CenterGridProps["onPanelMove"],
): ReactNode {
  // Full-row span: all 3 cells are the same panel (non-null).
  if (cells[0] && cells[0] === cells[1] && cells[1] === cells[2]) {
    return (
      <div
        data-testid={`center-grid-row-${rowIdx}-full`}
        data-panel={cells[0]}
        style={{ height: "100%" }}
      >
        <SlotFrame panel={cells[0]} slot={`r${rowIdx}c0` as CenterGridCellSlot} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(cells[0])}
        </SlotFrame>
      </div>
    );
  }
  return (
    <Splitter layout="horizontal" style={{ height: "100%" }} data-testid={`center-grid-row-${rowIdx}-splitter`}>
      <Splitter.Panel
        defaultSize={`${Math.round(col0 * 100)}%`}
        min="0%"
        data-testid={`center-grid-r${rowIdx}c0`}
        data-panel={cells[0] ?? ""}
      >
        <SlotFrame panel={cells[0]} slot={`r${rowIdx}c0` as CenterGridCellSlot} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(cells[0])}
        </SlotFrame>
      </Splitter.Panel>
      <Splitter.Panel
        defaultSize={`${Math.round(col1 * 100)}%`}
        min="0%"
        data-testid={`center-grid-r${rowIdx}c1`}
        data-panel={cells[1] ?? ""}
      >
        <SlotFrame panel={cells[1]} slot={`r${rowIdx}c1` as CenterGridCellSlot} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(cells[1])}
        </SlotFrame>
      </Splitter.Panel>
      <Splitter.Panel
        defaultSize={`${col2Percent}%`}
        min="0%"
        data-testid={`center-grid-r${rowIdx}c2`}
        data-panel={cells[2] ?? ""}
      >
        <SlotFrame panel={cells[2]} slot={`r${rowIdx}c2` as CenterGridCellSlot} occ={occ} onPanelMove={onPanelMove}>
          {renderSlot(cells[2])}
        </SlotFrame>
      </Splitter.Panel>
    </Splitter>
  );
}

function SlotFrame({
  panel,
  slot,
  occ,
  onPanelMove,
  children,
}: {
  panel: PanelKey | null;
  slot: CenterGridCellSlot;
  occ: GridOccupancy;
  onPanelMove?: CenterGridProps["onPanelMove"];
  children: ReactNode;
}) {
  if (!panel) {
    return <>{children}</>;
  }
  const menuItems = buildPanelMoveMenuItems(occ, panel, slot);
  const menu: MenuProps = {
    items: menuItems.map((item) => ({
      key: item.slot,
      label: item.label,
      onClick: () => onPanelMove?.(panel, slot, item.slot, item.action),
    })),
  };
  return (
    <section className="center-grid-panel" data-testid={`center-grid-panel-${slot}`} data-panel={panel}>
      <div className="center-grid-panel-titlebar">
        <span className="center-grid-panel-title">{panelLabel(panel)}</span>
        {onPanelMove && menuItems.length ? (
          <Dropdown menu={menu} trigger={["click"]}>
            <Button
              type="text"
              size="small"
              icon={<MoreOutlined />}
              aria-label={`Move ${panelLabel(panel)} panel`}
              data-testid={`center-grid-panel-menu-${slot}`}
            />
          </Dropdown>
        ) : null}
      </div>
      <div className="center-grid-panel-body">{children}</div>
    </section>
  );
}

// ---- helpers ----

function clamp01(v: number): number {
  if (Number.isNaN(v)) return 0;
  if (v < 0) return 0;
  if (v > 1) return 1;
  return v;
}

// Pure helper (exported for tests) — encodes the shape of a preset.
export type PresetShape =
  | "topFull-bottomFull"
  | "topSplit-bottomFull"
  | "topFull-bottomSplit"
  | "topSplit-bottomSplit"
  | "grid3x3";

export function presetShape(occ: GridOccupancy): PresetShape {
  if (is3x3(occ)) return "grid3x3";
  const topFull = occ.topLeft !== null && occ.topLeft === occ.topRight;
  const bottomFull = occ.bottomLeft !== null && occ.bottomLeft === occ.bottomRight;
  if (topFull && bottomFull) return "topFull-bottomFull";
  if (topFull && !bottomFull) return "topFull-bottomSplit";
  if (!topFull && bottomFull) return "topSplit-bottomFull";
  return "topSplit-bottomSplit";
}

export function panelLabel(panel: PanelKey): string {
  switch (panel) {
    case "appNav":
      return "App nav";
    case "sessions":
      return "Sessions";
    case "chat":
      return "Chat";
    case "tasks":
      return "Tasks";
    case "files":
      return "Files";
    case "terminal":
      return "Terminal";
    case "drawer":
      return "Drawer";
  }
}

export function slotLabel(slot: CenterGridCellSlot): string {
  switch (slot) {
    case "topLeft":
      return "Top left";
    case "topRight":
      return "Top right";
    case "bottomLeft":
      return "Bottom left";
    case "bottomRight":
      return "Bottom right";
    default: {
      const match = /^r([0-2])c([0-2])$/.exec(slot);
      if (!match) return slot;
      return `Row ${Number(match[1]) + 1} col ${Number(match[2]) + 1}`;
    }
  }
}

export function buildPanelMoveMenuItems(
  occ: GridOccupancy,
  panel: PanelKey,
  currentSlot: CenterGridCellSlot,
): PanelMoveMenuItem[] {
  return cellSlotsForOccupancy(occ)
    .filter((slot) => slot !== currentSlot)
    .filter((slot) => panelAtSlot(occ, slot) !== panel)
    .map((slot) => {
      const target = panelAtSlot(occ, slot);
      const label = target
        ? `Swap with ${panelLabel(target)} in ${slotLabel(slot)}`
        : `Move to ${slotLabel(slot)}`;
      return {
        slot,
        action: target ? "swap" : "move",
        label,
      };
    });
}

function cellSlotsForOccupancy(occ: GridOccupancy): CenterGridCellSlot[] {
  if (occ.rows === 3 && occ.cols === 3) {
    return ["r0c0", "r0c1", "r0c2", "r1c0", "r1c1", "r1c2", "r2c0", "r2c1", "r2c2"];
  }
  return ["topLeft", "topRight", "bottomLeft", "bottomRight"];
}

function panelAtSlot(occ: GridOccupancy, slot: CenterGridCellSlot): PanelKey | null {
  return occ[slot] ?? null;
}
