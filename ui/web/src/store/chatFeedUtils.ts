import type { AttachmentRef, FeedItem, FeedSegment, TodoFeedItem, TodoFeedStats } from "../lib/types";

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
 * - the group's `finalBody` holds the turn's VISIBLE ANSWER (see splitTurnSegments); copy,
 *   save-to-note and TTS act only on it, not on the process and not on a trailing wrap-up
 *   line.
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
      // finalBody is what copy / save-to-note / TTS return, so it must be the VISIBLE answer —
      // not the last text, which a trailing wrap-up line would win after a ledger
      // bookkeeping call. Same rule as the feed's answer slot, applied to one source of
      // truth: the split is computed here instead of assuming the last body won.
      const segments = openGroup.segments ?? [];
      if (segments.length > 0) {
        const answerText = splitTurnSegments(segments)
          .answer.filter((segment) => segment.type === "text" && !segment.thinking)
          .map((segment) => segment.text ?? "")
          .join("\n\n")
          .trim();
        if (answerText) {
          openGroup.finalBody = answerText;
          openGroup.summary = firstSummaryLine(answerText);
        }
      }
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
        // Provisional: the answer region is recomputed in closeGroup once the whole turn is
        // known (a trailing wrap-up line must not win this slot).
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

/**
 * Ledger actions that RECORD rather than advance the work.
 *
 * work_context is a "report, then record" tool: ack is defined as "mark the stream seen
 * AFTER reporting the focus", so a normal turn ends with a checkpoint/ack pair. Those calls
 * are bookkeeping, not part of the answer, and they must not be mistaken for the turn's
 * content. (todo_write is already dropped entirely at the same layer.)
 */
const LEDGER_BOOKKEEPING_ACTIONS: ReadonlySet<string> = new Set(["checkpoint", "ack", "propose_fact", "todo_patch"]);

/** True when a tool call only records work already reported (see above). */
export function isLedgerBookkeepingTool(name: string | undefined, input: Record<string, unknown> | undefined): boolean {
  if (name !== "work_context") {
    return false;
  }
  const action = typeof input?.action === "string" ? input.action : "";
  return LEDGER_BOOKKEEPING_ACTIONS.has(action);
}

/**
 * True only for a bookkeeping call that actually succeeded.
 *
 * A failed checkpoint/ack recorded nothing. Hiding it would leave the user believing
 * the state was saved when it was not, so failures stay visible in the default feed
 * and keep their error text in the expanded detail.
 */
export function isSilentLedgerBookkeeping(item: FeedItem | undefined): boolean {
  if (!item || !isLedgerBookkeepingTool(item.title, item.input)) {
    return false;
  }
  return !item.error?.trim() && item.status !== "failed";
}

/**
 * True when the text at `index` is a trailing WRAP-UP NOTE rather than the answer.
 *
 * The ledger tools are "report, then record" tools: ack is defined as "mark the stream
 * seen AFTER reporting the focus", so a normal turn ends with
 *
 *   …report → work_context checkpoint → work_context ack → one-line wrap-up
 *
 * Two signals together, because either one alone is wrong:
 *  - STRUCTURAL: a ledger bookkeeping call appears BETWEEN the preceding report and this
 *    line. Deliberately NOT "the immediately preceding segment": every assistant message in
 *    a real transcript carries a thinking block and read actions interleave, so the real shape
 *    is "report → propose_fact → thinking → focus → thinking → ack → thinking → closing".
 *    Requiring adjacency was the earlier bug — the interleaved segments broke it, the closing
 *    line took the answer slot again, and the report was folded away a second time.
 *  - LENGTH GUARD: an earlier non-thinking text is much longer. Without it, an "answer →
 *    ack → short but real closing question" would lose the answer slot.
 */
export function isTrailingWrapUpNote(segments: FeedSegment[], index: number): boolean {
  const segment = segments[index];
  if (!segment || segment.type !== "text" || segment.thinking || !segment.text?.trim()) {
    return false;
  }
  const hasTextAfter = segments
    .slice(index + 1)
    .some((later) => later.type === "text" && !later.thinking && Boolean(later.text?.trim()));
  if (hasTextAfter) {
    return false;
  }
  // Walk back to the previous non-thinking text (the report) and look for a bookkeeping call
  // anywhere in between, including thinking/read segments.
  let sawBookkeeping = false;
  for (let i = index - 1; i >= 0; i--) {
    const previous = segments[i]!;
    if (previous.type === "text" && !previous.thinking && previous.text?.trim()) {
      break;
    }
    if (previous.type === "tool" && isLedgerBookkeepingTool(previous.item?.title, previous.item?.input)) {
      sawBookkeeping = true;
    }
  }
  if (!sawBookkeeping) {
    return false;
  }
  const own = segment.text.trim().length;
  return segments.slice(0, index).some((earlier) => {
    if (earlier.type !== "text" || earlier.thinking || !earlier.text?.trim()) {
      return false;
    }
    const other = earlier.text.trim().length;
    return other >= own * 2 && other - own >= 80;
  });
}

/**
 * Split a grouped turn into the collapsible PROCESS and the visible ANSWER.
 *
 * Two departures from "the last text is the answer", both driven by the observed shape
 * "…report (720 chars) → work_context checkpoint → work_context ack → wrap-up (35 chars)",
 * where the wrap-up took the answer slot and folded the actual report into the collapsed
 * process summary — the turn the user was reading appeared to collapse:
 *
 *  1. Ledger bookkeeping calls (checkpoint / ack / propose_fact / todo_patch) are records
 *     of work already reported, so they are removed from both sides: rendering them made a
 *     finished turn look like it continued into two extra tool calls. Read actions
 *     (get / focus / recall / plan) are real work and stay visible.
 *  2. A trailing wrap-up note does not win the answer slot (see isTrailingWrapUpNote). It is
 *     NOT hidden — it stays visible under the report, inside the answer region.
 *
 * Falls back to the last text so a turn can never lose its visible body.
 */
export function splitTurnSegments(segments: FeedSegment[]): { process: FeedSegment[]; answer: FeedSegment[] } {
  // The boundary is found on the RAW sequence, before filtering: the "directly follows a
  // ledger bookkeeping call" signal that identifies a wrap-up line only exists until those
  // calls are removed. Filtering first made the wrap-up look like it directly followed the
  // report, and it took the answer slot again.
  const rawLastText = (): number => {
    for (let i = segments.length - 1; i >= 0; i--) {
      const segment = segments[i]!;
      if (segment.type === "text" && !segment.thinking && segment.text?.trim()) {
        return i;
      }
    }
    return -1;
  };
  let boundary = rawLastText();
  if (boundary >= 0 && isTrailingWrapUpNote(segments, boundary)) {
    for (let j = boundary - 1; j >= 0; j--) {
      const earlier = segments[j]!;
      if (earlier.type === "text" && !earlier.thinking && earlier.text?.trim()) {
        boundary = j;
        break;
      }
    }
  }
  // Then drop ledger bookkeeping calls for DISPLAY: they record work already reported, so
  // rendering them made a finished turn look like it continued into two extra tool calls.
  const forDisplay = (list: FeedSegment[]) =>
    list.filter((segment) => {
      if (segment.type === "text") {
        return Boolean(segment.text?.trim());
      }
      if (segment.type === "tool") {
        return !isSilentLedgerBookkeeping(segment.item);
      }
      return true;
    });
  if (boundary < 0) {
    return { process: forDisplay(segments), answer: [] };
  }
  return {
    process: forDisplay(segments.slice(0, boundary)),
    answer: forDisplay(segments.slice(boundary)),
  };
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
