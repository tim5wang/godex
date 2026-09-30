import type { FeedItem, PendingPermission, SessionTimelineEntry, SubagentProgressItem } from "./types";

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

// collectToolCalls rebuilds tool feed items from persisted timeline events
// (tool_call_started / tool_call_finished). Tool events stream into the live
// overlay while a turn runs, but the overlay is transient: after a reload the
// feed is rebuilt from messages only, so without this the ACP tool logs
// disappear and the conversation shows only the user input and the final
// assistant output. The tool call id is the stable key (same as the live
// overlay's toolItemId) so a later merge can prefer the live overlay item.
/**
 * Re-binds snapshot assistant messages to their REAL backend turn id so a
 * re-entered conversation groups each turn's text with its tool log.
 *
 * Snapshot messages (from the server state) carry a synthetic `msg-N` turnId
 * because the persisted state does not record the backend turn id. The timeline
 * DOES: each `assistant_message_completed` event is emitted with the real
 * `turn_id` and the full assistant text. The tool logs rebuilt from the
 * timeline also use that real `turn_id`.
 *
 * Without this, on re-entry the assistant text (turnId `msg-N`) and its tool
 * log (turnId `turn-...`) never match the same turn, so `groupFeedItemsIntoTurns`
 * splits a single turn into a text block and a separate tool block (the "two big
 * segments" ordering bug). Aligning the text to the tool's real turn_id keeps
 * them in one group, ordered by timestamp.
 *
 * Matching is by exact text so it never rewrites a message that has no
 * timeline counterpart (e.g. a turn whose events rolled out of the recorder
 * window); those keep their snapshot turnId and remain ordered by timestamp.
 */
export function alignAssistantTextTurnIds(historyItems: FeedItem[], timelineItems: SessionTimelineEntry[]): FeedItem[] {
  const byText = new Map<string, string>();
  for (const event of timelineItems) {
    if (event.type !== "assistant_message_completed") {
      continue;
    }
    const payload = (event.payload ?? {}) as { text?: string };
    const text = stringFromPayload(payload.text);
    const realTurnId = event.turn_id || "";
    if (text && realTurnId) {
      // Last write wins if two events share the same text (unlikely); keep the
      // first real turn id encountered so ordering stays deterministic.
      if (!byText.has(text)) {
        byText.set(text, realTurnId);
      }
    }
  }
  if (byText.size === 0) {
    return historyItems;
  }
  return historyItems.map((item) => {
    if (item.kind !== "assistant" || !item.body || !item.turnId || item.turnId.startsWith("turn-")) {
      return item;
    }
    const realTurnId = byText.get(item.body);
    if (!realTurnId) {
      return item;
    }
    return { ...item, turnId: realTurnId };
  });
}

export function collectToolCalls(items: SessionTimelineEntry[]): FeedItem[] {
  const tools = new Map<string, FeedItem>();
  for (const event of items) {
    if (event.type !== "tool_call_started" && event.type !== "tool_call_finished") {
      continue;
    }
    const payload = (event.payload ?? {}) as { id?: string; name?: string; input?: Record<string, unknown> };
    const id = stringFromPayload(payload.id);
    const turnId = event.turn_id || "";
    const name = stringFromPayload(payload.name) || "tool";
    const key = id ? `tool:${id}` : `tool:${turnId}:${name}`;
    const input = (payload.input ?? {}) as Record<string, unknown>;
    const inputSummary = previewText(JSON.stringify(input));
    const existing = tools.get(key);
    if (event.type === "tool_call_started") {
      tools.set(key, {
        id: key,
        kind: "tool",
        title: name,
        body: "",
        timestamp: event.timestamp,
        summary: inputSummary,
        input,
        status: "running",
        expanded: false,
        turnId: turnId || undefined,
      });
    } else {
      // A tool_call_finished may arrive without a preceding started event in
      // the window: the timeline is capped (TIMELINE_WINDOW_LIMIT) and pi-acp
      // turns emit hundreds of text/thinking deltas that can evict the
      // earlier started events. Create the row from the finished event alone
      // so the tool log survives re-entry; merge input when both exist.
      const base = existing ?? {
        id: key,
        kind: "tool",
        title: name,
        body: "",
        timestamp: event.timestamp,
        summary: inputSummary,
        input,
        status: "finished",
        expanded: false,
        turnId: turnId || undefined,
      };
      tools.set(key, {
        ...base,
        title: existing?.title || name,
        timestamp: event.timestamp,
        summary: inputSummary || existing?.summary,
        input: input || existing?.input,
        status: "finished",
        turnId: turnId || existing?.turnId,
      });
    }
  }
  return [...tools.values()].sort((left, right) => {
    const leftTime = Date.parse(left.timestamp ?? "");
    const rightTime = Date.parse(right.timestamp ?? "");
    if (!Number.isNaN(leftTime) && !Number.isNaN(rightTime)) {
      return leftTime - rightTime;
    }
    return 0;
  });
}

/**
 * Rebuilds the reasoning ("Thinking…") segments of a conversation from the
 * persisted timeline. The ACP harness streams `assistant_thinking_delta`
 * events between tool calls; live they are shown as transient "Thinking…"
 * overlay bubbles. Those deltas are now persisted in the timeline (they are
 * recordable events), so on re-entry (overlay cleared) this function
 * reconstructs the same per-turn thinking bubbles and lets
 * `mergeChronologicalFeedItems` interleave them with the rebuilt tool log by
 * timestamp — restoring the "thinking ↔ tool" alternation the user sees live.
 *
 * Consecutive deltas of the same turn merge into one bubble (mirroring the
 * live overlay's sameStream logic); a delta that follows a tool call starts a
 * new bubble.
 */
export function collectThinkingDeltas(items: SessionTimelineEntry[]): FeedItem[] {
  const ordered = [...items].sort((a, b) => Date.parse(a.timestamp ?? "") - Date.parse(b.timestamp ?? ""));
  const result: FeedItem[] = [];
  let open: FeedItem | null = null;
  let openTurnId = "";
  let toolSinceLastThinking = false;
  for (const event of ordered) {
    if (event.type === "tool_call_started" || event.type === "tool_call_finished") {
      // A tool call between thinking deltas closes the open bubble so the next
      // reasoning segment renders AFTER that tool (live sameStream semantics:
      // a tool item between two thinking deltas starts a new bubble).
      toolSinceLastThinking = true;
      open = null;
      continue;
    }
    if (event.type !== "assistant_thinking_delta") {
      continue;
    }
    const payload = (event.payload ?? {}) as { text?: string };
    // Use the raw text (NOT trimmed) so consecutive chunks keep their original
    // spacing when merged (mirrors the live overlay's `payload.text || ""`).
    const text = typeof payload.text === "string" ? payload.text : "";
    if (!text.trim()) {
      continue;
    }
    const turnId = event.turn_id || "";
    if (open && !toolSinceLastThinking && openTurnId === turnId) {
      // Same turn, still reasoning (no tool between): merge into the open
      // bubble (live sameStream semantics).
      open.body += text;
      open.summary = previewText(open.body);
      open.timestamp = event.timestamp;
    } else {
      const body = text.trim();
      open = {
        id: `thinking:${turnId || "current"}:${result.length}`,
        kind: "background",
        title: "Thinking…",
        body,
        timestamp: event.timestamp,
        summary: previewText(body),
        turnId: turnId || undefined,
      };
      result.push(open);
      openTurnId = turnId;
      toolSinceLastThinking = false;
    }
  }
  return result;
}

/**
 * Rebuilds the short process text that the model streams BETWEEN tool calls
 * ("收到两个问题。先诊断…" before each tool), mirroring collectThinkingDeltas:
 * `assistant_text_delta` events are persisted in the timeline, so on re-entry
 * (overlay cleared) this function reconstructs per-turn assistant text
 * segments split at tool boundaries, letting `mergeChronologicalFeedItems`
 * interleave them with the rebuilt tool log by timestamp.
 *
 * The FINAL consolidated answer is intentionally not rebuilt here: it comes
 * from `assistant_message_completed` / the snapshot message, and ChatPage
 * dedupes these process segments against that final text (see
 * alignAssistantTextTurnIds + the per-turn skip in the feed merge).
 */
export function collectTextDeltas(items: SessionTimelineEntry[]): FeedItem[] {
  const ordered = [...items].sort((a, b) => Date.parse(a.timestamp ?? "") - Date.parse(b.timestamp ?? ""));
  const result: FeedItem[] = [];
  let open: FeedItem | null = null;
  let openTurnId = "";
  let toolSinceLastText = false;
  for (const event of ordered) {
    if (event.type === "tool_call_started" || event.type === "tool_call_finished") {
      // A tool call between text deltas closes the open segment so the next
      // process text renders AFTER that tool (live sameStream semantics).
      toolSinceLastText = true;
      open = null;
      continue;
    }
    if (event.type !== "assistant_text_delta") {
      continue;
    }
    const payload = (event.payload ?? {}) as { text?: string };
    const text = typeof payload.text === "string" ? payload.text : "";
    if (!text.trim()) {
      continue;
    }
    const turnId = event.turn_id || "";
    if (open && !toolSinceLastText && openTurnId === turnId) {
      open.body += text;
      open.summary = previewText(open.body);
      open.timestamp = event.timestamp;
    } else {
      const body = text.trim();
      open = {
        id: `text:${turnId || "current"}:${result.length}`,
        kind: "assistant",
        title: "GoDex",
        body,
        timestamp: event.timestamp,
        summary: previewText(body),
        turnId: turnId || undefined,
      };
      result.push(open);
      openTurnId = turnId;
      toolSinceLastText = false;
    }
  }
  return result;
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
