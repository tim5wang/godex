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

export type TerminalStatus = "idle" | "connecting" | "connected" | "error" | "exited";

type TerminalState = {
  cursor: number;
  buffer: string;
  exited: boolean;
  status: TerminalStatus;
  error: string;
  poller: ReturnType<typeof setInterval> | null;
  baseUrl: string;
  serverTerminalId?: string;
  pendingInput: string[];
  createdAt: number;
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

async function postTerminalInput(state: TerminalState, terminalId: string, data: string): Promise<void> {
  const serverTerminalId = state.serverTerminalId ?? terminalId;
  await fetch(`${state.baseUrl}/v1/terminal/${encodeURIComponent(serverTerminalId)}/input`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ data }),
  });
}

function flushPendingInput(terminalId: string): void {
  const state = terminals.get(terminalId);
  if (!state || state.exited || !state.serverTerminalId || state.pendingInput.length === 0) return;
  const pending = [...state.pendingInput];
  state.pendingInput = [];
  void (async () => {
    for (let index = 0; index < pending.length; index += 1) {
      const data = pending[index]!;
      try {
        await postTerminalInput(state, terminalId, data);
      } catch {
        state.pendingInput = [...pending.slice(index), ...state.pendingInput];
        return;
      }
    }
  })();
}

// ---- public API (same signatures as terminalMock.ts) ----

/**
 * Create a real terminal backed by the Go PTY.
 * Fires an async POST to /v1/terminal/create and starts the
 * background polling loop. Returns immediately with a client-side
 * terminalId so the renderer does not block.
 */
export type TerminalExecutionConfig = {
  mode?: string;
  sshTarget?: string;
  sshWorkspace?: string;
  sshOptions?: string[];
  dockerImage?: string;
  dockerNetwork?: string;
};

export function createTerminal(workspaceDir?: string, execution?: TerminalExecutionConfig): CreateTerminalResponse {
  const terminalId = `term-${Math.random().toString(36).slice(2, 10)}`;
  const baseUrl = getBaseUrl();
  terminals.set(terminalId, {
    cursor: 0,
    buffer: "",
    exited: false,
    status: "connecting",
    error: "",
    poller: null,
    baseUrl,
    pendingInput: [],
    createdAt: Date.now(),
  });

  // Fire-and-forget: tell the Go backend to spawn a shell.
  void (async () => {
    try {
      const resp = await fetch(`${baseUrl}/v1/terminal/create`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          workspaceDir: workspaceDir ?? ".",
          ...(execution?.mode ? { executionMode: execution.mode } : {}),
          ...(execution?.sshTarget ? { sshTarget: execution.sshTarget } : {}),
          ...(execution?.sshWorkspace ? { sshWorkspace: execution.sshWorkspace } : {}),
          ...(execution?.sshOptions?.length ? { sshOptions: execution.sshOptions } : {}),
          ...(execution?.dockerImage ? { dockerImage: execution.dockerImage } : {}),
          ...(execution?.dockerNetwork ? { dockerNetwork: execution.dockerNetwork } : {}),
        }),
      });
      if (!resp.ok) {
        const state = terminals.get(terminalId);
        if (state) {
          state.status = "error";
          state.error = `Server returned ${resp.status}`;
          state.exited = true;
        }
        return;
      }
      const created = await resp.json() as Partial<CreateTerminalResponse>;
      const state = terminals.get(terminalId);
      if (!state) return;
      state.serverTerminalId = created.terminalId || terminalId;
      state.status = "connected";
      flushPendingInput(terminalId);
      // Start polling once the backend confirms creation.
      // If the create fails, poll will get 404 and mark exited.
      startPolling(terminalId);
      // Immediately fetch the first chunk (e.g. shell banner).
      await fetchOutput(terminalId);
    } catch {
      const state = terminals.get(terminalId);
      if (state) {
        state.status = "error";
        state.error = "Failed to connect to terminal backend";
        state.exited = true;
      }
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
      if (!state.serverTerminalId) {
        state.pendingInput.push(input.data);
        return;
      }
      await postTerminalInput(state, input.terminalId, input.data);
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

/**
 * Notify the backend of a terminal resize so the PTY gets the
 * new dimensions (needed for line-wrapping in the shell).
 */
export function resizeTerminal(terminalId: string, cols: number, rows: number): void {
  const state = terminals.get(terminalId);
  if (!state || state.exited || !state.serverTerminalId) return;
  void (async () => {
    try {
      await fetch(`${state.baseUrl}/v1/terminal/${encodeURIComponent(state.serverTerminalId!)}/resize`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cols, rows }),
      });
    } catch {
      // Best-effort.
    }
  })();
}

/**
 * Get the current status of a terminal session.
 * TerminalPanel calls this to detect errors / connection failures.
 */
export function getTerminalStatus(terminalId: string): { status: TerminalStatus; error: string } {
  const state = terminals.get(terminalId);
  if (!state) return { status: "error", error: "Terminal not found" };

  // Timeout: if still connecting after 8s, treat as error.
  if (state.status === "connecting" && Date.now() - state.createdAt > TERMINAL_CONNECT_TIMEOUT_MS) {
    state.status = "error";
    state.error = "Terminal backend unreachable (timeout)";
    state.exited = true;
    stopPolling(terminalId);
  }

  if (state.exited && state.status === "connecting") {
    return { status: "error", error: state.error || "Terminal exited unexpectedly" };
  }

  return { status: state.status, error: state.error };
}

/** Polling interval in ms. Matches mock v1.0 cadence. */
export const TERMINAL_POLL_MS = 500;

/** Max time to wait for backend to respond after create (ms). */
export const TERMINAL_CONNECT_TIMEOUT_MS = 8000;

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
