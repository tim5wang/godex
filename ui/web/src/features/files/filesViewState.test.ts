import { describe, it, expect } from "vitest";
import { filesViewKey, readFilesView, rememberFilesView } from "./filesViewState";

describe("Files navigation state", () => {
  it("restores file, view and draft without sharing workspace/node/relay/surface scopes", () => {
    const key = filesViewKey("page", "/project", "node-a");
    const view = { selectedPath: "src/main.go", draft: "unsaved", editorView: "git" as const };
    rememberFilesView(key, view);
    expect(readFilesView(key)).toEqual(view);
    for (const other of [
      filesViewKey("page", "/other", "node-a"),
      filesViewKey("page", "/project", "node-b"),
      filesViewKey("dock", "/project", "node-a"),
      filesViewKey("page", "/project", "node-a", "relay-b"),
    ]) expect(readFilesView(other)).toEqual({});
  });
  it("clearing a selection does not reopen its previous file or draft", () => {
    const key = filesViewKey("dock", "/cleared");
    rememberFilesView(key, { selectedPath: "old.go", draft: "old draft" });
    rememberFilesView(key, { selectedPath: undefined });
    expect(readFilesView(key).selectedPath).toBeUndefined();
    expect(readFilesView(key).draft).toBeUndefined();
  });
});
