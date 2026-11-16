import type { FeedItem } from "../../../lib/types";
import { groupFeedItemsIntoTurns } from "../../../store/chatFeedUtils";
import { translate } from "../../../i18n";
import type { Locale } from "../../../store/locale";
import type { CodexApproval, CodexEvent, CodexItem, CodexSnapshot, CodexThread, CodexTurn } from "./codexApi";

export interface CodexConversation {
  thread?: CodexThread;
  turns: CodexTurn[];
  approvals: CodexApproval[];
  cursor: string | null;
}
export const emptyCodexConversation = (): CodexConversation => ({ turns: [], approvals: [], cursor: null });

export function mergeCodexTurns(current: CodexTurn[], incoming: CodexTurn[], earlier = false): CodexTurn[] {
  const map = new Map(current.map((turn) => [turn.id, turn]));
  for (const turn of incoming) {
    const previous = map.get(turn.id);
    // A backscroll response must not overwrite a newer streaming turn.
    if (!earlier || !previous) map.set(turn.id, turn);
  }
  const order = earlier ? [...incoming.map((turn) => turn.id), ...current.map((turn) => turn.id)] : [...current.map((turn) => turn.id), ...incoming.map((turn) => turn.id)];
  return [...new Set(order)].map((id) => map.get(id)!);
}

export function hydrateCodex(state: CodexConversation, snapshot: CodexSnapshot): CodexConversation {
  return { thread: snapshot.thread, turns: mergeCodexTurns(state.turns, [...(snapshot.turns.data ?? [])].reverse()), cursor: state.turns.length ? state.cursor : snapshot.turns.nextCursor, approvals: snapshot.approvals ?? [] };
}

export function reduceCodexEvent(state: CodexConversation, event: CodexEvent): CodexConversation {
  const p = event.params;
  if (event.method === "codex/snapshot") return hydrateCodex(state, p as unknown as CodexSnapshot);
  if (p.threadId && state.thread && p.threadId !== state.thread.id) return state;
  if (event.method === "thread/status/changed" && state.thread) return { ...state, thread: { ...state.thread, status: p.status as CodexThread["status"] } };
  if (event.method === "thread/settings/updated" && state.thread) {
    const settings = p.threadSettings as { model?: string; effort?: string | null } | undefined;
    return settings ? { ...state, thread: { ...state.thread, ...(typeof settings.model === "string" ? { model: settings.model } : {}), reasoningEffort: settings.effort ?? null } } : state;
  }
  if (event.method === "codex/approval") {
    const request = p as unknown as CodexApproval;
    return { ...state, approvals: [...state.approvals.filter((a) => a.id !== request.id), request] };
  }
  if (event.method === "serverRequest/resolved") return { ...state, approvals: state.approvals.filter((a) => !a.id.endsWith(`:${JSON.stringify(p.requestId)}`)) };
  if (event.method === "turn/started" || event.method === "turn/completed") {
    const turn = p.turn as CodexTurn | undefined;
    if (!turn) return state;
    const previous = state.turns.find((t) => t.id === turn.id);
    // Completion notifications may omit items; do not erase streamed content.
    const updated = { ...turn, items: turn.items?.length ? turn.items : previous?.items ?? [] };
    return { ...state, turns: mergeCodexTurns(state.turns, [updated]) };
  }
  const turnId = String(p.turnId ?? "");
  if (!turnId) return state;
  const previous = state.turns.find((turn) => turn.id === turnId) ?? { id: turnId, status: "inProgress", items: [] };
  let items = previous.items;
  if (event.method === "item/started" || event.method === "item/completed") {
    const item = p.item as CodexItem | undefined;
    if (!item) return state;
    const existing = items.find((candidate) => candidate.id === item.id);
    const merged = { ...existing, ...item };
    items = existing ? items.map((candidate) => candidate.id === item.id ? merged : candidate) : [...items, merged];
  } else if (event.method === "item/agentMessage/delta" || event.method === "item/commandExecution/outputDelta") {
    const id = String(p.itemId ?? "");
    const type = event.method === "item/agentMessage/delta" ? "agentMessage" : "commandExecution";
    const key = type === "agentMessage" ? "text" : "aggregatedOutput";
    const existing = items.find((item) => item.id === id) ?? { id, type, status: "inProgress" };
    const next = { ...existing, [key]: String(existing[key] ?? "") + String(p.delta ?? "") };
    items = items.some((item) => item.id === id) ? items.map((item) => item.id === id ? next : item) : [...items, next];
  } else return state;
  return { ...state, turns: mergeCodexTurns(state.turns, [{ ...previous, items }]) };
}

const text = (value: unknown): string => typeof value === "string" ? value : "";
function itemText(item: CodexItem, attachment: string): string {
  if (typeof item.text === "string") return item.text;
  if (Array.isArray(item.content)) return item.content.map((block) => block && typeof block === "object" ? text((block as Record<string, unknown>).text) || `[${text((block as Record<string, unknown>).type) || attachment}]` : "").join("\n");
  return "";
}

export function codexFeedItems(turns: CodexTurn[], expanded: ReadonlySet<string>, locale: Locale = "en"): FeedItem[] {
  const t = (key: string) => translate(locale, `codex.${key}`);
  const result: FeedItem[] = [];
  for (const turn of turns) {
    for (const item of turn.items ?? []) {
      const id = `codex:${turn.id}:${item.id}`;
      const base = { id, turnId: turn.id, body: "", timestamp: turn.startedAt ? new Date(turn.startedAt * 1000).toISOString() : undefined };
      if (item.type === "userMessage") result.push({ ...base, kind: "user", title: t("user"), body: itemText(item, t("attachment")) });
      else if (item.type === "agentMessage") result.push({ ...base, kind: "assistant", title: "Codex", body: itemText(item, t("attachment")) });
      else if (item.type === "reasoning") {
        const summary = Array.isArray(item.summary) ? item.summary.filter((part): part is string => typeof part === "string").join("\n") : "";
        // The grouping helper uses this internal marker; MessageFeedV2 renders
        // its own localized thinking label after grouping.
        if (summary) result.push({ ...base, kind: "assistant", title: "Thinking…", body: summary });
      } else if (item.type === "contextCompaction") result.push({ ...base, kind: "summary", title: t("compaction"), body: t("compactionHint") });
      else {
        const input = item.type === "commandExecution" ? { command: item.command, cwd: item.cwd } : item;
        const nativeStatus = text(item.status);
        const status = nativeStatus === "inProgress" ? "running" : nativeStatus === "declined" ? "failed" : nativeStatus || (turn.status === "inProgress" ? "running" : "completed");
        result.push({ ...base, kind: "tool", title: text(item.tool) || (item.type === "commandExecution" ? "bash" : item.type), input, output: text(item.aggregatedOutput) || (item.result ? JSON.stringify(item.result, null, 2) : ""), status, expanded: expanded.has(id), startedAt: typeof item.startedAtMs === "number" ? new Date(item.startedAtMs).toISOString() : base.timestamp, durationMs: typeof item.durationMs === "number" ? item.durationMs : undefined });
      }
    }
    if (turn.error?.message) result.push({ id: `codex:${turn.id}:error`, kind: "error", title: "Codex", body: turn.error.message, turnId: turn.id });
  }
  return groupFeedItemsIntoTurns(result).map((item) => item.segments ? { ...item, title: "Codex" } : item);
}
