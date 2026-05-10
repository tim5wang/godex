import { useCallback, useEffect, useState, type PointerEvent as ReactPointerEvent } from "react";

type ResizableWidthOptions = {
  storageKey: string;
  defaultWidth: number;
  min: number;
  max: number;
  direction?: "left" | "right";
};

export function useResizableWidth({
  storageKey,
  defaultWidth,
  min,
  max,
  direction = "right",
}: ResizableWidthOptions) {
  const [width, setWidth] = useState(() => {
    if (typeof window === "undefined") {
      return clamp(defaultWidth, min, max);
    }
    const saved = Number(window.localStorage.getItem(storageKey));
    return Number.isFinite(saved) ? clamp(saved, min, max) : clamp(defaultWidth, min, max);
  });

  useEffect(() => {
    window.localStorage.setItem(storageKey, String(width));
  }, [storageKey, width]);

  const beginResize = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (event.button !== 0) {
        return;
      }
      event.preventDefault();
      const startX = event.clientX;
      const startWidth = width;

      const onPointerMove = (moveEvent: PointerEvent) => {
        const delta = moveEvent.clientX - startX;
        const signedDelta = direction === "right" ? delta : -delta;
        setWidth(clamp(startWidth + signedDelta, min, max));
      };
      const stopResize = () => {
        document.body.classList.remove("is-resizing-column");
        document.removeEventListener("pointermove", onPointerMove);
        document.removeEventListener("pointerup", stopResize);
        document.removeEventListener("pointercancel", stopResize);
      };

      document.body.classList.add("is-resizing-column");
      document.addEventListener("pointermove", onPointerMove);
      document.addEventListener("pointerup", stopResize);
      document.addEventListener("pointercancel", stopResize);
    },
    [direction, max, min, width],
  );

  return [width, beginResize] as const;
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, Math.round(value)));
}
