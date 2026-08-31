import { create } from "zustand";

// Chat layout store: left sessions rail + right dock rail state.
// Pure state machine + a tiny localStorage persistence layer (same
// hand-rolled pattern as store/layoutPersistence.ts, kept independent
// so the global workspace layout is untouched).

export type DockTab = "files" | "terminal" | "tasks" | "preview" | "status";

export const DOCK_TABS: ReadonlyArray<DockTab> = ["files", "terminal", "tasks", "preview", "status"];

// Keep the persisted key stable so existing user layouts survive the module rename.
export const CHAT_LAYOUT_STORAGE_KEY = "godex.web.chatV2.layout.v1";

export const LEFT_RAIL = { min: 200, max: 600, defaultWidth: 264 } as const;
export const RIGHT_DOCK = { min: 320, max: 1200, defaultWidth: 420 } as const;

export interface ChatLayoutSnapshot {
  leftCollapsed: boolean;
  rightCollapsed: boolean;
  activeDockTab: DockTab;
  leftWidth: number;
  rightWidth: number;
}

export interface ChatLayoutActions {
  toggleLeft: () => void;
  toggleRight: () => void;
  closeRight: () => void;
  setActiveDockTab: (tab: DockTab) => void;
  setLeftWidth: (width: number) => void;
  setRightWidth: (width: number) => void;
  reset: () => void;
}

export type ChatLayoutStoreState = ChatLayoutSnapshot & ChatLayoutActions;

export const DEFAULT_CHAT_LAYOUT_SNAPSHOT: ChatLayoutSnapshot = {
  leftCollapsed: false,
  rightCollapsed: false,
  activeDockTab: "files",
  leftWidth: LEFT_RAIL.defaultWidth,
  rightWidth: RIGHT_DOCK.defaultWidth,
};

// Matches the responsive CSS breakpoint in styles.css (max-width: 900px).
const NARROW_VIEWPORT_QUERY = "(max-width: 900px)";

export function isNarrowViewport(): boolean {
  if (typeof window === "undefined") return false;
  try {
    return window.matchMedia(NARROW_VIEWPORT_QUERY).matches;
  } catch {
    return typeof window.innerWidth === "number" && window.innerWidth <= 900;
  }
}

// On phones the sessions rail and the files dock should stay out of the way:
// opening the app straight into the files panel (the desktop default) forces
// the user to close the dock and collapse the rail before they can chat.
export function defaultChatLayoutSnapshot(): ChatLayoutSnapshot {
  if (!isNarrowViewport()) {
    return { ...DEFAULT_CHAT_LAYOUT_SNAPSHOT };
  }
  return { ...DEFAULT_CHAT_LAYOUT_SNAPSHOT, leftCollapsed: true, rightCollapsed: true };
}

function isDockTab(value: unknown): value is DockTab {
  return typeof value === "string" && (DOCK_TABS as ReadonlyArray<string>).includes(value);
}

function clamp(value: number, min: number, max: number, fallback: number): number {
  if (Number.isNaN(value) || !Number.isFinite(value)) return fallback;
  return Math.min(max, Math.max(min, Math.floor(value)));
}

export const useConversationLayoutStore = create<ChatLayoutStoreState>((set) => ({
  ...defaultChatLayoutSnapshot(),

  toggleLeft: () => set((state) => ({ leftCollapsed: !state.leftCollapsed })),
  toggleRight: () => set((state) => ({ rightCollapsed: !state.rightCollapsed })),
  closeRight: () => set({ rightCollapsed: true }),

  // Clicking a tab activates it and expands a collapsed dock; clicking the
  // already-active tab on an expanded dock collapses the rail instead
  // (mainstream agent UX: icon rail doubles as the collapse control).
  setActiveDockTab: (tab) => {
    if (!isDockTab(tab)) return;
    set((state) => {
      if (state.activeDockTab === tab && !state.rightCollapsed) {
        return { rightCollapsed: true };
      }
      return { activeDockTab: tab, rightCollapsed: false };
    });
  },

  setLeftWidth: (width) =>
    set(() => ({ leftWidth: clamp(width, LEFT_RAIL.min, LEFT_RAIL.max, DEFAULT_CHAT_LAYOUT_SNAPSHOT.leftWidth) })),
  setRightWidth: (width) =>
    set(() => ({ rightWidth: clamp(width, RIGHT_DOCK.min, RIGHT_DOCK.max, DEFAULT_CHAT_LAYOUT_SNAPSHOT.rightWidth) })),

  reset: () => set(() => ({ ...defaultChatLayoutSnapshot() })),
}));

// ---------------------------------------------------------------------------
// Persistence (hand-rolled, same rationale as store/layoutPersistence.ts).
// ---------------------------------------------------------------------------

export function serializeSnapshot(state: ChatLayoutStoreState): ChatLayoutSnapshot {
  return {
    leftCollapsed: state.leftCollapsed,
    rightCollapsed: state.rightCollapsed,
    activeDockTab: state.activeDockTab,
    leftWidth: state.leftWidth,
    rightWidth: state.rightWidth,
  };
}

export function hydrateSnapshot(raw: string | null): ChatLayoutSnapshot | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") return null;
    const candidate = parsed as Partial<ChatLayoutSnapshot>;
    if (!isDockTab(candidate.activeDockTab)) return null;
    return {
      leftCollapsed: Boolean(candidate.leftCollapsed),
      rightCollapsed: Boolean(candidate.rightCollapsed),
      activeDockTab: candidate.activeDockTab,
      leftWidth: clamp(candidate.leftWidth ?? LEFT_RAIL.defaultWidth, LEFT_RAIL.min, LEFT_RAIL.max, LEFT_RAIL.defaultWidth),
      rightWidth: clamp(candidate.rightWidth ?? RIGHT_DOCK.defaultWidth, RIGHT_DOCK.min, RIGHT_DOCK.max, RIGHT_DOCK.defaultWidth),
    };
  } catch {
    return null;
  }
}

export function readPersistedSnapshot(): ChatLayoutSnapshot | null {
  if (typeof window === "undefined") return null;
  // Mobile always starts from the conversation view; a layout persisted on a
  // desktop viewport (files dock open, rails wide) would otherwise cover the
  // whole screen with the files panel on a narrow device.
  if (isNarrowViewport()) {
    return defaultChatLayoutSnapshot();
  }
  try {
    return hydrateSnapshot(window.localStorage.getItem(CHAT_LAYOUT_STORAGE_KEY));
  } catch {
    return null;
  }
}

export function writePersistedSnapshot(state: ChatLayoutStoreState | ChatLayoutSnapshot): void {
  if (typeof window === "undefined") return;
  try {
    const snapshot = "toggleLeft" in state ? serializeSnapshot(state) : state;
    window.localStorage.setItem(CHAT_LAYOUT_STORAGE_KEY, JSON.stringify(snapshot));
  } catch {
    // Quota / private mode — in-memory state still works.
  }
}
