import { useEffect, useRef } from "react";
import type { GitDiffBase } from "../../lib/api";

type FilesView = {
  workspaceRoot?: string;
  selectedPath?: string;
  draft?: string;
  sidebarView?: "files" | "changes";
  editorView?: "editor" | "git";
  gitBase?: GitDiffBase;
};

// Navigation lifetime only. Never write file contents (possibly secrets) into
// browser storage; a reload still reads the actual file from the backend.
const views = new Map<string, FilesView>();

export function filesViewKey(surface: "page" | "dock" | "workspace", root: string, node?: string | null, relay?: string) {
  return JSON.stringify([surface, node ?? "", relay ?? "", root]);
}

export function readFilesView(key: string): FilesView {
  return views.get(key) ?? {};
}

export function rememberFilesView(key: string, view: FilesView) {
  views.set(key, view);
}

// The owner is keyed by workspace/node so a snapshot can never migrate into
// another project. A ref captures the latest buffer, even on route unmount.
export function useRememberFilesView(key: string, view: FilesView) {
  const latest = useRef(view);
  latest.current = view;
  useEffect(() => () => rememberFilesView(key, latest.current), [key]);
}
