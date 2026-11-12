import { useEffect, useRef, useCallback, useState } from "react";
import { Spin, Alert, Button, Space, Tooltip, Select } from "antd";
import { NodeCollapseOutlined, NodeExpandOutlined } from "@ant-design/icons";
import { usePrefersDark } from "../../hooks/usePrefersDark";
import { detectLanguage, loadLanguageSupport, EDITOR_LANGUAGES, languageLabel, type EditorLanguageId } from "./editorLanguage";
import { indentationFolding } from "./indentationFolding";

interface CodeEditorProps {
  content: string;
  filePath: string;
  readOnly?: boolean;
  onSave?: (content: string) => void;
  onChange?: (content: string) => void;
}

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
  // Keep the LATEST callbacks in refs. The editor is created by an async
  // useCallback that intentionally does not depend on the handlers (recreating
  // it on every render would throw away undo history and fold state), so a
  // closure capture would pin the first render's handlers forever: after the
  // parent re-renders, Mod-s would call a stale onSave that still holds the
  // old content.
  const onSaveRef = useRef(onSave);
  const onChangeRef = useRef(onChange);
  onSaveRef.current = onSave;
  onChangeRef.current = onChange;
  const setupGenerationRef = useRef(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [languageOverride, setLanguageOverride] = useState<{ path: string; id: EditorLanguageId } | null>(null);
  const manualLanguage = languageOverride?.path === filePath ? languageOverride.id : undefined;
  const language = manualLanguage ?? detectLanguage(filePath);
  const prefersDark = usePrefersDark();

  // Always keep the latest content in a ref so the async editor creation
  // can pick it up even if it finishes after the content has changed.
  contentRef.current = content;

  const isBinary = content.includes("\0");

  const setupEditor = useCallback(async () => {
    if (!editorRef.current) return;

    const targetFile = filePath;
    // Bumped on every (re)creation. Async work below checks it before touching
    // the DOM so a slow language load for the PREVIOUS file cannot attach its
    // parser — or its content — to the newly created editor.
    const generation = ++setupGenerationRef.current;

    setLoading(true);
    setError(null);

    try {
      const [{ EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter }, { defaultKeymap, history, historyKeymap }, languageApi, { oneDark }, { searchKeymap }, { autocompletion, closeBrackets, closeBracketsKeymap }] = await Promise.all([
        import("@codemirror/view"),
        import("@codemirror/commands"),
        import("@codemirror/language"),
        import("@codemirror/theme-one-dark"),
        import("@codemirror/search"),
        import("@codemirror/autocomplete"),
      ]);
      const { bracketMatching, foldGutter, foldKeymap, indentOnInput, syntaxHighlighting, defaultHighlightStyle, foldAll, unfoldAll } = languageApi;

      const lang = language;

      const extensions: any[] = [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightActiveLine(),
        bracketMatching(),
        // foldGutter provides clickable markers; foldKeymap binds fold/unfold
        // to Ctrl-Shift-[ / ] (Cmd-Alt on macOS), all to Ctrl-Alt-[ / ].
        foldGutter(),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle),
        keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, ...closeBracketsKeymap, ...foldKeymap]),
        history(),
        autocompletion(),
        closeBrackets(),
        EditorView.lineWrapping,
        EditorView.editable.of(!readOnly),
        EditorView.contentAttributes.of({ "aria-label": `Code editor: ${targetFile}` }),
        EditorView.updateListener.of((update: any) => {
          if (update.docChanged && onChangeRef.current && !suppressOnChange.current) {
            onChangeRef.current(update.state.doc.toString());
          }
        }),
      ];

      // Follow the OS color scheme: oneDark for dark mode, the default
      // light theme otherwise (matches the surrounding godex panel colors).
      if (prefersDark) {
        extensions.push(oneDark);
      }

      // Load the parser for THIS file. A missing/unknown format resolves to
      // null and simply stays plain text.
      const langSupport = await loadLanguageSupport(lang);
      if (generation !== setupGenerationRef.current) return;
      if (langSupport) {
        extensions.push(langSupport);
      }
      if (["text", "gomod", "makefile", "shell", "properties"].includes(lang)) {
        extensions.push(indentationFolding);
      }

      // Save shortcut. Reads from the ref so it always calls the current handler.
      extensions.push(
        keymap.of([
          {
            key: "Mod-s",
            run: (view: any) => {
              onSaveRef.current?.(view.state.doc.toString());
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

      if (generation !== setupGenerationRef.current) return;

      // Use the LATEST content from ref, not the stale closure value
      const latestContent = contentRef.current;

      const state = await (await import("@codemirror/state")).EditorState.create({
        doc: latestContent,
        extensions,
      });

      if (generation !== setupGenerationRef.current || !editorRef.current) return;

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
      if (generation === setupGenerationRef.current) {
        setError("Failed to load editor");
      }
    } finally {
      if (generation === setupGenerationRef.current) {
        setLoading(false);
      }
    }
  }, [filePath, readOnly, prefersDark, language]); // Callback changes never rebuild the editor

  useEffect(() => {
    setupEditor();
    return () => {
      // Invalidate any in-flight setup for this editor.
      setupGenerationRef.current += 1;
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

  const runFoldCommand = useCallback(async (command: "foldAll" | "unfoldAll") => {
    const view = viewRef.current;
    if (!view) return;
    const api = await import("@codemirror/language");
    (command === "foldAll" ? api.foldAll : api.unfoldAll)(view);
    view.focus();
  }, []);

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

  // Folding is a view concern: available even for read-only/unknown files
  // through the indentation fallback. Flat files simply have no fold ranges.

  return (
    <div style={{ position: "relative", height: "100%", display: "flex", flexDirection: "column", minHeight: 0 }}>
      <div
        data-testid="code-editor-toolbar"
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "flex-end",
          gap: 4,
          padding: "2px 6px",
          borderBottom: "1px solid var(--border)",
          flexShrink: 0,
        }}
      >
        <div data-testid="code-editor-language" data-language={language} style={{ marginRight: "auto" }}>
          <Select
            size="small"
            variant="borderless"
            aria-label="Editor language"
            value={manualLanguage ?? "auto"}
            onChange={(id) => setLanguageOverride(id === "auto" ? null : { path: filePath, id: id as EditorLanguageId })}
            options={[
              { value: "auto", label: `Auto (${languageLabel(detectLanguage(filePath))})` },
              ...EDITOR_LANGUAGES.map((id) => ({ value: id, label: languageLabel(id) })),
            ]}
            style={{ minWidth: 155 }}
          />
        </div>
        <Space size={2}>
          <Tooltip title="Fold all (Ctrl-Alt-[)">
            <Button
              size="small"
              type="text"
              icon={<NodeCollapseOutlined />}
              aria-label="Fold all code blocks"
              title="Fold all (Ctrl-Alt-[)"
              onClick={() => runFoldCommand("foldAll")}
              data-testid="code-editor-fold-all"
            />
          </Tooltip>
          <Tooltip title="Unfold all (Ctrl-Alt-])">
            <Button
              size="small"
              type="text"
              icon={<NodeExpandOutlined />}
              aria-label="Unfold all code blocks"
              title="Unfold all (Ctrl-Alt-])"
              onClick={() => runFoldCommand("unfoldAll")}
              data-testid="code-editor-unfold-all"
            />
          </Tooltip>
        </Space>
      </div>
      <div style={{ position: "relative", flex: 1, minHeight: 0 }}>
        {loading && (
          <div style={{ position: "absolute", top: "50%", left: "50%", zIndex: 10 }}>
            <Spin />
          </div>
        )}
        <div ref={editorRef} style={{ height: "100%", overflow: "auto" }} />
      </div>
    </div>
  );
}
