import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { foldable } from "@codemirror/language";
import { indentationFolding } from "./indentationFolding";

function fold(doc: string, line = 1) {
  const state = EditorState.create({ doc, extensions: indentationFolding });
  const header = state.doc.line(line);
  const range = foldable(state, header.from, header.to);
  return range ? state.sliceDoc(range.from, range.to) : null;
}

describe("indentation fold fallback", () => {
  it("folds Makefile tabbed recipes without swallowing the next target", () => {
    expect(fold('build:\n\techo "test"\n\tgo build ./...\n\nclean:\n\trm -rf out'))
      .toBe('\n\techo "test"\n\tgo build ./...');
  });
  it("folds go.mod requirement blocks without claiming the closing parenthesis", () => {
    expect(fold("require (\n\texample.com/a v1.0.0\n\texample.com/b v2.0.0\n)"))
      .toBe("\n\texample.com/a v1.0.0\n\texample.com/b v2.0.0");
  });
  it("handles nested blocks, blank lines, and the last block at EOF", () => {
    const text = "root\n  nested\n    leaf\n\n  child";
    expect(fold(text)).toBe("\n  nested\n    leaf\n\n  child");
    expect(fold(text, 2)).toBe("\n    leaf");
    expect(fold(text, 3)).toBeNull();
  });
  it("leaves flat env files, empty lines and single lines unfolded", () => {
    expect(fold("PORT=8080\nNAME=test")).toBeNull();
    expect(fold("\n  text")).toBeNull();
    expect(fold("just text")).toBeNull();
  });
  it("recomputes ranges on immutable document changes", () => {
    const state = EditorState.create({ doc: "root\n  leaf", extensions: indentationFolding });
    expect(foldable(state, 0, 4)).not.toBeNull();
    const updated = state.update({ changes: { from: 5, to: 7, insert: "" } }).state;
    expect(foldable(updated, 0, 4)).toBeNull();
  });
});
