import type { AttachmentRef, FeedItem, TodoFeedItem, TodoFeedStats } from "../lib/types";

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
  let openGroupIsArchive = false;
  // Track the max snapshot message index inside the open turn so the turn's
  // fork point (message_index) includes every message of that turn.
  let openGroupMaxIndex = -1;

  const closeGroup = () => {
    if (openGroup) {
      if (openGroupMaxIndex >= 0) {
        openGroup.forkMessageIndex = openGroupMaxIndex + 1;
      }
      result.push(openGroup);
      openGroup = null;
      openGroupIsArchive = false;
      openGroupMaxIndex = -1;
    }
  };

  for (const item of items) {
    if (openGroup && openGroupIsArchive !== Boolean(item.archiveOnly)) {
      closeGroup();
    }
    const mergeable = item.kind === "assistant" || item.kind === "background" || item.kind === "tool" || item.kind === "todo";

    if (!mergeable) {
      closeGroup();
      result.push(item);
      continue;
    }

    if (!openGroup) {
      openGroup = {
        id: item.archiveOnly
          ? `turn:archive:${item.id}`
          : item.turnId
            ? `turn:${item.turnId}`
            : `turn:group:${item.id}`,
        sessionId: item.sessionId,
        kind: "assistant",
        title: "GoDex",
        body: "",
        timestamp: item.timestamp,
        turnId: item.turnId,
        archiveOnly: item.archiveOnly,
        segments: [],
        finalBody: "",
      };
      openGroupIsArchive = Boolean(item.archiveOnly);
      openGroupMaxIndex = item.messageIndex ?? -1;
    } else if (item.messageIndex !== undefined && item.messageIndex > openGroupMaxIndex) {
      openGroupMaxIndex = item.messageIndex;
    }

    const group = openGroup;
    group.segments = group.segments ?? [];
    if (item.kind === "assistant" || item.kind === "background") {
      if (item.body.trim()) {
        const thinking = item.title === "Thinking…";
        group.segments.push({ type: "text", text: item.body, thinking });
        // Track the final result text (used for copy / save-to-note). Reasoning
        // bubbles ("Thinking…") are process, not the answer, so they must not
        // become the turn's finalBody.
        if (!thinking) {
          group.finalBody = item.body;
          group.summary = firstSummaryLine(item.body);
        }
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

export function shortID(id: string) {
  return id.length <= 10 ? id : `${id.slice(0, 10)}...`;
}

export function firstSummaryLine(text: string) {
  return text.trim().split("\n").find(Boolean) ?? "";
}

export function summarizeTool(input: Record<string, unknown> | undefined, output: string, error: string, running: boolean) {
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

export function primaryToolInput(input: Record<string, unknown> | undefined) {
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

export function summarizeToolOutput(output: string) {
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

export function renderTodoList(payload: {
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

export function normalizeTodoItems(items: Array<{ id?: number; content?: string; status?: string; active_form?: string }> | undefined): TodoFeedItem[] {
  return (items ?? [])
    .map((item) => ({
      id: typeof item.id === "number" ? item.id : undefined,
      content: (item.content || "").trim(),
      status: (item.status || "pending").trim(),
      activeForm: (item.active_form || "").trim() || undefined,
    }))
    .filter((item) => item.content);
}

export function normalizeTodoStats(
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

export function parseJSON(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

export function stringValue(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

export function summaryValue(value: unknown) {
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

export function truncateSummary(value: string, limit: number) {
  const text = value.trim();
  return text.length > limit ? `${text.slice(0, Math.max(0, limit - 3))}...` : text;
}

// ACP agents (pi-acp) stream the full bash command as the tool title; cap the
// display name so the status bar / tool row stay single-line even for very
// long commands. The full title/params remain available in the expandable
// tool details.
export function truncateToolTitle(value: string, limit = 48) {
  return truncateSummary(value, limit);
}

export function attachmentSummary(attachments: AttachmentRef[] | undefined) {
  if (!attachments || attachments.length === 0) {
    return "";
  }
  if (attachments.length === 1) {
    return attachments[0].name || attachments[0].path || "1 attachment";
  }
  return `${attachments.length} attachments`;
}
