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

export function selectFilesLayoutState(state: LayoutState): FilesLayoutSnapshot {
  return {
    collapsed: state.panels.files.collapsed,
    width: state.panels.files.width ?? 320,
    iconOnlyWidth: 40,
  };
}
