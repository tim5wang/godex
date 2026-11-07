import { create } from "zustand";
import type { RecallScope } from "../../lib/apiWorkContext";

// Work-desktop UI state shared between the chat left rail (desktop list) and the
// chat right dock (desktop sections). It lives in its own store because the rail
// and the dock are separate components that must agree on which desktop is open,
// which section is showing, and which editor modal is pending — the same reason
// the chat layout store exists. Deliberately NOT persisted except nothing: it is
// navigation state, and a stale selected id would show an empty desktop on load.

/** Sections a desktop shows in the chat right dock. These are the desktop's own
 *  boards; the chat page's own dock tabs (files/terminal/...) are untouched.
 *  "delegations" is the advertised task board (cards), distinct from the chat's
 *  own subagent "tasks" tab, which is a different data model. */
export type WorkDesktopSection = "overview" | "plan" | "conversations" | "delegations" | "knowledge" | "timeline" | "resources" | "attention";

export const WORK_DESKTOP_SECTIONS: ReadonlyArray<WorkDesktopSection> = [
  "overview",
  "plan",
  "conversations",
  "delegations",
  "knowledge",
  "timeline",
  "resources",
  "attention",
];

/** The desktop's inline editors (one modal). Opened from either the rail or the
 *  dock, so the pending editor is store state rather than local state: there is
 *  exactly one modal instance, rendered by the dock. */
export type WorkDesktopEditor = "create" | "delegate" | "session" | "fact" | "resource" | "relation" | "review";

/** Every field any of the editor forms can submit. Kept next to the editor type
 *  so the data hook and the modal share one shape without importing each other. */
export type EditorValues = {
  title?: string;
  background?: string;
  goal?: string;
  description?: string;
  acceptance?: string;
  project?: string;
  workDir?: string;
  template?: string;
  topic?: string;
  sessionId?: string;
  workspaceDir?: string;
  resourceKind?: string;
  locator?: string;
  node?: string;
  purpose?: string;
  target?: string;
  relationKind?: string;
  condition?: string;
  content?: string;
  scope?: string;
  subject?: string;
  predicate?: string;
  source?: string;
  sourceExcerpt?: string;
  observedAt?: string;
  provenance?: string;
  supersedes?: string;
  factKind?: string;
  reason?: string;
};

interface WorkDesktopState {
  /** Selected desktop (work context) id; empty = fall back to the first one. */
  contextId: string;
  section: WorkDesktopSection;
  editor?: WorkDesktopEditor;
  /** The fact a "review" editor is acting on. */
  reviewTargetId: string;
  briefOpen: boolean;
  recallQuery: string;
  recallScope: RecallScope;
  /** Which desktops the attention inbox covers: everything, or only the open one. */
  attentionScope: "current" | "all";
  /** Free-text filter over the desktop list in the rail. */
  search: string;
  /** Bumped when the user asks to be taken to the "changes since last seen" list
   *  (i.e. clicked a rail unread badge). A monotonically increasing nonce rather
   *  than a boolean, so repeated clicks re-trigger the reveal. */
  changesFocusNonce: number;
  /** The nonce the Overview board has already answered. It lives in the store rather
   *  than a component ref because the request and the mount happen in the SAME click:
   *  the rail switches to Overview and bumps the nonce together, so the panel is
   *  created BY the request. A ref initialised from the nonce would therefore always
   *  read "already handled" and silently drop the reveal — which is exactly the bug
   *  where jumping to the changes showed no marker at all. */
  changesFocusAck: number;
  setContextId: (id: string) => void;
  setSection: (section: WorkDesktopSection) => void;
  openEditor: (editor: WorkDesktopEditor, reviewTargetId?: string) => void;
  closeEditor: () => void;
  setBriefOpen: (open: boolean) => void;
  setRecallQuery: (query: string) => void;
  setRecallScope: (scope: RecallScope) => void;
  setAttentionScope: (scope: "current" | "all") => void;
  setSearch: (search: string) => void;
  revealChanges: () => void;
  /** Mark the pending reveal as answered, so it is not repeated on every remount. */
  ackChangesFocus: () => void;
}

export const useWorkDesktopStore = create<WorkDesktopState>((set) => ({
  contextId: "",
  section: "overview",
  editor: undefined,
  reviewTargetId: "",
  briefOpen: false,
  recallQuery: "",
  recallScope: "current",
  attentionScope: "all",
  search: "",
  changesFocusNonce: 0,
  changesFocusAck: 0,
  setContextId: (id) => set({ contextId: id, briefOpen: false }),
  setSection: (section) => set({ section }),
  openEditor: (editor, reviewTargetId = "") => set({ editor, reviewTargetId }),
  closeEditor: () => set({ editor: undefined, reviewTargetId: "" }),
  setBriefOpen: (briefOpen) => set({ briefOpen }),
  setRecallQuery: (recallQuery) => set({ recallQuery }),
  setRecallScope: (recallScope) => set({ recallScope }),
  setAttentionScope: (attentionScope) => set({ attentionScope }),
  setSearch: (search) => set({ search }),
  revealChanges: () => set((state) => ({ changesFocusNonce: state.changesFocusNonce + 1 })),
  ackChangesFocus: () => set((state) => ({ changesFocusAck: state.changesFocusNonce })),
}));

/** Attention-inbox items name the desktop tab where they resolve. The old tabs
 *  (tasks / conversations / files / chat) no longer exist as tabs, so aliases map
 *  them onto the section that inherited them — otherwise a click would land on
 *  Overview and the user would have to hunt for the item again. */
const SECTION_ALIASES: Record<string, WorkDesktopSection> = {
  tasks: "delegations",
  chat: "overview",
  files: "resources",
};

export function sectionForTarget(target: string): WorkDesktopSection {
  if ((WORK_DESKTOP_SECTIONS as ReadonlyArray<string>).includes(target)) {
    return target as WorkDesktopSection;
  }
  return SECTION_ALIASES[target] ?? "overview";
}
