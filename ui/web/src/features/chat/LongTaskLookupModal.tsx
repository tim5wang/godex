import { useState, type ReactNode } from "react";

import { useTaskCenterText } from "./taskCenter.i18n";

export type LongTaskLookupMode = "commit" | "story";

export interface LongTaskLookupModalProps {
  visible: boolean;
  onCancel: () => void;
  onSubmit: (mode: LongTaskLookupMode, query: string) => void | Promise<void>;
}

// LongTaskLookupModal is the Web entry point for the T12
// commit-hash / story-id reverse-lookup. The same modal chrome
// covers both modes so the user can switch without re-rendering.
export function LongTaskLookupModal(props: LongTaskLookupModalProps): ReactNode {
  const text = useTaskCenterText();
  const [mode, setMode] = useState<LongTaskLookupMode>("commit");
  const [query, setQuery] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!props.visible) {
    return null;
  }

  const submit = async () => {
    if (submitting || !query.trim()) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await props.onSubmit(mode, query.trim());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      data-testid="longtask-lookup-modal"
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
        <h3 style={{ margin: 0, marginBottom: 12 }}>{text.lookupLongTask}</h3>
        <div style={{ display: "flex", gap: 8, marginBottom: 12 }}>
          <button
            type="button"
            data-testid="lookup-mode-commit"
            onClick={() => setMode("commit")}
            style={{
              padding: "6px 12px",
              borderRadius: 4,
              background: mode === "commit" ? "var(--accent, #3b82f6)" : "transparent",
              color: "inherit",
              border: "1px solid #444",
              cursor: "pointer",
            }}
          >
            {text.lookupByCommit}
          </button>
          <button
            type="button"
            data-testid="lookup-mode-story"
            onClick={() => setMode("story")}
            style={{
              padding: "6px 12px",
              borderRadius: 4,
              background: mode === "story" ? "var(--accent, #3b82f6)" : "transparent",
              color: "inherit",
              border: "1px solid #444",
              cursor: "pointer",
            }}
          >
            {text.lookupByStory}
          </button>
        </div>
        <input
          data-testid="lookup-query"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void submit();
            }
          }}
          placeholder={mode === "commit" ? "commit hash or prefix" : "story id (e.g. US-001)"}
          style={{
            width: "100%",
            padding: 8,
            fontSize: 13,
            background: "var(--surface-2, #1a1d24)",
            color: "inherit",
            border: "1px solid #444",
            borderRadius: 4,
            boxSizing: "border-box",
          }}
        />
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
            disabled={submitting || !query.trim()}
            data-testid="lookup-submit"
            style={{
              padding: "6px 12px",
              borderRadius: 4,
              background: submitting || !query.trim() ? "#666" : "var(--accent, #3b82f6)",
              color: "#fff",
              border: "none",
              cursor: submitting || !query.trim() ? "not-allowed" : "pointer",
            }}
          >
            {submitting ? "..." : "submit"}
          </button>
        </div>
      </div>
    </div>
  );
}
