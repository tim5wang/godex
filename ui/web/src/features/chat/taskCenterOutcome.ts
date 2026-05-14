import type { FeedItem, LongTaskView, PendingPermission, QueuedTurn } from "../../lib/types";

export type TaskOutcomeStatus = "idle" | "running" | "blocked" | "ready_for_review" | "merged" | "failed";

export interface TaskOutcomeSignal {
  kind: "longtask" | "worker" | "permission" | "turn";
  id: string;
  status: string;
  detail?: string;
}

export interface TaskOutcome {
  id: string;
  status: TaskOutcomeStatus;
  title: string;
  detail?: string;
  recovered?: boolean;
  longTask?: LongTaskView;
  worker?: FeedItem;
  permission?: PendingPermission;
  queuedTurn?: QueuedTurn;
  activeTurnId?: string;
  signals: TaskOutcomeSignal[];
}

export interface BuildTaskOutcomesInput {
  longTasks?: LongTaskView[];
  subagents?: FeedItem[];
  pendingPermissions?: PendingPermission[];
  queuedTurns?: QueuedTurn[];
  running?: boolean;
  activeTurnId?: string;
  activePhase?: string;
}

export function buildTaskOutcomes(input: BuildTaskOutcomesInput): TaskOutcome[] {
  const longTasks = input.longTasks ?? [];
  const subagents = input.subagents ?? [];
  const outcomes: TaskOutcome[] = [];
  const matchedWorkers = new Set<number>();

  for (const longTask of longTasks) {
    let outcome = outcomeFromLongTask(longTask);
    const workerIndex = matchWorkerForLongTask(longTask, subagents, matchedWorkers);
    if (workerIndex >= 0) {
      matchedWorkers.add(workerIndex);
      outcome = mergeLongTaskWorkerOutcome(outcome, subagents[workerIndex]!);
    }
    outcomes.push(outcome);
  }

  subagents.forEach((worker, index) => {
    if (!matchedWorkers.has(index)) {
      outcomes.push(outcomeFromWorker(worker));
    }
  });

  for (const pending of input.pendingPermissions ?? []) {
    outcomes.push(outcomeFromPermission(pending));
  }

  for (const queued of input.queuedTurns ?? []) {
    outcomes.push(outcomeFromQueuedTurn(queued));
  }

  if (input.running && input.activeTurnId && !outcomes.some((item) => item.activeTurnId === input.activeTurnId)) {
    outcomes.unshift({
      id: `turn:${input.activeTurnId}`,
      status: "running",
      title: input.activeTurnId,
      detail: input.activePhase || "running",
      activeTurnId: input.activeTurnId,
      signals: [{ kind: "turn", id: input.activeTurnId, status: "running", detail: input.activePhase }],
    });
  }

  return outcomes.length ? outcomes : [idleOutcome()];
}

export function taskOutcomeLabel(status: TaskOutcomeStatus) {
  switch (status) {
    case "running":
      return "Running";
    case "blocked":
      return "Blocked";
    case "ready_for_review":
      return "Ready for review";
    case "merged":
      return "Merged";
    case "failed":
      return "Failed";
    default:
      return "Idle";
  }
}

export function taskOutcomeColor(status: TaskOutcomeStatus) {
  switch (status) {
    case "running":
      return "processing";
    case "blocked":
      return "gold";
    case "ready_for_review":
      return "blue";
    case "merged":
      return "green";
    case "failed":
      return "red";
    default:
      return "default";
  }
}

export function outcomeNeedsReview(outcome: TaskOutcome) {
  return outcome.status === "ready_for_review" || outcome.status === "merged" || outcome.status === "failed" || outcome.status === "blocked";
}

export function outcomeIsActive(outcome: TaskOutcome) {
  return outcome.status === "running" || outcome.status === "blocked";
}

function outcomeFromLongTask(longTask: LongTaskView): TaskOutcome {
  const id = longTask.longtask_id || longTask.workflow_id;
  const status = classifyLongTaskStatus(longTask);
  const title = firstNonBlank(longTask.project, firstStoryTitle(longTask), longTask.description, longTask.workflow_id, longTask.longtask_id, "LongTask");
  return {
    id: `longtask:${id}`,
    status,
    title,
    detail: longTaskDetail(longTask),
    longTask,
    signals: [{ kind: "longtask", id, status: longTask.status || status, detail: longTaskDetail(longTask) }],
  };
}

function outcomeFromWorker(worker: FeedItem): TaskOutcome {
  const id = worker.jobId || worker.id;
  const status = classifyWorkerStatus(worker);
  const title = firstNonBlank(worker.displayTitle, worker.title, worker.objective, worker.jobId, worker.id, "Worker");
  return {
    id: `worker:${id}`,
    status,
    title,
    detail: firstNonBlank(worker.lastMessage, worker.error, worker.summary, worker.body),
    worker,
    signals: [{ kind: "worker", id, status: firstNonBlank(worker.mergeStatus, worker.status, status) }],
  };
}

function outcomeFromPermission(permission: PendingPermission): TaskOutcome {
  const tool = permission.request?.tool_name || "permission";
  return {
    id: `permission:${permission.id}`,
    status: "blocked",
    title: permission.id || tool,
    detail: `${tool} ${permission.request?.action || "approval"}`.trim(),
    permission,
    signals: [{ kind: "permission", id: permission.id, status: "pending", detail: permission.reason }],
  };
}

function outcomeFromQueuedTurn(turn: QueuedTurn): TaskOutcome {
  return {
    id: `queued:${turn.id}`,
    status: "running",
    title: firstNonBlank(turn.summary, turn.id),
    detail: `${turn.mode || "queued"} · ${turn.status || "queued"}`,
    queuedTurn: turn,
    signals: [{ kind: "turn", id: turn.id, status: turn.status || "queued" }],
  };
}

function mergeLongTaskWorkerOutcome(longOutcome: TaskOutcome, worker: FeedItem): TaskOutcome {
  const workerOutcome = outcomeFromWorker(worker);
  const longFailed = longOutcome.status === "failed" || longOutcome.longTask?.status === "error" || longOutcome.longTask?.status === "failed";
  const recovered = longFailed && workerOutcome.status !== "failed";
  return {
    ...longOutcome,
    id: `${longOutcome.id}:${workerOutcome.id}`,
    status: workerOutcome.status === "idle" ? longOutcome.status : workerOutcome.status,
    title: firstNonBlank(longOutcome.title, workerOutcome.title),
    detail: firstNonBlank(workerOutcome.detail, longOutcome.detail),
    recovered,
    worker,
    signals: [...longOutcome.signals, ...workerOutcome.signals],
  };
}

function matchWorkerForLongTask(longTask: LongTaskView, workers: FeedItem[], used: Set<number>) {
  for (let index = 0; index < workers.length; index += 1) {
    if (used.has(index)) {
      continue;
    }
    if (longTaskMatchesWorkerByJobID(longTask, workers[index]?.jobId)) {
      return index;
    }
  }
  for (let index = 0; index < workers.length; index += 1) {
    if (used.has(index)) {
      continue;
    }
    if (longTaskMatchesWorkerByPath(longTask, workers[index]!)) {
      return index;
    }
  }
  return -1;
}

function longTaskMatchesWorkerByJobID(longTask: LongTaskView, jobId?: string) {
  const normalized = jobId?.trim();
  if (!normalized) {
    return false;
  }
  return longTask.stories.some((story) => story.job_id?.trim() === normalized);
}

function longTaskMatchesWorkerByPath(longTask: LongTaskView, worker: FeedItem) {
  return pathTokenSetsOverlap(longTaskPathTokens(longTask), workerPathTokens(worker));
}

function classifyLongTaskStatus(longTask: LongTaskView): TaskOutcomeStatus {
  const status = (longTask.status || "").toLowerCase();
  if (longTask.running > 0 || status === "running") {
    return "running";
  }
  if (longTask.failed > 0 || status === "error" || status === "failed") {
    return "failed";
  }
  if (longTask.pending > 0 || status === "pending") {
    return "running";
  }
  if (status === "completed" || (longTask.total > 0 && longTask.completed === longTask.total)) {
    return "merged";
  }
  return status ? "running" : "idle";
}

function classifyWorkerStatus(worker: FeedItem): TaskOutcomeStatus {
  const status = (worker.status || "").toLowerCase();
  const merge = (worker.mergeStatus || "").toLowerCase();
  if (merge === "merged" || merge === "no_changes") {
    return "merged";
  }
  if (status === "pending_approval") {
    return "blocked";
  }
  if (status === "running" || status === "pending" || status === "resuming") {
    return "running";
  }
  if (status === "completed") {
    return "ready_for_review";
  }
  if (["failed", "error", "cancelled", "canceled", "interrupted", "timeout"].includes(status)) {
    return "failed";
  }
  if (merge && merge !== "merged" && merge !== "no_changes") {
    return "ready_for_review";
  }
  return "idle";
}

function longTaskPathTokens(longTask: LongTaskView) {
  const values = [longTask.project, longTask.description, longTask.workflow_id, longTask.longtask_id];
  for (const story of longTask.stories) {
    values.push(story.title, story.description, story.error, story.result_preview);
    values.push(...(story.acceptance_criteria ?? []));
  }
  return extractPathTokens(values);
}

function workerPathTokens(worker: FeedItem) {
  return extractPathTokens([
    worker.title,
    worker.displayTitle,
    worker.objective,
    worker.prompt,
    worker.body,
    worker.summary,
    worker.error,
    worker.lastMessage,
    ...(worker.writeScope ?? []),
  ]);
}

function extractPathTokens(values: Array<string | undefined>) {
  const out = new Set<string>();
  for (const value of values) {
    for (const field of (value || "").split(/[\s`'"“”‘’()[\]{}<>，,;:]+/u)) {
      const token = normalizePathToken(field);
      if (token) {
        out.add(token);
      }
    }
  }
  return out;
}

function normalizePathToken(value: string) {
  let token = value.trim().replace(/^[.!?]+|[.!?]+$/g, "");
  token = token.replace(/^file:\/\//, "");
  if (!token || token.includes("://") || !token.includes("/")) {
    return "";
  }
  token = token.replace(/\/+/g, "/").replace(/^\.\//, "").replace(/^\/+|\/+$/g, "");
  if (!token || !token.includes("/")) {
    return "";
  }
  return token;
}

function pathTokenSetsOverlap(left: Set<string>, right: Set<string>) {
  for (const leftToken of left) {
    for (const rightToken of right) {
      if (pathsShareSpecificPrefix(leftToken, rightToken)) {
        return true;
      }
    }
  }
  return false;
}

function pathsShareSpecificPrefix(left: string, right: string) {
  if (left === right) {
    return true;
  }
  const [shorter, longer] = left.length <= right.length ? [left, right] : [right, left];
  return longer.startsWith(`${shorter}/`);
}

function longTaskDetail(longTask: LongTaskView) {
  return `${longTask.status || "unknown"} ${longTask.running}/${longTask.total} · pending ${longTask.pending} · failed ${longTask.failed}`;
}

function firstStoryTitle(longTask: LongTaskView) {
  return longTask.stories.find((story) => story.title?.trim())?.title || "";
}

function firstNonBlank(...values: Array<string | undefined>) {
  return values.find((value) => value?.trim())?.trim() || "";
}

function idleOutcome(): TaskOutcome {
  return {
    id: "idle",
    status: "idle",
    title: "No active task",
    detail: "Start a request or run a task to populate the Task Center.",
    signals: [],
  };
}
