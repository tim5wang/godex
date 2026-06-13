import { describe, expect, it, beforeEach, afterEach } from "vitest";

import {
  DEFAULT_GRID_OCCUPANCY,
  useLayoutStore,
  type GridPresetId,
} from "../src/store/layout";
import {
  buildPanelMoveMenuItems,
  getPanelDropAction,
  panelLabel,
  presetShape,
  slotLabel,
  splitterSizesTo3WayRatios,
  splitterSizesToRatio,
} from "../src/components/workspace/CenterGrid";

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

// P2 / T6b contract tests for the CenterGrid + ChatWorkspaceCanvas
// integration. The center grid is a 2x2 Splitter + 5-preset catalogue
// (SPEC §3.2). We do not import @testing-library/react (not in the
// dependency tree); instead we test the pure helpers (presetShape)
// and the store-level invariant (default preset + occupancy). The
// component itself is a thin Splitter renderer over the store; the
// store is the source of truth.

describe("presetShape (P2 / T6b contract)", () => {
  it("encodes the 5 SPEC §3.2 presets", () => {
    expect(presetShape(DEFAULT_GRID_OCCUPANCY.topChat_bottomTerminal)).toBe("topFull-bottomFull");
    expect(presetShape(DEFAULT_GRID_OCCUPANCY.topFilesChat_bottomTerminal)).toBe("topSplit-bottomFull");
    expect(presetShape(DEFAULT_GRID_OCCUPANCY.topChat_bottomFilesTerminal)).toBe("topFull-bottomSplit");
    // single preset: topLeft=chat, topRight=chat (collapses to topFull),
    // bottomLeft=null, bottomRight=null (treated as bottomSplit because
    // the two cells are not the same panel).
    expect(presetShape(DEFAULT_GRID_OCCUPANCY.single)).toBe("topFull-bottomSplit");
    // leftCol2x2: topLeft=files, topRight=chat (topSplit), bottomLeft=terminal,
    // bottomRight=chat (bottomSplit).
    expect(presetShape(DEFAULT_GRID_OCCUPANCY.leftCol2x2)).toBe("topSplit-bottomSplit");
  });

  it("treats a single-sided null + populated as the populated side's shape", () => {
    // Synthetic: topLeft=chat, topRight=chat, bottomLeft=null, bottomRight=null
    expect(
      presetShape({
        topLeft: "chat",
        topRight: "chat",
        bottomLeft: null,
        bottomRight: null,
      }),
    ).toBe("topFull-bottomSplit");
  });
});

describe("panel move menu contract (T13)", () => {
  it("labels panels and 2x2 slots for titlebar menus", () => {
    expect(panelLabel("chat")).toBe("Chat");
    expect(panelLabel("files")).toBe("Files");
    expect(panelLabel("terminal")).toBe("Terminal");
    expect(slotLabel("topLeft")).toBe("Top left");
    expect(slotLabel("bottomRight")).toBe("Bottom right");
  });

  it("builds move/swap items for 2x2 occupancy", () => {
    const items = buildPanelMoveMenuItems(DEFAULT_GRID_OCCUPANCY.topFilesChat_bottomTerminal, "files", "topLeft");
    expect(items.map((item) => [item.slot, item.action, item.label])).toEqual([
      ["topRight", "swap", "Swap with Chat in Top right"],
      ["bottomLeft", "swap", "Swap with Terminal in Bottom left"],
      ["bottomRight", "swap", "Swap with Terminal in Bottom right"],
    ]);
  });

  it("builds 3x3 slot labels and skips the current slot", () => {
    const items = buildPanelMoveMenuItems(DEFAULT_GRID_OCCUPANCY.grid3x3_filesChatTerminal, "files", "r0c0");
    expect(items[0]).toEqual({
      slot: "r0c1",
      action: "swap",
      label: "Swap with Chat in Row 1 col 2",
    });
    expect(items.some((item) => item.slot === "r0c0")).toBe(false);
    expect(items.at(-1)?.slot).toBe("r2c2");
  });

  it("derives drag/drop move or swap actions for valid targets", () => {
    const occ = DEFAULT_GRID_OCCUPANCY.topFilesChat_bottomTerminal;
    expect(getPanelDropAction(occ, "files", "topLeft", "topRight")).toBe("swap");
    expect(getPanelDropAction(DEFAULT_GRID_OCCUPANCY.single, "chat", "topLeft", "bottomLeft")).toBe("move");
    expect(getPanelDropAction(occ, "files", "topLeft", "topLeft")).toBeNull();
    expect(getPanelDropAction(occ, "files", "topLeft", "bottomLeft")).toBe("swap");
  });
});

describe("Default grid preset is topFilesChat_bottomTerminal", () => {
  it("store default is the SPEC §3.2 default", () => {
    expect(useLayoutStore.getState().centerGridPreset).toBe("topFilesChat_bottomTerminal");
  });

  it("default occupancy puts files in topLeft, chat in topRight, terminal across the bottom", () => {
    const occ = useLayoutStore.getState().centerGrid;
    expect(occ.topLeft).toBe("files");
    expect(occ.topRight).toBe("chat");
    expect(occ.bottomLeft).toBe("terminal");
    expect(occ.bottomRight).toBe("terminal");
  });
});

describe("CenterGrid occupancy contract (5 presets)", () => {
  const expected: Array<{ id: GridPresetId; topLeft: string | null; topRight: string | null; bottomLeft: string | null; bottomRight: string | null; shape: string }> = [
    {
      id: "topChat_bottomTerminal",
      topLeft: "chat",
      topRight: "chat",
      bottomLeft: "terminal",
      bottomRight: "terminal",
      shape: "topFull-bottomFull",
    },
    {
      id: "topFilesChat_bottomTerminal",
      topLeft: "files",
      topRight: "chat",
      bottomLeft: "terminal",
      bottomRight: "terminal",
      shape: "topSplit-bottomFull",
    },
    {
      id: "topChat_bottomFilesTerminal",
      topLeft: "chat",
      topRight: "chat",
      bottomLeft: "files",
      bottomRight: "terminal",
      shape: "topFull-bottomSplit",
    },
    {
      id: "leftCol2x2",
      topLeft: "files",
      topRight: "chat",
      bottomLeft: "terminal",
      bottomRight: "chat",
      shape: "topSplit-bottomSplit",
    },
    {
      id: "single",
      topLeft: "chat",
      topRight: "chat",
      bottomLeft: null,
      bottomRight: null,
      // topLeft === topRight (chat/chat) collapses to topFull;
      // bottomLeft and bottomRight are both null (NOT the same panel),
      // so the bottom row stays split.
      shape: "topFull-bottomSplit",
    },
  ];

  for (const { id, topLeft, topRight, bottomLeft, bottomRight, shape } of expected) {
    it(`${id} has the SPEC §3.2 occupancy + shape`, () => {
      const occ = DEFAULT_GRID_OCCUPANCY[id];
      expect(occ.topLeft).toBe(topLeft);
      expect(occ.topRight).toBe(topRight);
      expect(occ.bottomLeft).toBe(bottomLeft);
      expect(occ.bottomRight).toBe(bottomRight);
      expect(presetShape(occ)).toBe(shape);
    });
  }
});

describe("setGridPreset switches the centerGrid occupancy", () => {
  it("setGridPreset('single') collapses both bottom cells to null", () => {
    useLayoutStore.getState().setGridPreset("single");
    const occ = useLayoutStore.getState().centerGrid;
    expect(occ.bottomLeft).toBeNull();
    expect(occ.bottomRight).toBeNull();
    expect(occ.topLeft).toBe("chat");
    expect(occ.topRight).toBe("chat");
  });

  it("setGridPreset('topChat_bottomFilesTerminal') puts files in bottomLeft", () => {
    useLayoutStore.getState().setGridPreset("topChat_bottomFilesTerminal");
    const occ = useLayoutStore.getState().centerGrid;
    expect(occ.topLeft).toBe("chat");
    expect(occ.topRight).toBe("chat");
    expect(occ.bottomLeft).toBe("files");
    expect(occ.bottomRight).toBe("terminal");
  });
});

describe("3×3 grid presets (v2.0, M1+ candidate C)", () => {
  it("grid3x3_filesChatTerminal has rows=3, cols=3 with correct cells", () => {
    const occ = DEFAULT_GRID_OCCUPANCY.grid3x3_filesChatTerminal;
    expect(occ.rows).toBe(3);
    expect(occ.cols).toBe(3);
    expect(occ.r0c0).toBe("files");
    expect(occ.r0c1).toBe("chat");
    expect(occ.r0c2).toBe("chat");
    expect(occ.r1c0).toBe("files");
    expect(occ.r1c1).toBe("chat");
    expect(occ.r1c2).toBe("chat");
    expect(occ.r2c0).toBe("terminal");
    expect(occ.r2c1).toBe("terminal");
    expect(occ.r2c2).toBe("terminal");
    expect(presetShape(occ)).toBe("grid3x3");
  });

  it("grid3x3_tallThreeCol has notes in r2c0", () => {
    const occ = DEFAULT_GRID_OCCUPANCY.grid3x3_tallThreeCol;
    expect(occ.rows).toBe(3);
    expect(occ.cols).toBe(3);
    expect(occ.r0c0).toBe("files");
    expect(occ.r0c1).toBe("chat");
    expect(occ.r0c2).toBe("terminal");
    expect(occ.r2c0).toBe("drawer");
    expect(occ.r2c1).toBe("tasks");
    expect(occ.r2c2).toBe("drawer");
    expect(presetShape(occ)).toBe("grid3x3");
  });

  it("grid3x3_wideThreeRow has full-width rows", () => {
    const occ = DEFAULT_GRID_OCCUPANCY.grid3x3_wideThreeRow;
    expect(occ.rows).toBe(3);
    // All cells in rows 0-1 are chat, row 2 is terminal.
    expect(occ.r0c0 === "chat" && occ.r0c1 === "chat" && occ.r0c2 === "chat").toBe(true);
    expect(occ.r1c0 === "chat" && occ.r1c1 === "chat" && occ.r1c2 === "chat").toBe(true);
    expect(occ.r2c0 === "terminal" && occ.r2c1 === "terminal" && occ.r2c2 === "terminal").toBe(true);
    expect(presetShape(occ)).toBe("grid3x3");
  });

  it("setGridPreset to 3×3 preserves legacy 2×2 slots", () => {
    useLayoutStore.getState().setGridPreset("grid3x3_filesChatTerminal");
    const occ = useLayoutStore.getState().centerGrid;
    // Legacy 2x2 slots are filled for backward compat
    expect(occ.topLeft).toBe("files");
    expect(occ.topRight).toBe("chat");
    expect(occ.bottomLeft).toBe("terminal");
    expect(occ.bottomRight).toBe("terminal");
    expect(occ.rows).toBe(3);
    expect(occ.cols).toBe(3);
  });

  it("3×3 preset ratios default to ~1/3 each", () => {
    useLayoutStore.getState().setGridPreset("grid3x3_tallThreeCol");
    const ratios = useLayoutStore.getState().centerGridRatios;
    expect(ratios.row0Split).toBeCloseTo(0.33);
    expect(ratios.row1Split).toBeCloseTo(0.34);
    expect(ratios.col0Split).toBeCloseTo(0.33);
    expect(ratios.col1Split).toBeCloseTo(0.34);
  });

  it("setGridRatio updates 3×3 split ratios", () => {
    useLayoutStore.getState().setGridPreset("grid3x3_filesChatTerminal");
    useLayoutStore.getState().setGridRatio("row0Split", 0.4);
    useLayoutStore.getState().setGridRatio("col0Split", 0.2);
    expect(useLayoutStore.getState().centerGridRatios.row0Split).toBe(0.4);
    expect(useLayoutStore.getState().centerGridRatios.col0Split).toBe(0.2);
  });

  it("movePanelToGrid with 3×3 slot works", () => {
    useLayoutStore.getState().setGridPreset("grid3x3_filesChatTerminal");
    useLayoutStore.getState().movePanelToGrid("files", "r1c0");
    const occ = useLayoutStore.getState().centerGrid;
    expect(occ.r1c0).toBe("files");
  });
});

describe("setGridRatio updates the persisted ratio envelope", () => {
  it("derives persisted ratios from antd Splitter pixel sizes", () => {
    expect(splitterSizesToRatio([300, 700], 0)).toBe(0.3);
    expect(splitterSizesToRatio([300, 700], 1)).toBe(0.7);
    expect(splitterSizesToRatio([0, 0], 0)).toBe(0);
    expect(splitterSizesTo3WayRatios([200, 300, 500], "row")).toEqual({ row0Split: 0.2, row1Split: 0.3 });
    expect(splitterSizesTo3WayRatios([200, 300, 500], "col")).toEqual({ col0Split: 0.2, col1Split: 0.3 });
  });

  it("clamps to [0, 1]", () => {
    useLayoutStore.getState().setGridRatio("outerSplit", 0.3);
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(0.3);
    useLayoutStore.getState().setGridRatio("outerSplit", 1.5);
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(1);
    useLayoutStore.getState().setGridRatio("outerSplit", -0.5);
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(0);
  });

  it("toggleGridRowCollapse collapses and restores persisted 2x2 ratios", () => {
    useLayoutStore.getState().setGridRatio("outerSplit", 0.42);
    useLayoutStore.getState().toggleGridRowCollapse("top");
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(0);
    useLayoutStore.getState().toggleGridRowCollapse("top");
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(0.6);

    useLayoutStore.getState().setGridRatio("innerTopSplit", 0.32);
    useLayoutStore.getState().toggleGridRowCollapse("left");
    expect(useLayoutStore.getState().centerGridRatios.innerTopSplit).toBe(0);
    expect(useLayoutStore.getState().centerGridRatios.innerBottomSplit).toBe(0);
  });
});
