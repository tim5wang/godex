import { useRef } from "react";

/**
 * Local useShallow — avoids potential bundler / module resolution
 * issues with zustand/shallow in production builds.
 *
 * Returns a memoized selector that shallow-compares the previous and
 * current results. If they are shallow-equal, the previous reference
 * is returned so zustand's Object.is check sees no change.
 */
export function useShallow<S, U>(
  selector: (state: S) => U,
): (state: S) => U {
  const prev = useRef<U | undefined>(undefined);
  return (state: S): U => {
    const next = selector(state);
    if (prev.current !== undefined && shallowEqual(prev.current, next)) {
      return prev.current;
    }
    prev.current = next;
    return next;
  };
}

function shallowEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (a === null || b === null || typeof a !== "object" || typeof b !== "object") {
    return false;
  }
  const objA = a as Record<string, unknown>;
  const objB = b as Record<string, unknown>;
  const keysA = Object.keys(objA);
  const keysB = Object.keys(objB);
  if (keysA.length !== keysB.length) return false;
  for (const key of keysA) {
    if (!Object.is(objA[key], objB[key])) {
      return false;
    }
  }
  return true;
}
