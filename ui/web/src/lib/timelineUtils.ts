import type { SessionTimelineEntry, RuntimeEvent, SessionContextInspector, FeedItem, DurableSubagentJob, SubagentProgressItem, PendingPermission } from "./types";
import type { PendingSend } from "../store/chat";

export type TimelineFilterState = {
  types: string[];
  q: string;
  jobId: string;
  turnId: string;
  limit: number;
  currentTurnOnly: boolean;
};

export const defaultTimelineTypes = [
  "user_message_accepted",
  "runner_phase_changed",
  "subagent_job_updated",
  "tool_call_started",
  "tool_call_finished",
  "error_raised",
  "turn_completed",
];

export function defaultTimelineFilters(): TimelineFilterState {
  return {
    types: [...defaultTimelineTypes],
    q: "",
    jobId: "",
    turnId: "",
    limit: 50,
    currentTurnOnly: false,
  };
}

export type ContextStatusSummary = {
  text: string;
  tooltip: string;
  budgetPercent: number;
  suggestCompact: boolean;
};

export const timelineEventTypeOptions = [
  "user_message_accepted",
  "assistant_message_completed",
  "tool_call_started",
  "tool_call_finished",
  "warning_raised",
  "error_raised",
  "command_completed",
  "skill_state_changed",
  "history_recall_decision",
  "subagent_job_updated",
  "runner_phase_changed",
  "message_injected",
  "agent_identity_updated",
  "snapshot_ready",
  "turn_completed",
];

export function timelineEventTypeLabel(type: string) {
  return `${timelineEventLabel({ type: type as SessionTimelineEntry["type"], timestamp: "" })} · ${type}`;
}

export function turnStatusColor(status: string) {
  switch (status) {
    case "completed":
      return "green";
    case "running":
    case "canceling":
      return "processing";
    case "pending_approval":
      return "gold";
    case "canceled":
    case "interrupted":
      return "default";
    case "error":
      return "red";
    default:
      return "default";
  }
}

export function formatTurnError(error: string) {
  if (error.toLowerCase().includes("conversation runner reached max turns")) {
    return `${error} · The runner exhausted its turn budget before producing a final answer. Check Timeline for the last phase/tool and consider narrowing the task or raising max_turns.`;
  }
  return error;
}

export function shortTurnId(id: string) {
  return id.length <= 10 ? id : `${id.slice(0, 10)}…`;
}

export function appendTimelineEvent(current: SessionTimelineEntry[], event: RuntimeEvent) {
  if (event.type === "assistant_text_delta") {
    return current;
  }
  const next = [...current, event];
  return next.length <= 80 ? next : next.slice(next.length - 80);
}

export function timelineEventLabel(event: SessionTimelineEntry) {
  switch (event.type) {
    case "user_message_accepted":
      return "User message";
    case "assistant_message_completed":
      return "Assistant reply";
    case "tool_call_started":
      return "Tool started";
    case "tool_call_finished":
      return "Tool finished";
    case "warning_raised":
      return "Warning";
    case "error_raised":
      return "Error";
    case "command_completed":
      return "Command";
    case "skill_state_changed":
      return "Skill";
    case "history_recall_decision":
      return "History recall";
    case "subagent_job_updated":
      return "Subagent";
    case "runner_phase_changed":
      return "Runner phase";
    case "message_injected":
      return String(((event.payload ?? {}) as Record<string, unknown>).mode ?? "") === "steering" ? "Steer" : "Follow-up";
    case "agent_identity_updated":
      return "Agent identity";
    case "snapshot_ready":
      return "Snapshot refreshed";
    case "turn_completed":
      return "Turn completed";
    default:
      return event.type;
  }
}

export function timelineEventSummary(event: SessionTimelineEntry) {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  switch (event.type) {
    case "user_message_accepted":
      return withAppObjectSummary(payload, previewText(String(payload.text ?? "")) || attachmentTimelineSummary(payload.attachments));
    case "assistant_message_completed":
      return previewText(String(payload.text ?? "")) || "Assistant message completed.";
    case "tool_call_started":
      return String(payload.name ?? "tool");
    case "tool_call_finished":
      return payload.error ? `${String(payload.name ?? "tool")} failed: ${String(payload.error)}` : `${String(payload.name ?? "tool")} completed`;
    case "warning_raised":
    case "error_raised":
      return String(payload.message ?? "");
    case "command_completed":
      return payload.error ? `${String(payload.name ?? "command")} failed` : `${String(payload.name ?? "command")} completed`;
    case "subagent_job_updated":
      return subagentTimelineSummary(payload);
    case "runner_phase_changed":
      return [
        payload.actor_kind,
        payload.display_title || payload.objective || payload.actor_id,
        payload.phase,
        runnerIterationLabel(payload),
        payload.tool_name || payload.message,
        payload.recovery_hint,
      ].filter(Boolean).join(" · ");
    case "message_injected":
      return `${String(payload.mode ?? "follow_up")} · ${String(payload.count ?? 0)} injected${payload.remaining ? `, ${String(payload.remaining)} pending` : ""}${payload.summary ? `: ${previewText(String(payload.summary))}` : ""}`;
    case "agent_identity_updated":
      return [payload.name, payload.kind, payload.role].filter(Boolean).join(" · ");
    case "snapshot_ready":
      if (payload.compacted) {
        const before = Number(payload.token_estimate_before ?? 0);
        const after = Number(payload.token_estimate_after ?? 0);
        const reasons = Array.isArray(payload.compression_reasons) ? payload.compression_reasons.join(", ") : "";
        return [
          "Auto compacted context",
          before > 0 || after > 0 ? `${before} → ${after} tokens` : "",
          reasons,
        ].filter(Boolean).join(" · ");
      }
      return "Snapshot refreshed.";
    case "turn_completed":
      return `Status: ${String(payload.status ?? "completed")}`;
    default:
      return "";
  }
}

export function timelineEventFullText(event: SessionTimelineEntry, summary: string) {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  const focused = [
    summary,
    stringFromPayload(payload.message),
    stringFromPayload(payload.error),
    stringFromPayload(payload.result),
    stringFromPayload(payload.text),
    stringFromPayload(payload.summary),
  ].filter(Boolean);
  if (focused.length > 0) {
    return Array.from(new Set(focused)).join("\n");
  }
  try {
    return JSON.stringify(event.payload ?? {}, null, 2);
  } catch {
    return summary;
  }
}

export function withAppObjectSummary(payload: Record<string, unknown>, summary: string) {
  const metadata = payload.metadata as Record<string, unknown> | undefined;
  if (!metadata || metadata.app_object_type !== "note") {
    return summary;
  }
  const title = String(metadata.app_object_title || metadata.app_object_id || "").trim();
  if (!title) {
    return summary;
  }
  return summary ? `${summary} · note: ${title}` : `note: ${title}`;
}

export function subagentTimelineSummary(payload: Record<string, unknown>) {
  const jobID = String(payload.job_id ?? "subagent");
  const phase = String(payload.phase ?? "updated");
  const status = String(payload.status ?? "");
  const message = previewText(String(payload.message ?? payload.error ?? payload.result ?? ""));
  const title = stringFromPayload(payload.display_title);
  const objective = stringFromPayload(payload.objective);
  const identityID = stringFromPayload(payload.identity_id);
  const roleID = stringFromPayload(payload.role_id);
  const roleName = stringFromPayload(payload.role_name);
  const agentType = stringFromPayload(payload.agent_type);
  const capabilityCount = stringArrayFromPayload(payload.capability_summary)?.length ?? 0;
  const maxTurns = numberFromPayload(payload.max_turns);
  const calls = numberFromPayload(payload.model_request_count);
  const tools = numberFromPayload(payload.tool_call_count);
  const lastIteration = numberFromPayload(payload.last_iteration);
  const lastRecoveryHint = stringFromPayload(payload.last_recovery_hint);
  return `${title || shortTurnId(jobID)}${!title && identityID ? ` · ${shortTurnId(identityID)}` : ""}${!title && (roleName || roleID || agentType) ? ` · ${roleName || roleID || agentType}` : ""} ${phase}${status ? ` (${status})` : ""}${objective && !title ? ` · ${previewText(objective)}` : ""}${lastIteration && maxTurns ? ` · ${lastIteration}/${maxTurns}` : maxTurns ? ` · max ${maxTurns}` : ""}${calls ? ` · ${calls} calls` : ""}${tools ? ` · ${tools} tools` : ""}${capabilityCount ? ` · ${capabilityCount} caps` : ""}${lastRecoveryHint ? ` · ${previewText(lastRecoveryHint)}` : ""}${message ? `: ${message}` : ""}`;
}

export function runnerIterationLabel(payload: Record<string, unknown>) {
  const iteration = numberFromPayload(payload.iteration);
  const maxTurns = numberFromPayload(payload.max_turns);
  if (iteration && maxTurns) {
    return `${iteration}/${maxTurns}`;
  }
  if (iteration) {
    return `turn ${iteration}`;
  }
  if (maxTurns) {
    return `max ${maxTurns}`;
  }
  return "";
}

export function buildContextStatusSummary(
  inspector: SessionContextInspector | null,
  timelineItems: SessionTimelineEntry[],
  subagentJobs: FeedItem[],
): ContextStatusSummary {
  const context = inspector?.context;
  const breakdown = context?.token_breakdown;
  const tokens = context?.total_token_estimate ?? context?.token_estimate ?? breakdown?.total ?? 0;
  const threshold = context?.compress_threshold ?? 0;
  const percent = threshold > 0 ? Math.min(100, Math.round((tokens / threshold) * 100)) : 0;
  const mainCalls = timelineItems.filter((event) => {
    if (event.type !== "runner_phase_changed") {
      return false;
    }
    const payload = (event.payload ?? {}) as Record<string, unknown>;
    return stringFromPayload(payload.phase) === "model_request" && stringFromPayload(payload.actor_kind) !== "subagent";
  }).length;
  const subagentCalls = subagentJobs.reduce((sum, item) => sum + (item.modelRequestCount ?? 0), 0);
  // The timeline is capped at 80 events, so the event-based count
  // under-reports long sessions. The session cache stats counter is an
  // exact per-session total of model calls (subagents share the parent
  // session id, so their calls are already included); prefer it whenever
  // the provider reported any usage.
  const sessionCalls = context?.cache_usage?.calls ?? 0;
  const calls = sessionCalls > 0 ? sessionCalls : mainCalls + subagentCalls;
  const messages = context?.message_count ?? 0;
  const tokenLabel = threshold > 0 ? `${formatCompactNumber(tokens)}/${formatCompactNumber(threshold)} ${percent}%` : formatCompactNumber(tokens);
  return {
    text: `ctx ${tokenLabel} · calls ${calls} · msgs ${messages}`,
    tooltip: [
      `Context tokens: ${tokens}${threshold > 0 ? ` / ${threshold} (${percent}%)` : ""}`,
      sessionCalls > 0
        ? `Model requests in this session (from provider usage): ${calls}`
        : `Model requests seen in current timeline window: ${calls}`,
      `Messages in context: ${messages}`,
      context?.suggest_compact ? "Compaction is suggested." : "Compaction is not currently suggested.",
    ].join("\n"),
    budgetPercent: percent,
    suggestCompact: Boolean(context?.suggest_compact),
  };
}

export function collectSubagentJobs(items: SessionTimelineEntry[]): FeedItem[] {
  const jobs = new Map<string, FeedItem>();
  for (const event of items) {
    if (event.type !== "subagent_job_updated") {
      continue;
    }
    const payload = (event.payload ?? {}) as Record<string, unknown>;
    const jobID = stringFromPayload(payload.job_id) || "subagent";
    const agentType = stringFromPayload(payload.agent_type) || "Subagent";
    const roleName = stringFromPayload(payload.role_name);
    const phase = stringFromPayload(payload.phase);
    const status = stringFromPayload(payload.status) || phase || "updated";
    const message = stringFromPayload(payload.message);
    const error = stringFromPayload(payload.error);
    const result = stringFromPayload(payload.result);
    const toolName = stringFromPayload(payload.tool_name);
    const detail = message || error || result || toolName || "Subagent job updated.";
    const displayTitle = stringFromPayload(payload.display_title);
    const objective = stringFromPayload(payload.objective);
    const sequence = numberFromPayload(payload.sequence);
    const existing = jobs.get(jobID);
    const progress = appendTimelineSubagentProgress(existing?.progress, {
      timestamp: stringFromPayload(payload.updated_at) || event.timestamp,
      phase,
      status,
      message,
      toolName,
      error,
      result,
      iteration: numberFromPayload(payload.last_iteration),
      maxTurns: numberFromPayload(payload.max_turns),
      recoveryHint: stringFromPayload(payload.last_recovery_hint),
    });
    jobs.set(jobID, {
      id: `subagent-panel:${jobID}`,
      kind: "subagent",
      title: displayTitle || existing?.displayTitle || `${roleName || agentType} ${shortTurnId(jobID)}`,
      body: detail,
      summary: previewText(detail),
      status,
      jobId: jobID,
      parentTurnId: stringFromPayload(payload.parent_turn_id) || event.turn_id || existing?.parentTurnId,
      identityId: stringFromPayload(payload.identity_id) || existing?.identityId,
      agentType,
      roleId: stringFromPayload(payload.role_id) || existing?.roleId,
      roleName: roleName || existing?.roleName,
      packageName: stringFromPayload(payload.package_name) || existing?.packageName,
      sequence: sequence || existing?.sequence,
      objective: objective || existing?.objective,
      displayTitle: displayTitle || existing?.displayTitle,
      toolNames: stringArrayFromPayload(payload.tool_names) || existing?.toolNames,
      capabilitySummary: stringArrayFromPayload(payload.capability_summary) || existing?.capabilitySummary,
      modelHint: stringFromPayload(payload.model_hint) || existing?.modelHint,
      budgetHint: stringFromPayload(payload.budget_hint) || existing?.budgetHint,
      maxTurns: numberFromPayload(payload.max_turns) || existing?.maxTurns,
      modelRequestCount: numberFromPayload(payload.model_request_count) || existing?.modelRequestCount,
      toolCallCount: numberFromPayload(payload.tool_call_count) || existing?.toolCallCount,
      lastRunnerPhase: stringFromPayload(payload.last_runner_phase) || existing?.lastRunnerPhase,
      lastIteration: numberFromPayload(payload.last_iteration) || existing?.lastIteration,
      lastRecoveryHint: stringFromPayload(payload.last_recovery_hint) || existing?.lastRecoveryHint,
      phase,
      error,
      lastToolName: toolName || existing?.lastToolName,
      lastMessage: message || existing?.lastMessage,
      worktreeDir: stringFromPayload(payload.worktree_dir) || existing?.worktreeDir,
      isolation: stringFromPayload(payload.isolation) || existing?.isolation,
      workspaceOrigin: stringFromPayload(payload.workspace_origin) || existing?.workspaceOrigin,
      gitBranch: stringFromPayload(payload.git_branch) || existing?.gitBranch,
      cleanupState: stringFromPayload(payload.cleanup_state) || existing?.cleanupState,
      writeScope: stringArrayFromPayload(payload.write_scope) || existing?.writeScope,
      mergeStatus: stringFromPayload(payload.merge_status) || existing?.mergeStatus,
      progress,
      expanded: true,
    });
  }
  return [...jobs.values()].sort((left, right) => {
    const leftTime = Date.parse(left.progress?.at(-1)?.timestamp ?? "");
    const rightTime = Date.parse(right.progress?.at(-1)?.timestamp ?? "");
    return (Number.isNaN(rightTime) ? 0 : rightTime) - (Number.isNaN(leftTime) ? 0 : leftTime);
  });
}

export function subagentJobToFeedItem(job: DurableSubagentJob): FeedItem {
  const progress = (job.progress ?? []).map((entry) => ({
    timestamp: entry.timestamp,
    phase: entry.phase,
    status: job.status,
    message: entry.message,
    toolName: entry.tool_name,
    error: entry.error,
    result: entry.result,
    iteration: entry.iteration,
    maxTurns: entry.max_turns,
    model: entry.model,
    recoveryHint: entry.recovery_hint,
  }));
  const detail = job.error || job.last_message || job.result || job.last_tool_name || "Subagent job updated.";
  const title = job.display_title || `${job.role_name || job.agent_type || "Subagent"} ${shortTurnId(job.job_id)}`;
  return {
    id: `subagent-api:${job.job_id}`,
    kind: "subagent",
    title,
    body: detail,
    summary: previewText(detail),
    status: job.status || job.last_phase || "updated",
    jobId: job.job_id,
    parentTurnId: job.parent_turn_id,
    agentType: job.agent_type,
    roleId: job.role_id,
    roleName: job.role_name,
    identity: job.identity,
    identityId: job.identity_id,
    sequence: job.sequence,
    objective: job.objective,
    displayTitle: job.display_title,
    capabilitySummary: job.identity?.capability_summary,
    modelHint: job.identity?.model_hint,
    budgetHint: job.identity?.budget_hint,
    contextBudget: job.context_budget,
    maxTurns: job.max_turns,
    modelRequestCount: job.model_request_count,
    toolCallCount: job.tool_call_count,
    lastRunnerPhase: job.last_runner_phase,
    lastIteration: job.last_iteration,
    lastRecoveryHint: job.last_recovery_hint,
    packageName: job.package_name,
    prompt: job.prompt,
    phase: job.last_phase,
    lastToolName: job.last_tool_name,
    lastMessage: job.last_message,
    toolNames: job.tool_names,
    createdAt: job.created_at,
    updatedAt: job.updated_at,
    finishedAt: job.finished_at,
    error: job.error,
    worktreeDir: job.worktree_dir,
    isolation: job.isolation,
    workspaceOrigin: job.workspace_origin,
    gitBranch: job.git_branch,
    cleanupState: job.cleanup_state,
    writeScope: job.write_scope,
    mergeStatus: job.merge_status,
    progress,
    expanded: true,
  };
}

export function pendingSendToFeedItem(send: PendingSend): FeedItem {
  if (send.kind === "command") {
    const name = send.commandName || "command";
    return {
      id: send.id,
      kind: "command",
      title: `/${name}`,
      body: "",
      timestamp: new Date().toISOString(),
      summary: `Running /${name}…`,
      status: "running",
    };
  }
  return {
    id: send.id,
    kind: "user",
    title: send.sender || "You",
    body: send.text || "",
    timestamp: new Date().toISOString(),
    summary: (send.text || "").trim().split("\n").find(Boolean) ?? "",
    attachments: send.attachments,
    status: "sending",
  };
}

export function mergeChronologicalFeedItems(historyItems: FeedItem[], overlayItems: FeedItem[]) {
  return [...historyItems, ...overlayItems]
    .map((item, index) => ({ item, index, time: Date.parse(item.timestamp ?? "") }))
    .sort((left, right) => {
      const leftHasTime = !Number.isNaN(left.time);
      const rightHasTime = !Number.isNaN(right.time);
      if (leftHasTime && rightHasTime && left.time !== right.time) {
        return left.time - right.time;
      }
      if (leftHasTime !== rightHasTime) {
        return left.index - right.index;
      }
      return left.index - right.index;
    })
    .map((entry) => entry.item);
}

export function mergeSubagentItems(apiItems: FeedItem[], timelineItems: FeedItem[]) {
  const byJob = new Map<string, FeedItem>();
  for (const item of apiItems) {
    byJob.set(item.jobId || item.id, item);
  }
  for (const item of timelineItems) {
    const key = item.jobId || item.id;
    const existing = byJob.get(key);
    if (!existing) {
      byJob.set(key, item);
      continue;
    }
    byJob.set(key, {
      ...existing,
      ...item,
      id: existing.id,
      prompt: existing.prompt || item.prompt,
      createdAt: existing.createdAt || item.createdAt,
      updatedAt: item.updatedAt || existing.updatedAt,
      finishedAt: item.finishedAt || existing.finishedAt,
      parentTurnId: item.parentTurnId || existing.parentTurnId,
      identity: existing.identity || item.identity,
      identityId: existing.identityId || item.identityId,
      sequence: existing.sequence || item.sequence,
      objective: existing.objective || item.objective,
      displayTitle: existing.displayTitle || item.displayTitle,
      title: existing.displayTitle || item.displayTitle || existing.title || item.title,
      toolNames: existing.toolNames || item.toolNames,
      capabilitySummary: existing.capabilitySummary || item.capabilitySummary,
      modelHint: existing.modelHint || item.modelHint,
      budgetHint: existing.budgetHint || item.budgetHint,
      modelRequestCount: item.modelRequestCount || existing.modelRequestCount,
      toolCallCount: item.toolCallCount || existing.toolCallCount,
      lastRunnerPhase: item.lastRunnerPhase || existing.lastRunnerPhase,
      lastIteration: item.lastIteration || existing.lastIteration,
      lastRecoveryHint: item.lastRecoveryHint || existing.lastRecoveryHint,
      progress: mergeSubagentProgress(existing.progress, item.progress),
      expanded: existing.expanded ?? item.expanded,
    });
  }
  return [...byJob.values()].sort((left, right) => {
    const statusDelta = subagentStatusSortRank(left.status) - subagentStatusSortRank(right.status);
    if (statusDelta !== 0) {
      return statusDelta;
    }
    if (left.sequence && right.sequence && left.sequence !== right.sequence) {
      return left.sequence - right.sequence;
    }
    const leftTime = Date.parse(left.progress?.at(-1)?.timestamp ?? left.updatedAt ?? "");
    const rightTime = Date.parse(right.progress?.at(-1)?.timestamp ?? right.updatedAt ?? "");
    return (Number.isNaN(rightTime) ? 0 : rightTime) - (Number.isNaN(leftTime) ? 0 : leftTime);
  });
}

export function subagentStatusSortRank(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "running":
      return 0;
    case "pending":
    case "pending_approval":
      return 1;
    case "interrupted":
    case "error":
    case "failed":
    case "timeout":
      return 2;
    case "completed":
      return 3;
    case "canceled":
      return 4;
    default:
      return 5;
  }
}

export function mergeSubagentProgress(left: SubagentProgressItem[] | undefined, right: SubagentProgressItem[] | undefined) {
  const entries: SubagentProgressItem[] = [];
  for (const entry of [...(left ?? []), ...(right ?? [])]) {
    if (!entries.some((existing) => subagentProgressKey(existing) === subagentProgressKey(entry))) {
      entries.push(entry);
    }
  }
  return entries.slice(-30);
}

export function appendTimelineSubagentProgress(progress: SubagentProgressItem[] | undefined, next: SubagentProgressItem) {
  const key = subagentProgressKey(next);
  const entries = [...(progress ?? [])];
  if (!entries.some((entry) => subagentProgressKey(entry) === key)) {
    entries.push(next);
  }
  return entries.slice(-20);
}

export function subagentProgressKey(item: SubagentProgressItem) {
  return [item.timestamp, item.phase, item.status, item.toolName, item.message, item.error, item.result, item.iteration, item.recoveryHint].filter(Boolean).join("|");
}

export function stringFromPayload(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

export function numberFromPayload(value: unknown) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : 0;
  }
  return 0;
}

export function stringArrayFromPayload(value: unknown) {
  if (!Array.isArray(value)) {
    return undefined;
  }
  return value.map((item) => (typeof item === "string" ? item.trim() : "")).filter(Boolean);
}

export function attachmentTimelineSummary(value: unknown) {
  if (!Array.isArray(value) || value.length === 0) {
    return "";
  }
  return `${value.length} attachment${value.length === 1 ? "" : "s"} uploaded`;
}

export function previewText(value: string, maxLength = 96) {
  const normalized = value.trim().replace(/\s+/g, " ");
  return normalized.length <= maxLength ? normalized : `${normalized.slice(0, Math.max(0, maxLength - 3))}...`;
}

export function formatCompactNumber(value: number) {
  if (!Number.isFinite(value)) {
    return "0";
  }
  if (Math.abs(value) >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}m`;
  }
  if (Math.abs(value) >= 1_000) {
    return `${(value / 1_000).toFixed(1).replace(/\.0$/, "")}k`;
  }
  return String(Math.round(value));
}

export function formatTimelineTime(value?: string) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function permissionRequestTitle(item: PendingPermission) {
  const tool = item.request.tool_name || "tool";
  const action = item.request.action?.trim();
  return action ? `${tool} · ${action}` : tool;
}

export function permissionRequestSummary(item: PendingPermission) {
  const parts: string[] = [];
  if (item.request.command) {
    parts.push(`Command: ${item.request.command}`);
  }
  if (item.request.paths?.length) {
    parts.push(`Paths: ${item.request.paths.join(", ")}`);
  }
  if (item.request.source) {
    parts.push(`Source: ${item.request.source}`);
  }
  return parts.length === 0 ? "Awaiting approval for this tool call." : parts.join(" ");
}
