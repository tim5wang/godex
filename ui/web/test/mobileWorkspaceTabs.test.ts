import { describe, expect, it, beforeEach, afterEach } from "vitest";

import {
  MOBILE_TABS_ORDER,
  selectMobileWorkspaceTabs,
  useLayoutStore,
  type MobileTab,
} from "../src/store/layout";

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

// P2 / T6d (SPEC §3.3 + §3.4) contract tests for the mobile workspace
// tab contract. The renderer (MobileWorkspaceTabs.tsx) is intentionally
// thin: it reads the store's mobileActiveTab and renders the matching
// sub-view. These tests cover the store + selector layer — the source
// of truth — without depending on @testing-library/react. The renderer
// is exercised by the visual acceptance (SPEC §6) and is not a unit
// test target.

describe("MOBILE_TABS_ORDER (SPEC §3.3 canonical order)", () => {
  it("is exactly chat | terminal | files | drawer | tasks", () => {
    expect(MOBILE_TABS_ORDER).toEqual<MobileTab[]>(["chat", "terminal", "files", "drawer", "tasks"]);
  });

  it("is 5 tabs long", () => {
    expect(MOBILE_TABS_ORDER).toHaveLength(5);
  });
});

describe("selectMobileWorkspaceTabs default", () => {
  it("active starts at 'chat' (default mobile tab)", () => {
    const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
    expect(snap.active).toBe("chat");
  });

  it("tabs list mirrors MOBILE_TABS_ORDER with the right i18n keys + active flag", () => {
    const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
    expect(snap.tabs).toHaveLength(5);
    for (let i = 0; i < snap.tabs.length; i++) {
      expect(snap.tabs[i].key).toBe(MOBILE_TABS_ORDER[i]);
      expect(snap.tabs[i].i18nKey).toBe(`mobile.tabs.${MOBILE_TABS_ORDER[i]}`);
      expect(snap.tabs[i].iconKey).toBe(MOBILE_TABS_ORDER[i]);
    }
    expect(snap.tabs[0].active).toBe(true); // chat is default
    expect(snap.tabs[1].active).toBe(false);
    expect(snap.tabs[2].active).toBe(false);
    expect(snap.tabs[3].active).toBe(false);
    expect(snap.tabs[4].active).toBe(false);
  });
});

describe("setMobileActiveTab — 5-tab switching", () => {
  const all: MobileTab[] = ["chat", "terminal", "files", "drawer", "tasks"];

  for (const target of all) {
    it(`setMobileActiveTab('${target}') updates the active tab`, () => {
      useLayoutStore.getState().setMobileActiveTab(target);
      const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
      expect(snap.active).toBe(target);
      const activeFlags = snap.tabs.map((t) => t.active);
      const activeCount = activeFlags.filter(Boolean).length;
      expect(activeCount).toBe(1);
      const activeTab = snap.tabs.find((t) => t.active);
      expect(activeTab?.key).toBe(target);
    });
  }

  it("rejecting unknown tab keys is a no-op (action is guarded)", () => {
    useLayoutStore.getState().setMobileActiveTab("chat");
    // Cast through unknown so the call compiles even though the action
    // signature rejects it. The store's isMobileTab guard refuses the
    // value; state should stay at the prior 'chat'.
    useLayoutStore.getState().setMobileActiveTab("bogus" as unknown as MobileTab);
    expect(useLayoutStore.getState().mobileActiveTab).toBe("chat");
  });
});

describe("reset() restores the default mobile tab", () => {
  it("mobileActiveTab is 'chat' after reset, regardless of prior switching", () => {
    useLayoutStore.getState().setMobileActiveTab("files");
    expect(useLayoutStore.getState().mobileActiveTab).toBe("files");
    useLayoutStore.getState().reset();
    expect(useLayoutStore.getState().mobileActiveTab).toBe("chat");
  });
});

describe("Mobile workspace tab isolation", () => {
  it("changing mobileActiveTab does not affect center grid preset or panel state", () => {
    const before = {
      preset: useLayoutStore.getState().centerGridPreset,
      occ: { ...useLayoutStore.getState().centerGrid },
      panels: { ...useLayoutStore.getState().panels },
    };
    useLayoutStore.getState().setMobileActiveTab("files");
    const after = {
      preset: useLayoutStore.getState().centerGridPreset,
      occ: { ...useLayoutStore.getState().centerGrid },
      panels: { ...useLayoutStore.getState().panels },
    };
    expect(after).toEqual(before);
  });
});
