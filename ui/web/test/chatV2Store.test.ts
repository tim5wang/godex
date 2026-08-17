import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  CHAT_V2_STORAGE_KEY,
  DEFAULT_CHAT_V2_SNAPSHOT,
  DOCK_TABS,
  readPersistedSnapshot,
  useChatV2Store,
  writePersistedSnapshot,
} from "../src/features/chat-v2/chatV2Store";

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

describe("chatV2Store", () => {
  beforeEach(() => {
    installLocalStorageShim();
    setViewportWidth(null);
    useChatV2Store.getState().reset();
  });

  afterEach(() => {
    setViewportWidth(null);
    useChatV2Store.getState().reset();
  });

  it("starts from the default snapshot", () => {
    const state = useChatV2Store.getState();
    expect(state.leftCollapsed).toBe(false);
    expect(state.rightCollapsed).toBe(false);
    expect(state.activeDockTab).toBe("files");
    expect(state.leftWidth).toBe(DEFAULT_CHAT_V2_SNAPSHOT.leftWidth);
    expect(state.rightWidth).toBe(DEFAULT_CHAT_V2_SNAPSHOT.rightWidth);
  });

  it("starts collapsed on narrow viewports so the conversation is immediately visible", () => {
    setViewportWidth(390);
    useChatV2Store.getState().reset();
    const state = useChatV2Store.getState();
    expect(state.leftCollapsed).toBe(true);
    expect(state.rightCollapsed).toBe(true);
    expect(state.activeDockTab).toBe("files");
    expect(readPersistedSnapshot()).toMatchObject({ leftCollapsed: true, rightCollapsed: true });
  });

  it("toggles the left rail collapsed state", () => {
    useChatV2Store.getState().toggleLeft();
    expect(useChatV2Store.getState().leftCollapsed).toBe(true);
    useChatV2Store.getState().toggleLeft();
    expect(useChatV2Store.getState().leftCollapsed).toBe(false);
  });

  it("toggles the right dock collapsed state", () => {
    useChatV2Store.getState().toggleRight();
    expect(useChatV2Store.getState().rightCollapsed).toBe(true);
  });

  it("switches dock tab and expands a collapsed dock", () => {
    useChatV2Store.getState().toggleRight();
    expect(useChatV2Store.getState().rightCollapsed).toBe(true);
    useChatV2Store.getState().setActiveDockTab("terminal");
    expect(useChatV2Store.getState().activeDockTab).toBe("terminal");
    expect(useChatV2Store.getState().rightCollapsed).toBe(false);
  });

  it("collapses the dock when clicking the already-active tab", () => {
    useChatV2Store.getState().setActiveDockTab("terminal");
    expect(useChatV2Store.getState().rightCollapsed).toBe(false);
    useChatV2Store.getState().setActiveDockTab("terminal");
    expect(useChatV2Store.getState().activeDockTab).toBe("terminal");
    expect(useChatV2Store.getState().rightCollapsed).toBe(true);
  });

  it("rejects unknown dock tabs", () => {
    useChatV2Store.getState().setActiveDockTab("nope" as never);
    expect(useChatV2Store.getState().activeDockTab).toBe("files");
  });

  it("clamps rail widths", () => {
    useChatV2Store.getState().setLeftWidth(10);
    expect(useChatV2Store.getState().leftWidth).toBe(200);
    useChatV2Store.getState().setLeftWidth(5000);
    expect(useChatV2Store.getState().leftWidth).toBe(600);
    useChatV2Store.getState().setRightWidth(Number.NaN);
    expect(useChatV2Store.getState().rightWidth).toBe(DEFAULT_CHAT_V2_SNAPSHOT.rightWidth);
  });

  it("persists snapshot to localStorage and rehydrates", () => {
    useChatV2Store.getState().toggleLeft();
    useChatV2Store.getState().setActiveDockTab("tasks");
    writePersistedSnapshot(useChatV2Store.getState());
    useChatV2Store.getState().reset();
    expect(useChatV2Store.getState().leftCollapsed).toBe(false);
    const persisted = readPersistedSnapshot();
    expect(persisted).not.toBeNull();
    useChatV2Store.setState(persisted!);
    expect(useChatV2Store.getState().leftCollapsed).toBe(true);
    expect(useChatV2Store.getState().activeDockTab).toBe("tasks");
  });

  it("returns null for malformed persisted payloads", () => {
    window.localStorage.setItem(CHAT_V2_STORAGE_KEY, "{not json");
    expect(readPersistedSnapshot()).toBeNull();
    window.localStorage.setItem(CHAT_V2_STORAGE_KEY, JSON.stringify({ activeDockTab: "bad" }));
    expect(readPersistedSnapshot()).toBeNull();
  });

  it("provides an idempotent close action for returning to chat", () => {
    useChatV2Store.getState().setActiveDockTab("status");
    expect(useChatV2Store.getState().rightCollapsed).toBe(false);

    useChatV2Store.getState().closeRight();
    expect(useChatV2Store.getState().rightCollapsed).toBe(true);
    expect(useChatV2Store.getState().activeDockTab).toBe("status");

    useChatV2Store.getState().closeRight();
    expect(useChatV2Store.getState().rightCollapsed).toBe(true);
  });

  it("exposes all five dock tabs in order", () => {
    expect(DOCK_TABS).toEqual(["files", "terminal", "tasks", "preview", "status"]);
  });

  it("reset restores defaults", () => {
    useChatV2Store.getState().toggleLeft();
    useChatV2Store.getState().setLeftWidth(300);
    useChatV2Store.getState().reset();
    expect(useChatV2Store.getState().leftCollapsed).toBe(false);
    expect(useChatV2Store.getState().leftWidth).toBe(DEFAULT_CHAT_V2_SNAPSHOT.leftWidth);
  });
});
