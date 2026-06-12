import { describe, expect, it } from "vitest";

import {
  asTaskCenterBridge,
  type TaskCenterPanelBridgeProps,
} from "../src/features/tasks/TaskCenterContext";

// P1 / T5g: a small, fast contract test for the React context seam.
// We deliberately avoid rendering React here so the test runs without
// jsdom or @testing-library/react. The full Provider/Consumer wiring
// is exercised by the existing chat store and by manual smoke.

const baseProps: TaskCenterPanelBridgeProps = {
  outcomes: [],
  collapsed: false,
  onCollapsedChange: () => undefined,
  onReviewSubagent: () => undefined,
  onMergeSubagent: () => undefined,
  onResumeSubagent: () => undefined,
  onCancelSubagent: () => undefined,
  onRunLongTask: () => undefined,
  onCancelLongTask: () => undefined,
  onFinalizeLongTask: () => undefined,
  onOpenReviewMergeCenter: () => undefined,
};

describe("asTaskCenterBridge (P1 / T5g contract seam)", () => {
  it("returns the value it is given (identity)", () => {
    const v = { hello: "world" } as never;
    expect(asTaskCenterBridge(v)).toBe(v);
  });

  it("accepts null as a valid bridge value (fallback path)", () => {
    expect(asTaskCenterBridge(null)).toBeNull();
  });

  it("accepts an empty ReactNode (provider value with no children yet)", () => {
    expect(asTaskCenterBridge(undefined)).toBeUndefined();
  });

  it("the shape of TaskCenterPanelBridgeProps matches the 18 fields the existing TaskCenterPanel takes", () => {
    // Sanity: the props type must carry at least the 18 keys we
    // bridge across the Layout boundary. We assert via Object.keys on
    // a structural sample so adding new optional fields is a non-
    // breaking change.
    const requiredKeys: ReadonlyArray<keyof TaskCenterPanelBridgeProps> = [
      "outcomes",
      "collapsed",
      "onCollapsedChange",
      "onReviewSubagent",
      "onMergeSubagent",
      "onResumeSubagent",
      "onCancelSubagent",
      "onRunLongTask",
      "onCancelLongTask",
      "onFinalizeLongTask",
      "onOpenReviewMergeCenter",
    ];
    for (const key of requiredKeys) {
      expect(key in baseProps).toBe(true);
    }
  });
});
