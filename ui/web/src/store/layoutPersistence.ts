import type { LayoutSnapshot, LayoutState } from "./layout";

// P4 / T8 (SPEC §3.1 + §4.6): localStorage persistence + cross-tab
// `storage` event listener for the layout store. We deliberately
// hand-roll the persistence layer instead of adopting
// `zustand/middleware/persist` because:
//
//   1. The store's `set` shape changes per-action (toggle vs.
//      setGridPreset vs. setGridRatio) and we want to write a
//      *single* canonical snapshot on every change rather than
//      re-hydrate per key. `persist` middleware with `partialize`
//      would add a config surface and a serialization step we do
//      not need — the snapshot is the same shape we read from
//      `useLayoutStore.getState()` minus the actions.
//
//   2. The cross-tab `storage` event listener wants to dispatch
//      `set` with the full snapshot atomically; `persist` only
//      flushes on store updates in the current tab and cannot
//      react to a *different* tab's `storage` event cleanly.
//
//   3. The store file already exports `LayoutSnapshot`; a thin
//      `serializeLayoutSnapshot` / `hydrateLayoutSnapshot` pair
//      keeps the JSON shape independent of the store's zustand
//      wiring.
//
// The four exports here are pure functions (no React, no IO) —
// the React layer wires them in App.tsx via `useLayoutEffect` and
// a `window.addEventListener("storage", ...)` handler.

export const LAYOUT_STORAGE_KEY = "godex.web.layout.v1";

/** Strip the action fields from the layout state so we can JSON-
 *  serialize the snapshot. We keep everything else verbatim. */
export function serializeLayoutSnapshot(state: LayoutState): LayoutSnapshot {
  const { toggle: _t, setWidth: _sw, setGridPreset: _sgp, movePanelToGrid: _mp, swapPanelInGrid: _sp, setGridRatio: _sgr, setMobileActiveTab: _sm, openTaskCenterDrawer: _o, closeTaskCenterDrawer: _c, reset: _r, ...snapshot } = state;
  void _t; void _sw; void _sgp; void _mp; void _sp; void _sgr; void _sm; void _o; void _c; void _r;
  return snapshot;
}

/** Parse a stored JSON string. On any failure (malformed JSON, wrong
 *  shape, storage quota error) we return `null` so the caller can
 *  fall back to the default snapshot — the persisted state must
 *  never block the user from using the app. */
export function hydrateLayoutSnapshot(raw: string | null): LayoutSnapshot | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") return null;
    // We do not deep-validate every field here. The store's own
    // actions + selectors reject unknown shapes (isPanelKey /
    // isMobileTab / isPresetId). Worst case the user sees the
    // default snapshot after a bad write.
    return parsed as LayoutSnapshot;
  } catch {
    return null;
  }
}

/** Read the persisted snapshot from localStorage. Safe to call in
 *  SSR / non-browser contexts (returns null when `window` is
 *  undefined). */
export function readPersistedLayoutSnapshot(): LayoutSnapshot | null {
  if (typeof window === "undefined") return null;
  try {
    return hydrateLayoutSnapshot(window.localStorage.getItem(LAYOUT_STORAGE_KEY));
  } catch {
    return null;
  }
}

/** Write the snapshot to localStorage. Wrapped in try/catch so a
 *  quota-exceeded error never crashes the app. */
export function writePersistedLayoutSnapshot(snapshot: LayoutSnapshot): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(LAYOUT_STORAGE_KEY, JSON.stringify(snapshot));
  } catch {
    // Quota exceeded / private mode / disabled storage — silent
    // fallback. The in-memory state still works.
  }
}

/** Clear the persisted snapshot. Used by the store's `reset()`
 *  action so "Reset workspace" wipes the localStorage entry too. */
export function clearPersistedLayoutSnapshot(): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(LAYOUT_STORAGE_KEY);
  } catch {
    // See writePersistedLayoutSnapshot.
  }
}

/** Build a `StorageEvent`-like payload for cross-tab sync. We
 *  shape this so the listener can call `applyLayoutSnapshot` on
 *  the store without re-parsing the event. */
export function buildStoragePayload(snapshot: LayoutSnapshot): { key: string; newValue: string } {
  return { key: LAYOUT_STORAGE_KEY, newValue: JSON.stringify(snapshot) };
}

/** Apply a parsed snapshot back into the store shape. Returns
 *  the `set` payload — callers pass it to `useLayoutStore.setState`. */
export function applyLayoutSnapshot(snapshot: LayoutSnapshot): Partial<LayoutState> {
  return snapshot;
}
