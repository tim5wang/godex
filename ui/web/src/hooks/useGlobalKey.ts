import { useEffect } from "react";

// P3 / T7 (SPEC §4.4 + M0 doc §3.5): Ctrl/Cmd + ` shortcut to
// toggle the terminal panel visibility (PC only — mobile surfaces
// the terminal via the secondary tab bar, not a keyboard shortcut).
//
// The hook is intentionally tiny and framework-agnostic:
//   * matches `Ctrl+\`` on Win/Linux and `Cmd+\`` on macOS
//   * swallows the event so it does not propagate to other shortcuts
//   * dispatches the callback (typically a store action)
//   * cleans up on unmount
//
// We deliberately do not read the layout store inside the hook —
// keeping the keydown -> action plumbing pure makes the hook
// trivially testable and reusable (e.g. if a future "Command
// Palette" wants the same Ctrl+\`` behaviour it can call
// `useGlobalKey("`", "Ctrl", cb)`).

export type UseGlobalKeyOptions = {
  /** Active flag; pass `false` (e.g. on mobile) to detach the listener. */
  active?: boolean;
};

export type ShortcutModifier = "Ctrl" | "Cmd";

/** Pure predicate — exported for tests + reuse. Returns true if the
 *  KeyboardEvent matches the requested key + modifier combo. */
export function matchesShortcut(
  event: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey">,
  key: string,
  modifier: ShortcutModifier,
): boolean {
  if (event.key !== key) return false;
  if (modifier === "Ctrl") return event.ctrlKey;
  return event.metaKey;
}

export function useGlobalKey(
  key: string,
  modifier: ShortcutModifier,
  onTrigger: () => void,
  options: UseGlobalKeyOptions = {},
) {
  const { active = true } = options;
  useEffect(() => {
    if (!active) return;
    const handler = (event: KeyboardEvent) => {
      if (!matchesShortcut(event, key, modifier)) return;
      event.preventDefault();
      onTrigger();
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [active, key, modifier, onTrigger]);
}

/** P3 / T7 convenience wrapper: the canonical terminal shortcut
 *  per SPEC §4.4 / M0 doc §3.5 is `Ctrl/Cmd + \``. We detect
 *  macOS once on first call so we never pay the cost on every
 *  keystroke; the result is stable for the page lifetime. */
export function useTerminalShortcut(onTrigger: () => void, options: UseGlobalKeyOptions = {}) {
  const modifier: ShortcutModifier =
    typeof navigator !== "undefined" && /Mac|iPhone|iPad/i.test(navigator.platform)
      ? "Cmd"
      : "Ctrl";
  useGlobalKey("`", modifier, onTrigger, options);
}
