import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Space } from "antd";
import { ReloadOutlined, PoweroffOutlined } from "@ant-design/icons";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  createTerminal as createRealTerminal,
  TERMINAL_POLL_MS,
  getTerminalStatus,
  type TerminalExecutionConfig,
  type TerminalStatus,
  pollTerminal as pollRealTerminal,
  resizeTerminal,
  shouldKeepPolling,
  writeTerminalInput as writeRealTerminalInput,
  destroyTerminal,
} from "../../lib/terminalClient";
import type {
  CreateTerminalResponse,
  TerminalInputRequest,
  TerminalOutputChunk,
} from "../../lib/terminalMock";

export type TerminalPanelProps = {
  createTerminal?: (workspaceDir?: string, execution?: TerminalExecutionConfig) => CreateTerminalResponse;
  pollTerminal?: (id: string, cursor: number, tick: number) => TerminalOutputChunk;
  writeTerminalInput?: (req: TerminalInputRequest, tick: number) => TerminalOutputChunk;
  pollIntervalMs?: number;
  testId?: string;
  /** Session working directory for the PTY shell. */
  workspaceDir?: string;
  /** Execution backend config (local/ssh/docker). */
  execution?: TerminalExecutionConfig;
};

const SYS_INFO_BANNER = [
  "\x1b[1;36m┌──────────────────────────────────────────┐\x1b[0m\r\n",
  "\x1b[1;36m│\x1b[0m  \x1b[1;33mGoDex Terminal\x1b[0m                         \x1b[1;36m│\x1b[0m\r\n",
  "\x1b[1;36m│\x1b[0m  Backend: Go PTY (creack/pty)              \x1b[1;36m│\x1b[0m\r\n",
  "\x1b[1;36m│\x1b[0m  Shell:  bash -i                           \x1b[1;36m│\x1b[0m\r\n",
  "\x1b[1;36m└──────────────────────────────────────────┘\x1b[0m\r\n",
  "\r\n",
].join("");

export function TerminalPanel(props: TerminalPanelProps) {
  const {
    createTerminal = createRealTerminal,
    pollTerminal = pollRealTerminal,
    writeTerminalInput = writeRealTerminalInput,
    pollIntervalMs = TERMINAL_POLL_MS,
    testId = "terminal-panel",
    workspaceDir,
    execution,
  } = props;

  const hostRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const cursorRef = useRef(0);
  const tickRef = useRef(0);
  const idRef = useRef("");
  const stoppedRef = useRef(false);
  const outputTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const statusTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const roRef = useRef<ResizeObserver | null>(null);
  const onDataRef = useRef<{ dispose(): void } | null>(null);

  const [status, setStatus] = useState<TerminalStatus>("idle");
  const [errorMsg, setErrorMsg] = useState("");
  const [initKey, setInitKey] = useState(0);

  // ---- helpers ----

  const focusXterm = useCallback(() => {
    const t = termRef.current;
    if (!t) return;
    // Try focusing the native textarea xterm.js uses for input.
    // This is the element that receives keyboard events.
    if (t.textarea) {
      t.textarea.focus();
      return;
    }
    // Fallback: focus the container element.
    const el = t.element;
    if (el instanceof HTMLElement) {
      if (!el.hasAttribute("tabindex")) el.setAttribute("tabindex", "0");
      el.focus();
    }
  }, []);

  const teardown = useCallback(() => {
    stoppedRef.current = true;
    if (statusTimerRef.current) { clearInterval(statusTimerRef.current); statusTimerRef.current = null; }
    if (outputTimerRef.current) { clearInterval(outputTimerRef.current); outputTimerRef.current = null; }
    if (roRef.current) { roRef.current.disconnect(); roRef.current = null; }
    if (onDataRef.current) { onDataRef.current.dispose(); onDataRef.current = null; }
    if (termRef.current) { termRef.current.dispose(); termRef.current = null; }
    fitRef.current = null;
    if (idRef.current) { destroyTerminal(idRef.current); idRef.current = ""; }
  }, []);

  // ---- keep-alive session follow ----
  // Dock tabs stay mounted across switches (ChatPage keep-alive), so a
  // session workspace or execution-backend change must recreate the PTY
  // session explicitly — previously unmount/remount handled this for us.
  const lastWorkspaceRef = useRef<string | undefined>(workspaceDir);
  const lastExecutionRef = useRef<TerminalExecutionConfig | undefined>(execution);
  useEffect(() => {
    const wsChanged = lastWorkspaceRef.current !== workspaceDir;
    const execChanged =
      JSON.stringify(lastExecutionRef.current ?? null) !== JSON.stringify(execution ?? null);
    lastWorkspaceRef.current = workspaceDir;
    lastExecutionRef.current = execution;
    if (wsChanged || execChanged) {
      teardown();
      setInitKey((k) => k + 1);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceDir, execution]);

  // ---- init ----

  useEffect(() => {
    if (!hostRef.current) return;
    stoppedRef.current = false;
    setStatus("connecting");
    setErrorMsg("");

    const term = new Terminal({
      convertEol: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
      fontSize: 12,
      theme: { background: "#0b1020" },
      // allowProposedApi needed for some addons, doesn't affect input.
      allowProposedApi: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    fitRef.current = fit;

    const { terminalId } = createTerminal(workspaceDir, execution);
    idRef.current = terminalId;

    // Defer DOM-dependent init to next animation frame so the
    // browser has completed grid/flex layout (xterm.js docs require
    // the parent to have dimensions when open() is called).
    const raf = requestAnimationFrame(() => {
      if (!hostRef.current || stoppedRef.current) return;

      term.open(hostRef.current);

      // Fit immediately and then again after a microtask — the first
      // fit may see stale dimensions from before the open() reflow.
      try { fit.fit(); } catch { /* detached */ }
      queueMicrotask(() => {
        try { fit.fit(); } catch { /* detached */ }
      });

      // Focus: try the textarea, then the container.
      focusXterm();

      // ResizeObserver for dynamic resize.
      const ro = new ResizeObserver(() => {
        try { fit.fit(); } catch { /* detached */ }
        if (idRef.current && term) {
          resizeTerminal(idRef.current, term.cols, term.rows);
        }
      });
      ro.observe(hostRef.current!);
      roRef.current = ro;

      // Keyboard input → backend.
      onDataRef.current = term.onData((data) => {
        const req: TerminalInputRequest = { terminalId: idRef.current, data };
        writeTerminalInput(req, tickRef.current);
        tickRef.current += 1;
        // Don't echo locally — the PTY shell will echo back.
      });

      // Banner + initial output.
      term.write(SYS_INFO_BANNER);
      const first = pollTerminal(terminalId, 0, 0);
      cursorRef.current = first.cursor;
      tickRef.current = 1;
      if (first.data) term.write(first.data);
    });

    // Status polling.
    statusTimerRef.current = setInterval(() => {
      const st = getTerminalStatus(terminalId);
      if (st.status === "error") { setStatus("error"); setErrorMsg(st.error); if (statusTimerRef.current) { clearInterval(statusTimerRef.current); statusTimerRef.current = null; } return; }
      if (st.status === "connected") { setStatus((prev) => (prev === "connecting" ? "connected" : prev)); }
      if (st.status === "exited") { setStatus("exited"); if (statusTimerRef.current) { clearInterval(statusTimerRef.current); statusTimerRef.current = null; } }
    }, 500);

    // Output polling.
    outputTimerRef.current = setInterval(() => {
      if (stoppedRef.current) return;
      const next = pollTerminal(idRef.current, cursorRef.current, tickRef.current);
      cursorRef.current = next.cursor;
      tickRef.current += 1;
      if (next.data) term.write(next.data);
      if (!shouldKeepPolling(next, true)) {
        if (outputTimerRef.current) { clearInterval(outputTimerRef.current); outputTimerRef.current = null; }
        setStatus((prev) => (prev === "error" ? prev : "exited"));
      }
    }, pollIntervalMs);

    return () => { cancelAnimationFrame(raf); teardown(); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initKey, createTerminal, pollTerminal, writeTerminalInput, pollIntervalMs, teardown, focusXterm]);

  // ---- actions ----

  const handleReconnect = useCallback(() => { teardown(); setInitKey((k) => k + 1); }, [teardown]);
  const handleDisconnect = useCallback(() => { teardown(); setStatus("exited"); setErrorMsg(""); }, [teardown]);

  const isConnected = status === "connected" || status === "connecting";

  // ---- render ----

  return (
    <div
      data-testid={testId}
      data-terminal-id={idRef.current || ""}
      className="terminal-panel"
      style={{ display: "flex", width: "100%", height: "100%", minHeight: 0, flexDirection: "column", background: "#0b1020" }}
    >
      {/* header bar */}
      <div
        data-testid="terminal-status"
        style={{
          flex: "0 0 auto",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          borderBottom: "1px solid rgba(148, 163, 184, 0.22)",
          padding: "2px 8px",
          color: "#cbd5e1",
          fontSize: 11,
          minHeight: 28,
        }}
      >
        <span>
          Terminal{" "}
          <span style={{ color: status === "error" ? "#f87171" : status === "connected" ? "#4ade80" : "#94a3b8" }}>
            {status}
          </span>
          {errorMsg ? (
            <span data-testid="terminal-error" style={{ marginLeft: 8, color: "#f87171", fontWeight: 600 }}>
              — {errorMsg}
            </span>
          ) : null}
        </span>

        <Space size={4}>
          {isConnected ? (
            <Button type="text" size="small" danger icon={<PoweroffOutlined />} onClick={handleDisconnect}
              aria-label="Disconnect terminal" data-testid="terminal-btn-disconnect"
              style={{ color: "#f87171", fontSize: 11, height: 22 }}>Disconnect</Button>
          ) : null}
          <Button type="text" size="small" icon={<ReloadOutlined />} onClick={handleReconnect}
            aria-label="Reconnect terminal" data-testid="terminal-btn-reconnect"
            style={{ color: "#94a3b8", fontSize: 11, height: 22 }}>{isConnected ? "Reconnect" : "Connect"}</Button>
        </Space>
      </div>

      {/* xterm surface — click focuses keyboard input */}
      <div
        ref={hostRef}
        data-testid={`${testId}-surface`}
        style={{ flex: "1 1 auto", minHeight: 0, padding: "0 4px", cursor: "text" }}
        onClick={focusXterm}
      />
    </div>
  );
}
