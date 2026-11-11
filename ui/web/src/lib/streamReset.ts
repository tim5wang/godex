import type { FeedItem, SessionTimelineEntry } from "./types";

/** Discard only the failed attempt's suffix, preserving earlier conclusions. */
export function resetStreamFeed(items: FeedItem[], turnId: string, text: string, thinking: string): FeedItem[] {
  const result = [...items];
  let textLeft = text.length;
  let thinkingLeft = thinking.length;
  for (let i = result.length - 1; i >= 0 && (textLeft || thinkingLeft); i--) {
    const item = result[i];
    if (turnId && item.turnId !== turnId) continue;
    if (item.kind === "tool" || item.kind === "user") break;
    const isText = item.kind === "assistant";
    const isThinking = item.kind === "background" && item.title === "Thinking…";
    if (!isText && !isThinking) continue;
    const count = Math.min(item.body.length, isText ? textLeft : thinkingLeft);
    if (!count) continue;
    const body = item.body.slice(0, item.body.length - count);
    if (isText) textLeft -= count; else thinkingLeft -= count;
    if (body) result[i] = { ...item, body, summary: body.trim().split("\n")[0] };
    else result.splice(i, 1);
  }
  return result;
}

/** Apply reset events before reconstructing persisted streaming bubbles. */
export function discardFailedStreamDeltas(items: SessionTimelineEntry[]): SessionTimelineEntry[] {
  const result: SessionTimelineEntry[] = [];
  for (const event of [...items].sort((a, b) => {
    const difference = Date.parse(a.timestamp) - Date.parse(b.timestamp);
    if (difference) return difference;
    // Go timestamps retain nanoseconds; Date.parse only retains milliseconds.
    const fraction = (value: string) => (value.match(/\.(\d+)/)?.[1] ?? "").padEnd(9, "0");
    return fraction(a.timestamp).localeCompare(fraction(b.timestamp));
  })) {
    if (event.type !== "assistant_stream_reset") { result.push(event); continue; }
    const payload = (event.payload ?? {}) as { text?: string; thinking?: string };
    let textLeft = payload.text?.length ?? 0;
    let thinkingLeft = payload.thinking?.length ?? 0;
    for (let i = result.length - 1; i >= 0 && (textLeft || thinkingLeft); i--) {
      const previous = result[i];
      if (previous.turn_id !== event.turn_id) continue;
      if (["assistant_message_completed", "tool_call_started", "tool_call_finished"].includes(previous.type)) break;
      const isText = previous.type === "assistant_text_delta";
      if (!isText && previous.type !== "assistant_thinking_delta") continue;
      const data = (previous.payload ?? {}) as { text?: string };
      const body = data.text ?? "";
      const count = Math.min(body.length, isText ? textLeft : thinkingLeft);
      if (!count) continue;
      if (isText) textLeft -= count; else thinkingLeft -= count;
      const kept = body.slice(0, body.length - count);
      if (kept) result[i] = { ...previous, payload: { ...data, text: kept } };
      else result.splice(i, 1);
    }
  }
  return result;
}
