import { useMemo, useState, type ReactNode } from "react";

import { useTaskCenterText } from "./taskCenter.i18n";

export interface LongTaskRefluxBubbleProps {
  // T15 acceptance: the bubble is "floating" — it sits over the
  // page (position: fixed) rather than inside the chat list. The
  // chat list still renders a low-key reference line, but the
  // full body is in the bubble so the user can see it from any
  // scroll position.
  longtaskId: string;
  runId?: string;
  status: string;
  // The full text of the reflux message. The bubble parses the
  // leading 'LongTask <id>: <status>' line and uses the rest as
  // the body so we do not depend on the agent sending a
  // structured payload.
  content: string;
  // Suggested actions rendered as clickable buttons. The chat
  // page wires these to the longtask API.
  suggestedActions?: string[];
  onAction?: (action: string) => void;
  onDismiss?: () => void;
  // T15.2: position is the right-side stack index; multiple
  // bubbles stack vertically.
  stackIndex?: number;
}

const MAX_VISIBLE_BODY = 600;

export function LongTaskRefluxBubble(props: LongTaskRefluxBubbleProps): ReactNode {
  const text = useTaskCenterText();
  const [expanded, setExpanded] = useState(false);
  const body = useMemo(() => extractRefluxBody(props.content), [props.content]);
  const visibleBody = expanded || body.length <= MAX_VISIBLE_BODY
    ? body
    : body.slice(0, MAX_VISIBLE_BODY) + "...";
  const stackOffset = (props.stackIndex ?? 0) * 16;
  return (
    <div
      data-testid="longtask-reflux-bubble"
      style={{
        position: "fixed",
        right: 16,
        bottom: 16 + stackOffset,
        maxWidth: 360,
        maxHeight: 320,
        overflowY: "auto",
        padding: 12,
        borderRadius: 8,
        background: "var(--surface-2, #1a1d24)",
        color: "var(--text, #f1f3f5)",
        boxShadow: "0 4px 12px rgba(0,0,0,0.4)",
        zIndex: 1000,
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 8 }}>
        <strong>{text.refluxPrefix} {props.longtaskId}</strong>
        <span style={{ opacity: 0.7, fontSize: 12 }}>{props.status}</span>
      </div>
      <pre
        style={{
          whiteSpace: "pre-wrap",
          wordBreak: "break-word",
          fontFamily: "inherit",
          margin: 0,
          fontSize: 13,
        }}
      >
        {visibleBody}
      </pre>
      {body.length > MAX_VISIBLE_BODY ? (
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          style={{ marginTop: 6, fontSize: 12, opacity: 0.7, background: "transparent", border: "none", color: "inherit", cursor: "pointer" }}
        >
          {expanded ? "show less" : "show more"}
        </button>
      ) : null}
      {props.suggestedActions && props.suggestedActions.length > 0 ? (
        <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 10 }}>
          {props.suggestedActions.map((action) => (
            <button
              key={action}
              type="button"
              onClick={() => props.onAction?.(action)}
              style={{ fontSize: 12, padding: "4px 8px", borderRadius: 4, background: "var(--accent, #3b82f6)", color: "#fff", border: "none", cursor: "pointer" }}
            >
              {action}
            </button>
          ))}
        </div>
      ) : null}
      {props.onDismiss ? (
        <button
          type="button"
          onClick={props.onDismiss}
          style={{ marginTop: 8, fontSize: 11, opacity: 0.5, background: "transparent", border: "none", color: "inherit", cursor: "pointer" }}
        >
          dismiss
        </button>
      ) : null}
    </div>
  );
}

function extractRefluxBody(content: string): string {
  // The agent emits "<id>: <status>\n<body>". We strip the
  // leading header so the bubble can render id/status separately.
  const newline = content.indexOf("\n");
  if (newline < 0) {
    return content;
  }
  return content.slice(newline + 1);
}

// isLongTaskRefluxMessage is the Web-side sniff test that the
// chat list uses to decide which messages get a bubble. The
// strict authority is the message metadata; the content sniff
// is a fallback for messages that lost the metadata on the wire.
export function isLongTaskRefluxMessage(content: string): boolean {
  return content.trimStart().startsWith("LongTask ");
}
