import { describe, expect, it, beforeEach, afterEach } from "vitest";

import {
  useLayoutStore,
  type AppNavLayoutSnapshot,
  selectAppNavLayoutState,
} from "../src/store/layout";
import { DEFAULT_GRID_PRESET } from "../src/store/layout";

const APP_NAV_ICON_WIDTH = 48;
const APP_NAV_DEFAULT_WIDTH = 200;

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

describe("selectAppNavLayoutState (T2 AppNav contract)", () => {
  it("returns the canonical default snapshot (expanded, width 200, iconOnly 48)", () => {
    const snap = selectAppNavLayoutState(useLayoutStore.getState());
    expect(snap).toEqual<AppNavLayoutSnapshot>({
      collapsed: false,
      width: APP_NAV_DEFAULT_WIDTH,
      iconOnlyWidth: APP_NAV_ICON_WIDTH,
    });
  });

  it("derives collapsed from panels.appNav.collapsed", () => {
    useLayoutStore.getState().toggle("appNav");
    const snap = selectAppNavLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(true);
    useLayoutStore.getState().toggle("appNav");
    expect(selectAppNavLayoutState(useLayoutStore.getState()).collapsed).toBe(false);
  });

  it("derives width from panels.appNav.width (clamped)", () => {
    useLayoutStore.getState().setWidth("appNav", 240);
    expect(selectAppNavLayoutState(useLayoutStore.getState()).width).toBe(240);
    useLayoutStore.getState().setWidth("appNav", 5);
    expect(selectAppNavLayoutState(useLayoutStore.getState()).width).toBe(32);
  });

  it("iconOnlyWidth is the SPEC §3.2 fixed narrow width (48px)", () => {
    expect(selectAppNavLayoutState(useLayoutStore.getState()).iconOnlyWidth).toBe(48);
  });

  it("is independent of the center grid preset (T2 does not touch the grid)", () => {
    const before = selectAppNavLayoutState(useLayoutStore.getState());
    useLayoutStore.getState().setGridPreset("single");
    const after = selectAppNavLayoutState(useLayoutStore.getState());
    expect(after).toEqual(before);
    // Sanity: preset really did change
    expect(useLayoutStore.getState().centerGridPreset).toBe("single");
  });
});

describe("selectAppNavLayoutState: derived render width", () => {
  it("collapsed === true → Layout.Sider should render iconOnlyWidth (48)", () => {
    useLayoutStore.getState().toggle("appNav");
    const snap = selectAppNavLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(true);
    // App.tsx will use: <Layout.Sider width={snap.collapsed ? snap.iconOnlyWidth : snap.width} />
    expect(snap.collapsed ? snap.iconOnlyWidth : snap.width).toBe(48);
  });

  it("collapsed === false → Layout.Sider should render panels.appNav.width (default 200)", () => {
    const snap = selectAppNavLayoutState(useLayoutStore.getState());
    expect(snap.collapsed).toBe(false);
    expect(snap.collapsed ? snap.iconOnlyWidth : snap.width).toBe(200);
  });
});

describe("AppNav contract: non-AppNav panels do not affect selectAppNavLayoutState", () => {
  it("toggling sessions/files/terminal does not change AppNav snap", () => {
    const before = selectAppNavLayoutState(useLayoutStore.getState());
    useLayoutStore.getState().toggle("sessions");
    useLayoutStore.getState().toggle("files");
    useLayoutStore.getState().toggle("terminal");
    const after = selectAppNavLayoutState(useLayoutStore.getState());
    expect(after).toEqual(before);
  });
});

describe("AppNav contract: default layout unchanged", () => {
  it("default grid preset is still topFilesChat_bottomTerminal", () => {
    expect(useLayoutStore.getState().centerGridPreset).toBe(DEFAULT_GRID_PRESET);
  });
});
