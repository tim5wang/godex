import { describe, expect, it, beforeEach, afterEach } from "vitest";

import { useLayoutStore } from "../src/store/layout";
import {
  LAYOUT_STORAGE_KEY,
  applyLayoutSnapshot,
  buildStoragePayload,
  clearPersistedLayoutSnapshot,
  hydrateLayoutSnapshot,
  readPersistedLayoutSnapshot,
  serializeLayoutSnapshot,
  writePersistedLayoutSnapshot,
} from "../src/store/layoutPersistence";
import { messages } from "../src/i18n/messages";

// Vitest's default environment is `node` (no `window` /
// `localStorage`). The P4 / T8 persistence helpers need a real
// `window.localStorage`, so we polyfill it with an in-memory shim
// in `beforeEach`. The shim implements just enough of the Web
// Storage API for the helpers under test (getItem / setItem /
// removeItem / clear) and is recreated per test so persistence
// state never leaks between cases.
function installLocalStorageShim() {
  const store = new Map<string, string>();
  const shim = {
    getItem(key: string): string | null {
      return store.has(key) ? store.get(key)! : null;
    },
    setItem(key: string, value: string): void {
      store.set(key, String(value));
    },
    removeItem(key: string): void {
      store.delete(key);
    },
    clear(): void {
      store.clear();
    },
    key(i: number): string | null {
      return Array.from(store.keys())[i] ?? null;
    },
    get length(): number {
      return store.size;
    },
  };
  (globalThis as unknown as { window: { localStorage: typeof shim } }).window = {
    localStorage: shim,
  };
  return shim;
}

function reset() {
  useLayoutStore.getState().reset();
  clearPersistedLayoutSnapshot();
}

beforeEach(() => {
  installLocalStorageShim();
  reset();
});
afterEach(() => reset());

// P4 / T8 + T10 + T11 contract tests for the layout persistence
// layer + the new i18n keys. We deliberately avoid React / DOM
// renderers (no @testing-library/react) and exercise the store
// through its public action surface.

// ---------------------------------------------------------------------------
// serializeLayoutSnapshot
// ---------------------------------------------------------------------------

describe("serializeLayoutSnapshot (P4 / T8)", () => {
  it("drops the action fields and keeps the snapshot", () => {
    const full = useLayoutStore.getState();
    const snap = serializeLayoutSnapshot(full);
    expect(snap).not.toHaveProperty("toggle");
    expect(snap).not.toHaveProperty("setWidth");
    expect(snap).not.toHaveProperty("setGridPreset");
    expect(snap).not.toHaveProperty("movePanelToGrid");
    expect(snap).not.toHaveProperty("swapPanelInGrid");
    expect(snap).not.toHaveProperty("swapGridSlots");
    expect(snap).not.toHaveProperty("setGridRatio");
    expect(snap).not.toHaveProperty("toggleGridRowCollapse");
    expect(snap).not.toHaveProperty("setMobileActiveTab");
    expect(snap).not.toHaveProperty("openTaskCenterDrawer");
    expect(snap).not.toHaveProperty("closeTaskCenterDrawer");
    expect(snap).not.toHaveProperty("reset");
  });

  it("preserves the data fields verbatim", () => {
    useLayoutStore.getState().toggle("files");
    useLayoutStore.getState().setGridPreset("single");
    const snap = serializeLayoutSnapshot(useLayoutStore.getState());
    expect(snap.panels.files.collapsed).toBe(false);
    expect(snap.centerGridPreset).toBe("single");
    expect(snap.centerGrid.topLeft).toBe("chat");
  });
});

// ---------------------------------------------------------------------------
// hydrateLayoutSnapshot
// ---------------------------------------------------------------------------

describe("hydrateLayoutSnapshot (P4 / T8)", () => {
  it("returns null on empty input", () => {
    expect(hydrateLayoutSnapshot(null)).toBeNull();
    expect(hydrateLayoutSnapshot("")).toBeNull();
  });

  it("returns null on malformed JSON", () => {
    expect(hydrateLayoutSnapshot("not-json")).toBeNull();
  });

  it("returns null on non-object payloads", () => {
    expect(hydrateLayoutSnapshot("null")).toBeNull();
    expect(hydrateLayoutSnapshot("42")).toBeNull();
    expect(hydrateLayoutSnapshot('"a string"')).toBeNull();
  });

  it("returns the parsed object for valid JSON", () => {
    const original = serializeLayoutSnapshot(useLayoutStore.getState());
    const raw = JSON.stringify(original);
    const hydrated = hydrateLayoutSnapshot(raw);
    expect(hydrated).toEqual(original);
  });
});

// ---------------------------------------------------------------------------
// readPersistedLayoutSnapshot / writePersistedLayoutSnapshot
// ---------------------------------------------------------------------------

describe("writePersistedLayoutSnapshot + readPersistedLayoutSnapshot (P4 / T8 round-trip)", () => {
  it("round-trips a snapshot through localStorage", () => {
    useLayoutStore.getState().toggle("terminal");
    useLayoutStore.getState().setMobileActiveTab("files");
    writePersistedLayoutSnapshot(serializeLayoutSnapshot(useLayoutStore.getState()));
    const raw = window.localStorage.getItem(LAYOUT_STORAGE_KEY);
    expect(raw).toBeTruthy();
    const rehydrated = readPersistedLayoutSnapshot();
    expect(rehydrated).not.toBeNull();
    expect(rehydrated?.panels.terminal.collapsed).toBe(false);
    expect(rehydrated?.mobileActiveTab).toBe("files");
  });  it("returns null when the key is absent", () => {
    window.localStorage.removeItem(LAYOUT_STORAGE_KEY);
    expect(readPersistedLayoutSnapshot()).toBeNull();
  });

  it("returns null on a corrupted payload", () => {
    window.localStorage.setItem(LAYOUT_STORAGE_KEY, "{not-json");
    expect(readPersistedLayoutSnapshot()).toBeNull();
  });

  it("is safe to call when window is undefined (SSR-style)", () => {
    // Temporarily remove the shim and confirm the helpers fall
    // back gracefully. We restore the shim in afterEach via
    // beforeEach's installLocalStorageShim() in the next test.
    const saved = (globalThis as unknown as { window: unknown }).window;
    (globalThis as unknown as { window: undefined }).window = undefined;
    try {
      writePersistedLayoutSnapshot(serializeLayoutSnapshot(useLayoutStore.getState()));
      clearPersistedLayoutSnapshot();
      // No assertion on the shim — it was never touched.
    } finally {
      (globalThis as unknown as { window: unknown }).window = saved;
    }
  });
});

// ---------------------------------------------------------------------------
// clearPersistedLayoutSnapshot
// ---------------------------------------------------------------------------

describe("clearPersistedLayoutSnapshot (P4 / T8)", () => {
  it("removes the entry from localStorage", () => {
    writePersistedLayoutSnapshot(serializeLayoutSnapshot(useLayoutStore.getState()));
    expect(window.localStorage.getItem(LAYOUT_STORAGE_KEY)).toBeTruthy();
    clearPersistedLayoutSnapshot();
    expect(window.localStorage.getItem(LAYOUT_STORAGE_KEY)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// buildStoragePayload + applyLayoutSnapshot
// ---------------------------------------------------------------------------

describe("buildStoragePayload + applyLayoutSnapshot (P4 / T8 cross-tab bridge)", () => {
  it("buildStoragePayload round-trips a snapshot", () => {
    const original = serializeLayoutSnapshot(useLayoutStore.getState());
    const payload = buildStoragePayload(original);
    expect(payload.key).toBe(LAYOUT_STORAGE_KEY);
    const rehydrated = hydrateLayoutSnapshot(payload.newValue);
    expect(rehydrated).toEqual(original);
  });

  it("applyLayoutSnapshot returns the snapshot fields verbatim for setState", () => {
    const original = serializeLayoutSnapshot(useLayoutStore.getState());
    const applied = applyLayoutSnapshot(original);
    expect(applied).toEqual(original);
  });
});

// ---------------------------------------------------------------------------
// reset() integration
// ---------------------------------------------------------------------------

describe("store reset() wipes the persisted snapshot (P4 / T8)", () => {
  it("after a write + reset the localStorage entry is gone", () => {
    writePersistedLayoutSnapshot(serializeLayoutSnapshot(useLayoutStore.getState()));
    expect(window.localStorage.getItem(LAYOUT_STORAGE_KEY)).toBeTruthy();
    useLayoutStore.getState().reset();
    expect(window.localStorage.getItem(LAYOUT_STORAGE_KEY)).toBeNull();
    // And the in-memory store is back to the factory default.
    expect(useLayoutStore.getState().panels.terminal.collapsed).toBe(true);
    expect(useLayoutStore.getState().centerGridPreset).toBe("topFilesChat_bottomTerminal");
  });
});

// ---------------------------------------------------------------------------
// i18n keys (P4 / T10)
// ---------------------------------------------------------------------------

describe("i18n keys for mobile.tabs.* + terminal.* + panel.* (P4 / T10)", () => {
  const expectedEn: Record<string, string> = {
    "mobile.tabs.chat": "Chat",
    "mobile.tabs.terminal": "Terminal",
    "mobile.tabs.files": "Files",
    "mobile.tabs.drawer": "Drawer",
    "mobile.tabs.tasks": "Task center",
    "terminal.title": "Terminal",
    "terminal.ready": "ready (mock PTY v1.0).",
    "terminal.hint": "Type something. Polling fallback is active; real PTY lands in v2.0.",
    "terminal.shortcutHint": "Toggle with Ctrl/Cmd + `",
    "panel.toggleTerminal": "Toggle terminal",
    "panel.collapse": "Collapse",
    "panel.expand": "Expand",
  };
  const expectedZh: Record<string, string> = {
    "mobile.tabs.chat": "聊天",
    "mobile.tabs.terminal": "终端",
    "mobile.tabs.files": "文件",
    "mobile.tabs.drawer": "抽屉",
    "mobile.tabs.tasks": "任务中心",
    "terminal.title": "终端",
    "terminal.ready": "已就绪（Mock PTY v1.0）。",
    "terminal.hint": "请输入。v1.0 使用轮询回退模式，真正的 PTY 将在 v2.0 接入。",
    "terminal.shortcutHint": "使用 Ctrl/Cmd + ` 切换",
    "panel.toggleTerminal": "切换终端",
    "panel.collapse": "收起",
    "panel.expand": "展开",
  };

  const lookup = (locale: "en" | "zh", key: string): unknown => {
    // `messages` is `as const satisfies Record<Locale, ...>`, so
    // deep index access (e.g. `messages.en.mobile.tabs.chat`) is
    // a type error under strict TypeScript. The plain-object
    // cast below is the minimum-impact escape hatch: we only use
    // it to walk the tree at runtime in the test, and the
    // expected value is a `string` literal we control.
    const root = messages[locale] as unknown as Record<string, unknown>;
    const parts = key.split(".");
    let cursor: unknown = root;
    for (const part of parts) {
      if (!cursor || typeof cursor !== "object") return undefined;
      cursor = (cursor as Record<string, unknown>)[part];
    }
    return cursor;
  };

  for (const [key, expected] of Object.entries(expectedEn)) {
    it(`en.${key} === ${JSON.stringify(expected)}`, () => {
      expect(lookup("en", key)).toBe(expected);
    });
  }

  for (const [key, expected] of Object.entries(expectedZh)) {
    it(`zh.${key} === ${JSON.stringify(expected)}`, () => {
      expect(lookup("zh", key)).toBe(expected);
    });
  }

  // Note: we deliberately do not add a `parity` test that walks
  // the entire `messages` tree with Object.entries + recursion.
  // With the current `as const satisfies Record<Locale, unknown>`
  // shape, vitest's esbuild transpiler + TS 5.9 type checker
  // chain trips a "Maximum call stack size exceeded" on the deep
  // readonly entries. The 24 explicit `it()` blocks above are
  // the contract surface — they cover the exact key set we added
  // in P4 / T10 and any future drift will surface as a missing
  // or wrong value in those tests. A future commit can introduce
  // a stricter tree-walker test once the upstream transpiler
  // type-checker interaction is fixed.
});
