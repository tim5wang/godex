import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Empty, Space, Spin, Tooltip, Typography } from "antd";
import {
  PushpinOutlined,
  PushpinFilled,
  ExportOutlined,
  ReloadOutlined,
  ChromeOutlined,
} from "@ant-design/icons";
import { apiURL } from "../../lib/apiClient";
import { useBrowserViewStore } from "./browserViewStore";

/**
 * Browser use dock panel (PRD docs/prd-browser-use-inside.md, 方案 A).
 *
 * Shows the page the agent is operating as a live JPEG frame stream:
 *   - WS endpoint /api/browser/frames?session={sessionId} (web token auth)
 *   - Frame message contract: {pageID, url, title, jpeg} (jpeg = base64)
 *   - <img> re-renders on every frame (1fps+ target from the backend)
 *
 * Fallback (方案 B): when the frame stream is unavailable (endpoint missing,
 * auth failed, connection dropped) the panel shows the last known URL card
 * and can open the URL in a Preview-style iframe container.
 *
 * `browser.view` SSE events drive auto-activation + follow (see
 * useChatSessionState). `follow` here is the pin toggle on the toolbar: when
 * off, events keep updating the view but never steal focus from another tab.
 */
export type BrowserPanelProps = {
  sessionId: string;
  token?: string | null;
  /** Session workspace dir, forwarded to the preview iframe fallback. */
  workspaceDir?: string;
};

type StreamState = "idle" | "connecting" | "live" | "degraded";

/** Mock toggle for联调 when the backend WS endpoint is not ready yet:
 *  open the chat with ?browserMock=1 to see the panel render synthetic
 *  frames through the exact same {pageID,url,title,jpeg} message path. */
function mockEnabled(): boolean {
  if (typeof window === "undefined") return false;
  return new URLSearchParams(window.location.search).get("browserMock") === "1";
}

function dataURLFromJpeg(jpeg: unknown): string | null {
  if (typeof jpeg !== "string" || !jpeg) return null;
  return jpeg.startsWith("data:") ? jpeg : `data:image/jpeg;base64,${jpeg}`;
}

export function BrowserPanel({ sessionId, token, workspaceDir }: BrowserPanelProps) {
  const view = useBrowserViewStore((state) => state.view);
  const follow = useBrowserViewStore((state) => state.follow);
  const setFollow = useBrowserViewStore((state) => state.setFollow);

  const [streamState, setStreamState] = useState<StreamState>("idle");
  const [frameSrc, setFrameSrc] = useState<string | null>(null);
  // URL/title carried by the latest frame message (more current than the SSE
  // view when navigation happened between events).
  const [frameInfo, setFrameInfo] = useState<{ url?: string; title?: string }>({});
  const [previewOpen, setPreviewOpen] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const firstFrameTimerRef = useRef<number | null>(null);
  const mock = useMemo(mockEnabled, []);

  const currentUrl = frameInfo.url || view?.url || "";
  const currentTitle = frameInfo.title || view?.title || "";

  const teardown = useCallback(() => {
    if (firstFrameTimerRef.current) {
      window.clearTimeout(firstFrameTimerRef.current);
      firstFrameTimerRef.current = null;
    }
    if (wsRef.current) {
      wsRef.current.onclose = null;
      wsRef.current.onerror = null;
      wsRef.current.close();
      wsRef.current = null;
    }
  }, []);

  const handleFrame = useCallback((msg: { pageID?: string; url?: string; title?: string; jpeg?: string }) => {
    setStreamState("live");
    if (msg.url || msg.title) {
      setFrameInfo((prev) => ({ url: msg.url || prev.url, title: msg.title || prev.title }));
    }
    const src = dataURLFromJpeg(msg.jpeg);
    if (src) setFrameSrc(src);
    if (firstFrameTimerRef.current) {
      window.clearTimeout(firstFrameTimerRef.current);
      firstFrameTimerRef.current = null;
    }
  }, []);

  // Synthetic mock stream for frontend联调 without the backend endpoint.
  const startMock = useCallback(() => {
    if (!sessionId) return;
    let seq = 0;
    setStreamState("connecting");
    const timer = window.setInterval(() => {
      seq += 1;
      const url = `https://example.com/page/${seq}`;
      const title = `Mock page ${seq}`;
      // Draw a cheap synthetic frame so the <img> render path is exercised.
      const canvas = document.createElement("canvas");
      canvas.width = 640;
      canvas.height = 400;
      const ctx = canvas.getContext("2d");
      if (ctx) {
        const hue = (seq * 37) % 360;
        ctx.fillStyle = `hsl(${hue}, 60%, 80%)`;
        ctx.fillRect(0, 0, 640, 400);
        ctx.fillStyle = "#222";
        ctx.font = "bold 24px system-ui, sans-serif";
        ctx.fillText(title, 24, 60);
        ctx.font = "16px system-ui, sans-serif";
        ctx.fillText(url, 24, 96);
        ctx.fillText(`frame #${seq} · ${new Date().toLocaleTimeString()}`, 24, 360);
      }
      const jpeg = canvas.toDataURL("image/jpeg", 0.7);
      // Feed through the same handler as a real {pageID,url,title,jpeg} message.
      handleFrame({ pageID: `mock-${seq}`, url, title, jpeg });
    }, 500);
    wsRef.current = {
      close: () => window.clearInterval(timer),
      onclose: null,
      onerror: null,
    } as unknown as WebSocket;
    return timer;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, handleFrame]);

  const connect = useCallback(() => {
    teardown();
    if (!sessionId) {
      setStreamState("degraded");
      return;
    }
    if (mock) {
      startMock();
      return;
    }
    setStreamState("connecting");
    const params = new URLSearchParams();
    if (token) params.set("token", token);
    const path = apiURL("/browser/frames");
    const wsURL = `${window.location.origin.replace(/^http/, "ws")}${path}?session=${encodeURIComponent(sessionId)}&${params.toString()}`;
    let ws: WebSocket;
    try {
      ws = new WebSocket(wsURL);
    } catch {
      setStreamState("degraded");
      return;
    }
    wsRef.current = ws;
    // If the first frame does not arrive in time the endpoint is most likely
    // not implemented yet — fall back to the URL card (方案 B).
    firstFrameTimerRef.current = window.setTimeout(() => {
      if (ws.readyState !== WebSocket.OPEN) return;
      setStreamState((prev) => (prev === "live" ? prev : "degraded"));
    }, 6000);
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") return;
      try {
        const msg = JSON.parse(ev.data) as { pageID?: string; url?: string; title?: string; jpeg?: string; error?: string };
        if (msg.error) {
          setStreamState("degraded");
          return;
        }
        handleFrame(msg);
      } catch {
        // Non-JSON message — ignore (contract messages are JSON).
      }
    };
    ws.onclose = () => {
      setStreamState((prev) => (prev === "live" ? prev : "degraded"));
    };
    ws.onerror = () => {
      setStreamState((prev) => (prev === "live" ? prev : "degraded"));
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, token, mock, teardown, startMock]);

  useEffect(() => {
    connect();
    return () => {
      teardown();
    };
  }, [connect, teardown]);

  // Follow the last known view: keep the toolbar URL/title fresh even when
  // the frame stream is down (the card + preview fallback read from here).
  const effectiveTitle = currentTitle || "Agent browser";
  const effectiveUrl = currentUrl || "Waiting for agent to open a page…";

  const openPreview = () => setPreviewOpen((open) => !open);
  const openInNewWindow = () => {
    if (currentUrl) window.open(currentUrl, "_blank", "noopener,noreferrer");
  };

  const showFrame = streamState === "live" && frameSrc;
  const showFallbackCard = streamState === "degraded" || (!showFrame && !frameSrc);

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      {/* Toolbar: title / URL / pin (follow) toggle */}
      <div style={{ padding: "8px 8px 4px", borderBottom: "1px solid #f0f0f0" }}>
        <Space style={{ width: "100%", justifyContent: "space-between" }} size={4}>
          <Typography.Text strong ellipsis style={{ maxWidth: 150 }} title={effectiveTitle}>
            {effectiveTitle}
          </Typography.Text>
          <Space size={2}>
            <Tooltip title={follow ? "Auto-follow agent browser (on)" : "Auto-follow agent browser (off)"}>
              <Button
                type="text"
                size="small"
                icon={follow ? <PushpinFilled style={{ color: "#1677ff" }} /> : <PushpinOutlined />}
                aria-label="Toggle auto-follow"
                data-testid="browser-follow-toggle"
                onClick={() => setFollow(!follow)}
              />
            </Tooltip>
            <Tooltip title="Open in new window">
              <Button
                type="text"
                size="small"
                icon={<ExportOutlined />}
                aria-label="Open browser view in new window"
                disabled={!currentUrl}
                onClick={openInNewWindow}
              />
            </Tooltip>
          </Space>
        </Space>
        <Typography.Text
          type="secondary"
          style={{ fontSize: 12, display: "block", marginTop: 2 }}
          ellipsis={{ tooltip: effectiveUrl }}
        >
          {effectiveUrl}
        </Typography.Text>
      </div>

      {/* Frame viewport */}
      <div style={{ flex: 1, minHeight: 0, overflow: "auto", background: "#111", display: "flex", alignItems: "center", justifyContent: "center" }}>
        {showFrame ? (
          <img
            src={frameSrc}
            alt={`Agent browser: ${effectiveTitle}`}
            style={{ width: "100%", height: "100%", objectFit: "contain" }}
          />
        ) : streamState === "connecting" ? (
          <Space direction="vertical" align="center" style={{ color: "#ccc", padding: 16 }}>
            <Spin size="small" />
            <Typography.Text style={{ color: "#ccc", fontSize: 12 }}>Connecting to browser frame stream…</Typography.Text>
          </Space>
        ) : (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={
              <Typography.Text style={{ color: "#ccc", fontSize: 12 }}>
                {mock ? "Mock frame stream (browserMock=1)" : "Frame stream unavailable"}
              </Typography.Text>
            }
            style={{ margin: 0 }}
          />
        )}
      </div>

      {/* 方案 B fallback: URL card + Preview iframe attempt */}
      {showFallbackCard ? (
        <div style={{ padding: 8, borderTop: "1px solid #f0f0f0" }}>
          <Space direction="vertical" size={6} style={{ width: "100%" }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {mock
                ? "联调 mock：按约定消息契约 {pageID,url,title,jpeg} 生成合成帧"
                : "帧流不可用，已降级为 URL 跟随（方案 B）。"}
            </Typography.Text>
            <Button size="small" icon={<ChromeOutlined />} onClick={openPreview} data-testid="browser-open-preview">
              {previewOpen ? "Close Preview iframe" : "Open Preview iframe"}
            </Button>
            {previewOpen && currentUrl ? (
              <div
                style={{
                  height: 260,
                  background: "#fff",
                  border: "1px solid #d9d9d9",
                  borderRadius: 4,
                  overflow: "hidden",
                }}
              >
                <iframe
                  title="Browser URL preview (fallback)"
                  src={currentUrl}
                  style={{ width: "100%", height: "100%", border: "none" }}
                  sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-downloads"
                />
              </div>
            ) : null}
          </Space>
        </div>
      ) : null}
    </div>
  );
}
