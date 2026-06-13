import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { buildTaskOutcomes } from "../src/features/chat/taskCenterOutcome.ts";
import type { FeedItem, LongTaskView } from "../src/lib/types.ts";

function longTask(partial: Partial<LongTaskView>): LongTaskView {
  return {
    longtask_id: "lt",
    workflow_id: "lt",
    status: "running",
    total: 1,
    pending: 0,
    running: 0,
    completed: 0,
    failed: 0,
    stories: [],
    ...partial,
  };
}

function worker(partial: Partial<FeedItem>): FeedItem {
  return {
    id: partial.jobId || "subagent-1",
    kind: "subagent",
    title: "worker",
    body: "",
    ...partial,
  };
}

describe("taskCenterOutcome", () => {
  it("correlates long tasks and subagent outcomes", () => {
{
  const outcomes = buildTaskOutcomes({
    longTasks: [
      longTask({
        longtask_id: "lt_demo",
        workflow_id: "lt_demo",
        status: "error",
        failed: 1,
        description: "Create docs/superpowers/tmp/tui-mvp-demo.md",
        stories: [
          {
            id: "story-1",
            title: "Create demo doc",
            description: "Write docs/superpowers/tmp/tui-mvp-demo.md",
            status: "error",
            passes: false,
          },
        ],
      }),
    ],
    subagents: [
      worker({
        jobId: "subagent_demo",
        title: "Create demo doc",
        objective: "Create docs/superpowers/tmp/tui-mvp-demo.md",
        status: "completed",
        mergeStatus: "merged",
        writeScope: ["docs/superpowers/tmp"],
      }),
    ],
  });

  assert.equal(outcomes.length, 1);
  assert.equal(outcomes[0]?.status, "merged");
  assert.equal(outcomes[0]?.recovered, true);
  assert.equal(outcomes[0]?.longTask?.longtask_id, "lt_demo");
  assert.equal(outcomes[0]?.worker?.jobId, "subagent_demo");
}

{
  const outcomes = buildTaskOutcomes({
    longTasks: [
      longTask({
        longtask_id: "lt_direct",
        workflow_id: "lt_direct",
        status: "error",
        failed: 1,
        stories: [{ id: "story-1", status: "error", passes: false, job_id: "subagent_direct" }],
      }),
    ],
    subagents: [worker({ jobId: "subagent_direct", status: "completed", mergeStatus: "merged" })],
  });

  assert.equal(outcomes.length, 1);
  assert.equal(outcomes[0]?.status, "merged");
  assert.equal(outcomes[0]?.recovered, true);
}

{
  const outcomes = buildTaskOutcomes({
    longTasks: [
      longTask({
        longtask_id: "lt_generic",
        workflow_id: "lt_generic",
        project: "Task Center",
        description: "Improve Task Center",
        status: "error",
        failed: 1,
      }),
    ],
    subagents: [worker({ jobId: "subagent_generic", title: "Task Center", objective: "Improve Task Center", status: "completed", mergeStatus: "merged" })],
  });

  assert.equal(outcomes.length, 2);
  assert.equal(outcomes.find((item) => item.longTask?.longtask_id === "lt_generic")?.status, "failed");
  assert.equal(outcomes.find((item) => item.worker?.jobId === "subagent_generic")?.status, "merged");
}

  });
});
