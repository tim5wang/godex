import { describe, expect, it, beforeEach, afterEach } from "vitest";

import { useLayoutStore } from "../src/store/layout";
import {
  selectTaskCenterHeaderContract,
  selectTaskCenterDrawerState,
  TASK_CENTER_DRAWER_MIN_WIDTH,
  TASK_CENTER_DRAWER_MAX_WIDTH,
  TASK_CENTER_DRAWER_DEFAULT_WIDTH,
  type TaskCenterHeaderContract,
  type TaskCenterDrawerState,
} from "../src/features/tasks/selectors";
import type { TaskOutcome, TaskOutcomeStatus } from "../src/features/chat/taskCenterOutcome";

function reset() {
  useLayoutStore.getState().reset();
}

beforeEach(() => reset());
afterEach(() => reset());

function makeOutcome(status: TaskOutcomeStatus, id = `o:${status}`): TaskOutcome {
  return { id, status, title: id, signals: [] };
}

describe("selectTaskCenterHeaderContract (P1 / T4 chip contract)", () => {
  it("returns zeros for an empty outcome list (header chip shows no badge)", () => {
    const c = selectTaskCenterHeaderContract([]);
    expect(c).toEqual<TaskCenterHeaderContract>({
      running: 0,
      blocked: 0,
      pendingReview: 0,
      merged: 0,
      failed: 0,
      total: 0,
      hasUnread: false,
      dotColor: "none",
    });
  });

  it("counts each status independently", () => {
    const c = selectTaskCenterHeaderContract([
      makeOutcome("running", "a"),
      makeOutcome("running", "b"),
      makeOutcome("blocked", "c"),
      makeOutcome("ready_for_review", "d"),
      makeOutcome("merged", "e"),
      makeOutcome("failed", "f"),
      makeOutcome("idle", "g"), // idle does not count toward any bucket
    ]);
    expect(c.running).toBe(2);
    expect(c.blocked).toBe(1);
    expect(c.pendingReview).toBe(1);
    expect(c.merged).toBe(1);
    expect(c.failed).toBe(1);
    expect(c.total).toBe(7);
  });

  it("dotColor is 'red' when pendingReview > 0 (SPEC §5: 🔴 for review)", () => {
    const c = selectTaskCenterHeaderContract([makeOutcome("ready_for_review")]);
    expect(c.dotColor).toBe("red");
    expect(c.hasUnread).toBe(true);
  });

  it("dotColor is 'amber' when blocked > 0 and pendingReview == 0 (SPEC §5: 🟠 for blocked)", () => {
    const c = selectTaskCenterHeaderContract([makeOutcome("blocked")]);
    expect(c.dotColor).toBe("amber");
    expect(c.hasUnread).toBe(true);
  });

  it("dotColor is 'none' when there is no review/blocked work (idle / running only)", () => {
    const c1 = selectTaskCenterHeaderContract([makeOutcome("running"), makeOutcome("merged")]);
    expect(c1.dotColor).toBe("none");
    expect(c1.hasUnread).toBe(false);
    const c2 = selectTaskCenterHeaderContract([makeOutcome("idle")]);
    expect(c2.dotColor).toBe("none");
  });

  it("red dot wins over amber when both pendingReview and blocked > 0 (priority order)", () => {
    const c = selectTaskCenterHeaderContract([
      makeOutcome("ready_for_review"),
      makeOutcome("blocked"),
    ]);
    expect(c.dotColor).toBe("red");
  });
});

describe("selectTaskCenterDrawerState (P1 / T5 drawer width contract)", () => {
  it("returns the canonical default width (560) on a fresh store", () => {
    const s = selectTaskCenterDrawerState(useLayoutStore.getState());
    expect(s).toEqual<TaskCenterDrawerState>({
      width: TASK_CENTER_DRAWER_DEFAULT_WIDTH,
      min: TASK_CENTER_DRAWER_MIN_WIDTH,
      max: TASK_CENTER_DRAWER_MAX_WIDTH,
    });
  });

  it("constants are 320 / 800 (SPEC §4.1.1 width envelope)", () => {
    expect(TASK_CENTER_DRAWER_MIN_WIDTH).toBe(320);
    expect(TASK_CENTER_DRAWER_MAX_WIDTH).toBe(800);
    expect(TASK_CENTER_DRAWER_DEFAULT_WIDTH).toBe(560);
  });

  it("clamps the persisted panels.tasks.width to [320, 800]", () => {
    useLayoutStore.getState().setWidth("tasks", 200);
    expect(selectTaskCenterDrawerState(useLayoutStore.getState()).width).toBe(320);
    useLayoutStore.getState().setWidth("tasks", 1200);
    expect(selectTaskCenterDrawerState(useLayoutStore.getState()).width).toBe(800);
  });

  it("is independent of the center grid preset (P1 does not touch the grid)", () => {
    const before = selectTaskCenterDrawerState(useLayoutStore.getState());
    useLayoutStore.getState().setGridPreset("single");
    const after = selectTaskCenterDrawerState(useLayoutStore.getState());
    expect(after).toEqual(before);
  });
});
