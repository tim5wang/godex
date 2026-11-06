import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  CHAT_LAYOUT_STORAGE_KEY,
  DEFAULT_CHAT_LAYOUT_SNAPSHOT,
  DOCK_TABS,
  readPersistedSnapshot,
  useConversationLayoutStore,
  writePersistedSnapshot,
} from "../src/features/chat/layout/layoutStore";

// Vitest runs in the node environment (no window/localStorage). Install a
// minimal in-memory shim, same pattern as test/layoutPersistence.test.ts.
function installLocalStorageShim() {
  const store = new Map<string, string>();
  const shim = {
    getItem: (key: string) => (store.has(key) ? store.get(key)! : null),
    setItem: (key: string, value: string) => void store.set(key, String(value)),
    removeItem: (key: string) => void store.delete(key),
    clear: () => store.clear(),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() {
      return store.size;
    },
  };
  (globalThis as unknown as { window: { localStorage: typeof shim } }).window = { localStorage: shim };
}

function setViewportWidth(width: number | null) {
  const win = globalThis as unknown as { window: { innerWidth?: number } };
  if (width === null) {
    delete win.window.innerWidth;
    return;
  }
  Object.defineProperty(win.window, "innerWidth", { value: width, configurable: true, writable: true });
}

describe("layout store", () => {
  beforeEach(() => {
    installLocalStorageShim();
    setViewportWidth(null);
    useConversationLayoutStore.getState().reset();
  });

  afterEach(() => {
    setViewportWidth(null);
    useConversationLayoutStore.getState().reset();
  });

  it("starts from the default snapshot", () => {
    const state = useConversationLayoutStore.getState();
    expect(state.leftCollapsed).toBe(false);
    expect(state.rightCollapsed).toBe(false);
    expect(state.activeDockTab).toBe("files");
    expect(state.leftWidth).toBe(DEFAULT_CHAT_LAYOUT_SNAPSHOT.leftWidth);
    expect(state.rightWidth).toBe(DEFAULT_CHAT_LAYOUT_SNAPSHOT.rightWidth);
  });

  it("starts collapsed on narrow viewports so the conversation is immediately visible", () => {
    setViewportWidth(390);
    useConversationLayoutStore.getState().reset();
    const state = useConversationLayoutStore.getState();
    expect(state.leftCollapsed).toBe(true);
    expect(state.rightCollapsed).toBe(true);
    expect(state.activeDockTab).toBe("files");
    expect(readPersistedSnapshot()).toMatchObject({ leftCollapsed: true, rightCollapsed: true });
  });

  it("toggles the left rail collapsed state", () => {
    useConversationLayoutStore.getState().toggleLeft();
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(true);
    useConversationLayoutStore.getState().toggleLeft();
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(false);
  });

  it("toggles the right dock collapsed state", () => {
    useConversationLayoutStore.getState().toggleRight();
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(true);
  });

  it("switches dock tab and expands a collapsed dock", () => {
    useConversationLayoutStore.getState().toggleRight();
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(true);
    useConversationLayoutStore.getState().setActiveDockTab("terminal");
    expect(useConversationLayoutStore.getState().activeDockTab).toBe("terminal");
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(false);
  });

  it("collapses the dock when clicking the already-active tab", () => {
    useConversationLayoutStore.getState().setActiveDockTab("terminal");
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(false);
    useConversationLayoutStore.getState().setActiveDockTab("terminal");
    expect(useConversationLayoutStore.getState().activeDockTab).toBe("terminal");
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(true);
  });

  it("rejects unknown dock tabs", () => {
    useConversationLayoutStore.getState().setActiveDockTab("nope" as never);
    expect(useConversationLayoutStore.getState().activeDockTab).toBe("files");
  });

  it("clamps rail widths", () => {
    useConversationLayoutStore.getState().setLeftWidth(10);
    expect(useConversationLayoutStore.getState().leftWidth).toBe(200);
    useConversationLayoutStore.getState().setLeftWidth(5000);
    expect(useConversationLayoutStore.getState().leftWidth).toBe(600);
    useConversationLayoutStore.getState().setRightWidth(Number.NaN);
    expect(useConversationLayoutStore.getState().rightWidth).toBe(DEFAULT_CHAT_LAYOUT_SNAPSHOT.rightWidth);
  });

  it("persists snapshot to localStorage and rehydrates", () => {
    useConversationLayoutStore.getState().toggleLeft();
    useConversationLayoutStore.getState().setActiveDockTab("tasks");
    writePersistedSnapshot(useConversationLayoutStore.getState());
    useConversationLayoutStore.getState().reset();
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(false);
    const persisted = readPersistedSnapshot();
    expect(persisted).not.toBeNull();
    useConversationLayoutStore.setState(persisted!);
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(true);
    expect(useConversationLayoutStore.getState().activeDockTab).toBe("tasks");
  });

  it("returns null for malformed persisted payloads", () => {
    window.localStorage.setItem(CHAT_LAYOUT_STORAGE_KEY, "{not json");
    expect(readPersistedSnapshot()).toBeNull();
    window.localStorage.setItem(CHAT_LAYOUT_STORAGE_KEY, JSON.stringify({ activeDockTab: "bad" }));
    expect(readPersistedSnapshot()).toBeNull();
  });

  it("provides an idempotent close action for returning to chat", () => {
    useConversationLayoutStore.getState().setActiveDockTab("status");
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(false);

    useConversationLayoutStore.getState().closeRight();
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(true);
    expect(useConversationLayoutStore.getState().activeDockTab).toBe("status");

    useConversationLayoutStore.getState().closeRight();
    expect(useConversationLayoutStore.getState().rightCollapsed).toBe(true);
  });

  it("exposes all seven dock tabs in order", () => {
    expect(DOCK_TABS).toEqual(["files", "terminal", "tasks", "workdesk", "preview", "status", "browser"]);
  });

  it("defaults to the conversation directory and switches the left view", () => {
    expect(DEFAULT_CHAT_LAYOUT_SNAPSHOT.leftView).toBe("directory");
    useConversationLayoutStore.getState().setLeftView("desktop");
    expect(useConversationLayoutStore.getState().leftView).toBe("desktop");
  });

  it("expands the rail when switching the left view, so the switch stays reachable", () => {
    // Regression guard: if switching the view left the rail collapsed, the toggle
    // that switched it would vanish with the rail and the user could not switch back.
    useConversationLayoutStore.getState().toggleLeft();
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(true);
    useConversationLayoutStore.getState().setLeftView("desktop");
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(false);
  });

  it("persists the left view across a rehydrate", () => {
    useConversationLayoutStore.getState().setLeftView("desktop");
    writePersistedSnapshot(useConversationLayoutStore.getState());
    useConversationLayoutStore.getState().reset();
    expect(useConversationLayoutStore.getState().leftView).toBe("directory");
    useConversationLayoutStore.setState(readPersistedSnapshot()!);
    expect(useConversationLayoutStore.getState().leftView).toBe("desktop");
  });

  it("treats a pre-fusion snapshot without leftView as the directory view", () => {
    // Older persisted layouts have no leftView field; they showed the conversation
    // list, so that is what they must still show rather than failing to hydrate.
    window.localStorage.setItem(CHAT_LAYOUT_STORAGE_KEY, JSON.stringify({ leftCollapsed: false, rightCollapsed: true, activeDockTab: "files", leftWidth: 264, rightWidth: 420 }));
    const persisted = readPersistedSnapshot();
    expect(persisted).not.toBeNull();
    expect(persisted!.leftView).toBe("directory");
  });

  it("keeps the left view on narrow viewports while still collapsing the rails", () => {
    // The view is remembered even on a phone: the desktop is a list, not a panel
    // that would cover the screen, so preserving it costs nothing — while the rails
    // must still start collapsed so the conversation is immediately usable.
    useConversationLayoutStore.getState().setLeftView("desktop");
    writePersistedSnapshot(useConversationLayoutStore.getState());
    useConversationLayoutStore.getState().reset();
    setViewportWidth(390);
    const persisted = readPersistedSnapshot();
    expect(persisted!.leftView).toBe("desktop");
    expect(persisted!.leftCollapsed).toBe(true);
    expect(persisted!.rightCollapsed).toBe(true);
  });

  it("reset restores defaults", () => {
    useConversationLayoutStore.getState().toggleLeft();
    useConversationLayoutStore.getState().setLeftWidth(300);
    useConversationLayoutStore.getState().reset();
    expect(useConversationLayoutStore.getState().leftCollapsed).toBe(false);
    expect(useConversationLayoutStore.getState().leftWidth).toBe(DEFAULT_CHAT_LAYOUT_SNAPSHOT.leftWidth);
  });
});
