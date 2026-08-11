import { useLocaleStore } from "../../store/locale";

interface TaskCenterStrings {
  title: string;
  outcomes: string;
  active: string;
  review: string;
  collapse: string;
  expand: string;
  noOutcomes: string;
  idle: string;
  nothingReview: string;
  running: string;
  blocked: string;
  readyForReview: string;
  merged: string;
  failed: string;
  recovered: string;
  reviewSubagent: string;
  mergeSubagent: string;
  mergeConfirm: string;
  resumeSubagent: string;
  cancelSubagent: string;
  runLongTask: string;
  finalizeLongTask: string;
  cancelLongTaskNode: string;
  cancelLongTaskConfirm: string;
  dismissed: string;
  showDismissed: string;
  hideDismissed: string;
  actionableOnly: string;
  showAll: string;
  // T15: rollback / lookup / gc / reflux bubble keys.
  rollbackLongTask: string;
  rollbackReason: string;
  rollbackReasonTooLong: string;
  rollbackReasonPlaceholder: string;
  lookupLongTask: string;
  lookupByCommit: string;
  lookupByStory: string;
  gcLongTask: string;
  gcDryRun: string;
  gcApply: string;
  resumeLongTask: string;
  refluxPrefix: string;
  reverted: string;
}

const taskCenterText: Record<"en" | "zh", TaskCenterStrings> = {
  en: {
    title: "Task Center",
    outcomes: "Outcomes",
    active: "Active",
    review: "Review",
    collapse: "Collapse",
    expand: "Expand",
    noOutcomes: "No task outcomes yet",
    idle: "Idle",
    nothingReview: "Nothing waiting for review",
    running: "Running",
    blocked: "Blocked",
    readyForReview: "Ready for review",
    merged: "Merged",
    failed: "Failed",
    recovered: "recovered",
    reviewSubagent: "Review subagent changes",
    mergeSubagent: "Merge subagent changes",
    mergeConfirm: "Merge subagent changes?",
    resumeSubagent: "Resume subagent",
    cancelSubagent: "Cancel subagent",
    runLongTask: "Run LongTask",
    finalizeLongTask: "Finalize LongTask story",
    cancelLongTaskNode: "Cancel LongTask node",
    cancelLongTaskConfirm: "Cancel this LongTask node?",
    dismissed: "Dismissed",
    showDismissed: "Show dismissed",
    hideDismissed: "Hide dismissed",
    actionableOnly: "Actionable only",
    showAll: "Show all",
    rollbackLongTask: "Rollback LongTask story",
    rollbackReason: "Rollback reason (optional, max 1024 bytes)",
    rollbackReasonTooLong: "Reason exceeds 1024 bytes",
    rollbackReasonPlaceholder: "Optional: explain why you are rolling back",
    lookupLongTask: "Lookup LongTask",
    lookupByCommit: "By commit hash",
    lookupByStory: "By story id",
    gcLongTask: "Garbage-collect LongTask",
    gcDryRun: "Dry run (default)",
    gcApply: "Apply (delete)",
    resumeLongTask: "Resume LongTask",
    refluxPrefix: "[LongTask]",
    reverted: "reverted",
  },
  zh: {
    title: "任务中心",
    outcomes: "全部结果",
    active: "进行中",
    review: "待审核",
    collapse: "收起",
    expand: "展开",
    noOutcomes: "暂无任务",
    idle: "空闲",
    nothingReview: "无待审核项",
    running: "运行中",
    blocked: "已阻塞",
    readyForReview: "待审核",
    merged: "已合并",
    failed: "失败",
    recovered: "已恢复",
    reviewSubagent: "审核子任务变更",
    mergeSubagent: "合并子任务变更",
    mergeConfirm: "确认合并子任务变更？",
    resumeSubagent: "恢复子任务",
    cancelSubagent: "取消子任务",
    runLongTask: "运行 LongTask",
    finalizeLongTask: "完成 LongTask story",
    cancelLongTaskNode: "取消 LongTask 节点",
    cancelLongTaskConfirm: "确认取消此 LongTask 节点？",
    dismissed: "已忽略",
    showDismissed: "显示已忽略",
    hideDismissed: "隐藏已忽略",
    actionableOnly: "仅待办",
    showAll: "全部",
    rollbackLongTask: "回滚 LongTask story",
    rollbackReason: "回滚原因（可选，最多 1024 字节）",
    rollbackReasonTooLong: "回滚原因超过 1024 字节",
    rollbackReasonPlaceholder: "可选：说明为什么回滚",
    lookupLongTask: "反查 LongTask",
    lookupByCommit: "按 commit hash",
    lookupByStory: "按 story id",
    gcLongTask: "清理 LongTask",
    gcDryRun: "预演（默认）",
    gcApply: "执行（删除）",
    resumeLongTask: "恢复 LongTask",
    refluxPrefix: "[LongTask]",
    reverted: "已回滚",
  },
};

export type TaskCenterText = TaskCenterStrings;

export function useTaskCenterText(): TaskCenterText {
  const locale = useLocaleStore((s) => s.locale);
  return taskCenterText[locale];
}
