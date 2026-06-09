import { useState, type ReactNode } from "react";

import { useTaskCenterText } from "./taskCenter.i18n";

export const ROLLBACK_REASON_MAX_BYTES = 1024;

export interface LongTaskRollbackModalProps {
  visible: boolean;
  nodeId: string;
  onCancel: () => void;
  onSubmit: (reason: string) => void | Promise<void>;
}

// LongTaskRollbackModal is the T12 audit-and-undo entry point
// for the Web task center. The reason input enforces the same
// 1024-byte cap the agent / CLI / HTTP layer enforce (defense
// in depth) and shows a live byte counter so the user knows
// when they are about to exceed the cap.
export function LongTaskRollbackModal(props: LongTaskRollbackModalProps): ReactNode {
  const text = useTaskCenterText();
  const [reason, setReason] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!props.visible) {
    return null;
  }

  const byteSize = new TextEncoder().encode(reason).length;
  const overCap = byteSize > ROLLBACK_REASON_MAX_BYTES;

  const submit = async () => {
    if (overCap || submitting) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await props.onSubmit(reason);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      data-testid="longtask-rollback-modal"
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,0.5)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 1100,
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) {
          props.onCancel();
        }
      }}
    >
      <div
        style={{
          background: "var(--surface-1, #14171c)",
          color: "var(--text, #f1f3f5)",
          padding: 20,
          borderRadius: 8,
          width: 480,
          maxWidth: "90%",
        }}
      >
        <h3 style={{ margin: 0, marginBottom: 12 }}>{text.rollbackLongTask}</h3>
        <p style={{ margin: 0, marginBottom: 8, opacity: 0.7, fontSize: 13 }}>
          Story: <code>{props.nodeId}</code>
        </p>
        <label style={{ display: "block", marginBottom: 4, fontSize: 13 }}>
          {text.rollbackReason}
        </label>
        <textarea
          data-testid="rollback-reason-input"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder={text.rollbackReasonPlaceholder}
          rows={4}
          style={{
            width: "100%",
            padding: 8,
            fontFamily: "inherit",
            fontSize: 13,
            background: "var(--surface-2, #1a1d24)",
            color: "inherit",
            border: overCap ? "1px solid #ef4444" : "1px solid #444",
            borderRadius: 4,
            boxSizing: "border-box",
            resize: "vertical",
          }}
        />
        <div
          data-testid="rollback-reason-counter"
          style={{
            fontSize: 12,
            marginTop: 4,
            color: overCap ? "#ef4444" : "inherit",
            opacity: overCap ? 1 : 0.7,
          }}
        >
          {byteSize} / {ROLLBACK_REASON_MAX_BYTES} bytes
        </div>
        {overCap ? (
          <div style={{ fontSize: 12, color: "#ef4444", marginTop: 4 }}>
            {text.rollbackReasonTooLong}
          </div>
        ) : null}
        {error ? (
          <div style={{ fontSize: 12, color: "#ef4444", marginTop: 4 }}>{error}</div>
        ) : null}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 16 }}>
          <button
            type="button"
            onClick={props.onCancel}
            style={{ padding: "6px 12px", borderRadius: 4, background: "transparent", color: "inherit", border: "1px solid #444", cursor: "pointer" }}
          >
            cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={overCap || submitting}
            data-testid="rollback-submit"
            style={{
              padding: "6px 12px",
              borderRadius: 4,
              background: overCap ? "#666" : "var(--accent, #3b82f6)",
              color: "#fff",
              border: "none",
              cursor: overCap || submitting ? "not-allowed" : "pointer",
            }}
          >
            {submitting ? "..." : "submit"}
          </button>
        </div>
      </div>
    </div>
  );
}
