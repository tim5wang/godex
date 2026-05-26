import { useEffect, useRef, useCallback, useState } from "react";
import { Spin, Alert } from "antd";

interface CodeEditorProps {
  content: string;
  filePath: string;
  readOnly?: boolean;
  onSave?: (content: string) => void;
  onChange?: (content: string) => void;
}

function detectLanguage(path: string): string {
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  const langMap: Record<string, string> = {
    go: "go",
    ts: "typescript",
    tsx: "typescript",
    js: "javascript",
    jsx: "javascript",
    json: "json",
    md: "markdown",
    yaml: "yaml",
    yml: "yaml",
    css: "css",
    html: "html",
    py: "python",
    sql: "sql",
    txt: "text",
  };
  return langMap[ext] ?? "text";
}

const LANG_LOADERS: Record<string, () => Promise<any>> = {
  javascript: () => import("@codemirror/lang-javascript"),
  typescript: () => import("@codemirror/lang-javascript"),
  json: () => import("@codemirror/lang-json"),
  markdown: () => import("@codemirror/lang-markdown"),
  css: () => import("@codemirror/lang-css"),
  html: () => import("@codemirror/lang-html"),
  python: () => import("@codemirror/lang-python"),
  sql: () => import("@codemirror/lang-sql"),
};

export default function CodeEditor({
  content,
  filePath,
  readOnly = false,
  onSave,
  onChange,
}: CodeEditorProps) {
  const editorRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<any>(null);
  const contentRef = useRef(content);
  const suppressOnChange = useRef(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Always keep the latest content in a ref so the async editor creation
  // can pick it up even if it finishes after the content has changed.
  contentRef.current = content;

  const isBinary = content.includes("\0");

  const setupEditor = useCallback(async () => {
    if (!editorRef.current) return;

    setLoading(true);
    setError(null);

    try {
      const [{ EditorView, keymap, lineNumbers, highlightActiveLine }, { defaultKeymap, history, historyKeymap }, { bracketMatching, foldGutter, indentOnInput, syntaxHighlighting, defaultHighlightStyle }, { oneDark }, { searchKeymap }, { autocompletion, closeBrackets, closeBracketsKeymap }] = await Promise.all([
        import("@codemirror/view"),
        import("@codemirror/commands"),
        import("@codemirror/language"),
        import("@codemirror/theme-one-dark"),
        import("@codemirror/search"),
        import("@codemirror/autocomplete"),
      ]);

      const lang = detectLanguage(filePath);
      const langLoader = LANG_LOADERS[lang];

      const extensions: any[] = [
        lineNumbers(),
        highlightActiveLine(),
        bracketMatching(),
        foldGutter(),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle),
        oneDark,
        keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, ...closeBracketsKeymap]),
        history(),
        autocompletion(),
        closeBrackets(),
        EditorView.lineWrapping,
        EditorView.editable.of(!readOnly),
        EditorView.updateListener.of((update: any) => {
          if (update.docChanged && onChange && !suppressOnChange.current) {
            onChange(update.state.doc.toString());
          }
        }),
      ];

      if (langLoader) {
        const langModule = await langLoader();
        const langSupport = langModule.javascript ? langModule.javascript() : langModule[lang] ? langModule[lang]() : null;
        if (langSupport) {
          extensions.push(langSupport);
        }
      }

      // Save shortcut
      extensions.push(
        keymap.of([
          {
            key: "Mod-s",
            run: (view: any) => {
              if (onSave) {
                onSave(view.state.doc.toString());
              }
              return true;
            },
            preventDefault: true,
          },
        ]),
      );

      // Destroy previous editor
      if (viewRef.current) {
        viewRef.current.destroy();
        viewRef.current = null;
      }

      // Use the LATEST content from ref, not the stale closure value
      const latestContent = contentRef.current;

      const state = await (await import("@codemirror/state")).EditorState.create({
        doc: latestContent,
        extensions,
      });

      if (!editorRef.current) return;

      viewRef.current = new EditorView({
        state,
        parent: editorRef.current,
      });

      // After creation, check if content changed while we were building
      const finalContent = contentRef.current;
      if (finalContent !== latestContent) {
        suppressOnChange.current = true;
        viewRef.current.dispatch({
          changes: { from: 0, to: viewRef.current.state.doc.length, insert: finalContent },
        });
        suppressOnChange.current = false;
      }
    } catch (err) {
      console.error("Failed to init editor", err);
      setError("Failed to load editor");
    } finally {
      setLoading(false);
    }
  }, [filePath, readOnly]); // Only recreate editor when file or readOnly changes

  useEffect(() => {
    setupEditor();
    return () => {
      if (viewRef.current) {
        viewRef.current.destroy();
        viewRef.current = null;
      }
    };
  }, [setupEditor]);

  // Update content from outside (file loaded, etc.)
  useEffect(() => {
    if (viewRef.current && content !== viewRef.current.state.doc.toString()) {
      suppressOnChange.current = true;
      viewRef.current.dispatch({
        changes: {
          from: 0,
          to: viewRef.current.state.doc.length,
          insert: content,
        },
      });
      suppressOnChange.current = false;
    }
  }, [content]);

  if (isBinary) {
    return (
      <div style={{ padding: 24, display: "flex", alignItems: "center", justifyContent: "center", height: "100%" }}>
        <Alert
          type="warning"
          showIcon
          message="Binary file"
          description="This file cannot be edited because it contains binary data."
        />
      </div>
    );
  }

  if (error) {
    return (
      <div style={{ padding: 24 }}>
        <Alert type="error" message={error} />
      </div>
    );
  }

  return (
    <div style={{ position: "relative", height: "100%" }}>
      {loading && (
        <div style={{ position: "absolute", top: "50%", left: "50%", zIndex: 10 }}>
          <Spin />
        </div>
      )}
      <div ref={editorRef} style={{ height: "100%", overflow: "auto" }} />
    </div>
  );
}
