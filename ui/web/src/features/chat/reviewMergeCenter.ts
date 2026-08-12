import type { DurableSubagentFileChange, DurableSubagentMerge, DurableSubagentReview, FeedItem } from "../../lib/types";

export type ReviewMergeStatus = "running" | "blocked" | "ready" | "review_loaded" | "merged" | "no_changes" | "conflicted" | "failed";
export type ReviewMergeFilter = "reviewable" | "conflicted" | "merged" | "failed" | "all";

export interface ReviewMergeItem {
  id: string;
  jobId: string;
  status: ReviewMergeStatus;
  title: string;
  detail?: string;
  worker: FeedItem;
  changedPathCount: number;
  writeScope: string[];
  mergeStatus?: string;
  workerStatus?: string;
  updatedAt?: string;
  finishedAt?: string;
}

export interface ReviewMergeSummary {
  items: ReviewMergeItem[];
  ready: number;
  merged: number;
  blocked: number;
  failed: number;
  running: number;
}

export interface BuildReviewMergeSummaryOptions {
  reviewedJobId?: string;
}

export interface ReviewMergeSafety {
  diffStatus: "not_loaded" | "complete" | "truncated";
  conflictStatus: "unknown" | "none" | "conflicts";
  changedFiles: number;
  writeScope: string[];
  mergeCaution: boolean;
}

export interface ReviewMergeDiffPreview {
  files: DurableSubagentFileChange[];
  diff: string;
  fullDiff: string;
  large: boolean;
  truncatedByBackend: boolean;
}

export interface ReviewMergeOutcomeTrailStep {
  label: string;
  tone: "default" | "processing" | "success" | "warning" | "danger";
}

export interface ReviewMergeOutcomeTrail {
  recovered: boolean;
  steps: ReviewMergeOutcomeTrailStep[];
}

export interface ReviewMergeMergeResult {
  status: string;
  appliedCount: number;
  applied: DurableSubagentFileChange[];
  conflicts: string[];
  worktreeDir?: string;
}

interface ReviewMergeOutcomeSource {
  status?: string;
  recovered?: boolean;
  longTask?: { longtask_id?: string; workflow_id?: string; status?: string };
  worker?: { jobId?: string; job_id?: string; status?: string; mergeStatus?: string; merge_status?: string };
}

export function buildReviewMergeSummary(workers: FeedItem[] = [], options: BuildReviewMergeSummaryOptions = {}): ReviewMergeSummary {
  const items = workers
    .filter((worker) => worker.kind === "subagent" && (worker.jobId || worker.id))
    .map((worker) => reviewMergeItemFromWorker(worker, options.reviewedJobId))
    .sort(compareReviewMergeItems);

  return {
    items,
    ready: items.filter((item) => item.status === "ready" || item.status === "review_loaded" || item.status === "conflicted").length,
    merged: items.filter((item) => item.status === "merged" || item.status === "no_changes").length,
    blocked: items.filter((item) => item.status === "blocked").length,
    failed: items.filter((item) => item.status === "failed").length,
    running: items.filter((item) => item.status === "running").length,
  };
}

export function defaultReviewMergeJobId(items: ReviewMergeItem[]) {
  return items.find((item) => item.status === "conflicted" || item.status === "ready" || item.status === "review_loaded")?.jobId || items[0]?.jobId || "";
}

export function filterReviewMergeItems(items: ReviewMergeItem[], filter: ReviewMergeFilter) {
  switch (filter) {
    case "reviewable":
      return items.filter((item) => item.status === "ready" || item.status === "review_loaded" || item.status === "conflicted");
    case "conflicted":
      return items.filter((item) => item.status === "conflicted");
    case "merged":
      return items.filter((item) => item.status === "merged" || item.status === "no_changes");
    case "failed":
      return items.filter((item) => item.status === "failed");
    case "all":
      return items;
  }
}

export function shouldAutoLoadReview(item?: ReviewMergeItem, loadedJobId?: string, loadingJobId?: string) {
  if (!item || loadedJobId === item.jobId || loadingJobId === item.jobId) {
    return false;
  }
  return item.status === "ready" || item.status === "conflicted";
}

export function buildReviewMergeSafety(item: ReviewMergeItem, review?: DurableSubagentReview | null): ReviewMergeSafety {
  if (!review || review.job_id !== item.jobId) {
    return {
      diffStatus: "not_loaded",
      conflictStatus: "unknown",
      changedFiles: item.changedPathCount,
      writeScope: item.writeScope,
      mergeCaution: false,
    };
  }
  const conflictStatus = review.conflicts?.length ? "conflicts" : "none";
  return {
    diffStatus: review.diff_truncated ? "truncated" : "complete",
    conflictStatus,
    changedFiles: review.changes.length,
    writeScope: review.write_scope?.length ? review.write_scope : item.writeScope,
    mergeCaution: review.diff_truncated || conflictStatus === "conflicts",
  };
}

export function buildReviewMergeDiffPreview(review?: DurableSubagentReview | null, maxLength = 8000, expanded = false): ReviewMergeDiffPreview {
  const fullDiff = review?.diff?.trim() || "";
  const large = fullDiff.length > maxLength;
  return {
    files: review?.changes ?? [],
    fullDiff,
    diff: large && !expanded ? `${fullDiff.slice(0, maxLength).trimEnd()}\n...` : fullDiff,
    large,
    truncatedByBackend: !!review?.diff_truncated,
  };
}

export function buildReviewMergeOutcomeTrail(
  item: ReviewMergeItem,
  outcomes: ReviewMergeOutcomeSource[] = [],
  review?: DurableSubagentReview | null,
  merge?: DurableSubagentMerge | null,
): ReviewMergeOutcomeTrail {
  const outcome = outcomes.find((candidate) => candidate.worker?.jobId === item.jobId || candidate.worker?.job_id === item.jobId);
  const steps: ReviewMergeOutcomeTrailStep[] = [];
  const longTaskStatus = outcome?.longTask?.status?.toLowerCase() || "";
  if (outcome?.longTask) {
    const failed = longTaskStatus === "error" || longTaskStatus === "failed";
    steps.push({ label: failed ? "LongTask failed" : "LongTask linked", tone: failed ? "danger" : "default" });
  }
  steps.push({ label: `Worker ${workerTrailStatus(item)}`, tone: workerTrailTone(item) });
  steps.push({ label: review?.job_id === item.jobId ? "Review loaded" : "Review pending", tone: review?.job_id === item.jobId ? "success" : "warning" });
  const mergeResult = buildReviewMergeMergeResult(item, merge);
  if (mergeResult) {
    steps.push({ label: `Merge ${mergeResult.status}`, tone: mergeResult.status === "merged" || mergeResult.status === "no_changes" ? "success" : "warning" });
  } else {
    steps.push({ label: item.status === "merged" || item.status === "no_changes" ? `Merge ${item.status}` : "Merge pending", tone: item.status === "merged" || item.status === "no_changes" ? "success" : "warning" });
  }
  return {
    recovered: !!outcome?.recovered,
    steps,
  };
}

export function buildReviewMergeMergeResult(item: ReviewMergeItem, merge?: DurableSubagentMerge | null): ReviewMergeMergeResult | null {
  if (!merge || merge.job_id !== item.jobId) {
    return null;
  }
  const applied = merge.applied ?? [];
  return {
    status: merge.status,
    appliedCount: applied.length,
    applied,
    conflicts: merge.conflicts ?? [],
    worktreeDir: merge.worktree_dir,
  };
}

export function reviewMergeStatusLabel(status: ReviewMergeStatus) {
  switch (status) {
    case "running":
      return "Running";
    case "blocked":
      return "Blocked";
    case "ready":
      return "Ready for review";
    case "review_loaded":
      return "Review loaded";
    case "merged":
      return "Merged";
    case "no_changes":
      return "No changes";
    case "conflicted":
      return "Conflicted";
    case "failed":
      return "Failed";
  }
}

export function reviewMergeStatusColor(status: ReviewMergeStatus) {
  switch (status) {
    case "running":
      return "processing";
    case "blocked":
      return "gold";
    case "ready":
    case "review_loaded":
      return "blue";
    case "merged":
    case "no_changes":
      return "green";
    case "conflicted":
      return "volcano";
    case "failed":
      return "red";
  }
}

function reviewMergeItemFromWorker(worker: FeedItem, reviewedJobId?: string): ReviewMergeItem {
  const jobId = worker.jobId || worker.id;
  const status = classifyWorker(worker);
  return {
    id: `review-merge:${jobId}`,
    jobId,
    status: status === "ready" && reviewedJobId === jobId ? "review_loaded" : status,
    title: firstNonBlank(worker.displayTitle, worker.title, worker.objective, worker.jobId, worker.id, "Worker"),
    detail: firstNonBlank(worker.lastMessage, worker.error, worker.summary, worker.body),
    worker,
    changedPathCount: worker.writeScope?.length ?? 0,
    writeScope: worker.writeScope ?? [],
    mergeStatus: worker.mergeStatus,
    workerStatus: worker.status,
    updatedAt: worker.updatedAt,
    finishedAt: worker.finishedAt,
  };
}

function classifyWorker(worker: FeedItem): ReviewMergeStatus {
  const status = (worker.status || "").toLowerCase();
  const merge = (worker.mergeStatus || "").toLowerCase();
  if (merge === "merged") {
    return "merged";
  }
  if (merge === "no_changes") {
    return "no_changes";
  }
  if (merge === "conflicted" || merge === "conflicts") {
    return "conflicted";
  }
  if (status === "pending_approval") {
    return "blocked";
  }
  if (status === "running" || status === "pending" || status === "resuming") {
    return "running";
  }
  if (status === "completed") {
    // Read-only subagent (no write_scope) that finished has nothing in its
    // worktree to merge or review; normalize stale pending/empty merge
    // status to no_changes so it never surfaces as an un-actionable
    // "待审核" item (SSE/timeline payloads may still carry pending).
    if ((merge === "" || merge === "pending") && !(worker.writeScope?.length)) {
      return "no_changes";
    }
    return "ready";
  }
  if (["failed", "error", "cancelled", "canceled", "interrupted", "timeout"].includes(status)) {
    return "failed";
  }
  return "ready";
}

function compareReviewMergeItems(left: ReviewMergeItem, right: ReviewMergeItem) {
  const rankDelta = reviewMergeRank(left) - reviewMergeRank(right);
  if (rankDelta !== 0) {
    return rankDelta;
  }
  return itemTime(right) - itemTime(left);
}

function reviewMergeRank(item: ReviewMergeItem) {
  switch (item.status) {
    case "blocked":
      return 0;
    case "conflicted":
      return 1;
    case "ready":
    case "review_loaded":
      return 2;
    case "running":
      return 3;
    case "failed":
      return 4;
    case "merged":
    case "no_changes":
      return 5;
  }
}

function itemTime(item: ReviewMergeItem) {
  const value = Date.parse(item.updatedAt || item.finishedAt || "");
  return Number.isFinite(value) ? value : 0;
}

function workerTrailStatus(item: ReviewMergeItem) {
  switch (item.status) {
    case "ready":
    case "review_loaded":
      return "completed";
    case "no_changes":
      return "completed";
    default:
      return item.status;
  }
}

function workerTrailTone(item: ReviewMergeItem): ReviewMergeOutcomeTrailStep["tone"] {
  switch (item.status) {
    case "merged":
    case "no_changes":
    case "ready":
    case "review_loaded":
      return "success";
    case "running":
      return "processing";
    case "blocked":
    case "conflicted":
      return "warning";
    case "failed":
      return "danger";
  }
}

function firstNonBlank(...values: Array<string | undefined>) {
  return values.find((value) => value?.trim())?.trim() || "";
}
