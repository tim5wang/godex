import { Alert, Button } from "antd";

export type BinaryPreviewKind = "image" | "video" | "audio" | "pdf";

export default function BinaryPreview(props: {
  kind: BinaryPreviewKind;
  url: string;
  fileName: string;
  failed: boolean;
  onFailed: () => void;
  t: (key: string) => string;
}) {
  // Degrade gracefully when the browser cannot decode the format.
  if (props.failed) {
    return (
      <div style={{ padding: 16 }}>
        <Alert
          type="warning"
          showIcon
          message={props.t("files.previewUnsupported") || "This format cannot be previewed in your browser."}
          action={
            <Button size="small" href={props.url} target="_blank" rel="noreferrer">
              {props.t("files.openInBrowser") || "Open in browser"}
            </Button>
          }
        />
      </div>
    );
  }
  if (props.kind === "image") {
    return (
      <div style={{ display: "grid", placeItems: "center", padding: 16, minHeight: "100%", boxSizing: "border-box" }}>
        <img
          src={props.url}
          alt={props.fileName}
          style={{ maxWidth: "100%", maxHeight: "100%", objectFit: "contain" }}
          onError={props.onFailed}
        />
      </div>
    );
  }
  if (props.kind === "video") {
    return <video src={props.url} controls style={{ width: "100%", maxHeight: "100%" }} onError={props.onFailed} />;
  }
  if (props.kind === "audio") {
    return <audio src={props.url} controls style={{ width: "100%" }} onError={props.onFailed} />;
  }
  // pdf: browser-native viewer inside an iframe, with an external-open
  // fallback for browsers without a built-in PDF plugin.
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <div style={{ padding: "4px 8px", borderBottom: "1px solid var(--border)", display: "flex", justifyContent: "flex-end" }}>
        <Button type="link" size="small" href={props.url} target="_blank" rel="noreferrer">
          {props.t("files.openInBrowser") || "Open in browser"}
        </Button>
      </div>
      <iframe src={props.url} title={props.fileName} style={{ flex: 1, width: "100%", border: "none" }} />
    </div>
  );
}
