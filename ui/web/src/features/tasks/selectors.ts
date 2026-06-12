import type { TaskOutcome, TaskOutcomeStatus } from "../chat/taskCenterOutcome";
import type { LayoutState } from "../../store/layout";

// ---------------------------------------------------------------------------
// Task Center selectors (P1 / T4 + T5).
//
// Per SPEC §4.1.1 and §4.2 the task center is reorganized into a
// `features/tasks/` module. Data flow is unchanged: callers feed
// `selectTaskCenterHeaderContract` a `TaskOutcome[]` (already produced by
// `features/chat/taskCenterOutcome.ts::buildTaskOutcomes`). The selectors
// are pure functions so they are unit-testable without React or jsdom.
//
// Two derived snapshots:
//   * selectTaskCenterHeaderContract: chip rendering (counts + dot color)
//   * selectTaskCenterDrawerState: drawer width (default 560, 320-800 envelope)
// ---------------------------------------------------------------------------

/** SPEC §4.1.1: Drawer width envelope (in px) for the task center drawer. */
export const TASK_CENTER_DRAWER_MIN_WIDTH = 320;
export const TASK_CENTER_DRAWER_MAX_WIDTH = 800;
export const TASK_CENTER_DRAWER_DEFAULT_WIDTH = 560;

export type TaskCenterDotColor = "red" | "amber" | "none";

export type TaskCenterHeaderContract = {
  running: number;
  blocked: number;
  pendingReview: number;
  merged: number;
  failed: number;
  total: number;
  hasUnread: boolean;
  dotColor: TaskCenterDotColor;
};

export type TaskCenterDrawerState = {
  width: number;
  min: typeof TASK_CENTER_DRAWER_MIN_WIDTH;
  max: typeof TASK_CENTER_DRAWER_MAX_WIDTH;
};

/**
 * Reduce a list of `TaskOutcome` (from `buildTaskOutcomes`) into the
 * header-chip contract. `idle` outcomes are counted toward `total` so
 * the chip can show "0/N" when there are no real tasks, but they do not
 * trigger the unread dot.
 *
 * Priority order for `dotColor` (SPEC §5):
 *   1. any `ready_for_review` -> red
 *   2. else any `blocked`      -> amber
 *   3. else                    -> none
 */
export function selectTaskCenterHeaderContract(outcomes: ReadonlyArray<TaskOutcome>): TaskCenterHeaderContract {
  let running = 0;
  let blocked = 0;
  let pendingReview = 0;
  let merged = 0;
  let failed = 0;
  for (const outcome of outcomes) {
    const status: TaskOutcomeStatus = outcome.status;
    switch (status) {
      case "running":
        running += 1;
        break;
      case "blocked":
        blocked += 1;
        break;
      case "ready_for_review":
        pendingReview += 1;
        break;
      case "merged":
        merged += 1;
        break;
      case "failed":
        failed += 1;
        break;
      case "idle":
      default:
        break;
    }
  }
  const total = outcomes.length;
  let dotColor: TaskCenterDotColor = "none";
  if (pendingReview > 0) {
    dotColor = "red";
  } else if (blocked > 0) {
    dotColor = "amber";
  }
  return {
    running,
    blocked,
    pendingReview,
    merged,
    failed,
    total,
    hasUnread: dotColor !== "none",
    dotColor,
  };
}

/**
 * Drawer width envelope. The persisted value lives in
 * `panels.tasks.width`; this selector clamps it into [320, 800] so the
 * render layer never has to defend against out-of-range values.
 */
export function selectTaskCenterDrawerState(state: LayoutState): TaskCenterDrawerState {
  const persisted = state.panels.tasks.width;
  const raw = typeof persisted === "number" ? persisted : TASK_CENTER_DRAWER_DEFAULT_WIDTH;
  let width = raw;
  if (Number.isNaN(width)) {
    width = TASK_CENTER_DRAWER_DEFAULT_WIDTH;
  } else if (width < TASK_CENTER_DRAWER_MIN_WIDTH) {
    width = TASK_CENTER_DRAWER_MIN_WIDTH;
  } else if (width > TASK_CENTER_DRAWER_MAX_WIDTH) {
    width = TASK_CENTER_DRAWER_MAX_WIDTH;
  }
  return {
    width,
    min: TASK_CENTER_DRAWER_MIN_WIDTH,
    max: TASK_CENTER_DRAWER_MAX_WIDTH,
  };
}
