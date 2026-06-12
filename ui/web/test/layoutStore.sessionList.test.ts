import { describe, expect, it, beforeEach, afterEach } from "vitest";

import {
  useLayoutStore,
  selectSessionListLayoutState,
  type SessionListLayoutSnapshot,
} from "../src/store/layout";

const SESSIONS_ICON_WIDTH = 40;
const SESSIONS_DEFAULT_WIDTH = 280;

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

describe("selectSessionListLayoutState (T3 SessionList contract)", () => {
  it("returns the canonical default snapshot (expanded, width 280, iconOnly 40)", () => {
    const snap = selectSessionListLayoutState(useLayoutStore.getState());
    expect(snap).toEqual<SessionListLayoutSnapshot>({
      collapsed: false,
      width: SESSIONS_DEFAULT_WIDTH,
      iconOnlyWidth: SESSIONS_ICON_WIDTH,
    });
  });

  it("derives collapsed from panels.sessions.collapsed", () => {
    useLayoutStore.getState().toggle("sessions");
    expect(selectSessionListLayoutState(useLayoutStore.getState()).collapsed).toBe(true);
    useLayoutStore.getState().toggle("sessions");
    expect(selectSessionListLayoutState(useLayoutStore.getState()).collapsed).toBe(false);
  });

  it("derives width from panels.sessions.width (clamped to >= 32)", () => {
    useLayoutStore.getState().setWidth("sessions", 320);
    expect(selectSessionListLayoutState(useLayoutStore.getState()).width).toBe(320);
    useLayoutStore.getState().setWidth("sessions", 5);
    // setWidth clamps to >= 32 (store layer invariant)
    expect(selectSessionListLayoutState(useLayoutStore.getState()).width).toBe(32);
  });

  it("iconOnlyWidth is the SPEC §3.2 fixed narrow width (40px)", () => {
    expect(selectSessionListLayoutState(useLayoutStore.getState()).iconOnlyWidth).toBe(40);
  });

  it("is independent of the center grid preset (T3 does not touch the grid)", () => {
    const before = selectSessionListLayoutState(useLayoutStore.getState());
    useLayoutStore.getState().setGridPreset("single");
    const after = selectSessionListLayoutState(useLayoutStore.getState());
    expect(after).toEqual(before);
    expect(useLayoutStore.getState().centerGridPreset).toBe("single");
  });
});

describe("selectSessionListLayoutState: derived render width", () => {
  it("collapsed === true → column should render iconOnlyWidth (40)", () => {
    useLayoutStore.getState().toggle("sessions");
    const snap = selectSessionListLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(true);
    expect(snap.collapsed ? snap.iconOnlyWidth : snap.width).toBe(40);
  });

  it("collapsed === false → column should render panels.sessions.width (default 280)", () => {
    const snap = selectSessionListLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(false);
    expect(snap.collapsed ? snap.iconOnlyWidth : snap.width).toBe(280);
  });
});

describe("SessionList contract: other panels do not affect selectSessionListLayoutState", () => {
  it("toggling appNav/files/terminal does not change SessionList snap", () => {
    const before = selectSessionListLayoutState(useLayoutStore.getState());
    useLayoutStore.getState().toggle("appNav");
    useLayoutStore.getState().toggle("files");
    useLayoutStore.getState().toggle("terminal");
    const after = selectSessionListLayoutState(useLayoutStore.getState());
    expect(after).toEqual(before);
  });
});

describe("SessionList contract: default layout unchanged after T3 changes", () => {
  it("default grid preset is still topFilesChat_bottomTerminal", () => {
    expect(useLayoutStore.getState().centerGridPreset).toBe("topFilesChat_bottomTerminal");
  });

  it("default appNav collapse state is still false (T2 invariant)", () => {
    expect(useLayoutStore.getState().panels.appNav.collapsed).toBe(false);
  });
});
