import { useMemo, useState } from "react";
import { Button, Input, Segmented, Space, Tooltip, Typography } from "antd";
import { ReloadOutlined, ExportOutlined, ChromeOutlined } from "@ant-design/icons";

/**
 * Web App Preview dock panel.
 *
 * Two modes, auto-detected from the address bar input:
 *   - Static: serves files from the session workspace (local or SSH) via
 *     /api/preview/static/{path} with SPA fallback to index.html. Input is a
 *     workspace-relative path such as "/" or "/index.html".
 *   - Dev server: reverse-proxies a dev server running on 127.0.0.1:{port}
 *     via /api/preview/proxy/{port}/ (vite dev on 5173, etc.). Input is a
 *     port ("5173", ":5173") or a localhost URL ("http://localhost:5173").
 *
 * The iframe cannot attach an Authorization header, so when the server
 * requires a web token it is forwarded as a ?token= query parameter.
 */
export type PreviewPanelProps = {
  /** Session working directory used as the static preview root. */
  workspaceDir?: string;
  /** Web token forwarded as ?token= when the server requires auth. */
  token?: string | null;
};

type AddressTarget =
  | { kind: "static"; path: string }
  | { kind: "dev"; port: string }
  | { kind: "url"; url: string };

type DeviceWidth = "desktop" | "tablet" | "mobile";

const DEVICE_WIDTHS: Record<DeviceWidth, string> = {
  desktop: "100%",
  tablet: "768px",
  mobile: "390px",
};

const DEVICE_OPTIONS: Array<{ value: DeviceWidth; label: string }> = [
  { value: "desktop", label: "Desktop" },
  { value: "tablet", label: "Tablet" },
  { value: "mobile", label: "Mobile" },
];

export function parsePreviewAddress(input: string): AddressTarget {
  const trimmed = input.trim();
  const urlMatch = trimmed.match(/^https?:\/\/(?:localhost|127\.0\.0\.1):(\d+)(?:\/.*)?$/i);
  if (urlMatch) return { kind: "dev", port: urlMatch[1] };
  if (/^https?:\/\//i.test(trimmed)) return { kind: "url", url: trimmed };
  const colonPort = trimmed.match(/^:(\d+)$/);
  if (colonPort) return { kind: "dev", port: colonPort[1] };
  if (/^\d+$/.test(trimmed)) return { kind: "dev", port: trimmed };
  const path = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return { kind: "static", path };
}

export function buildPreviewSrc(
  target: AddressTarget,
  opts: { workspaceDir?: string; token?: string | null; reloadKey?: number }
): string {
  const params = new URLSearchParams();
  if (opts.token) params.set("token", opts.token);
  if (typeof opts.reloadKey === "number" && opts.reloadKey > 0) params.set("_", String(opts.reloadKey));
  const qs = params.toString();

  if (target.kind === "static") {
    if (opts.workspaceDir) params.set("root", opts.workspaceDir);
    const path = target.path === "/" ? "" : target.path.replace(/^\//, "");
    const query = params.toString();
    return `/api/preview/static/${path}${query ? `?${query}` : ""}`;
  }
  if (target.kind === "url") {
    // External URL: embed directly (server-side /preview/http proxy not
    // needed; most sites without X-Frame-Options restrictions render fine).
    return target.url;
  }
  return `/api/preview/proxy/${target.port}/${qs ? `?${qs}` : ""}`;
}

export function PreviewPanel({ workspaceDir, token }: PreviewPanelProps) {
  const [address, setAddress] = useState("/");
  const [device, setDevice] = useState<DeviceWidth>("desktop");
  const [reloadKey, setReloadKey] = useState(0);

  const target = useMemo(() => parsePreviewAddress(address), [address]);
  const src = useMemo(
    () => buildPreviewSrc(target, { workspaceDir, token, reloadKey }),
    [target, workspaceDir, token, reloadKey]
  );
  const isDev = target.kind === "dev";

  const refresh = () => setReloadKey((k) => k + 1);
  const openInNewWindow = () => {
    window.open(src, "_blank", "noopener,noreferrer");
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <Space.Compact style={{ padding: "8px 8px 4px", width: "100%" }}>
        <Input
          aria-label="Preview address"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          onPressEnter={refresh}
          placeholder='Static path ("/") or dev server port ("5173")'
          prefix={<ChromeOutlined />}
          allowClear
        />
        <Tooltip title="Reload">
          <Button aria-label="Reload preview" icon={<ReloadOutlined />} onClick={refresh} />
        </Tooltip>
        <Tooltip title="Open in new window">
          <Button aria-label="Open preview in new window" icon={<ExportOutlined />} onClick={openInNewWindow} />
        </Tooltip>
      </Space.Compact>
      <Space style={{ padding: "0 8px 4px", width: "100%", justifyContent: "space-between" }}>
        <Segmented
          size="small"
          value={device}
          onChange={(v) => setDevice(v as DeviceWidth)}
          options={DEVICE_OPTIONS}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {isDev ? `Dev server :${target.port}` : target.kind === "url" ? "External URL" : "Static workspace"}
        </Typography.Text>
      </Space>
      <div
        style={{
          flex: 1,
          minHeight: 0,
          overflow: "auto",
          padding: "4px 8px 8px",
          background: "#f5f5f5",
        }}
      >
        <div
          style={{
            width: DEVICE_WIDTHS[device],
            maxWidth: "100%",
            height: "100%",
            margin: "0 auto",
            background: "#fff",
            border: "1px solid #d9d9d9",
            borderRadius: 4,
            overflow: "hidden",
          }}
        >
          <iframe
            key={reloadKey}
            title="Web app preview"
            src={src}
            style={{ width: "100%", height: "100%", border: "none" }}
            sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-downloads"
          />
        </div>
      </div>
    </div>
  );
}
