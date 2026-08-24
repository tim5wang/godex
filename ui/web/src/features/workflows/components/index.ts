/**
 * Workflows embeddable components — third-party UIs can import these to build
 * their own agent-backed interactive surfaces without re-implementing the
 * card rendering layer.
 *
 * ```tsx
 * import { UiCardView, type UiCardData } from "./features/workflows/components";
 * ```
 */
export { UiCardView } from "./UiCardView";
export type { UiCardData, UiCardField, UiCardAction } from "./UiCardView";
