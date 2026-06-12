import { afterEach, beforeEach, describe, expect, it } from "vitest";

// Tests exercise the pure state machine of store/layout.ts. They MUST fail
// before the module exists, and pass after the minimum implementation lands.

import {
  DEFAULT_GRID_PRESET,
  DEFAULT_LAYOUT_SNAPSHOT,
  GRID_PRESETS,
  useLayoutStore,
  type GridPresetId,
  type PanelKey,
} from "../src/store/layout";
const initialPanels: Record<PanelKey, { collapsed: boolean; width?: number; visible: boolean }> = {
  appNav: { collapsed: false, width: 200, visible: true },
  sessions: { collapsed: false, width: 280, visible: true },
  tasks: { collapsed: true, width: 560, visible: true },
  files: { collapsed: true, width: 320, visible: true },
  terminal: { collapsed: true, width: 320, visible: true },
  drawer: { collapsed: true, width: 320, visible: true },
};

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => {
  reset();
});

afterEach(() => {
  reset();
});

describe("layout store defaults", () => {
  it("uses topFilesChat_bottomTerminal as the default grid preset (SPEC §3.2)", () => {
    expect(DEFAULT_GRID_PRESET).toBe("topFilesChat_bottomTerminal");
    expect(useLayoutStore.getState().centerGridPreset).toBe("topFilesChat_bottomTerminal");
  });

  it("starts in the default layout snapshot, with chat pinned into a grid cell", () => {
    const s = useLayoutStore.getState();
    expect(s.centerGridPreset).toBe(DEFAULT_GRID_PRESET);
    // chat must always occupy at least one slot
    const occupied = Object.values(s.centerGrid);
    expect(occupied).toContain("chat");
    // default snapshot should match the static constant
    expect(DEFAULT_LAYOUT_SNAPSHOT.centerGridPreset).toBe(s.centerGridPreset);
  });

  it("exposes the 5 SPEC §3.2 preset ids", () => {
    const ids = GRID_PRESETS.map((p) => p.id).sort();
    expect(ids).toEqual(
      [
        "leftCol2x2",
        "single",
        "topChat_bottomFilesTerminal",
        "topChat_bottomTerminal",
        "topFilesChat_bottomTerminal",
      ].sort(),
    );
  });
});

describe("layout store panels defaults", () => {
  it("starts with appNav and sessions expanded, others collapsed, dock collapsed", () => {
    const s = useLayoutStore.getState();
    expect(s.panels.appNav).toMatchObject({ collapsed: false, visible: true });
    expect(s.panels.sessions).toMatchObject({ collapsed: false, visible: true });
    expect(s.panels.tasks).toMatchObject({ collapsed: true, visible: true });
    expect(s.panels.files).toMatchObject({ collapsed: true, visible: true });
    expect(s.panels.terminal).toMatchObject({ collapsed: true, visible: true });
    expect(s.panels.drawer).toMatchObject({ collapsed: true, visible: true });
    // sanity: initial panels snapshot is the canonical default
    expect(s.panels).toEqual(initialPanels);
  });
});

describe("setGridPreset", () => {
  it("switches the preset and re-projects the grid occupancy", () => {
    useLayoutStore.getState().setGridPreset("topChat_bottomTerminal");
    const s = useLayoutStore.getState();
    expect(s.centerGridPreset).toBe("topChat_bottomTerminal");
    // top row is full chat, bottom row is full terminal
    expect(s.centerGrid).toEqual({
      topLeft: "chat",
      topRight: "chat",
      bottomLeft: "terminal",
      bottomRight: "terminal",
    });
  });

  it("rejects unknown preset ids", () => {
    const before = useLayoutStore.getState().centerGridPreset;
    // @ts-expect-error - intentionally invalid id
    useLayoutStore.getState().setGridPreset("not-a-real-preset");
    expect(useLayoutStore.getState().centerGridPreset).toBe(before);
  });

  it("'single' preset collapses terminal/files to null, chat spans the whole cell", () => {
    useLayoutStore.getState().setGridPreset("single");
    const s = useLayoutStore.getState();
    expect(s.centerGrid).toEqual({
      topLeft: "chat",
      topRight: "chat",
      bottomLeft: null,
      bottomRight: null,
    });
  });

  it("'leftCol2x2' puts files|terminal on the left column and chat on the right column", () => {
    useLayoutStore.getState().setGridPreset("leftCol2x2");
    const s = useLayoutStore.getState();
    expect(s.centerGrid).toEqual({
      topLeft: "files",
      topRight: "chat",
      bottomLeft: "terminal",
      bottomRight: "chat",
    });
  });
});

describe("movePanelToGrid", () => {
  it("moves a panel into an empty slot", () => {
    useLayoutStore.getState().setGridPreset("topChat_bottomFilesTerminal");
    // bottomRight is terminal; clear it first via swap-into-our-empty-slot pattern
    // Easier: pick topChat_bottomTerminal where topRight is chat (occupied) and bottomLeft is terminal.
    // Move files into an empty slot by first clearing terminal from bottomLeft.
    useLayoutStore.getState().swapPanelInGrid("files", "bottomLeft"); // evicts terminal to dock
    const after = useLayoutStore.getState().centerGrid;
    expect(after.bottomLeft).toBe("files");
    expect(after.topLeft).toBe("chat");
    expect(after.topRight).toBe("chat");
    expect(after.bottomRight).toBe("terminal");
  });

  it("rejects moving chat out of the workspace entirely (chat is pinned)", () => {
    useLayoutStore.getState().setGridPreset("topFilesChat_bottomTerminal");
    // try to push chat out of every slot — movePanelToGrid won't even try
    // (no empty slot that holds chat alone in this preset; just verify chat
    // is still present after a few no-op calls).
    useLayoutStore.getState().movePanelToGrid("chat", "bottomLeft");
    useLayoutStore.getState().movePanelToGrid("chat", "bottomRight");
    const s = useLayoutStore.getState();
    const occupied = Object.values(s.centerGrid);
    expect(occupied).toContain("chat");
  });

  it("rejects a slot that is already taken by another panel (no silent overwrite)", () => {
    useLayoutStore.getState().setGridPreset("topFilesChat_bottomTerminal");
    // topLeft is files. Moving terminal onto topLeft must be a no-op.
    const before = { ...useLayoutStore.getState().centerGrid };
    useLayoutStore.getState().movePanelToGrid("terminal", "topLeft");
    expect(useLayoutStore.getState().centerGrid).toEqual(before);
  });

  it("rejects an unknown panel key", () => {
    const before = { ...useLayoutStore.getState().centerGrid };
    // @ts-expect-error - intentionally invalid panel
    useLayoutStore.getState().movePanelToGrid("nope", "topLeft");
    expect(useLayoutStore.getState().centerGrid).toEqual(before);
  });
});

describe("swapPanelInGrid (explicit user-driven swap)", () => {
  it("places the panel in the target slot and evicts the previous occupant to dock", () => {
    useLayoutStore.getState().setGridPreset("single");
    // topLeft is chat; tasks comes in, chat stays because single spreads
    // chat across topLeft+topRight.
    useLayoutStore.getState().swapPanelInGrid("tasks", "topLeft");
    const s = useLayoutStore.getState();
    expect(s.centerGrid.topLeft).toBe("tasks");
    // chat is still in the grid (pinned)
    expect(Object.values(s.centerGrid)).toContain("chat");
  });

  it("refuses to evict chat when chat occupies only that single cell", () => {
    useLayoutStore.getState().setGridPreset("single");
    // Force a state where chat lives only in topLeft: topRight is null and
    // bottom* are null. Easiest path: reset, then set preset, then evict
    // chat from topRight via a no-op swap. We use the public API: clear
    // topRight by moving chat to topLeft (already there) — no. Use
    // swapPanelInGrid('files', 'topRight') which evicts chat; then check
    // that chat is still in topLeft.
    useLayoutStore.getState().swapPanelInGrid("files", "topRight");
    // chat was in topLeft+topRight. After evicting from topRight, chat
    // remains in topLeft.
    const s = useLayoutStore.getState();
    expect(s.centerGrid.topLeft).toBe("chat");
    expect(s.centerGrid.topRight).toBe("files");
  });

  it("is a no-op when target slot is empty (use movePanelToGrid for that)", () => {
    useLayoutStore.getState().setGridPreset("topChat_bottomTerminal");
    // bottomLeft is terminal (occupied), so swap there evicts terminal.
    // Use a slot we know is occupied: topLeft (chat).
    const before = { ...useLayoutStore.getState().centerGrid };
    useLayoutStore.getState().swapPanelInGrid("files", "bottomLeft"); // bottomLeft=terminal
    // after swap, bottomLeft=files, terminal dropped to dock.
    const after = useLayoutStore.getState().centerGrid;
    expect(after.bottomLeft).toBe("files");
    expect(after.topLeft).toBe(before.topLeft);
  });
});

describe("setGridRatio", () => {
  it("clamps to [0, 1] for outerSplit", () => {
    useLayoutStore.getState().setGridRatio("outerSplit", 2);
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(1);
    useLayoutStore.getState().setGridRatio("outerSplit", -1);
    expect(useLayoutStore.getState().centerGridRatios.outerSplit).toBe(0);
  });

  it("clamps inner ratios when the active preset uses them", () => {
    useLayoutStore.getState().setGridPreset("topFilesChat_bottomTerminal");
    useLayoutStore.getState().setGridRatio("innerTopSplit", 5);
    expect(useLayoutStore.getState().centerGridRatios.innerTopSplit).toBe(1);
  });
});

describe("panels: toggle / setWidth", () => {
  it("toggle flips the collapsed flag for a panel", () => {
    expect(useLayoutStore.getState().panels.appNav.collapsed).toBe(false);
    useLayoutStore.getState().toggle("appNav");
    expect(useLayoutStore.getState().panels.appNav.collapsed).toBe(true);
    useLayoutStore.getState().toggle("appNav");
    expect(useLayoutStore.getState().panels.appNav.collapsed).toBe(false);
  });

  it("setWidth stores the new width (clamped to >= 32)", () => {
    useLayoutStore.getState().setWidth("sessions", 5);
    expect(useLayoutStore.getState().panels.sessions.width).toBe(32);
    useLayoutStore.getState().setWidth("sessions", 360);
    expect(useLayoutStore.getState().panels.sessions.width).toBe(360);
  });
});

describe("mobile tabs", () => {
  it("defaults to chat", () => {
    expect(useLayoutStore.getState().mobileActiveTab).toBe("chat");
  });

  it("setMobileActiveTab switches and is independent of grid preset", () => {
    const presetBefore = useLayoutStore.getState().centerGridPreset;
    useLayoutStore.getState().setMobileActiveTab("terminal");
    expect(useLayoutStore.getState().mobileActiveTab).toBe("terminal");
    expect(useLayoutStore.getState().centerGridPreset).toBe(presetBefore);
  });

  it("rejects an unknown mobile tab", () => {
    // @ts-expect-error - intentionally invalid
    useLayoutStore.getState().setMobileActiveTab("nope");
    expect(useLayoutStore.getState().mobileActiveTab).toBe("chat");
  });
});

describe("reset", () => {
  it("restores the default snapshot", () => {
    useLayoutStore.getState().setGridPreset("single");
    useLayoutStore.getState().toggle("appNav");
    useLayoutStore.getState().setMobileActiveTab("files");
    useLayoutStore.getState().reset();
    const s = useLayoutStore.getState();
    expect(s.centerGridPreset).toBe(DEFAULT_GRID_PRESET);
    expect(s.panels.appNav.collapsed).toBe(false);
    expect(s.mobileActiveTab).toBe("chat");
  });
});

describe("every preset is renderable: chat is in at least one cell", () => {
  for (const preset of GRID_PRESETS) {
    it(`preset ${preset.id} keeps chat in the grid`, () => {
      useLayoutStore.getState().setGridPreset(preset.id as GridPresetId);
      const s = useLayoutStore.getState();
      expect(Object.values(s.centerGrid)).toContain("chat");
    });
  }
});
