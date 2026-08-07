import { create } from "zustand";

// Chat V2 layout store: left sessions rail + right dock rail state.
// Pure state machine + a tiny localStorage persistence layer (same
// hand-rolled pattern as store/layoutPersistence.ts, kept independent
// so the legacy chat workspace layout is untouched).

export type DockTab = "files" | "terminal" | "tasks" | "preview" | "status";

export const DOCK_TABS: ReadonlyArray<DockTab> = ["files", "terminal", "tasks", "preview", "status"];

export const CHAT_V2_STORAGE_KEY = "godex.web.chatV2.layout.v1";

export const LEFT_RAIL = { min: 200, max: 600, defaultWidth: 264 } as const;
export const RIGHT_DOCK = { min: 320, max: 1200, defaultWidth: 420 } as const;

export interface ChatV2Snapshot {
  leftCollapsed: boolean;
  rightCollapsed: boolean;
  activeDockTab: DockTab;
  leftWidth: number;
  rightWidth: number;
}

export interface ChatV2Actions {
  toggleLeft: () => void;
  toggleRight: () => void;
  setActiveDockTab: (tab: DockTab) => void;
  setLeftWidth: (width: number) => void;
  setRightWidth: (width: number) => void;
  reset: () => void;
}

export type ChatV2State = ChatV2Snapshot & ChatV2Actions;

export const DEFAULT_CHAT_V2_SNAPSHOT: ChatV2Snapshot = {
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
export function defaultChatV2Snapshot(): ChatV2Snapshot {
  if (!isNarrowViewport()) {
    return { ...DEFAULT_CHAT_V2_SNAPSHOT };
  }
  return { ...DEFAULT_CHAT_V2_SNAPSHOT, leftCollapsed: true, rightCollapsed: true };
}

function isDockTab(value: unknown): value is DockTab {
  return typeof value === "string" && (DOCK_TABS as ReadonlyArray<string>).includes(value);
}

function clamp(value: number, min: number, max: number, fallback: number): number {
  if (Number.isNaN(value) || !Number.isFinite(value)) return fallback;
  return Math.min(max, Math.max(min, Math.floor(value)));
}

export const useChatV2Store = create<ChatV2State>((set) => ({
  ...defaultChatV2Snapshot(),

  toggleLeft: () => set((state) => ({ leftCollapsed: !state.leftCollapsed })),
  toggleRight: () => set((state) => ({ rightCollapsed: !state.rightCollapsed })),

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
    set(() => ({ leftWidth: clamp(width, LEFT_RAIL.min, LEFT_RAIL.max, DEFAULT_CHAT_V2_SNAPSHOT.leftWidth) })),
  setRightWidth: (width) =>
    set(() => ({ rightWidth: clamp(width, RIGHT_DOCK.min, RIGHT_DOCK.max, DEFAULT_CHAT_V2_SNAPSHOT.rightWidth) })),

  reset: () => set(() => ({ ...defaultChatV2Snapshot() })),
}));

// ---------------------------------------------------------------------------
// Persistence (hand-rolled, same rationale as store/layoutPersistence.ts).
// ---------------------------------------------------------------------------

export function serializeSnapshot(state: ChatV2State): ChatV2Snapshot {
  return {
    leftCollapsed: state.leftCollapsed,
    rightCollapsed: state.rightCollapsed,
    activeDockTab: state.activeDockTab,
    leftWidth: state.leftWidth,
    rightWidth: state.rightWidth,
  };
}

export function hydrateSnapshot(raw: string | null): ChatV2Snapshot | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") return null;
    const candidate = parsed as Partial<ChatV2Snapshot>;
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

export function readPersistedSnapshot(): ChatV2Snapshot | null {
  if (typeof window === "undefined") return null;
  // Mobile always starts from the conversation view; a layout persisted on a
  // desktop viewport (files dock open, rails wide) would otherwise cover the
  // whole screen with the files panel on a narrow device.
  if (isNarrowViewport()) {
    return defaultChatV2Snapshot();
  }
  try {
    return hydrateSnapshot(window.localStorage.getItem(CHAT_V2_STORAGE_KEY));
  } catch {
    return null;
  }
}

export function writePersistedSnapshot(state: ChatV2State | ChatV2Snapshot): void {
  if (typeof window === "undefined") return;
  try {
    const snapshot = "toggleLeft" in state ? serializeSnapshot(state) : state;
    window.localStorage.setItem(CHAT_V2_STORAGE_KEY, JSON.stringify(snapshot));
  } catch {
    // Quota / private mode — in-memory state still works.
  }
}
