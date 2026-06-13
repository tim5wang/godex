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
  createTerminal?: (workspaceDir?: string) => CreateTerminalResponse;
  pollTerminal?: (id: string, cursor: number, tick: number) => TerminalOutputChunk;
  writeTerminalInput?: (req: TerminalInputRequest, tick: number) => TerminalOutputChunk;
  pollIntervalMs?: number;
  testId?: string;
};

const SYS_INFO_BANNER = [
  "\x1b[1;36m┌──────────────────────────────────────────┐\x1b[0m\r\n",
  "\x1b[1;36m│\x1b[0m  \x1b[1;33mGoDex Terminal\x1b[0m                         \x1b[1;36m│\x1b[0m\r\n",
  "\x1b[1;36m│\x1b[0m  Backend: Go pipe-I/O (PTY pending)       \x1b[1;36m│\x1b[0m\r\n",
  "\x1b[1;36m│\x1b[0m  Shell:  bash / sh                         \x1b[1;36m│\x1b[0m\r\n",
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
  } = props;

  const hostRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const cursorRef = useRef(0);
  const tickRef = useRef(0);
  const idRef = useRef("");
  const stoppedRef = useRef(false);
  const outputTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const statusTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const roRef = useRef<ResizeObserver | null>(null);

  const [status, setStatus] = useState<TerminalStatus>("idle");
  const [errorMsg, setErrorMsg] = useState("");
  const [inputValue, setInputValue] = useState("");
  const [initKey, setInitKey] = useState(0);

  // ---- helpers ----

  const sendInput = useCallback((text: string) => {
    const tid = idRef.current;
    if (!tid || !text) return;
    const req: TerminalInputRequest = { terminalId: tid, data: text };
    writeTerminalInput(req, tickRef.current);
    tickRef.current += 1;
    // Echo the input to xterm so the user sees what they typed.
    termRef.current?.write(text.replace(/\r?\n/g, "\r\n"));
  }, [writeTerminalInput]);

  const teardown = useCallback(() => {
    stoppedRef.current = true;
    if (statusTimerRef.current) { clearInterval(statusTimerRef.current); statusTimerRef.current = null; }
    if (outputTimerRef.current) { clearInterval(outputTimerRef.current); outputTimerRef.current = null; }
    if (roRef.current) { roRef.current.disconnect(); roRef.current = null; }
    if (termRef.current) { termRef.current.dispose(); termRef.current = null; }
    fitRef.current = null;
    if (idRef.current) { destroyTerminal(idRef.current); idRef.current = ""; }
  }, []);

  // ---- init ----

  useEffect(() => {
    if (!hostRef.current) return;
    stoppedRef.current = false;
    setStatus("connecting");
    setErrorMsg("");
    setInputValue("");

    const term = new Terminal({
      convertEol: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
      fontSize: 12,
      theme: { background: "#0b1020" },
      disableStdin: true, // xterm keyboard input disabled — we use own textarea
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    fitRef.current = fit;

    const { terminalId } = createTerminal();
    idRef.current = terminalId;

    // Defer DOM init (xterm.js needs parent with dimensions).
    requestAnimationFrame(() => {
      if (!hostRef.current || stoppedRef.current) return;
      term.open(hostRef.current);
      try { fit.fit(); } catch { /* detached */ }

      const ro = new ResizeObserver(() => {
        try { fit.fit(); } catch { /* detached */ }
        // Notify the backend so the PTY gets correct rows/cols.
        if (idRef.current && term) {
          resizeTerminal(idRef.current, term.cols, term.rows);
        }
      });
      ro.observe(hostRef.current!);
      roRef.current = ro;

      term.write(SYS_INFO_BANNER);

      const first = pollTerminal(terminalId, 0, 0);
      cursorRef.current = first.cursor;
      tickRef.current = 1;
      if (first.data) term.write(first.data);

      // Focus the input textarea.
      inputRef.current?.focus();
    });

    // Status polling.
    statusTimerRef.current = setInterval(() => {
      const st = getTerminalStatus(terminalId);
      if (st.status === "error") {
        setStatus("error");
        setErrorMsg(st.error);
        if (statusTimerRef.current) { clearInterval(statusTimerRef.current); statusTimerRef.current = null; }
        return;
      }
      if (st.status === "connected") {
        setStatus((prev) => (prev === "connecting" ? "connected" : prev));
      }
      if (st.status === "exited") {
        setStatus("exited");
        if (statusTimerRef.current) { clearInterval(statusTimerRef.current); statusTimerRef.current = null; }
      }
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

    return () => { teardown(); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initKey, createTerminal, pollTerminal, writeTerminalInput, pollIntervalMs, teardown]);

  // ---- input handler ----

  const handleInputKeyDown = useCallback((e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      const text = inputValue + "\n";
      sendInput(text);
      setInputValue("");
    }
  }, [inputValue, sendInput]);

  // ---- actions ----

  const handleReconnect = useCallback(() => {
    teardown();
    setInitKey((k) => k + 1);
  }, [teardown]);

  const handleDisconnect = useCallback(() => {
    teardown();
    setStatus("exited");
    setErrorMsg("");
  }, [teardown]);

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
            <Button
              type="text" size="small" danger
              icon={<PoweroffOutlined />}
              onClick={handleDisconnect}
              aria-label="Disconnect terminal"
              data-testid="terminal-btn-disconnect"
              style={{ color: "#f87171", fontSize: 11, height: 22 }}
            >
              Disconnect
            </Button>
          ) : null}
          <Button
            type="text" size="small"
            icon={<ReloadOutlined />}
            onClick={handleReconnect}
            aria-label="Reconnect terminal"
            data-testid="terminal-btn-reconnect"
            style={{ color: "#94a3b8", fontSize: 11, height: 22 }}
          >
            {isConnected ? "Reconnect" : "Connect"}
          </Button>
        </Space>
      </div>

      {/* xterm output surface */}
      <div
        ref={hostRef}
        data-testid={`${testId}-surface`}
        style={{ flex: "1 1 auto", minHeight: 0, padding: "0 4px" }}
      />

      {/* native textarea input bar */}
      <div
        style={{
          flex: "0 0 auto",
          display: "flex",
          alignItems: "center",
          borderTop: "1px solid rgba(148, 163, 184, 0.22)",
          padding: "2px 6px",
          background: "#0d1525",
        }}
      >
        <span style={{ color: "#4ade80", fontSize: 12, marginRight: 6, flexShrink: 0 }}>$</span>
        <textarea
          ref={inputRef}
          data-testid="terminal-input"
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          onKeyDown={handleInputKeyDown}
          rows={1}
          placeholder={isConnected ? "Type a command, Enter to send..." : "Disconnected"}
          disabled={!isConnected}
          style={{
            flex: "1 1 auto",
            background: "transparent",
            border: "none",
            outline: "none",
            color: "#e2e8f0",
            fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
            fontSize: 12,
            resize: "none",
            padding: 0,
            lineHeight: "20px",
          }}
        />
      </div>
    </div>
  );
}
