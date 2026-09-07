// Terminal quick scripts: named preset commands shown as compact bubbles in
// the terminal header. Clicking a bubble runs its command in the active PTY.
//
// Storage is localStorage (per browser), so scripts survive reloads without
// needing backend changes. The module is pure (no React) so it is unit-testable.

export type QuickScript = {
  id: string;
  name: string;
  command: string;
};

const STORAGE_KEY = "godex.terminal.quickScripts";

/** Hard cap so a corrupted/oversized payload cannot blow up the header. */
const MAX_SCRIPTS = 24;

export function loadQuickScripts(): QuickScript[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter(
        (s): s is QuickScript =>
          !!s &&
          typeof (s as QuickScript).name === "string" &&
          typeof (s as QuickScript).command === "string",
      )
      .slice(0, MAX_SCRIPTS);
  } catch {
    // Unavailable/corrupted storage — treat as empty, never crash the panel.
    return [];
  }
}

export function saveQuickScripts(scripts: QuickScript[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(scripts.slice(0, MAX_SCRIPTS)));
  } catch {
    // Storage full or blocked — non-fatal; scripts just won't persist.
  }
}

export function createQuickScript(name: string, command: string): QuickScript {
  const trimmedName = name.trim();
  const trimmedCommand = command.trim();
  return {
    id: `qs-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
    // Fall back to the first word of the command when no name is given.
    name: trimmedName || trimmedCommand.split(/\s+/)[0] || "script",
    command: trimmedCommand,
  };
}
