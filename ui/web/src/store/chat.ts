import { create } from "zustand";
import type { AttachmentRef, FeedItem, ProtocolBlock, ProtocolMessage, RuntimeEvent, TodoFeedItem, TodoFeedStats } from "../lib/types";

/**
 * A send that is in flight but not yet reflected in the server
 * snapshot: an optimistic user message or a running slash command
 * (e.g. /compact). Rendered as a loading placeholder until the
 * matching SSE event (user_message_accepted / command_completed)
 * or the next snapshot replaces it with the real item.
 */
export interface PendingSend {
  id: string;
  kind: "user" | "command";
  text?: string;
  commandName?: string;
  attachments?: AttachmentRef[];
  sender?: string;
}

interface ChatState {
  sessionId: string;
  sessionKey: string;
  historyItems: FeedItem[];
  overlayItems: FeedItem[];
  pendingSends: PendingSend[];
  status: string;
  running: boolean;
  currentTurnId: string;
  streamConnected: boolean;
  setSession: (sessionId: string, sessionKey: string) => void;
  syncSnapshot: (messages: ProtocolMessage[], running: boolean, activeTurnId?: string) => void;
  handleEvent: (event: RuntimeEvent) => void;
  setRunningTurn: (turnId: string) => void;
  addPendingSend: (send: PendingSend) => void;
  removePendingSend: (id: string) => void;
  toggleTool: (id: string) => void;
  setStreamConnected: (connected: boolean) => void;
  reset: () => void;
}

export const useChatStore = create<ChatState>((set, get) => ({
  sessionId: "",
  sessionKey: "",
  historyItems: [],
  overlayItems: [],
  pendingSends: [],
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
        pendingSends: [],
        status: "Connected",
        running: false,
        currentTurnId: "",
      };
    }),
  syncSnapshot: (messages, running, activeTurnId) =>
    set((state) => {
      const historyItems = snapshotToItems(messages, expansionMap([...state.historyItems, ...state.overlayItems]));
      // Drop optimistic user placeholders whose text already appears in
      // the server snapshot (backstop for a missed user_message_accepted
      // event, e.g. while the SSE stream is reconnecting). Command
      // placeholders are resolved by command_completed instead.
      const userTexts = new Set(historyItems.filter((item) => item.kind === "user").map((item) => item.body));
      const pendingSends = state.pendingSends.filter((send) => !(send.kind === "user" && send.text && userTexts.has(send.text)));
      return {
        historyItems,
        overlayItems: state.overlayItems.filter(
          (item) =>
            (!item.sessionId || item.sessionId === state.sessionId) &&
            (item.kind === "subagent" || item.kind === "command" || item.kind === "warning" || item.kind === "error"),
        ),
        pendingSends,
        running,
        currentTurnId: running ? activeTurnId || state.currentTurnId : "",
        status: running ? "Running…" : state.status,
      };
    }),
  handleEvent: (event) =>
    set((state) => {
      if (event.session_id && state.sessionId && event.session_id !== state.sessionId) {
        return {};
      }
      const eventSessionId = event.session_id || state.sessionId;
      const overlayItems = [...state.overlayItems];
      const pendingSends = [...state.pendingSends];
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
          // The server accepted the message: drop the matching
          // optimistic placeholder (first pending user send) so it
          // is replaced by the real item instead of duplicated.
          const pendingUserIndex = pendingSends.findIndex((send) => send.kind === "user");
          if (pendingUserIndex >= 0) {
            pendingSends.splice(pendingUserIndex, 1);
          }
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
            current.turnId = current.turnId ?? (event.turn_id || undefined);
          } else {
            overlayItems.push({
              id,
              kind: background ? "background" : "assistant",
              title: background ? "Background update" : "GoDex",
              body: payload.text || "",
              timestamp: event.timestamp,
              summary: firstSummaryLine(payload.text || ""),
              turnId: event.turn_id || undefined,
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
            turnId: event.turn_id || undefined,
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
            turnId: event.turn_id || undefined,
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
            turnId: event.turn_id || undefined,
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
          // Drop the running placeholder for this command; the
          // completed item below replaces it.
          const commandName = payload.name;
          if (commandName) {
            const pendingCommandIndex = pendingSends.findIndex((send) => send.kind === "command" && send.commandName === commandName);
            if (pendingCommandIndex >= 0) {
              pendingSends.splice(pendingCommandIndex, 1);
            }
          }
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
          // The turn failed: clear any optimistic placeholders that
          // were waiting for user_message_accepted / command_completed.
          pendingSends.length = 0;
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
        pendingSends,
        status,
        running,
        currentTurnId,
      };
    }),
  setRunningTurn: (currentTurnId) => set({ currentTurnId, running: true, status: "Running…" }),
  addPendingSend: (send) => set((state) => ({ pendingSends: [...state.pendingSends, send] })),
  removePendingSend: (id) => set((state) => ({ pendingSends: state.pendingSends.filter((send) => send.id !== id) })),
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
      pendingSends: [],
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
    // Synthesize a turnId for assistant messages so their text + tool blocks
    // group together in the V2 feed layout. History snapshots don't carry
    // backend turn ids, so the message index is the grouping boundary.
    const syntheticTurnId = msg.role === "assistant" ? `msg-${messageIndex}` : undefined;
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
        turnId: syntheticTurnId,
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
          turnId: syntheticTurnId,
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
            turnId: syntheticTurnId,
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

/**
 * Group a flat chronological feed into per-turn items for the Chat V2 layout.
 *
 * Rules:
 * - CONSECUTIVE assistant/background, tool and todo items merge into a single
 *   assistant "turn" message whose `segments` preserve chronological order
 *   (thinking text, tool calls, todo updates interleaved). The merge is based
 *   on adjacency, not turnId, because a single logical turn can span several
 *   assistant messages in history snapshots (thinking / tool-result / answer).
 * - user, subagent, command, warning and error items always stay standalone
 *   and close any open turn.
 * - the group's `finalBody` holds the LAST assistant text (the result); copy
 *   and save-to-note act only on it, not on the process.
 *
 * Pure: does not mutate its input items.
 */
export function groupFeedItemsIntoTurns(items: FeedItem[]): FeedItem[] {
  const result: FeedItem[] = [];
  let openGroup: FeedItem | null = null;

  const closeGroup = () => {
    if (openGroup) {
      result.push(openGroup);
      openGroup = null;
    }
  };

  for (const item of items) {
    const mergeable = item.kind === "assistant" || item.kind === "background" || item.kind === "tool" || item.kind === "todo";

    if (!mergeable) {
      closeGroup();
      result.push(item);
      continue;
    }

    if (!openGroup) {
      openGroup = {
        id: item.turnId ? `turn:${item.turnId}` : `turn:group:${item.id}`,
        sessionId: item.sessionId,
        kind: "assistant",
        title: "GoDex",
        body: "",
        timestamp: item.timestamp,
        turnId: item.turnId,
        segments: [],
        finalBody: "",
      };
    }

    const group = openGroup;
    group.segments = group.segments ?? [];
    if (item.kind === "assistant" || item.kind === "background") {
      if (item.body.trim()) {
        group.segments.push({ type: "text", text: item.body });
        // Track the final result text (used for copy / save-to-note).
        group.finalBody = item.body;
        group.summary = firstSummaryLine(item.body);
      }
      group.timestamp = item.timestamp ?? group.timestamp;
      if (item.attachments?.length) {
        group.attachments = [...(group.attachments ?? []), ...item.attachments];
      }
    } else if (item.kind === "tool") {
      group.segments.push({ type: "tool", item });
      group.timestamp = item.timestamp ?? group.timestamp;
    } else if (item.kind === "todo") {
      group.segments.push({ type: "todo", item });
      group.timestamp = item.timestamp ?? group.timestamp;
    }
  }
  closeGroup();
  return result;
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
