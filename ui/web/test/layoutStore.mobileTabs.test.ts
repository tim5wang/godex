import { describe, expect, it, beforeEach, afterEach } from "vitest";

import {
  useLayoutStore,
  selectMobileWorkspaceTabs,
  type MobileTab,
  type MobileWorkspaceTabsSnapshot,
} from "../src/store/layout";

const EXPECTED_TABS: ReadonlyArray<{ key: MobileTab; i18nKey: string; iconKey: string }> = [
  { key: "chat", i18nKey: "mobile.tabs.chat", iconKey: "chat" },
  { key: "terminal", i18nKey: "mobile.tabs.terminal", iconKey: "terminal" },
  { key: "files", i18nKey: "mobile.tabs.files", iconKey: "files" },
  { key: "drawer", i18nKey: "mobile.tabs.drawer", iconKey: "drawer" },
  { key: "tasks", i18nKey: "mobile.tabs.tasks", iconKey: "tasks" },
];

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

describe("selectMobileWorkspaceTabs (P0-D / T9 contract)", () => {
  it("returns 5 tabs in the SPEC §3.3 order: chat, terminal, files, drawer, tasks", () => {
    const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
    expect(snap.tabs.map((t) => t.key)).toEqual(["chat", "terminal", "files", "drawer", "tasks"]);
    // Each tab descriptor carries: key, i18nKey, iconKey, active.
    // active=true only for the default ('chat' here); the rest are false.
    expect(snap.tabs).toEqual([
      { key: "chat", i18nKey: "mobile.tabs.chat", iconKey: "chat", active: true },
      { key: "terminal", i18nKey: "mobile.tabs.terminal", iconKey: "terminal", active: false },
      { key: "files", i18nKey: "mobile.tabs.files", iconKey: "files", active: false },
      { key: "drawer", i18nKey: "mobile.tabs.drawer", iconKey: "drawer", active: false },
      { key: "tasks", i18nKey: "mobile.tabs.tasks", iconKey: "tasks", active: false },
    ]);
    // Sanity: structural subset still matches the static EXPECTED_TABS shape (3 fields).
    expect(snap.tabs.map((t) => ({ key: t.key, i18nKey: t.i18nKey, iconKey: t.iconKey }))).toEqual(EXPECTED_TABS);
  });

  it("default active tab is 'chat' (SPEC §3.3: switching chat session resets to chat)", () => {
    const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
    expect(snap.active).toBe<MobileTab>("chat");
  });

  it("derives active from store.mobileActiveTab", () => {
    useLayoutStore.getState().setMobileActiveTab("terminal");
    expect(selectMobileWorkspaceTabs(useLayoutStore.getState()).active).toBe("terminal");
    useLayoutStore.getState().setMobileActiveTab("files");
    expect(selectMobileWorkspaceTabs(useLayoutStore.getState()).active).toBe("files");
    useLayoutStore.getState().setMobileActiveTab("drawer");
    expect(selectMobileWorkspaceTabs(useLayoutStore.getState()).active).toBe("drawer");
    useLayoutStore.getState().setMobileActiveTab("tasks");
    expect(selectMobileWorkspaceTabs(useLayoutStore.getState()).active).toBe("tasks");
  });

  it("exactly one tab carries active=true (others false)", () => {
    useLayoutStore.getState().setMobileActiveTab("terminal");
    const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
    const activeCount = snap.tabs.filter((t) => t.active).length;
    expect(activeCount).toBe(1);
    expect(snap.tabs.find((t) => t.active)?.key).toBe("terminal");
  });

  it("every tab carries a stable i18n key and icon key (no per-render drift)", () => {
    const a = selectMobileWorkspaceTabs(useLayoutStore.getState());
    useLayoutStore.getState().setMobileActiveTab("files");
    const b = selectMobileWorkspaceTabs(useLayoutStore.getState());
    // tab list shape (keys / labels) does not change with active selection
    expect(b.tabs.map((t) => ({ i18nKey: t.i18nKey, iconKey: t.iconKey }))).toEqual(
      a.tabs.map((t) => ({ i18nKey: t.i18nKey, iconKey: t.iconKey })),
    );
  });
});

describe("selectMobileWorkspaceTabs: derived render payload", () => {
  it("the snapshot type is MobileWorkspaceTabsSnapshot and contains tabs + active", () => {
    const snap = selectMobileWorkspaceTabs(useLayoutStore.getState());
    // structural check: at minimum { tabs, active }
    const sample: MobileWorkspaceTabsSnapshot = snap;
    expect(Array.isArray(sample.tabs)).toBe(true);
    expect(typeof sample.active).toBe("string");
  });

  it("consecutive renders return fresh tab arrays (callers can rely on identity stability inside one render)", () => {
    const a = selectMobileWorkspaceTabs(useLayoutStore.getState());
    const b = selectMobileWorkspaceTabs(useLayoutStore.getState());
    // selector is pure: same input => same output
    expect(a.tabs).toEqual(b.tabs);
  });
});

describe("Mobile tabs contract: other panels / grid preset do not affect selectMobileWorkspaceTabs", () => {
  it("toggling appNav/sessions/files/terminal/drawer/tasks panel flags does not change tabs", () => {
    const before = selectMobileWorkspaceTabs(useLayoutStore.getState());
    useLayoutStore.getState().toggle("appNav");
    useLayoutStore.getState().toggle("sessions");
    useLayoutStore.getState().toggle("files");
    useLayoutStore.getState().toggle("terminal");
    useLayoutStore.getState().toggle("drawer");
    useLayoutStore.getState().toggle("tasks");
    const after = selectMobileWorkspaceTabs(useLayoutStore.getState());
    expect(after.tabs).toEqual(before.tabs);
    expect(after.active).toBe(before.active);
  });

  it("changing grid preset does not change active tab", () => {
    useLayoutStore.getState().setMobileActiveTab("terminal");
    const before = selectMobileWorkspaceTabs(useLayoutStore.getState());
    useLayoutStore.getState().setGridPreset("single");
    useLayoutStore.getState().setGridPreset("topChat_bottomFilesTerminal");
    const after = selectMobileWorkspaceTabs(useLayoutStore.getState());
    expect(after.active).toBe(before.active);
  });
});

describe("Mobile tabs contract: reset() restores active=chat (SPEC §3.3)", () => {
  it("after switching away and calling reset, active returns to chat", () => {
    useLayoutStore.getState().setMobileActiveTab("files");
    expect(selectMobileWorkspaceTabs(useLayoutStore.getState()).active).toBe("files");
    useLayoutStore.getState().reset();
    expect(selectMobileWorkspaceTabs(useLayoutStore.getState()).active).toBe("chat");
  });
});
