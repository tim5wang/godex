import type { LayoutState } from "../../store/layout";

// ---------------------------------------------------------------------------
// Files layout state (P2 / T6, SPEC §3.2 + §4.1 + §4.3). The files panel
// is one of the six workspace-level panels tracked by the layout store.
// When collapsed it shrinks to a 40px strip showing a single "expand"
// affordance (SPEC §4.5 bookmark). The expanded width lives in
// `panels.files.width`; this selector clamps the persisted value into a
// safe envelope so the renderer never has to defend out-of-range.
// ---------------------------------------------------------------------------

export const FILES_ICON_ONLY_WIDTH = 40;

export type FilesLayoutSnapshot = {
  collapsed: boolean;
  width: number; // expanded width in px
  iconOnlyWidth: 40; // SPEC §3.2 fixed narrow width
};

let _filesCache: FilesLayoutSnapshot | null = null;
export function selectFilesLayoutState(state: LayoutState): FilesLayoutSnapshot {
  const collapsed = state.panels.files.collapsed;
  const width = state.panels.files.width ?? 320;
  if (_filesCache && _filesCache.collapsed === collapsed && _filesCache.width === width) {
    return _filesCache;
  }
  _filesCache = { collapsed, width, iconOnlyWidth: 40 as const };
  return _filesCache;
}
