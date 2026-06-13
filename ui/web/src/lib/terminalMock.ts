// P3 / T7 (SPEC §4.4 + M0 doc §3.5): v1.0 polling-fallback terminal
// client. The real PTY channel (CreateTerminal / TerminalOutput /
// KillTerminal / ReleaseTerminal) lives in the Go side
// (internal/acp/server/agent.go) and is exposed over ACP. Until a
// follow-up commit wires the HTTP/SSE endpoints, the Web client
// simulates a PTY: it generates deterministic output on a polling
// interval and echoes user input back into the buffer. The shape
// (create -> poll loop with cursor -> write input -> kill) mirrors
// the real protocol so the v2.0 swap is a one-file change.
//
// This module exports only pure functions and a thin closure-based
// polling client; no React, no IO. The renderer wires it into
// <TerminalPanel> in features/terminal/TerminalPanel.tsx.

export type TerminalOutputChunk = {
  terminalId: string;
  cursor: number; // monotonically increasing; client tracks it
  data: string;
  exited?: boolean;
};

export type CreateTerminalResponse = {
  terminalId: string;
  initialCursor: number;
};

export type TerminalInputRequest = {
  terminalId: string;
  data: string;
};

/**
 * Create a mock terminal. v2.0 will POST to /v1/terminal/create and
 * ask the Go runtime to spawn a PTY in the agent sandbox. For now we
 * return a synthetic id + cursor 0 and seed the buffer with a
 * friendly banner so the user sees something on first render.
 */
export function createMockTerminal(): CreateTerminalResponse {
  const terminalId = `term-${Math.random().toString(36).slice(2, 10)}`;
  return { terminalId, initialCursor: 0 };
}

/**
 * Pull the next batch of output from a mock terminal. The function is
 * deterministic on the cursor: at any given cursor it produces the
 * same chunk. Real v2.0 implementation will return whatever has been
 * written to the PTY between [cursor, cursor'].
 *
 * Chunks are short (one mock line per poll) so the polling cadence
 * is observable in tests. The function also accepts a small
 * `tick` counter so successive calls return different chunks.
 */
export function pollMockTerminal(terminalId: string, cursor: number, tick: number): TerminalOutputChunk {
  if (cursor < 0) cursor = 0;
  const lines = [
    `\x1b[32m${terminalId}\x1b[0m ready (mock PTY v1.0).\r\n`,
    "Type something. Polling fallback is active; real PTY lands in v2.0.\r\n",
    `[tick=${tick}] $ `,
  ];
  const idx = cursor % lines.length;
  return {
    terminalId,
    cursor: cursor + 1,
    data: lines[idx],
    exited: false,
  };
}

/**
 * Apply a write-input event to the in-memory echo buffer. v2.0 will
 * POST the input to the Go runtime; for now we just echo it back
 * with a CRLF so the user sees their characters rendered. Returned
 * chunk uses the same cursor-passing convention as pollMockTerminal
 * so the renderer can append it to the existing xterm buffer.
 */
export function writeMockTerminalInput(input: TerminalInputRequest, tick: number): TerminalOutputChunk {
  return {
    terminalId: input.terminalId,
    cursor: tick + 1,
    data: `${input.data}\r\n`,
  };
}

/**
 * Hard-coded "polling interval" (ms) for v1.0. Real v2.0 will read
 * this from the server's stream cadence. Kept in one place so tests
 * can advance virtual time without race conditions.
 */
export const MOCK_TERMINAL_POLL_MS = 500;

/**
 * Predicate: should we keep polling? Returns false when the chunk
 * marks the terminal as exited, or when the renderer is unmounted
 * (caller passes `false` for `active`).
 */
export function shouldKeepPolling(chunk: TerminalOutputChunk | null, active: boolean): boolean {
  if (!active) return false;
  if (chunk && chunk.exited) return false;
  return true;
}
