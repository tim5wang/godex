import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  createMockTerminal,
  MOCK_TERMINAL_POLL_MS,
  pollMockTerminal,
  shouldKeepPolling,
  writeMockTerminalInput,
  type CreateTerminalResponse,
  type TerminalInputRequest,
  type TerminalOutputChunk,
} from "../../lib/terminalMock";

// P3 / T7 (SPEC §4.4 + M0 doc §3.5): the chat workspace mounts
// <TerminalPanel> inside one of the CenterGrid cells (PC) or as the
// "terminal" tab of <MobileWorkspaceTabs> (mobile). The panel is
// responsible for:
//   1. Spinning up an xterm.js Terminal + FitAddon on mount.
//   2. Pulling output via the polling client (v1.0 mock, v2.0 real).
//   3. Echoing user keystrokes back through the client (v1.0 mock
//      echoes locally; v2.0 forwards to the Go PTY).
//   4. Cleaning up on unmount.
//
// The component owns its own xterm instance + DOM ref. It does NOT
// share state with the layout store (the store is grid-level only —
// which cell holds the terminal — and the terminal itself is local
// to this panel). v2.0 will add a `terminalId` to the layout store
// so the same terminal survives a cell move; that is intentionally
// not in v1.0 to keep the scope small.

export type TerminalPanelProps = {
  /** Test seam: override the polling client. Default = mock client. */
  createTerminal?: () => CreateTerminalResponse;
  pollTerminal?: (id: string, cursor: number, tick: number) => TerminalOutputChunk;
  writeTerminalInput?: (req: TerminalInputRequest, tick: number) => TerminalOutputChunk;
  /** Test seam: override the poll interval. */
  pollIntervalMs?: number;
  /** Test seam: data-testid for the host div. */
  testId?: string;
};

export function TerminalPanel(props: TerminalPanelProps) {
  const {
    createTerminal = createMockTerminal,
    pollTerminal = pollMockTerminal,
    writeTerminalInput = writeMockTerminalInput,
    pollIntervalMs = MOCK_TERMINAL_POLL_MS,
    testId = "terminal-panel",
  } = props;

  const hostRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const cursorRef = useRef<number>(0);
  const tickRef = useRef<number>(0);
  const idRef = useRef<string>("");
  const stoppedRef = useRef<boolean>(false);

  useEffect(() => {
    if (!hostRef.current) return;
    const term = new Terminal({
      convertEol: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
      fontSize: 12,
      theme: { background: "#0b1020" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    // Seed the terminal with a mock PTY.
    const { terminalId } = createTerminal();
    idRef.current = terminalId;
    const first = pollTerminal(terminalId, 0, 0);
    cursorRef.current = first.cursor;
    tickRef.current = 1;
    term.write(first.data);

    // Polling loop. setInterval is fine for v1.0 (mock data);
    // v2.0 will swap to EventSource / WebSocket.
    const handle = window.setInterval(() => {
      if (stoppedRef.current) return;
      const next = pollTerminal(idRef.current, cursorRef.current, tickRef.current);
      cursorRef.current = next.cursor;
      tickRef.current += 1;
      term.write(next.data);
      if (!shouldKeepPolling(next, true)) {
        window.clearInterval(handle);
      }
    }, pollIntervalMs);

    // User keystroke echo (mock). v2.0 will write to the Go PTY
    // and the polling loop will read the echo back.
    const onData = term.onData((data) => {
      const req: TerminalInputRequest = { terminalId: idRef.current, data };
      const echo = writeTerminalInput(req, tickRef.current);
      tickRef.current += 1;
      term.write(echo.data);
    });

    const onResize = () => {
      try {
        fit.fit();
      } catch {
        // host not yet attached / detached — safe to ignore
      }
    };
    window.addEventListener("resize", onResize);

    return () => {
      stoppedRef.current = true;
      window.clearInterval(handle);
      window.removeEventListener("resize", onResize);
      onData.dispose();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [createTerminal, pollTerminal, writeTerminalInput, pollIntervalMs]);

  return (
    <div
      ref={hostRef}
      data-testid={testId}
      data-terminal-id={idRef.current || ""}
      className="terminal-panel"
      style={{ width: "100%", height: "100%", minHeight: 0, background: "#0b1020", padding: 4 }}
    />
  );
}
