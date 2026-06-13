import { describe, expect, it, beforeEach, afterEach } from "vitest";

import { useLayoutStore } from "../src/store/layout";
import {
  createMockTerminal,
  MOCK_TERMINAL_POLL_MS,
  pollMockTerminal,
  shouldKeepPolling,
  writeMockTerminalInput,
  type CreateTerminalResponse,
  type TerminalOutputChunk,
} from "../src/lib/terminalMock";
import { matchesShortcut } from "../src/hooks/useGlobalKey";

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

// P3 / T7 (SPEC §4.4 + M0 doc §3.5) contract tests for the v1.0
// polling-fallback terminal client + the keyboard shortcut. The
// renderer (TerminalPanel.tsx) is intentionally not a unit test
// target — it owns an xterm instance and a DOM ref, both of which
// require jsdom + @xterm/xterm loaders. The pure helpers below
// cover the v1.0 contract; the v2.0 swap to the real Go PTY will
// replace the mock helpers with a real client and these tests
// will be updated to assert against the real protocol.

describe("createMockTerminal", () => {
  it("returns a fresh terminalId + cursor 0 on every call", () => {
    const a = createMockTerminal();
    const b = createMockTerminal();
    expect(a.terminalId).toMatch(/^term-/);
    expect(b.terminalId).toMatch(/^term-/);
    expect(a.terminalId).not.toBe(b.terminalId);
    expect(a.initialCursor).toBe(0);
    expect(b.initialCursor).toBe(0);
  });

  it("returns an object that conforms to CreateTerminalResponse", () => {
    const r: CreateTerminalResponse = createMockTerminal();
    expect(typeof r.terminalId).toBe("string");
    expect(typeof r.initialCursor).toBe("number");
  });
});

describe("pollMockTerminal", () => {
  it("advances the cursor by exactly 1", () => {
    const c = pollMockTerminal("term-x", 0, 0);
    expect(c.cursor).toBe(1);
    expect(c.terminalId).toBe("term-x");
  });

  it("clamps a negative cursor to 0", () => {
    const c = pollMockTerminal("term-x", -5, 0);
    expect(c.cursor).toBe(1);
  });

  it("rotates through the 3 mock lines (banner, hint, prompt) on successive calls", () => {
    const a = pollMockTerminal("term-x", 0, 0);
    const b = pollMockTerminal("term-x", 1, 1);
    const c = pollMockTerminal("term-x", 2, 2);
    const d = pollMockTerminal("term-x", 3, 3);
    expect(a.data).toContain("ready (mock PTY v1.0)");
    expect(b.data).toContain("Polling fallback is active");
    expect(c.data).toContain("[tick=2]");
    // The rotation wraps; the 4th call returns the same data as the 1st.
    expect(d.data).toBe(a.data);
  });

  it("echoes the terminalId inside the banner chunk", () => {
    const c = pollMockTerminal("term-mine", 0, 0);
    expect(c.data).toContain("term-mine");
  });
});

describe("writeMockTerminalInput", () => {
  it("echoes the input back with a CRLF", () => {
    const c = writeMockTerminalInput({ terminalId: "term-x", data: "ls" }, 7);
    expect(c.terminalId).toBe("term-x");
    expect(c.data).toBe("ls\r\n");
    expect(c.cursor).toBe(8);
  });
});

describe("shouldKeepPolling", () => {
  it("returns false when active=false (panel unmounted)", () => {
    expect(shouldKeepPolling(null, false)).toBe(false);
  });

  it("returns true for a non-exited chunk when active=true", () => {
    const chunk: TerminalOutputChunk = { terminalId: "term-x", cursor: 1, data: "x" };
    expect(shouldKeepPolling(chunk, true)).toBe(true);
    expect(shouldKeepPolling(null, true)).toBe(true);
  });

  it("returns false when the chunk marks the terminal as exited", () => {
    const chunk: TerminalOutputChunk = { terminalId: "term-x", cursor: 1, data: "", exited: true };
    expect(shouldKeepPolling(chunk, true)).toBe(false);
  });
});

describe("MOCK_TERMINAL_POLL_MS", () => {
  it("is a positive integer (default cadence for v1.0)", () => {
    expect(typeof MOCK_TERMINAL_POLL_MS).toBe("number");
    expect(MOCK_TERMINAL_POLL_MS).toBeGreaterThan(0);
  });
});

describe("matchesShortcut (pure keydown predicate)", () => {
  it("matches Ctrl+` only when ctrlKey is true", () => {
    const ctrl: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey"> = { key: "`", ctrlKey: true, metaKey: false };
    const meta: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey"> = { key: "`", ctrlKey: false, metaKey: true };
    const noMod: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey"> = { key: "`", ctrlKey: false, metaKey: false };
    expect(matchesShortcut(ctrl, "`", "Ctrl")).toBe(true);
    expect(matchesShortcut(meta, "`", "Ctrl")).toBe(false);
    expect(matchesShortcut(noMod, "`", "Ctrl")).toBe(false);
  });

  it("matches Cmd+` only when metaKey is true", () => {
    const ctrl: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey"> = { key: "`", ctrlKey: true, metaKey: false };
    const meta: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey"> = { key: "`", ctrlKey: false, metaKey: true };
    expect(matchesShortcut(ctrl, "`", "Cmd")).toBe(false);
    expect(matchesShortcut(meta, "`", "Cmd")).toBe(true);
  });

  it("rejects when the key does not match", () => {
    const e: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey"> = { key: "1", ctrlKey: true, metaKey: false };
    expect(matchesShortcut(e, "`", "Ctrl")).toBe(false);
  });
});

describe("store-level: terminal panel toggle (used by the shortcut)", () => {
  it("default terminal panel is collapsed (SPEC §3.2 'others collapsed')", () => {
    expect(useLayoutStore.getState().panels.terminal.collapsed).toBe(true);
  });

  it("toggle('terminal') flips collapsed and back", () => {
    useLayoutStore.getState().toggle("terminal");
    expect(useLayoutStore.getState().panels.terminal.collapsed).toBe(false);
    useLayoutStore.getState().toggle("terminal");
    expect(useLayoutStore.getState().panels.terminal.collapsed).toBe(true);
  });
});
