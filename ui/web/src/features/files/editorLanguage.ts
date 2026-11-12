import type { Extension } from "@codemirror/state";

/**
 * Language identity for the Files editor.
 *
 * These are deliberately finer than "one loader per file group": TypeScript,
 * TSX, JavaScript and JSX are four different parses. Before, `ts`/`tsx` were
 * both mapped onto `javascript()` with no `typescript` flag, so TS syntax
 * (`: Type` annotations, `interface`, generics) highlighted as if it were JS —
 * generics in particular re-parsed as comparisons.
 */
export type EditorLanguageId =
  | "go"
  | "gomod"
  | "makefile"
  | "shell"
  | "properties"
  | "typescript"
  | "tsx"
  | "javascript"
  | "jsx"
  | "json"
  | "markdown"
  | "yaml"
  | "css"
  | "html"
  | "python"
  | "sql"
  | "text";

const EXTENSION_LANGUAGES: Record<string, EditorLanguageId> = {
  go: "go",
  // TypeScript family — must NOT fall through to javascript().
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  tsx: "tsx",
  // JavaScript family.
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "jsx",
  json: "json",
  jsonc: "json",
  md: "markdown",
  mdx: "markdown",
  markdown: "markdown",
  yaml: "yaml",
  yml: "yaml",
  css: "css",
  // SCSS/LESS are close enough to CSS that the CSS parser is far more useful
  // than plain text; it degrades to unhighlighted text past the first $/@ mixin.
  scss: "css",
  less: "css",
  html: "html",
  htm: "html",
  py: "python",
  sql: "sql",
  sh: "shell",
  bash: "shell",
  zsh: "shell",
  env: "shell",
  mk: "makefile",
  ini: "properties",
  properties: "properties",
  txt: "text",
  text: "text",
};

export const EDITOR_LANGUAGES: EditorLanguageId[] = [
  "text", "go", "gomod", "makefile", "shell", "properties", "typescript", "tsx",
  "javascript", "jsx", "json", "markdown", "yaml", "css", "html", "python", "sql",
];

export function languageLabel(id: EditorLanguageId): string {
  return id === "gomod" ? "go.mod (basic)" : id === "makefile" ? "Makefile (basic)" : id;
}

/** File names first, extensions second; unknown formats stay text (no guessing). */
export function detectLanguage(path: string): EditorLanguageId {
  const base = (path.split(/[\\/]/).pop() ?? path).toLowerCase();
  if (base === "go.mod" || base === "go.work") return "gomod";
  if (/^(?:gnu)?makefile(?:\..+)?$/.test(base)) return "makefile";
  if (base === ".env" || base.startsWith(".env.")) return "shell";
  const dot = base.lastIndexOf(".");
  // A dotfile (".gitignore") has no extension; only a dot past index 0 counts.
  const ext = dot > 0 ? base.slice(dot + 1).toLowerCase() : "";
  return EXTENSION_LANGUAGES[ext] ?? "text";
}

/** True when the language has no parser and the editor shows plain text. */
export function isPlainTextLanguage(id: EditorLanguageId): boolean {
  return id === "text";
}

/**
 * Load the CodeMirror language support for `id`, or null for plain text.
 *
 * Every branch is a dynamic import so the language parsers stay out of the
 * entry chunk (the editor itself is lazy for the same reason).
 */
export async function loadLanguageSupport(id: EditorLanguageId): Promise<Extension | null> {
  switch (id) {
    case "go":
    case "gomod":
      // Basic Go tokens, not a full module-file grammar. Indentation supplies
      // folding for require/replace/use blocks independently of the parser.
      return (await import("@codemirror/lang-go")).go();
    case "makefile":
    case "shell": {
      // Make recipes share shell tokens (comments, strings, variables). This is
      // deliberately labelled basic, not a complete GNU Make grammar.
      const { StreamLanguage } = await import("@codemirror/language");
      return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/shell")).shell);
    }
    case "properties": {
      const { StreamLanguage } = await import("@codemirror/language");
      return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/properties")).properties);
    }
    case "typescript":
      return (await import("@codemirror/lang-javascript")).javascript({ typescript: true });
    case "tsx":
      // TSX needs BOTH flags: typescript for the type syntax, jsx for the
      // element syntax. Setting only one makes half the file parse as errors.
      return (await import("@codemirror/lang-javascript")).javascript({ typescript: true, jsx: true });
    case "jsx":
      return (await import("@codemirror/lang-javascript")).javascript({ jsx: true });
    case "javascript":
      return (await import("@codemirror/lang-javascript")).javascript();
    case "json":
      return (await import("@codemirror/lang-json")).json();
    case "markdown":
      return (await import("@codemirror/lang-markdown")).markdown();
    case "yaml":
      return (await import("@codemirror/lang-yaml")).yaml();
    case "css":
      return (await import("@codemirror/lang-css")).css();
    case "html":
      return (await import("@codemirror/lang-html")).html();
    case "python":
      return (await import("@codemirror/lang-python")).python();
    case "sql":
      return (await import("@codemirror/lang-sql")).sql();
    case "text":
      return null;
    default:
      return null;
  }
}
