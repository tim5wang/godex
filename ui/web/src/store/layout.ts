import { create } from "zustand";
import { toggleRowCollapse, type GridRow } from "./layoutGridToggles";
import { clearPersistedLayoutSnapshot } from "./layoutPersistence";

// ---------------------------------------------------------------------------
// Public types (mirror SPEC §4.6). Pure types only — no React, no IO.
// Persistence to localStorage and the Splitter / Antd integration land in
// later phases (P4). Keep this module a pure state machine so it is easy to
// unit-test in isolation.
// ---------------------------------------------------------------------------

export type PanelKey = "appNav" | "sessions" | "chat" | "tasks" | "files" | "terminal" | "drawer";

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
  | "single"
  | "grid3x3_filesChatTerminal"
  | "grid3x3_tallThreeCol"
  | "grid3x3_wideThreeRow";

export type GridSlot =
  | "topLeft" | "topRight" | "bottomLeft" | "bottomRight"
  | "topFull" | "bottomFull"
  | `r${0|1|2}c${0|1|2}`;

export type GridOccupancy = {
  // Legacy 2×2 slots
  topLeft: PanelKey | null;
  topRight: PanelKey | null;
  bottomLeft: PanelKey | null;
  bottomRight: PanelKey | null;
  // 3×3 mode (all optional — absent = legacy 2×2)
  rows?: 2 | 3;
  cols?: 2 | 3;
  r0c0?: PanelKey | null;
  r0c1?: PanelKey | null;
  r0c2?: PanelKey | null;
  r1c0?: PanelKey | null;
  r1c1?: PanelKey | null;
  r1c2?: PanelKey | null;
  r2c0?: PanelKey | null;
  r2c1?: PanelKey | null;
  r2c2?: PanelKey | null;
};

export type GridRatios = {
  outerSplit: number; // 0~1, top vs bottom (2×2)
  innerTopSplit?: number; // 0~1, topLeft vs topRight (when top is split)
  innerBottomSplit?: number; // 0~1, bottomLeft vs bottomRight (when bottom is split)
  // 3×3 split ratios
  row0Split?: number; // 0~1, row 0 height % (default 0.33)
  row1Split?: number; // 0~1, row 1 height % (default 0.34)
  col0Split?: number; // 0~1, col 0 width % (default 0.33)
  col1Split?: number; // 0~1, col 1 width % (default 0.34)
};

export type MobileTab = "chat" | "terminal" | "files" | "drawer" | "tasks";

export type LayoutSnapshot = {
  panels: Record<PanelKey, PanelState>;
  centerGridPreset: GridPresetId;
  centerGridRatios: GridRatios;
  centerGrid: GridOccupancy;
  centerGridCollapsedPanels: Partial<Record<PanelKey, boolean>>;
  mobileActiveTab: MobileTab;
  dockSide: "right" | "bottom";
  taskCenterDrawerOpen: boolean;
};

export type LayoutActions = {
  toggle: (k: PanelKey) => void;
  setWidth: (k: PanelKey, w: number) => void;
  setGridPreset: (id: GridPresetId) => void;
  movePanelToGrid: (panel: PanelKey, slot: Exclude<GridSlot, "topFull" | "bottomFull">) => void;
  swapPanelInGrid: (panel: PanelKey, slot: Exclude<GridSlot, "topFull" | "bottomFull">) => void;
  swapGridSlots: (from: Exclude<GridSlot, "topFull" | "bottomFull">, to: Exclude<GridSlot, "topFull" | "bottomFull">) => void;
  setCenterGridPanelCollapsed: (panel: PanelKey, collapsed: boolean) => void;
  setGridRatio: (key: keyof GridRatios, v: number) => void;
  toggleGridRowCollapse: (row: GridRow) => void;
  setMobileActiveTab: (t: MobileTab) => void;
  openTaskCenterDrawer: () => void;
  closeTaskCenterDrawer: () => void;
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
  row0Split: 0.33,
  row1Split: 0.34,
  col0Split: 0.33,
  col1Split: 0.34,
};

const DEFAULT_PANELS: Record<PanelKey, PanelState> = {
  appNav: { collapsed: false, width: 200, visible: true },
  sessions: { collapsed: false, width: 280, visible: true },
  chat: { collapsed: false, width: 0, visible: true },
  tasks: { collapsed: true, width: 560, visible: true },
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
  // 3×3 presets (v2.0, SPEC §3.2 ">2×>2" extension)
  grid3x3_filesChatTerminal: {
    rows: 3,
    cols: 3,
    topLeft: "files", topRight: "chat", bottomLeft: "terminal", bottomRight: "terminal",
    r0c0: "files", r0c1: "chat", r0c2: "chat",
    r1c0: "files", r1c1: "chat", r1c2: "chat",
    r2c0: "terminal", r2c1: "terminal", r2c2: "terminal",
  },
  grid3x3_tallThreeCol: {
    rows: 3,
    cols: 3,
    topLeft: "files", topRight: "chat", bottomLeft: "terminal", bottomRight: "terminal",
    r0c0: "files", r0c1: "chat", r0c2: "terminal",
    r1c0: "files", r1c1: "chat", r1c2: "terminal",
    r2c0: "drawer", r2c1: "tasks", r2c2: "drawer",
  },
  grid3x3_wideThreeRow: {
    rows: 3,
    cols: 3,
    topLeft: "chat", topRight: "chat", bottomLeft: "terminal", bottomRight: "terminal",
    r0c0: "chat", r0c1: "chat", r0c2: "chat",
    r1c0: "chat", r1c1: "chat", r1c2: "chat",
    r2c0: "terminal", r2c1: "terminal", r2c2: "terminal",
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
  {
    id: "grid3x3_filesChatTerminal",
    label: "3×3: Files · Chat / Terminal",
    occupancy: DEFAULT_GRID_OCCUPANCY.grid3x3_filesChatTerminal,
  },
  {
    id: "grid3x3_tallThreeCol",
    label: "3×3: Files · Chat · Terminal / Notes · Tasks · Drawer",
    occupancy: DEFAULT_GRID_OCCUPANCY.grid3x3_tallThreeCol,
  },
  {
    id: "grid3x3_wideThreeRow",
    label: "3×3: Wide Chat / Terminal",
    occupancy: DEFAULT_GRID_OCCUPANCY.grid3x3_wideThreeRow,
  },
];

export const DEFAULT_LAYOUT_SNAPSHOT: LayoutSnapshot = {
  panels: clonePanels(DEFAULT_PANELS),
  centerGridPreset: DEFAULT_GRID_PRESET,
  centerGridRatios: { ...DEFAULT_GRID_RATIOS },
  centerGrid: { ...DEFAULT_GRID_OCCUPANCY[DEFAULT_GRID_PRESET] },
  centerGridCollapsedPanels: {},
  mobileActiveTab: "chat",
  taskCenterDrawerOpen: false,
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

// ---------------------------------------------------------------------------
// SessionList contract (T3 / SPEC §3.2). The session list is the second
// column from the left in the chat workspace. When collapsed it shrinks to
// a 40px strip with a quick "+ New" affordance and a popover that reveals
// the full session list on demand (SPEC §4.5 bookmark behavior).
// ---------------------------------------------------------------------------

export const SESSIONS_ICON_ONLY_WIDTH = 40;

export type SessionListLayoutSnapshot = {
  collapsed: boolean;
  width: number; // expanded width in px
  iconOnlyWidth: 40; // SPEC §3.2 fixed narrow width
};

export function selectSessionListLayoutState(state: LayoutState): SessionListLayoutSnapshot {
  return {
    collapsed: state.panels.sessions.collapsed,
    width: state.panels.sessions.width ?? 280,
    iconOnlyWidth: 40,
  };
}

// ---------------------------------------------------------------------------
// MobileWorkspaceTabs contract (T9 / SPEC §3.3). On screens <1024px the chat
// workspace exposes a 5-tab secondary navigation bar: chat | terminal | files
// | drawer | tasks. The active tab is stored in mobileActiveTab; this module
// exports a selector that derives the render payload (tab list + active
// flag) so the component layer is a thin renderer. UI concerns (visibility
// driven by Grid.useBreakpoint) stay in the component layer — we don't push
// viewport state into the layout store.
// ---------------------------------------------------------------------------

export const MOBILE_TABS_ORDER: ReadonlyArray<MobileTab> = ["chat", "terminal", "files", "drawer", "tasks"];

export type MobileWorkspaceTabDescriptor = {
  key: MobileTab;
  i18nKey: string;
  iconKey: string;
  active: boolean;
};

export type MobileWorkspaceTabsSnapshot = {
  active: MobileTab;
  tabs: ReadonlyArray<MobileWorkspaceTabDescriptor>;
};

export function selectMobileWorkspaceTabs(state: LayoutState): MobileWorkspaceTabsSnapshot {
  const active = state.mobileActiveTab;
  return {
    active,
    tabs: MOBILE_TABS_ORDER.map((key) => ({
      key,
      i18nKey: `mobile.tabs.${key}`,
      iconKey: key,
      active: key === active,
    })),
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

/** Cell slots that can hold a panel (non-metadata keys in GridOccupancy). */
const CELL_SLOTS_2X2 = ["topLeft", "topRight", "bottomLeft", "bottomRight"] as const;
const CELL_SLOTS_3X3 = ["r0c0", "r0c1", "r0c2", "r1c0", "r1c1", "r1c2", "r2c0", "r2c1", "r2c2"] as const;
type CellSlotKey = (typeof CELL_SLOTS_2X2)[number] | (typeof CELL_SLOTS_3X3)[number];

function isCellSlot(v: unknown): v is CellSlotKey {
  if (typeof v !== "string") return false;
  return (CELL_SLOTS_2X2 as ReadonlyArray<string>).includes(v) ||
    (CELL_SLOTS_3X3 as ReadonlyArray<string>).includes(v);
}

function cellKeysForGrid(occ: GridOccupancy): CellSlotKey[] {
  if (occ.rows === 3 && occ.cols === 3) {
    return [...CELL_SLOTS_3X3];
  }
  return [...CELL_SLOTS_2X2];
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
      centerGridCollapsedPanels: {},
    }));
  },

  movePanelToGrid: (panel, slot) => {
    if (!isPanelKey(panel)) return;
    if (!isCellSlot(slot)) return;
    set((state) => {
      const occupant = state.centerGrid[slot];
      if (occupant && occupant !== panel) return state;
      if (occupant === panel) return state;
      const next: GridOccupancy = { ...state.centerGrid };
      for (const k of cellKeysForGrid(next)) {
        if (next[k] === panel) (next as Record<string, unknown>)[k] = null;
      }
      (next as Record<string, unknown>)[slot] = panel;
      return { centerGrid: next };
    });
  },

  swapPanelInGrid: (panel, slot) => {
    if (!isPanelKey(panel)) return;
    if (!isCellSlot(slot)) return;
    set((state) => {
      const occupant = state.centerGrid[slot];
      if (!occupant) return state;
      if (occupant === panel) return state;
      if (panel === "chat") {
        const chatCells = cellKeysForGrid(state.centerGrid).filter(
          (k) => state.centerGrid[k] === "chat",
        );
        if (chatCells.length <= 1 && chatCells.includes(slot)) return state;
      }
      if (occupant === "chat") {
        const chatCells = cellKeysForGrid(state.centerGrid).filter(
          (k) => state.centerGrid[k] === "chat",
        );
        if (chatCells.length <= 1) return state;
      }
      const next: GridOccupancy = { ...state.centerGrid };
      for (const k of cellKeysForGrid(next)) {
        if (next[k] === panel) (next as Record<string, unknown>)[k] = null;
      }
      (next as Record<string, unknown>)[slot] = panel;
      return { centerGrid: next };
    });
  },

  swapGridSlots: (from, to) => {
    if (!isCellSlot(from)) return;
    if (!isCellSlot(to)) return;
    if (from === to) return;
    set((state) => {
      const next: GridOccupancy = { ...state.centerGrid };
      const fromPanel = next[from] ?? null;
      const toPanel = next[to] ?? null;
      if (!fromPanel && !toPanel) return state;
      (next as Record<string, unknown>)[from] = toPanel;
      (next as Record<string, unknown>)[to] = fromPanel;
      return { centerGrid: next };
    });
  },

  setCenterGridPanelCollapsed: (panel, collapsed) => {
    if (!isPanelKey(panel) || panel === "chat") return;
    set((state) => {
      const next = { ...state.centerGridCollapsedPanels };
      if (collapsed) {
        next[panel] = true;
      } else {
        delete next[panel];
      }
      return { centerGridCollapsedPanels: next };
    });
  },

  setGridRatio: (key, v) => {
    set((state) => {
      const next: GridRatios = { ...state.centerGridRatios, [key]: clamp01(v) };
      return { centerGridRatios: next };
    });
  },

  toggleGridRowCollapse: (row) => {
    set((state) => ({
      centerGridRatios: toggleRowCollapse(state.centerGridRatios, row),
    }));
  },

  setMobileActiveTab: (t) => {
    if (!isMobileTab(t)) return;
    set(() => ({ mobileActiveTab: t }));
  },

  openTaskCenterDrawer: () => {
    set(() => ({ taskCenterDrawerOpen: true }));
  },

  closeTaskCenterDrawer: () => {
    set(() => ({ taskCenterDrawerOpen: false }));
  },

  reset: () => {
    set(() => ({
      ...DEFAULT_LAYOUT_SNAPSHOT,
      panels: clonePanels(DEFAULT_PANELS),
      centerGrid: { ...DEFAULT_GRID_OCCUPANCY[DEFAULT_GRID_PRESET] },
      centerGridRatios: { ...DEFAULT_GRID_RATIOS },
      centerGridCollapsedPanels: {},
    }));
    // P4 / T8: also wipe the persisted snapshot so the next
    // reload returns to the factory defaults (not to whatever
    // was in localStorage before the reset).
    clearPersistedLayoutSnapshot();
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
