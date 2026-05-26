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
  },
};

export type TaskCenterText = TaskCenterStrings;

export function useTaskCenterText(): TaskCenterText {
  const locale = useLocaleStore((s) => s.locale);
  return taskCenterText[locale];
}
