import type { RuntimeEvent } from "./types";
import { apiURL } from "./api";

export async function streamEvents(
  sessionId: string,
  token: string | null,
  signal: AbortSignal,
  onEvent: (event: RuntimeEvent) => void,
  onOpen?: () => void,
) {
  const response = await fetch(apiURL(`/sessions/${encodeURIComponent(sessionId)}/events?replay=active`), {
    method: "GET",
    headers: {
      Accept: "text/event-stream",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    signal,
  });

  if (!response.ok || !response.body) {
    throw new Error(`SSE request failed with status ${response.status}`);
  }
  onOpen?.();

  const decoder = new TextDecoder();
  const reader = response.body.getReader();
  let buffer = "";

  while (!signal.aborted) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    buffer += decoder.decode(value, { stream: true });

    while (true) {
      const delimiter = buffer.indexOf("\n\n");
      if (delimiter < 0) {
        break;
      }
      const rawEvent = buffer.slice(0, delimiter);
      buffer = buffer.slice(delimiter + 2);

      const dataLines = rawEvent
        .split("\n")
        .filter((line) => line.startsWith("data: "))
        .map((line) => line.slice(6));

      if (dataLines.length === 0) {
        continue;
      }

      const payload = dataLines.join("\n");
      try {
        onEvent(JSON.parse(payload) as RuntimeEvent);
      } catch {
        // Ignore malformed events and keep the stream alive.
      }
    }
  }
}
