import { describe, expect, it } from "vitest";
import { detectLanguage, isPlainTextLanguage, loadLanguageSupport } from "./editorLanguage";
import { EditorState } from "@codemirror/state";
import { ensureSyntaxTree, syntaxTree } from "@codemirror/language";
import { highlightTree, classHighlighter } from "@lezer/highlight";

describe("detectLanguage", () => {
  it("maps Go to the Go grammar", () => {
    // Regression: "go" was in the extension table but had no loader, so Go
    // files rendered as unhighlighted plain text.
    expect(detectLanguage("internal/agent/agent.go")).toBe("go");
  });

  it("maps YAML and YML to the YAML grammar", () => {
    expect(detectLanguage("godex.yaml")).toBe("yaml");
    expect(detectLanguage("config/app.yml")).toBe("yaml");
  });

  it("does not fall back to JavaScript for TypeScript", () => {
    // Regression: ts/tsx were both initialized as javascript(), which reparsed
    // generics as comparisons and left type annotations unstyled.
    expect(detectLanguage("src/lib/api.ts")).toBe("typescript");
    expect(detectLanguage("src/types.d.ts")).not.toBe("javascript");
  });

  it("distinguishes TSX from plain TypeScript", () => {
    // TSX needs both flags; treating it as TS makes JSX elements parse as errors.
    expect(detectLanguage("src/App.tsx")).toBe("tsx");
    expect(detectLanguage("App.tsx")).not.toBe(detectLanguage("api.ts"));
  });

  it("distinguishes JSX from plain JavaScript", () => {
    expect(detectLanguage("src/App.jsx")).toBe("jsx");
    expect(detectLanguage("src/util.js")).toBe("javascript");
  });

  it("keeps the already-supported languages", () => {
    expect(detectLanguage("a.json")).toBe("json");
    expect(detectLanguage("a.md")).toBe("markdown");
    expect(detectLanguage("a.css")).toBe("css");
    expect(detectLanguage("a.html")).toBe("html");
    expect(detectLanguage("a.py")).toBe("python");
    expect(detectLanguage("a.sql")).toBe("sql");
  });

  it("handles modern module extensions", () => {
    expect(detectLanguage("a.mjs")).toBe("javascript");
    expect(detectLanguage("a.cjs")).toBe("javascript");
    expect(detectLanguage("a.mts")).toBe("typescript");
    expect(detectLanguage("a.cts")).toBe("typescript");
  });

  it("degrades unknown formats to plain text", () => {
    expect(detectLanguage("binary.wasm")).toBe("text");
    expect(detectLanguage("data.unknownext")).toBe("text");
    expect(detectLanguage("LICENSE")).toBe("text");
    expect(isPlainTextLanguage(detectLanguage("data.unknownext"))).toBe(true);
  });

  it("does not treat a dotfile's name as an extension", () => {
    // ".gitignore" must not resolve to a "gitignore" extension, and a leading
    // dot is not an extension separator.
    expect(detectLanguage(".gitignore")).toBe("text");
    expect(detectLanguage("dir/.env")).toBe("shell");
  });

  it("recognizes extensionless and configuration file names before extensions", () => {
    expect(detectLanguage("go.mod")).toBe("gomod");
    expect(detectLanguage("src/go.work")).toBe("gomod");
    expect(detectLanguage("Makefile")).toBe("makefile");
    expect(detectLanguage("GNUmakefile")).toBe("makefile");
    expect(detectLanguage("Makefile.local")).toBe("makefile");
    expect(detectLanguage("build/rules.mk")).toBe("makefile");
    expect(detectLanguage(".env.local")).toBe("shell");
    expect(detectLanguage("config/.env.production")).toBe("shell");
    expect(detectLanguage("settings.ini")).toBe("properties");
    expect(detectLanguage("app.properties")).toBe("properties");
    expect(detectLanguage(".env.ts")).toBe("shell");
    expect(detectLanguage("C:\\settings\\.env")).toBe("shell");
  });

  it.each([
    ["go.mod", "module example.com/app\n\nrequire (\n example.com/lib v1.0.0 // indirect\n)\n"],
    [".env", '# settings\nexport PORT=8080\nNAME="test"\n'],
    ["Makefile", '# build\nall:\n\techo "$(TARGET)"\n'],
    ["config.ini", '# comment\n[server]\nport=8080\n'],
  ])("loads real token highlighting for %s", async (path, doc) => {
    const extension = await loadLanguageSupport(detectLanguage(path));
    expect(extension).not.toBeNull();
    const state = EditorState.create({ doc, extensions: extension! });
    ensureSyntaxTree(state, doc.length, 1000);
    const highlighted: string[] = [];
    highlightTree(syntaxTree(state), classHighlighter, (from, to) => highlighted.push(doc.slice(from, to)));
    expect(highlighted.length).toBeGreaterThan(0);
  });

  it("is case-insensitive about the extension", () => {
    expect(detectLanguage("A.GO")).toBe("go");
    expect(detectLanguage("B.Ts")).toBe("typescript");
  });

  it("uses the last segment of a dotted filename", () => {
    expect(detectLanguage("archive.test.ts")).toBe("typescript");
    expect(detectLanguage("a/b.c/deep/file.go")).toBe("go");
  });
});
