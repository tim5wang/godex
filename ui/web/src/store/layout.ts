import { create } from "zustand";

// ---------------------------------------------------------------------------
// Public types (mirror SPEC §4.6). Pure types only — no React, no IO.
// Persistence to localStorage and the Splitter / Antd integration land in
// later phases (P4). Keep this module a pure state machine so it is easy to
// unit-test in isolation.
// ---------------------------------------------------------------------------

export type PanelKey = "appNav" | "sessions" | "tasks" | "files" | "terminal" | "drawer";

export type PanelState = {
  collapsed: boolean;
  width?: number;
  visible: boolean;
};

export type GridPresetId =
  | "topChat_bottomTerminal"
  | "topFilesChat_bottomTerminal"
  | "topChat_bottomFilesTerminal"
  | "leftCol2x2"
  | "single";

export type GridSlot = "topLeft" | "topRight" | "bottomLeft" | "bottomRight" | "topFull" | "bottomFull";

export type GridOccupancy = {
  topLeft: PanelKey | null;
  topRight: PanelKey | null;
  bottomLeft: PanelKey | null;
  bottomRight: PanelKey | null;
};

export type GridRatios = {
  outerSplit: number; // 0~1, top vs bottom
  innerTopSplit?: number; // 0~1, topLeft vs topRight (when top is split)
  innerBottomSplit?: number; // 0~1, bottomLeft vs bottomRight (when bottom is split)
};

export type MobileTab = "chat" | "terminal" | "files" | "drawer" | "tasks";

export type LayoutSnapshot = {
  panels: Record<PanelKey, PanelState>;
  centerGridPreset: GridPresetId;
  centerGridRatios: GridRatios;
  centerGrid: GridOccupancy;
  mobileActiveTab: MobileTab;
  dockSide: "right" | "bottom";
};

export type LayoutActions = {
  toggle: (k: PanelKey) => void;
  setWidth: (k: PanelKey, w: number) => void;
  setGridPreset: (id: GridPresetId) => void;
  movePanelToGrid: (panel: PanelKey, slot: Exclude<GridSlot, "topFull" | "bottomFull">) => void;
  swapPanelInGrid: (panel: PanelKey, slot: Exclude<GridSlot, "topFull" | "bottomFull">) => void;
  setGridRatio: (key: keyof GridRatios, v: number) => void;
  setMobileActiveTab: (t: MobileTab) => void;
  reset: () => void;
};

export type LayoutState = LayoutSnapshot & LayoutActions;

// ---------------------------------------------------------------------------
// Default snapshot. Matches SPEC §3.2 (preset 2: topFilesChat_bottomTerminal)
// and §4.6 (panels: appNav + sessions expanded, others collapsed, dock on
// the right). Mobile tab defaults to chat.
// ---------------------------------------------------------------------------

export const DEFAULT_GRID_PRESET: GridPresetId = "topFilesChat_bottomTerminal";

export const DEFAULT_GRID_RATIOS: GridRatios = {
  outerSplit: 0.6, // top ~60% / bottom ~40% — chat dominated, terminal at the bottom
  innerTopSplit: 0.32, // files column ~32% / chat column ~68% of the top row
  innerBottomSplit: 0.5,
};

const DEFAULT_PANELS: Record<PanelKey, PanelState> = {
  appNav: { collapsed: false, width: 200, visible: true },
  sessions: { collapsed: false, width: 280, visible: true },
  tasks: { collapsed: true, width: 320, visible: true },
  files: { collapsed: true, width: 320, visible: true },
  terminal: { collapsed: true, width: 320, visible: true },
  drawer: { collapsed: true, width: 320, visible: true },
};

export const DEFAULT_GRID_OCCUPANCY: Record<GridPresetId, GridOccupancy> = {
  topFilesChat_bottomTerminal: {
    topLeft: "files",
    topRight: "chat",
    bottomLeft: "terminal",
    bottomRight: "terminal",
  },
  topChat_bottomTerminal: {
    topLeft: "chat",
    topRight: "chat",
    bottomLeft: "terminal",
    bottomRight: "terminal",
  },
  topChat_bottomFilesTerminal: {
    topLeft: "chat",
    topRight: "chat",
    bottomLeft: "files",
    bottomRight: "terminal",
  },
  leftCol2x2: {
    topLeft: "files",
    topRight: "chat",
    bottomLeft: "terminal",
    bottomRight: "chat",
  },
  single: {
    topLeft: "chat",
    topRight: "chat",
    bottomLeft: null,
    bottomRight: null,
  },
};

// Static catalogue that test/UI iterate over. `id` is unique, `label` is
// human-readable, `occupancy` is the SPEC §3.2 truth.
export const GRID_PRESETS: ReadonlyArray<{ id: GridPresetId; label: string; occupancy: GridOccupancy }> = [
  {
    id: "topChat_bottomTerminal",
    label: "Chat / Terminal (top · bottom)",
    occupancy: DEFAULT_GRID_OCCUPANCY.topChat_bottomTerminal,
  },
  {
    id: "topFilesChat_bottomTerminal",
    label: "Files · Chat / Terminal",
    occupancy: DEFAULT_GRID_OCCUPANCY.topFilesChat_bottomTerminal,
  },
  {
    id: "topChat_bottomFilesTerminal",
    label: "Chat / Files · Terminal",
    occupancy: DEFAULT_GRID_OCCUPANCY.topChat_bottomFilesTerminal,
  },
  {
    id: "leftCol2x2",
    label: "Left column: Files · Terminal, Right: Chat",
    occupancy: DEFAULT_GRID_OCCUPANCY.leftCol2x2,
  },
  {
    id: "single",
    label: "Single Chat",
    occupancy: DEFAULT_GRID_OCCUPANCY.single,
  },
];

export const DEFAULT_LAYOUT_SNAPSHOT: LayoutSnapshot = {
  panels: clonePanels(DEFAULT_PANELS),
  centerGridPreset: DEFAULT_GRID_PRESET,
  centerGridRatios: { ...DEFAULT_GRID_RATIOS },
  centerGrid: { ...DEFAULT_GRID_OCCUPANCY[DEFAULT_GRID_PRESET] },
  mobileActiveTab: "chat",
  dockSide: "right",
};

// ---------------------------------------------------------------------------
// AppNav contract (T2 / SPEC §3.2). The AppNav (top-level app navigation) is
// the leftmost column with chat / files / automation / … . It can be
// collapsed to an icon-only column. The collapsed flag and the panel width
// live in panels.appNav (see DEFAULT_PANELS); this module exports a
// selector that derives the layout snapshot a Sider component needs.
//
// The selector is a pure function so it can be unit-tested without a React
// renderer (no jsdom / @testing-library/react dependency).
// ---------------------------------------------------------------------------

export const APP_NAV_ICON_ONLY_WIDTH = 48;

export type AppNavLayoutSnapshot = {
  collapsed: boolean;
  width: number; // expanded width in px
  iconOnlyWidth: 48; // SPEC §3.2 fixed narrow width
};

export function selectAppNavLayoutState(state: LayoutState): AppNavLayoutSnapshot {
  return {
    collapsed: state.panels.appNav.collapsed,
    width: state.panels.appNav.width ?? 200,
    iconOnlyWidth: 48,
  };
}

function clonePanels(panels: Record<PanelKey, PanelState>): Record<PanelKey, PanelState> {
  return Object.fromEntries(Object.entries(panels).map(([k, v]) => [k, { ...v }])) as Record<PanelKey, PanelState>;
}

// ---------------------------------------------------------------------------
// Pure helpers (exported so tests can call them directly if they want to).
// ---------------------------------------------------------------------------

const ALL_PANEL_KEYS: ReadonlyArray<PanelKey> = [
  "appNav",
  "sessions",
  "tasks",
  "files",
  "terminal",
  "drawer",
];

const ALL_MOBILE_TABS: ReadonlyArray<MobileTab> = ["chat", "terminal", "files", "drawer", "tasks"];

function isPanelKey(v: unknown): v is PanelKey {
  return typeof v === "string" && (ALL_PANEL_KEYS as ReadonlyArray<string>).includes(v);
}

function isMobileTab(v: unknown): v is MobileTab {
  return typeof v === "string" && (ALL_MOBILE_TABS as ReadonlyArray<string>).includes(v);
}

function isPresetId(v: unknown): v is GridPresetId {
  return typeof v === "string" && GRID_PRESETS.some((p) => p.id === v);
}

function clamp01(v: number): number {
  if (Number.isNaN(v)) return 0;
  if (v < 0) return 0;
  if (v > 1) return 1;
  return v;
}

function clampWidth(v: number): number {
  if (Number.isNaN(v)) return 32;
  if (v < 32) return 32;
  return Math.floor(v);
}

function gridHas(occupancy: GridOccupancy, panel: PanelKey): boolean {
  return (Object.values(occupancy) as Array<PanelKey | null>).includes(panel);
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

export const useLayoutStore = create<LayoutState>((set) => ({
  ...DEFAULT_LAYOUT_SNAPSHOT,

  toggle: (k) => {
    if (!isPanelKey(k)) return;
    set((state) => ({
      panels: {
        ...state.panels,
        [k]: { ...state.panels[k], collapsed: !state.panels[k].collapsed },
      },
    }));
  },

  setWidth: (k, w) => {
    if (!isPanelKey(k)) return;
    set((state) => ({
      panels: {
        ...state.panels,
        [k]: { ...state.panels[k], width: clampWidth(w) },
      },
    }));
  },

  setGridPreset: (id) => {
    if (!isPresetId(id)) return;
    set(() => ({
      centerGridPreset: id,
      centerGrid: { ...DEFAULT_GRID_OCCUPANCY[id] },
    }));
  },

  movePanelToGrid: (panel, slot) => {
    if (!isPanelKey(panel)) return;
    set((state) => {
      // Refuse silently if the slot is taken by another panel. Use
      // swapPanelInGrid for explicit user-driven swap (drag-and-drop).
      const occupant = state.centerGrid[slot];
      if (occupant && occupant !== panel) {
        return state;
      }
      // If the panel is already in this exact slot, no-op.
      if (occupant === panel) {
        return state;
      }
      // Slot is empty: clear any other cell where this panel lives, then
      // place it.
      const next: GridOccupancy = { ...state.centerGrid };
      (Object.keys(next) as Array<keyof GridOccupancy>).forEach((k) => {
        if (next[k] === panel) next[k] = null;
      });
      next[slot] = panel;
      return { centerGrid: next };
    });
  },

  swapPanelInGrid: (panel, slot) => {
    if (!isPanelKey(panel)) return;
    set((state) => {
      // No-op if the slot is empty — caller should use movePanelToGrid
      // instead.
      const occupant = state.centerGrid[slot];
      if (!occupant) {
        return state;
      }
      // If the slot is already occupied by the same panel, no-op.
      if (occupant === panel) {
        return state;
      }
      // If the panel being moved is chat and chat currently occupies only
      // this slot, refuse — chat is pinned to at least one cell.
      if (panel === "chat") {
        const chatCells = (Object.keys(state.centerGrid) as Array<keyof GridOccupancy>).filter(
          (k) => state.centerGrid[k] === "chat",
        );
        if (chatCells.length <= 1 && chatCells.includes(slot)) {
          return state;
        }
      }
      // If the occupant being evicted is chat and chat currently occupies
      // only this slot, refuse.
      if (occupant === "chat") {
        const chatCells = (Object.keys(state.centerGrid) as Array<keyof GridOccupancy>).filter(
          (k) => state.centerGrid[k] === "chat",
        );
        if (chatCells.length <= 1) {
          return state;
        }
      }
      // Otherwise: clear all cells holding the incoming panel, then place
      // it in the target slot. The previous occupant is dropped from the
      // grid (Dock fallback per SPEC §3.2 occupancy rule).
      const next: GridOccupancy = { ...state.centerGrid };
      (Object.keys(next) as Array<keyof GridOccupancy>).forEach((k) => {
        if (next[k] === panel) next[k] = null;
      });
      next[slot] = panel;
      return { centerGrid: next };
    });
  },

  setGridRatio: (key, v) => {
    set((state) => {
      const next: GridRatios = { ...state.centerGridRatios, [key]: clamp01(v) };
      return { centerGridRatios: next };
    });
  },

  setMobileActiveTab: (t) => {
    if (!isMobileTab(t)) return;
    set(() => ({ mobileActiveTab: t }));
  },

  reset: () => {
    set(() => ({
      ...DEFAULT_LAYOUT_SNAPSHOT,
      panels: clonePanels(DEFAULT_PANELS),
      centerGrid: { ...DEFAULT_GRID_OCCUPANCY[DEFAULT_GRID_PRESET] },
      centerGridRatios: { ...DEFAULT_GRID_RATIOS },
    }));
  },
}));

export const __test__ = {
  ALL_PANEL_KEYS,
  ALL_MOBILE_TABS,
  isPanelKey,
  isMobileTab,
  isPresetId,
  clamp01,
  clampWidth,
  gridHas,
  clonePanels,
  DEFAULT_PANELS,
};
