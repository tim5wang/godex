// v2.0 terminal HTTP client (SPEC M1+ candidate A).
// Replaces lib/terminalMock.ts when the Go backend terminal endpoints
// are wired.  Keeps the exact same public signatures so TerminalPanel
// can swap the import without changes.
//
// Architecture: a background polling loop (setInterval) fetches
// output from the Go backend into an in-memory buffer.  The sync
// functions (createTerminal / pollTerminal / writeTerminalInput) only
// read/write the local buffer, so the renderer remains synchronous
// and the terminal feels real-time.
//
// v1.0 (terminalMock.ts) → v2.0 (this file) is a drop-in replacement.

import type { CreateTerminalResponse, TerminalInputRequest, TerminalOutputChunk } from "./terminalMock";

// ---- internal buffer per terminal ----

type TerminalState = {
  cursor: number;
  buffer: string;
  exited: boolean;
  poller: ReturnType<typeof setInterval> | null;
  baseUrl: string;
  serverTerminalId?: string;
};

const terminals = new Map<string, TerminalState>();

function getBaseUrl(): string {
  // In dev the Vite proxy forwards /v1 to the Go backend.
  // In production the Go backend serves the API at the same origin.
  return "";
}

async function fetchOutput(terminalId: string): Promise<void> {
  const state = terminals.get(terminalId);
  if (!state || state.exited) return;
  const serverTerminalId = state.serverTerminalId ?? terminalId;
  try {
    const resp = await fetch(
      `${state.baseUrl}/v1/terminal/${encodeURIComponent(serverTerminalId)}/output?cursor=${state.cursor}`
    );
    if (!resp.ok) {
      if (resp.status === 404) {
        state.exited = true;
        stopPolling(terminalId);
      }
      return;
    }
    const chunk: TerminalOutputChunk = await resp.json();
    state.cursor = chunk.cursor;
    state.buffer += chunk.data;
    if (chunk.exited) {
      state.exited = true;
      stopPolling(terminalId);
    }
  } catch {
    // Network error — keep polling, it may recover.
  }
}

function startPolling(terminalId: string): void {
  const state = terminals.get(terminalId);
  if (!state) return;
  if (state.poller) return;
  state.poller = setInterval(() => {
    void fetchOutput(terminalId);
  }, TERMINAL_POLL_MS);
}

function stopPolling(terminalId: string): void {
  const state = terminals.get(terminalId);
  if (!state) return;
  if (state.poller) {
    clearInterval(state.poller);
    state.poller = null;
  }
}

// ---- public API (same signatures as terminalMock.ts) ----

/**
 * Create a real terminal backed by the Go PTY.
 * Fires an async POST to /v1/terminal/create and starts the
 * background polling loop. Returns immediately with a client-side
 * terminalId so the renderer does not block.
 */
export function createTerminal(workspaceDir?: string): CreateTerminalResponse {
  const terminalId = `term-${Math.random().toString(36).slice(2, 10)}`;
  const baseUrl = getBaseUrl();
  terminals.set(terminalId, {
    cursor: 0,
    buffer: "",
    exited: false,
    poller: null,
    baseUrl,
  });

  // Fire-and-forget: tell the Go backend to spawn a shell.
  void (async () => {
    try {
      const resp = await fetch(`${baseUrl}/v1/terminal/create`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ workspaceDir: workspaceDir ?? "." }),
      });
      if (!resp.ok) {
        const state = terminals.get(terminalId);
        if (state) state.exited = true;
        return;
      }
      const created = await resp.json() as Partial<CreateTerminalResponse>;
      const state = terminals.get(terminalId);
      if (!state) return;
      state.serverTerminalId = created.terminalId || terminalId;
      // Start polling once the backend confirms creation.
      // If the create fails, poll will get 404 and mark exited.
      startPolling(terminalId);
      // Immediately fetch the first chunk (e.g. shell banner).
      await fetchOutput(terminalId);
    } catch {
      const state = terminals.get(terminalId);
      if (state) state.exited = true;
    }
  })();

  return { terminalId, initialCursor: 0 };
}

/**
 * Read the latest buffered output since the given cursor.
 * Called synchronously by the renderer's setInterval loop.
 * Returns empty data if nothing new has arrived yet.
 */
export function pollTerminal(
  terminalId: string,
  cursor: number,
  _tick: number,
): TerminalOutputChunk {
  const state = terminals.get(terminalId);
  if (!state) {
    return { terminalId, cursor, data: "", exited: true };
  }
  if (cursor < 0) cursor = 0;
  const currentCursor = state.cursor;
  if (cursor >= currentCursor) {
    return {
      terminalId,
      cursor: currentCursor,
      data: "",
      exited: state.exited,
    };
  }
  const data = state.buffer.slice(cursor - (currentCursor - state.buffer.length));
  return {
    terminalId,
    cursor: currentCursor,
    data,
    exited: state.exited,
  };
}

/**
 * Write user input to the terminal. Fires an async POST and
 * returns an empty chunk immediately — the shell echo will appear
 * in the next polling cycle.
 */
export function writeTerminalInput(
  input: TerminalInputRequest,
  _tick: number,
): TerminalOutputChunk {
  void (async () => {
    try {
      const state = terminals.get(input.terminalId);
      if (!state || state.exited) return;
      const serverTerminalId = state.serverTerminalId ?? input.terminalId;
      await fetch(`${state.baseUrl}/v1/terminal/${encodeURIComponent(serverTerminalId)}/input`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ data: input.data }),
      });
    } catch {
      // Best-effort.
    }
  })();
  return {
    terminalId: input.terminalId,
    cursor: 0,
    data: "",
  };
}

/**
 * Clean up a terminal — stops polling and tells the backend to
 * kill the PTY. Call this when the TerminalPanel unmounts.
 */
export function destroyTerminal(terminalId: string): void {
  stopPolling(terminalId);
  const state = terminals.get(terminalId);
  if (!state) return;
  terminals.delete(terminalId);
  const serverTerminalId = state.serverTerminalId ?? terminalId;
  void (async () => {
    try {
      await fetch(`${state.baseUrl}/v1/terminal/${encodeURIComponent(serverTerminalId)}`, {
        method: "DELETE",
      });
    } catch {
      // Best-effort.
    }
  })();
}

/** Polling interval in ms. Matches mock v1.0 cadence. */
export const TERMINAL_POLL_MS = 500;

/**
 * Predicate: should the renderer keep polling?
 * Same logic as terminalMock.shouldKeepPolling.
 */
export function shouldKeepPolling(
  chunk: TerminalOutputChunk | null,
  active: boolean,
): boolean {
  if (!active) return false;
  if (chunk && chunk.exited) return false;
  return true;
}
