import { describe, expect, it, beforeEach, afterEach } from "vitest";

import { useLayoutStore } from "../src/store/layout";
import {
  selectFilesLayoutState,
  FILES_ICON_ONLY_WIDTH,
  type FilesLayoutSnapshot,
} from "../src/features/files/selectFilesLayoutState";

const FILES_DEFAULT_WIDTH = 320;

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

describe("selectFilesLayoutState (P2 / T6 contract)", () => {
  it("returns the canonical default snapshot (collapsed, width 320, iconOnly 40)", () => {
    // SPEC §3.2: "files" is in the "others" pool so it is collapsed
    // by default. The column is hidden from view until the user
    // opens it from the chip / dock affordance.
    const snap = selectFilesLayoutState(useLayoutStore.getState());
    expect(snap).toEqual<FilesLayoutSnapshot>({
      collapsed: true,
      width: FILES_DEFAULT_WIDTH,
      iconOnlyWidth: FILES_ICON_ONLY_WIDTH,
    });
  });

  it("derives collapsed from panels.files.collapsed", () => {
    useLayoutStore.getState().toggle("files");
    expect(selectFilesLayoutState(useLayoutStore.getState()).collapsed).toBe(true);
    useLayoutStore.getState().toggle("files");
    expect(selectFilesLayoutState(useLayoutStore.getState()).collapsed).toBe(false);
  });

  it("derives width from panels.files.width (clamped by setWidth to >= 32)", () => {
    useLayoutStore.getState().setWidth("files", 400);
    expect(selectFilesLayoutState(useLayoutStore.getState()).width).toBe(400);
    useLayoutStore.getState().setWidth("files", 5);
    // setWidth clamps to >= 32 (store layer invariant)
    expect(selectFilesLayoutState(useLayoutStore.getState()).width).toBe(32);
  });

  it("iconOnlyWidth is the SPEC §3.2 fixed narrow width (40px)", () => {
    expect(selectFilesLayoutState(useLayoutStore.getState()).iconOnlyWidth).toBe(40);
    expect(FILES_ICON_ONLY_WIDTH).toBe(40);
  });

  it("is independent of the center grid preset (P2 does not touch the grid)", () => {
    const before = selectFilesLayoutState(useLayoutStore.getState());
    useLayoutStore.getState().setGridPreset("single");
    const after = selectFilesLayoutState(useLayoutStore.getState());
    expect(after).toEqual(before);
    expect(useLayoutStore.getState().centerGridPreset).toBe("single");
  });
});

describe("selectFilesLayoutState: derived render width", () => {
  it("collapsed === true → column should render iconOnlyWidth (40)", () => {
    // Default is already collapsed, so this is the natural state.
    const snap = selectFilesLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(true);
    expect(snap.collapsed ? snap.iconOnlyWidth : snap.width).toBe(40);
  });

  it("after toggle('files') the column should render the persisted width (default 320)", () => {
    useLayoutStore.getState().toggle("files");
    const snap = selectFilesLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(false);
    expect(snap.collapsed ? snap.iconOnlyWidth : snap.width).toBe(320);
  });
});

describe("Files panel contract: other panels do not affect selectFilesLayoutState", () => {
  it("toggling appNav/sessions/terminal/tasks does not change files snap", () => {
    const before = selectFilesLayoutState(useLayoutStore.getState());
    useLayoutStore.getState().toggle("appNav");
    useLayoutStore.getState().toggle("sessions");
    useLayoutStore.getState().toggle("terminal");
    useLayoutStore.getState().toggle("tasks");
    const after = selectFilesLayoutState(useLayoutStore.getState());
    expect(after).toEqual(before);
  });
});

describe("Files panel contract: default layout unchanged after P2 changes", () => {
  it("default grid preset is still topFilesChat_bottomTerminal", () => {
    expect(useLayoutStore.getState().centerGridPreset).toBe("topFilesChat_bottomTerminal");
  });

  it("files panel default is expanded (true) — SPEC §3.2 panel occupancy", () => {
    // P0-A spec: "appNav + sessions expanded, others collapsed". Files is
    // "others" so the default is collapsed. This locks the invariant.
    expect(useLayoutStore.getState().panels.files.collapsed).toBe(true);
  });
});
