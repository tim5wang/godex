import { describe, expect, it, beforeEach, afterEach } from "vitest";

import { useLayoutStore } from "../src/store/layout";

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

describe("taskCenterDrawerOpen (P1 / T5e cross-page wiring contract)", () => {
  it("defaults to closed (a fresh layout has the drawer shut)", () => {
    expect(useLayoutStore.getState().taskCenterDrawerOpen).toBe(false);
  });

  it("openTaskCenterDrawer() flips the flag on", () => {
    useLayoutStore.getState().openTaskCenterDrawer();
    expect(useLayoutStore.getState().taskCenterDrawerOpen).toBe(true);
  });

  it("closeTaskCenterDrawer() flips the flag off (idempotent)", () => {
    useLayoutStore.getState().openTaskCenterDrawer();
    useLayoutStore.getState().closeTaskCenterDrawer();
    expect(useLayoutStore.getState().taskCenterDrawerOpen).toBe(false);
    // Closing when already closed is a no-op.
    useLayoutStore.getState().closeTaskCenterDrawer();
    expect(useLayoutStore.getState().taskCenterDrawerOpen).toBe(false);
  });

  it("reset() restores the flag to its default (closed)", () => {
    useLayoutStore.getState().openTaskCenterDrawer();
    expect(useLayoutStore.getState().taskCenterDrawerOpen).toBe(true);
    useLayoutStore.getState().reset();
    expect(useLayoutStore.getState().taskCenterDrawerOpen).toBe(false);
  });

  it("opening the task center drawer does not perturb the center grid preset or AppNav state", () => {
    const presetBefore = useLayoutStore.getState().centerGridPreset;
    const appNavCollapsedBefore = useLayoutStore.getState().panels.appNav.collapsed;
    useLayoutStore.getState().openTaskCenterDrawer();
    expect(useLayoutStore.getState().centerGridPreset).toBe(presetBefore);
    expect(useLayoutStore.getState().panels.appNav.collapsed).toBe(appNavCollapsedBefore);
  });
});
