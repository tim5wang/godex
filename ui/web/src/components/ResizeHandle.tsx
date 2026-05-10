import type { PointerEventHandler } from "react";

export function ResizeHandle({
  label,
  onPointerDown,
  placement = "right",
}: {
  label: string;
  onPointerDown: PointerEventHandler<HTMLElement>;
  placement?: "left" | "right";
}) {
  return (
    <button
      type="button"
      aria-label={label}
      className={`column-resize-handle column-resize-handle-${placement}`}
      onPointerDown={onPointerDown}
    />
  );
}
