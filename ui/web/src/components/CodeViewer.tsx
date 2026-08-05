import { useEffect, useRef, useState } from "react";
import { Spin } from "antd";

interface CodeViewerProps {
  value: string;
  language?: "json" | "text";
  className?: string;
  maxHeight?: number;
}

/**
 * Read-only syntax-highlighted code block backed by CodeMirror (lazy-loaded).
 * Used for tool-call Input/Output JSON so expanded logs are readable instead
 * of a plain <pre>. Language modules are loaded on demand to keep the main
 * bundle small (same pattern as features/files/CodeEditor.tsx).
 */
export function CodeViewer({ value, language = "text", className, maxHeight = 340 }: CodeViewerProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<{ destroy(): void } | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const host = hostRef.current;
    if (!host) return;

    setLoading(true);
    (async () => {
      try {
        const [{ EditorView, lineNumbers }, { oneDark }, { syntaxHighlighting, defaultHighlightStyle }, { EditorState }] = await Promise.all([
          import("@codemirror/view"),
          import("@codemirror/theme-one-dark"),
          import("@codemirror/language"),
          import("@codemirror/state"),
        ]);

        if (cancelled || !hostRef.current) return;

        const extensions: any[] = [
          lineNumbers(),
          syntaxHighlighting(defaultHighlightStyle),
          oneDark,
          EditorView.lineWrapping,
          EditorView.editable.of(false),
          EditorView.contentAttributes.of({ "aria-readonly": "true" }),
        ];

        if (language === "json") {
          const { json } = await import("@codemirror/lang-json");
          extensions.push(json());
        }

        const state = EditorState.create({ doc: value, extensions });
        viewRef.current = new EditorView({ state, parent: hostRef.current });
      } catch (err) {
        // Fall back to the raw text if CodeMirror fails to load.
        if (!cancelled && hostRef.current) {
          hostRef.current.textContent = value;
        }
        console.error("CodeViewer init failed", err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => {
      cancelled = true;
      viewRef.current?.destroy();
      viewRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language]);

  // Push external content updates into the existing editor without recreating it.
  useEffect(() => {
    const view = viewRef.current as any;
    if (view && value !== view.state.doc.toString()) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
    }
  }, [value]);

  return (
    <div className={className} style={{ position: "relative" }}>
      {loading ? (
        <div style={{ position: "absolute", top: 8, right: 8, zIndex: 1 }}>
          <Spin size="small" />
        </div>
      ) : null}
      <div
        ref={hostRef}
        style={{
          maxHeight,
          overflow: "auto",
          background: "#0b1020",
          borderRadius: 8,
          fontSize: 12,
        }}
      />
    </div>
  );
}
