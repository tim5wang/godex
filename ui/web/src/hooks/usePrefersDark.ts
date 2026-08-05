import { useEffect, useState } from "react";

/**
 * Tracks the OS color-scheme preference (prefers-color-scheme) reactively.
 * Used to pick light/dark themes for CodeMirror editors so they stay in
 * sync with the surrounding godex panel colors (styles.css switches the
 * --godex-* variables on the same media query).
 */
export function usePrefersDark(): boolean {
  const [dark, setDark] = useState<boolean>(() => {
    if (typeof window === "undefined" || !window.matchMedia) {
      return false;
    }
    return window.matchMedia("(prefers-color-scheme: dark)").matches;
  });

  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) {
      return;
    }
    const mql = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (event: MediaQueryListEvent) => setDark(event.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return dark;
}
