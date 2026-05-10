import { create } from "zustand";
import type { AttachmentRef, FeedItem, ProtocolBlock, ProtocolMessage, RuntimeEvent, TodoFeedItem, TodoFeedStats } from "../lib/types";

interface ChatState {
  sessionId: string;
  sessionKey: string;
  historyItems: FeedItem[];
  overlayItems: FeedItem[];
  status: string;
  running: boolean;
  currentTurnId: string;
  streamConnected: boolean;
  setSession: (sessionId: string, sessionKey: string) => void;
  syncSnapshot: (messages: ProtocolMessage[], running: boolean, activeTurnId?: string) => void;
  handleEvent: (event: RuntimeEvent) => void;
  setRunningTurn: (turnId: string) => void;
  toggleTool: (id: string) => void;
  setStreamConnected: (connected: boolean) => void;
  reset: () => void;
}

export const useChatStore = create<ChatState>((set, get) => ({
  sessionId: "",
  sessionKey: "",
  historyItems: [],
  overlayItems: [],
  status: "Idle",
  running: false,
  currentTurnId: "",
  streamConnected: false,
  setSession: (sessionId, sessionKey) =>
    set((state) => {
      if (state.sessionId === sessionId) {
        return { sessionId, sessionKey, status: "Connected" };
      }
      return {
        sessionId,
        sessionKey,
        historyItems: [],
        overlayItems: [],
        status: "Connected",
        running: false,
        currentTurnId: "",
      };
    }),
  syncSnapshot: (messages, running, activeTurnId) =>
    set((state) => ({
      historyItems: snapshotToItems(messages, expansionMap([...state.historyItems, ...state.overlayItems])),
      overlayItems: state.overlayItems.filter(
        (item) =>
          (!item.sessionId || item.sessionId === state.sessionId) &&
          (item.kind === "subagent" || item.kind === "command" || item.kind === "warning" || item.kind === "error"),
      ),
      running,
      currentTurnId: running ? activeTurnId || state.currentTurnId : "",
      status: running ? "Running…" : state.status,
    })),
  handleEvent: (event) =>
    set((state) => {
      if (event.session_id && state.sessionId && event.session_id !== state.sessionId) {
        return {};
      }
      const eventSessionId = event.session_id || state.sessionId;
      const overlayItems = [...state.overlayItems];
      let status = state.status;
      let running = state.running;
      let currentTurnId = state.currentTurnId;
      switch (event.type) {
        case "user_message_accepted": {
          const payload = event.payload as { sender?: string; text?: string; attachments?: AttachmentRef[] };
          const summary = payload.text?.trim() ? firstSummaryLine(payload.text) : attachmentSummary(payload.attachments);
          upsertItem(overlayItems, {
            id: `user:${event.turn_id}`,
            kind: "user",
            title: payload.sender || "You",
            body: payload.text || "",
            timestamp: event.timestamp,
            attachments: payload.attachments,
            summary,
          });
          status = "Accepted message";
          running = true;
          currentTurnId = event.turn_id || currentTurnId;
          break;
        }
        case "assistant_text_delta": {
          const payload = event.payload as { text?: string };
          const id = `assistant:${event.turn_id}`;
          const background = !state.running;
          const current = overlayItems.find((item) => item.id === id);
          if (current) {
            current.body += payload.text || "";
            current.summary = firstSummaryLine(current.body);
          } else {
            overlayItems.push({
              id,
              kind: background ? "background" : "assistant",
              title: background ? "Background update" : "GoDex",
              body: payload.text || "",
              timestamp: event.timestamp,
              summary: firstSummaryLine(payload.text || ""),
            });
          }
          status = background ? "Background update received" : "Writing response…";
          break;
        }
        case "tool_call_started": {
          const payload = event.payload as { id?: string; name?: string; input?: Record<string, unknown> };
          if (payload.name === "todo_write") {
            break;
          }
          upsertItem(overlayItems, {
            id: toolItemId(event.turn_id || "", payload.id, payload.name || "tool"),
            kind: "tool",
            title: payload.name || "tool",
            body: "",
            timestamp: event.timestamp,
            summary: summarizeTool(payload.input, "", "", true),
            input: payload.input,
            status: "running",
            expanded: false,
          });
          status = `Running tool ${payload.name || "tool"}`;
          break;
        }
        case "tool_call_finished": {
          const payload = event.payload as { id?: string; name?: string; input?: Record<string, unknown>; output?: string; error?: string };
          if (payload.name === "todo_write" && !payload.error) {
            break;
          }
          upsertItem(overlayItems, {
            id: toolItemId(event.turn_id || "", payload.id, payload.name || "tool"),
            kind: "tool",
            title: payload.name || "tool",
            body: "",
            timestamp: event.timestamp,
            summary: summarizeTool(payload.input, payload.output || "", payload.error || "", false),
            input: payload.input,
            output: payload.output,
            error: payload.error,
            status: payload.error ? "failed" : "finished",
            expanded: false,
          });
          status = payload.error ? `Tool failed: ${payload.name || "tool"}` : `Finished tool ${payload.name || "tool"}`;
          break;
        }
        case "todo_list_updated": {
          const payload = event.payload as {
            items?: Array<{ id?: number; content?: string; status?: string; active_form?: string }>;
            total?: number;
            completed?: number;
            in_progress?: number;
            pending?: number;
          };
          const todoItems = normalizeTodoItems(payload.items);
          const todoStats = normalizeTodoStats(payload, todoItems);
          const body = renderTodoList(payload);
          upsertItem(overlayItems, {
            id: `todo:${event.turn_id || "current"}`,
            kind: "todo",
            title: "Todo list",
            body,
            timestamp: event.timestamp,
            summary: `${todoStats.completed}/${todoStats.total} completed`,
            status: "updated",
            todoItems,
            todoStats,
          });
          status = `Todo list ${todoStats.completed}/${todoStats.total} completed`;
          break;
        }
        case "command_completed": {
          const payload = event.payload as {
            name?: string;
            output?: string;
            error?: string;
            dispatch_mode?: string;
            dispatch_invocation?: string;
            dispatch_status?: string;
            dispatch_error?: string;
            dispatched_turn_id?: string;
            dispatched_job_id?: string;
          };
          const details = [
            payload.output,
            payload.dispatch_mode ? `Dispatch: ${payload.dispatch_mode}${payload.dispatch_invocation ? ` (${payload.dispatch_invocation})` : ""}` : "",
            payload.dispatch_status ? `Status: ${payload.dispatch_status}` : "",
            payload.dispatch_error ? `Dispatch error: ${payload.dispatch_error}` : "",
            payload.dispatched_turn_id ? `Queued turn: ${payload.dispatched_turn_id}` : "",
            payload.dispatched_job_id ? `Started subagent: ${payload.dispatched_job_id}` : "",
          ].filter(Boolean).join("\n");
          overlayItems.push({
            id: `command:${event.turn_id}:${payload.name}`,
            kind: payload.error ? "error" : "command",
            title: payload.error ? "Command error" : `/${payload.name || "command"}`,
            body: payload.error || details || "Command completed.",
            timestamp: event.timestamp,
            summary: firstSummaryLine(payload.error || details || "Command completed."),
          });
          status = payload.error ? "Command failed" : `/${payload.name || "command"} completed`;
          running = false;
          break;
        }
        case "subagent_job_updated": {
          const payload = event.payload as {
            job_id?: string;
            parent_turn_id?: string;
            identity_id?: string;
            agent_type?: string;
            role_id?: string;
            role_name?: string;
            package_name?: string;
            status?: string;
            phase?: string;
            message?: string;
            tool_name?: string;
            error?: string;
            result?: string;
            tool_names?: string[];
            capability_summary?: string[];
            model_hint?: string;
            budget_hint?: string;
            write_scope?: string[];
            worktree_dir?: string;
            isolation?: string;
            workspace_origin?: string;
            git_branch?: string;
            cleanup_state?: string;
            merge_status?: string;
            updated_at?: string;
          };
          const jobID = payload.job_id || "subagent";
          const detail = payload.message || payload.error || payload.result || payload.tool_name || "Subagent job updated.";
          const existing = overlayItems.find((item) => item.id === `subagent:${jobID}`);
          const progress = appendSubagentProgress(existing?.progress, {
            timestamp: payload.updated_at || event.timestamp,
            phase: payload.phase,
            status: payload.status,
            message: payload.message,
            toolName: payload.tool_name,
            error: payload.error,
            result: payload.result,
          });
          upsertItem(overlayItems, {
            id: `subagent:${jobID}`,
            kind: "subagent",
            title: `${payload.role_name || payload.agent_type || "Subagent"} ${shortID(jobID)}`,
            body: detail,
            timestamp: payload.updated_at || event.timestamp,
            summary: firstSummaryLine(detail),
            status: payload.status || payload.phase || "updated",
            jobId: jobID,
            parentTurnId: payload.parent_turn_id || event.turn_id || existing?.parentTurnId,
            identityId: payload.identity_id || existing?.identityId,
            agentType: payload.agent_type || existing?.agentType,
            roleId: payload.role_id || existing?.roleId,
            roleName: payload.role_name || existing?.roleName,
            packageName: payload.package_name || existing?.packageName,
            phase: payload.phase,
            error: payload.error,
            toolNames: Array.isArray(payload.tool_names) ? payload.tool_names : existing?.toolNames,
            capabilitySummary: Array.isArray(payload.capability_summary) ? payload.capability_summary : existing?.capabilitySummary,
            modelHint: payload.model_hint || existing?.modelHint,
            budgetHint: payload.budget_hint || existing?.budgetHint,
            lastToolName: payload.tool_name || existing?.lastToolName,
            lastMessage: payload.message || existing?.lastMessage,
            worktreeDir: payload.worktree_dir || existing?.worktreeDir,
            isolation: payload.isolation || existing?.isolation,
            workspaceOrigin: payload.workspace_origin || existing?.workspaceOrigin,
            gitBranch: payload.git_branch || existing?.gitBranch,
            cleanupState: payload.cleanup_state || existing?.cleanupState,
            writeScope: Array.isArray(payload.write_scope) ? payload.write_scope : existing?.writeScope,
            mergeStatus: payload.merge_status || existing?.mergeStatus,
            progress,
            expanded: existing?.expanded ?? false,
          });
          status = `Subagent ${payload.phase || payload.status || "updated"}`;
          break;
        }
        case "warning_raised": {
          const payload = event.payload as { message?: string };
          overlayItems.push({
            id: `warning:${event.turn_id}:${payload.message}`,
            kind: "warning",
            title: "Warning",
            body: payload.message || "",
            timestamp: event.timestamp,
            summary: firstSummaryLine(payload.message || ""),
          });
          status = "Warning received";
          break;
        }
        case "error_raised": {
          const payload = event.payload as { message?: string };
          overlayItems.push({
            id: `error:${event.turn_id}:${payload.message}`,
            kind: "error",
            title: "Error",
            body: payload.message || "",
            timestamp: event.timestamp,
            summary: firstSummaryLine(payload.message || ""),
          });
          status = "Error received";
          running = false;
          currentTurnId = "";
          break;
        }
        case "turn_completed": {
          const payload = event.payload as { status?: string };
          status = `Turn ${payload.status || "completed"}`;
          running = false;
          if (!event.turn_id || event.turn_id === currentTurnId) {
            currentTurnId = "";
          }
          break;
        }
        default:
          break;
      }
      return {
        overlayItems: overlayItems.map((item) => (item.sessionId ? item : { ...item, sessionId: eventSessionId })),
        status,
        running,
        currentTurnId,
      };
    }),
  setRunningTurn: (currentTurnId) => set({ currentTurnId, running: true, status: "Running…" }),
  toggleTool: (id) =>
    set((state) => ({
      historyItems: state.historyItems.map((item) => (item.id === id ? { ...item, expanded: !item.expanded } : item)),
      overlayItems: state.overlayItems.map((item) => (item.id === id ? { ...item, expanded: !item.expanded } : item)),
    })),
  setStreamConnected: (streamConnected) => set({ streamConnected }),
  reset: () =>
    set({
      sessionId: "",
      sessionKey: "",
      historyItems: [],
      overlayItems: [],
      status: "Idle",
      running: false,
      currentTurnId: "",
      streamConnected: false,
    }),
}));

function snapshotToItems(messages: ProtocolMessage[], expanded: Record<string, boolean>): FeedItem[] {
  const items: FeedItem[] = [];
  const toolIndices = new Map<string, number>();

  messages.forEach((msg, messageIndex) => {
    const text = msg.metadata?.text ?? msg.content.filter((block) => block.type === "text").map((block) => block.text || "").join("");
    const attachments = msg.metadata?.attachments ?? [];
    if (text.trim() || attachments.length > 0) {
      const kind = msg.role === "assistant" ? (msg.metadata?.kind === "background" ? "background" : "assistant") : "user";
      items.push({
        id: `message:${messageIndex}:${kind}`,
        kind,
        title: msg.role === "assistant" ? (msg.metadata?.kind === "background" ? "Background update" : "GoDex") : "You",
        body: text,
        timestamp: msg.metadata?.timestamp,
        attachments,
        summary: text.trim() ? firstSummaryLine(text) : attachmentSummary(attachments),
      });
    }

    msg.content.forEach((block, blockIndex) => {
      if (block.type === "tool_use") {
        const item: FeedItem = {
          id: toolSnapshotId(messageIndex, blockIndex, block),
          kind: "tool",
          title: block.name || "tool",
          body: "",
          timestamp: msg.metadata?.timestamp,
          summary: summarizeTool(block.input, "", "", true),
          input: block.input,
          status: "running",
          expanded: expanded[toolSnapshotId(messageIndex, blockIndex, block)] ?? false,
        };
        items.push(item);
        if (block.id) {
          toolIndices.set(block.id, items.length - 1);
        }
      }
      if (block.type === "tool_result") {
        const idx = block.tool_use_id ? toolIndices.get(block.tool_use_id) : undefined;
        if (idx !== undefined) {
          const current = items[idx];
          items[idx] = {
            ...current,
            summary: summarizeTool(current.input, block.content || "", "", false),
            output: block.content,
            status: "finished",
            expanded: expanded[current.id] ?? false,
          };
        } else {
          items.push({
            id: `tool-result:${messageIndex}:${blockIndex}`,
            kind: "tool",
            title: "tool result",
            body: "",
            timestamp: msg.metadata?.timestamp,
            summary: firstSummaryLine(block.content || ""),
            output: block.content,
            status: "finished",
            expanded: expanded[`tool-result:${messageIndex}:${blockIndex}`] ?? false,
          });
        }
      }
    });
  });

  return items;
}

function upsertItem(items: FeedItem[], item: FeedItem) {
  const index = items.findIndex((candidate) => candidate.id === item.id);
  if (index >= 0) {
    items[index] = { ...items[index], ...item, expanded: items[index].expanded ?? item.expanded };
    return;
  }
  items.push(item);
}

function expansionMap(items: FeedItem[]) {
  return items.reduce<Record<string, boolean>>((acc, item) => {
    if (item.kind === "tool" || item.kind === "subagent") {
      acc[item.id] = !!item.expanded;
    }
    return acc;
  }, {});
}

function appendSubagentProgress(progress: FeedItem["progress"], next: NonNullable<FeedItem["progress"]>[number]) {
  const normalized = {
    timestamp: next.timestamp,
    phase: next.phase,
    status: next.status,
    message: next.message,
    toolName: next.toolName,
    error: next.error,
    result: next.result,
  };
  const key = subagentProgressKey(normalized);
  const items = [...(progress ?? [])];
  if (items.some((item) => subagentProgressKey(item) === key)) {
    return items;
  }
  items.push(normalized);
  return items.slice(-12);
}

function subagentProgressKey(item: NonNullable<FeedItem["progress"]>[number]) {
  return [item.timestamp, item.phase, item.status, item.toolName, item.message, item.error, item.result].filter(Boolean).join("|");
}

function toolSnapshotId(messageIndex: number, blockIndex: number, block: ProtocolBlock) {
  return block.id ? `tool:${block.id}` : `tool:${messageIndex}:${blockIndex}:${block.name ?? "tool"}`;
}

function toolItemId(turnId: string, id: string | undefined, name: string) {
  return id ? `tool:${id}` : `tool:${turnId}:${name}`;
}

function shortID(id: string) {
  return id.length <= 10 ? id : `${id.slice(0, 10)}...`;
}

function firstSummaryLine(text: string) {
  return text.trim().split("\n").find(Boolean) ?? "";
}

function summarizeTool(input: Record<string, unknown> | undefined, output: string, error: string, running: boolean) {
  const primary = primaryToolInput(input);
  if (typeof primary === "string" && primary.trim()) {
    return truncateSummary(primary, 120);
  }
  if (error.trim()) {
    return truncateSummary(firstSummaryLine(error), 120);
  }
  if (output.trim()) {
    return summarizeToolOutput(output);
  }
  return running ? "Working..." : "Completed.";
}

function primaryToolInput(input: Record<string, unknown> | undefined) {
  if (!input) {
    return undefined;
  }
  const pattern = stringValue(input.pattern);
  const root = stringValue(input.root);
  if (pattern) {
    return root ? `${pattern} in ${root}` : pattern;
  }
  return stringValue(input.path) ?? stringValue(input.command) ?? stringValue(input.query) ?? stringValue(input.url) ?? stringValue(input.name);
}

function summarizeToolOutput(output: string) {
  const parsed = parseJSON(output);
  if (Array.isArray(parsed)) {
    return `${parsed.length} item${parsed.length === 1 ? "" : "s"}.`;
  }
  if (isRecord(parsed)) {
    const matches = parsed.matches;
    if (Array.isArray(matches)) {
      const root = stringValue(parsed.root);
      const suffix = parsed.truncated === true ? " (truncated)" : "";
      return `${matches.length} match${matches.length === 1 ? "" : "es"}${root ? ` in ${root}` : ""}${suffix}.`;
    }
    const parts = Object.entries(parsed)
      .slice(0, 4)
      .map(([key, value]) => `${key}: ${summaryValue(value)}`)
      .filter(Boolean);
    if (parts.length > 0) {
      return truncateSummary(parts.join(" · "), 120);
    }
  }
  return truncateSummary(firstSummaryLine(output), 120);
}

function renderTodoList(payload: {
  items?: Array<{ content?: string; status?: string; active_form?: string }>;
  total?: number;
  completed?: number;
}) {
  const items = payload.items ?? [];
  const total = payload.total ?? items.length;
  const completed = payload.completed ?? items.filter((item) => item.status === "completed").length;
  const lines = [`Todo list (${completed}/${total} completed)`];
  for (const item of items) {
    const content = (item.content || "").trim();
    if (!content) {
      continue;
    }
    const status = (item.status || "").trim();
    const marker = status === "completed" ? "[x]" : status === "in_progress" ? "[>]" : status === "pending" ? "[ ]" : "[?]";
    const suffix = status === "in_progress" && item.active_form ? ` <- ${item.active_form}` : "";
    lines.push(`${marker} ${content}${suffix}`);
  }
  return lines.join("\n");
}

function normalizeTodoItems(items: Array<{ id?: number; content?: string; status?: string; active_form?: string }> | undefined): TodoFeedItem[] {
  return (items ?? [])
    .map((item) => ({
      id: typeof item.id === "number" ? item.id : undefined,
      content: (item.content || "").trim(),
      status: (item.status || "pending").trim(),
      activeForm: (item.active_form || "").trim() || undefined,
    }))
    .filter((item) => item.content);
}

function normalizeTodoStats(
  payload: { total?: number; completed?: number; in_progress?: number; pending?: number },
  items: TodoFeedItem[],
): TodoFeedStats {
  return {
    total: payload.total ?? items.length,
    completed: payload.completed ?? items.filter((item) => item.status === "completed").length,
    inProgress: payload.in_progress ?? items.filter((item) => item.status === "in_progress").length,
    pending: payload.pending ?? items.filter((item) => item.status === "pending").length,
  };
}

function parseJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function stringValue(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function summaryValue(value: unknown) {
  if (Array.isArray(value)) {
    return `${value.length} item${value.length === 1 ? "" : "s"}`;
  }
  if (isRecord(value)) {
    return "object";
  }
  if (typeof value === "string") {
    return truncateSummary(value, 48);
  }
  return String(value);
}

function truncateSummary(value: string, limit: number) {
  const text = value.trim();
  return text.length > limit ? `${text.slice(0, Math.max(0, limit - 3))}...` : text;
}

function attachmentSummary(attachments: AttachmentRef[] | undefined) {
  if (!attachments || attachments.length === 0) {
    return "";
  }
  if (attachments.length === 1) {
    return attachments[0].name || attachments[0].path || "1 attachment";
  }
  return `${attachments.length} attachments`;
}
