import { describe, expect, it, beforeEach } from "vitest";
import {
  createQuickScript,
  loadQuickScripts,
  saveQuickScripts,
  type QuickScript,
} from "../src/lib/terminalQuickScripts";

const STORAGE_KEY = "godex.terminal.quickScripts";

// Vitest's default environment is `node` (no `window` / `localStorage`), so
// polyfill an in-memory shim per test — same pattern as layoutPersistence.test.ts.
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
  const win = globalThis as unknown as { window?: { localStorage: typeof shim }; localStorage?: typeof shim };
  win.window = { localStorage: shim };
  // The module under test references the bare `localStorage` global (browser
  // semantics), so expose the shim there too — `window.localStorage` alone is
  // not enough under Vitest's node environment.
  win.localStorage = shim;
  return shim;
}

describe("terminalQuickScripts", () => {
  beforeEach(() => {
    installLocalStorageShim();
  });

  it("returns an empty list when nothing is stored", () => {
    expect(loadQuickScripts()).toEqual([]);
  });

  it("round-trips scripts through localStorage", () => {
    const scripts: QuickScript[] = [
      { id: "qs-1", name: "git status", command: "git status" },
      { id: "qs-2", name: "ls", command: "ls -la" },
    ];
    saveQuickScripts(scripts);
    expect(loadQuickScripts()).toEqual(scripts);
  });

  it("ignores corrupted JSON without throwing", () => {
    localStorage.setItem(STORAGE_KEY, "{not json");
    expect(loadQuickScripts()).toEqual([]);
  });

  it("ignores non-array payloads", () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ name: "x" }));
    expect(loadQuickScripts()).toEqual([]);
  });

  it("drops malformed entries but keeps valid ones", () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify([
        { id: "qs-ok", name: "ok", command: "echo ok" },
        { id: "qs-bad", name: 42 },
        null,
      ]),
    );
    expect(loadQuickScripts()).toEqual([{ id: "qs-ok", name: "ok", command: "echo ok" }]);
  });

  it("caps the stored list at MAX_SCRIPTS", () => {
    const many: QuickScript[] = Array.from({ length: 40 }, (_, i) => ({
      id: `qs-${i}`,
      name: `s${i}`,
      command: `echo ${i}`,
    }));
    saveQuickScripts(many);
    expect(loadQuickScripts()).toHaveLength(24);
  });

  it("creates a script with trimmed fields and generated id", () => {
    const s = createQuickScript("  git log  ", "  git log --oneline  ");
    expect(s.name).toBe("git log");
    expect(s.command).toBe("git log --oneline");
    expect(s.id).toMatch(/^qs-/);
  });

  it("falls back to the command's first word when name is blank", () => {
    const s = createQuickScript("   ", "  ls -la  ");
    expect(s.name).toBe("ls");
  });

  it("falls back to 'script' when both name and command are blank", () => {
    const s = createQuickScript("", "");
    expect(s.name).toBe("script");
  });
});
