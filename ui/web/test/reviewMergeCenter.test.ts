import assert from "node:assert/strict";
import { describe, it } from "vitest";
import {
  buildReviewMergeDiffPreview,
  buildReviewMergeMergeResult,
  buildReviewMergeOutcomeTrail,
  buildReviewMergeSafety,
  buildReviewMergeSummary,
  defaultReviewMergeJobId,
  filterReviewMergeItems,
  reviewMergeStatusLabel,
  shouldAutoLoadReview,
} from "../src/features/chat/reviewMergeCenter.ts";
import type { DurableSubagentMerge, DurableSubagentReview, FeedItem } from "../src/lib/types.ts";

function worker(partial: Partial<FeedItem>): FeedItem {
  return {
    id: partial.jobId || partial.id || "subagent-1",
    kind: "subagent",
    title: "worker",
    body: "",
    ...partial,
  };
}

describe("reviewMergeCenter", () => {
  it("builds review, safety, preview, outcome trail, and merge contracts", () => {
{
  const summary = buildReviewMergeSummary([
    worker({
      jobId: "job-ready",
      title: "Implement UI",
      status: "completed",
      writeScope: ["ui/web/src"],
      updatedAt: "2026-05-14T10:00:00Z",
    }),
    worker({
      jobId: "job-merged",
      title: "Docs",
      status: "completed",
      mergeStatus: "merged",
      updatedAt: "2026-05-14T11:00:00Z",
    }),
    worker({
      jobId: "job-blocked",
      title: "Needs approval",
      status: "pending_approval",
      updatedAt: "2026-05-14T12:00:00Z",
    }),
  ]);

  assert.equal(summary.ready, 1);
  assert.equal(summary.merged, 1);
  assert.equal(summary.blocked, 1);
  assert.equal(summary.items[0]?.jobId, "job-blocked");
  assert.equal(summary.items[1]?.jobId, "job-ready");
  assert.equal(summary.items[2]?.jobId, "job-merged");
  assert.equal(reviewMergeStatusLabel("ready"), "Ready for review");
}

{
  const summary = buildReviewMergeSummary([
    worker({ jobId: "job-no-changes", status: "completed", mergeStatus: "no_changes" }),
    worker({ jobId: "job-failed", status: "failed", error: "tests failed" }),
    worker({ jobId: "job-running", status: "running" }),
  ]);

  assert.equal(summary.merged, 1);
  assert.equal(summary.failed, 1);
  assert.equal(summary.running, 1);
  assert.equal(summary.items.find((item) => item.jobId === "job-no-changes")?.status, "no_changes");
}

{
  const summary = buildReviewMergeSummary([
    worker({ jobId: "job-conflict", status: "completed", mergeStatus: "conflicted" }),
    worker({ id: "assistant-1", kind: "assistant", title: "Assistant", body: "not a subagent" }),
  ]);

  assert.equal(summary.items.length, 1);
  assert.equal(summary.items[0]?.status, "conflicted");
  assert.equal(summary.ready, 1);
}

{
  const summary = buildReviewMergeSummary([worker({ jobId: "job-reviewed", status: "completed", writeScope: ["ui/web/src"] })], {
    reviewedJobId: "job-reviewed",
  });

  assert.equal(summary.items[0]?.status, "review_loaded");
  assert.equal(reviewMergeStatusLabel("review_loaded"), "Review loaded");
}

{
  const summary = buildReviewMergeSummary([
    worker({ jobId: "job-blocked", status: "pending_approval", updatedAt: "2026-05-14T12:00:00Z" }),
    worker({ jobId: "job-ready", status: "completed", writeScope: ["ui/web/src"], updatedAt: "2026-05-14T10:00:00Z" }),
    worker({ jobId: "job-conflict", status: "completed", mergeStatus: "conflicted", updatedAt: "2026-05-14T11:00:00Z" }),
    worker({ jobId: "job-merged", status: "completed", mergeStatus: "merged", updatedAt: "2026-05-14T09:00:00Z" }),
    worker({ jobId: "job-failed", status: "failed", updatedAt: "2026-05-14T08:00:00Z" }),
  ]);

  assert.equal(defaultReviewMergeJobId(summary.items), "job-conflict");
  assert.deepEqual(filterReviewMergeItems(summary.items, "reviewable").map((item) => item.jobId), ["job-conflict", "job-ready"]);
  assert.deepEqual(filterReviewMergeItems(summary.items, "conflicted").map((item) => item.jobId), ["job-conflict"]);
  assert.deepEqual(filterReviewMergeItems(summary.items, "merged").map((item) => item.jobId), ["job-merged"]);
  assert.deepEqual(filterReviewMergeItems(summary.items, "failed").map((item) => item.jobId), ["job-failed"]);
}

{
  const summary = buildReviewMergeSummary([worker({ jobId: "job-ready", status: "completed", writeScope: ["ui/web/src"] })]);
  const item = summary.items[0]!;

  assert.equal(shouldAutoLoadReview(item, undefined, undefined), true);
  assert.equal(shouldAutoLoadReview(item, "job-ready", undefined), false);
  assert.equal(shouldAutoLoadReview(item, undefined, "job-ready"), false);
  assert.equal(shouldAutoLoadReview({ ...item, status: "merged" }, undefined, undefined), false);
}

{
  const summary = buildReviewMergeSummary([worker({ jobId: "job-ready", status: "completed", writeScope: ["ui/web/src"] })]);
  const review: DurableSubagentReview = {
    job_id: "job-ready",
    write_scope: ["ui/web/src"],
    changes: [
      { path: "ui/web/src/App.tsx", status: "modified" },
      { path: "ui/web/src/styles.css", status: "added" },
    ],
    diff: "@@ diff",
    diff_truncated: true,
    conflicts: ["ui/web/src/App.tsx"],
  };
  const safety = buildReviewMergeSafety(summary.items[0]!, review);

  assert.equal(safety.diffStatus, "truncated");
  assert.equal(safety.conflictStatus, "conflicts");
  assert.equal(safety.changedFiles, 2);
  assert.deepEqual(safety.writeScope, ["ui/web/src"]);
  assert.equal(safety.mergeCaution, true);
}

{
  const review: DurableSubagentReview = {
    job_id: "job-ready",
    changes: [
      { path: "ui/web/src/App.tsx", status: "modified" },
      { path: "ui/web/src/styles.css", status: "added" },
    ],
    diff: "0123456789abcdef",
    diff_truncated: true,
  };
  const collapsed = buildReviewMergeDiffPreview(review, 8, false);
  const expanded = buildReviewMergeDiffPreview(review, 8, true);

  assert.equal(collapsed.large, true);
  assert.equal(collapsed.diff, "01234567\n...");
  assert.equal(collapsed.files[0]?.path, "ui/web/src/App.tsx");
  assert.equal(collapsed.truncatedByBackend, true);
  assert.equal(expanded.diff, "0123456789abcdef");
}

{
  const summary = buildReviewMergeSummary([worker({ jobId: "job-ready", status: "completed", writeScope: ["ui/web/src"] })]);
  const review: DurableSubagentReview = { job_id: "job-ready", changes: [], diff: "" };
  const trail = buildReviewMergeOutcomeTrail(summary.items[0]!, [
    {
      status: "merged",
      recovered: true,
      longTask: { longtask_id: "lt_demo", status: "error" },
      worker: { jobId: "job-ready" },
    },
  ], review);

  assert.equal(trail.recovered, true);
  assert.deepEqual(trail.steps.map((step) => step.label), ["LongTask failed", "Worker completed", "Review loaded", "Merge pending"]);
}

{
  const summary = buildReviewMergeSummary([worker({ jobId: "job-ready", status: "completed", writeScope: ["ui/web/src"] })]);
  const merge: DurableSubagentMerge = {
    job_id: "job-ready",
    status: "merged",
    applied: [{ path: "ui/web/src/App.tsx", status: "modified" }],
  };
  const result = buildReviewMergeMergeResult(summary.items[0]!, merge);

  assert.equal(result?.status, "merged");
  assert.equal(result?.appliedCount, 1);
  assert.equal(result?.applied[0]?.path, "ui/web/src/App.tsx");
  assert.equal(buildReviewMergeMergeResult(summary.items[0]!, { ...merge, job_id: "other" }), null);
}

  });
});
