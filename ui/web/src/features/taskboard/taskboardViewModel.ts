import type { TaskboardStatus, TaskboardUrgency } from "../../lib/types";

export const COLUMNS: { status: TaskboardStatus; labelKey: string; dot: string }[] = [
  { status: "backlog", labelKey: "taskboard.col.backlog", dot: "#8c8c8c" },
  { status: "todo", labelKey: "taskboard.col.todo", dot: "#1677ff" },
  { status: "in_progress", labelKey: "taskboard.col.inProgress", dot: "#fa8c16" },
  { status: "in_review", labelKey: "taskboard.col.inReview", dot: "#722ed1" },
  { status: "done", labelKey: "taskboard.col.done", dot: "#52c41a" },
];

export const URGENCY_COLORS: Record<TaskboardUrgency, string> = {
  urgent: "#ff4d4f",
  normal: "#1677ff",
  low: "#8c8c8c",
};

export const EXECUTION_STATUS_COLORS: Record<string, string> = {
  running: "processing",
  completed: "success",
  failed: "error",
  cancelled: "default",
};

export const STAGE_COLORS: Record<string, string> = {
  thinking: "purple",
  tool_call: "geekblue",
  final_response: "green",
  waiting_approval: "warning",
  error: "error",
  interrupted: "default",
  idle: "default",
};

export function urgencyRank(urgency: TaskboardUrgency): number {
  return urgency === "urgent" ? 0 : urgency === "low" ? 2 : 1;
}
