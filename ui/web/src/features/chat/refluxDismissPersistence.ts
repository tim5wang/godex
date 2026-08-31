// Reflux bubble dismissal persistence.
//
// The LongTask reflux popup's dismiss state lives in ChatPage's
// component state and is lost on refresh / session re-entry. This
// module mirrors the hand-rolled localStorage pattern used by
// layoutStore.ts / layoutPersistence.ts so a dismissed popup stays
// dismissed across reloads.
//
// We key dismissals by "<longtaskId>:<status>" — both parsed from the
// reflux message content ("LongTask <id>: <status>") — because feed
// item ids (message:<index>:assistant, assistant:<turnId>:<counter>)
// are index/counter-based and NOT stable across reloads, while the
// longtask id is a stable backend identifier. Status is part of the
// key so a later *different* status of the same longtask (e.g.
// "blocked" -> "running") still surfaces as a new notification.

export const REFLUX_DISMISS_STORAGE_KEY = "godex.web.refluxDismissed.v1";

// Keep at most this many dismissed keys; oldest entries are dropped
// first so the localStorage payload stays bounded.
const MAX_DISMISSED = 200;

/** Read the persisted dismissed-reflux key set. Safe in SSR / non-
 *  browser contexts and on any parse failure (returns an empty set). */
export function readPersistedRefluxDismissed(): Set<string> {
  if (typeof window === "undefined") return new Set();
  try {
    const raw = window.localStorage.getItem(REFLUX_DISMISS_STORAGE_KEY);
    if (!raw) return new Set();
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((key): key is string => typeof key === "string"));
  } catch {
    // Malformed JSON / quota error / disabled storage — fall back to
    // an empty set; the in-memory dismissal still works.
    return new Set();
  }
}

/** Persist the dismissed-reflux key set, capped to the most recent
 *  MAX_DISMISSED entries. Wrapped in try/catch so a quota-exceeded
 *  error never crashes the app. */
export function writePersistedRefluxDismissed(dismissed: ReadonlySet<string>): void {
  if (typeof window === "undefined") return;
  try {
    const keys = Array.from(dismissed);
    const capped = keys.length > MAX_DISMISSED ? keys.slice(keys.length - MAX_DISMISSED) : keys;
    window.localStorage.setItem(REFLUX_DISMISS_STORAGE_KEY, JSON.stringify(capped));
  } catch {
    // Quota / private mode — in-memory dismissal still works.
  }
}
