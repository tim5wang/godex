import { useLocaleStore } from "../../store/locale";

interface ReviewMergeStrings {
  title: string;
  ready: string;
  conflicted: string;
  merged: string;
  failed: string;
  all: string;
  noWorkers: string;
  selectWorker: string;
  loadReview: string;
  merge: string;
  resume: string;
  cancel: string;
  mergeConflicted: string;
  mergeConfirm: string;
  cancelWorker: string;
  loadingReview: string;
  reviewNotLoaded: string;
  reviewNotLoadedDesc: string;
  mergeConflicts: string;
  diffTruncated: string;
  diffTruncatedDesc: string;
  noChangedFiles: string;
  collapseDiff: string;
  showFullDiff: string;
  copyDiff: string;
  focused: string;
  noDiff: string;
  mergeStatus: string;
  appliedFiles: string;
  binary: string;
  copyPath: string;
  statusRunning: string;
  statusBlocked: string;
  statusReady: string;
  statusReviewLoaded: string;
  statusMerged: string;
  statusNoChanges: string;
  statusConflicted: string;
  statusFailed: string;
  readyCount: string;
  blockedCount: string;
  mergedCount: string;
  failedCount: string;
  diffLabel: string;
  notLoaded: string;
  complete: string;
  truncated: string;
  conflicts: string;
  noConflicts: string;
  conflictsUnknown: string;
  files: string;
  scopeUnknown: string;
  extraConfirmation: string;
  longTaskFailed: string;
  longTaskLinked: string;
  workerLabel: string;
  reviewLoaded: string;
  reviewPending: string;
  mergePending: string;
  recovered: string;
}

const reviewMergeText: Record<"en" | "zh", ReviewMergeStrings> = {
  en: {
    title: "Review & Merge",
    ready: "Ready",
    conflicted: "Conflicted",
    merged: "Merged",
    failed: "Failed",
    all: "All",
    noWorkers: "No workers to review",
    selectWorker: "Select a worker",
    loadReview: "Load review",
    merge: "Merge",
    resume: "Resume",
    cancel: "Cancel",
    mergeConflicted: "This worker has conflicts. Merge anyway?",
    mergeConfirm: "Merge this worker's changes into the workspace?",
    cancelWorker: "Cancel this worker?",
    loadingReview: "Loading review...",
    reviewNotLoaded: "Review not loaded",
    reviewNotLoadedDesc: "Load review to inspect changed files and diff before merge.",
    mergeConflicts: "Merge conflicts",
    diffTruncated: "Diff is truncated",
    diffTruncatedDesc: "Use the changed file list and worktree path for deeper inspection.",
    noChangedFiles: "No changed files",
    collapseDiff: "Collapse diff",
    showFullDiff: "Show full diff",
    copyDiff: "Copy diff",
    focused: "Focused",
    noDiff: "No diff",
    mergeStatus: "Merge",
    appliedFiles: "applied file",
    binary: "binary",
    copyPath: "Copy path",
    // Status labels
    statusRunning: "Running",
    statusBlocked: "Blocked",
    statusReady: "Ready for review",
    statusReviewLoaded: "Review loaded",
    statusMerged: "Merged",
    statusNoChanges: "No changes",
    statusConflicted: "Conflicted",
    statusFailed: "Failed",
    // Counters
    readyCount: "ready",
    blockedCount: "blocked",
    mergedCount: "merged",
    failedCount: "failed",
    // Safety bar
    diffLabel: "Diff",
    notLoaded: "not loaded",
    complete: "complete",
    truncated: "truncated",
    conflicts: "Conflicts",
    noConflicts: "No conflicts",
    conflictsUnknown: "Conflicts unknown",
    files: "files",
    scopeUnknown: "scope unknown",
    extraConfirmation: "extra confirmation",
    // Trail
    longTaskFailed: "LongTask failed",
    longTaskLinked: "LongTask linked",
    workerLabel: "Worker",
    reviewLoaded: "Review loaded",
    reviewPending: "Review pending",
    mergePending: "Merge pending",
    recovered: "recovered",
  },
  zh: {
    title: "审核与合并",
    ready: "待处理",
    conflicted: "有冲突",
    merged: "已合并",
    failed: "失败",
    all: "全部",
    noWorkers: "暂无待审核任务",
    selectWorker: "选择一个任务",
    loadReview: "加载审核",
    merge: "合并",
    resume: "恢复",
    cancel: "取消",
    mergeConflicted: "该任务存在冲突，确认合并？",
    mergeConfirm: "确认将该任务的变更合并到工作区？",
    cancelWorker: "确认取消该任务？",
    loadingReview: "正在加载审核...",
    reviewNotLoaded: "审核未加载",
    reviewNotLoadedDesc: "加载审核以检查变更文件和 diff 后再合并。",
    mergeConflicts: "合并冲突",
    diffTruncated: "Diff 已截断",
    diffTruncatedDesc: "请使用变更文件列表和 worktree 路径进行更深入的检查。",
    noChangedFiles: "无变更文件",
    collapseDiff: "收起 diff",
    showFullDiff: "显示完整 diff",
    copyDiff: "复制 diff",
    focused: "聚焦于",
    noDiff: "无 diff",
    mergeStatus: "合并",
    appliedFiles: "个文件已应用",
    binary: "二进制",
    copyPath: "复制路径",
    // Status labels
    statusRunning: "运行中",
    statusBlocked: "已阻塞",
    statusReady: "待审核",
    statusReviewLoaded: "审核已加载",
    statusMerged: "已合并",
    statusNoChanges: "无变更",
    statusConflicted: "有冲突",
    statusFailed: "失败",
    // Counters
    readyCount: "待处理",
    blockedCount: "已阻塞",
    mergedCount: "已合并",
    failedCount: "失败",
    // Safety bar
    diffLabel: "Diff",
    notLoaded: "未加载",
    complete: "完整",
    truncated: "已截断",
    conflicts: "有冲突",
    noConflicts: "无冲突",
    conflictsUnknown: "冲突未知",
    files: "文件",
    scopeUnknown: "范围未知",
    extraConfirmation: "需额外确认",
    // Trail
    longTaskFailed: "LongTask 失败",
    longTaskLinked: "LongTask 已关联",
    workerLabel: "Worker",
    reviewLoaded: "审核已加载",
    reviewPending: "审核待处理",
    mergePending: "合并待处理",
    recovered: "已恢复",
  },
} as const satisfies Record<"en" | "zh", ReviewMergeStrings>;

export type ReviewMergeText = ReviewMergeStrings;

export function useReviewMergeText(): ReviewMergeText {
  const locale = useLocaleStore((s) => s.locale);
  return reviewMergeText[locale];
}

export function reviewMergeStatusI18n(status: string, t: ReviewMergeText): string {
  switch (status) {
    case "running":
      return t.statusRunning;
    case "blocked":
      return t.statusBlocked;
    case "ready":
      return t.statusReady;
    case "review_loaded":
      return t.statusReviewLoaded;
    case "merged":
      return t.statusMerged;
    case "no_changes":
      return t.statusNoChanges;
    case "conflicted":
      return t.statusConflicted;
    case "failed":
      return t.statusFailed;
    default:
      return status;
  }
}
