import { create } from "zustand";

/**
 * Latest known browser view of the active session, fed by two sources:
 *   - `browser.view` SSE events (backend P1): pushed when the agent operates
 *     the browser tool (open/navigate/switch_tab...). Drives auto-activation
 *     of the Browser dock panel and provides URL/title for the fallback card.
 *   - WS frame messages ({pageID,url,title,jpeg}): the frame stream carries
 *     the same URL/title per frame, so the panel updates them live.
 *
 * `follow` controls whether a `browser.view` event auto-activates the panel
 * (default on). Turning it off keeps the view updated but never steals focus
 * from the tab the user is looking at.
 */
export interface BrowserViewInfo {
  pageID?: string;
  url?: string;
  title?: string;
  updatedAt?: number;
}

interface BrowserViewStore {
  view: BrowserViewInfo | null;
  follow: boolean;
  setView: (view: BrowserViewInfo) => void;
  setFollow: (follow: boolean) => void;
  clear: () => void;
}

export const useBrowserViewStore = create<BrowserViewStore>((set) => ({
  view: null,
  follow: true,
  setView: (view) => set({ view: { ...view, updatedAt: Date.now() } }),
  setFollow: (follow) => set({ follow }),
  clear: () => set({ view: null }),
}));
