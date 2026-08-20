import type { RuntimeEvent } from "./types";
import { apiURL } from "./api";

/**
 * streamEvents opens one SSE event stream for a session and resolves when the
 * stream ends (server close) or rejects on error/timeout.
 *
 * Connection hygiene is the critical part of this function: the browser allows
 * at most 6 concurrent HTTP/1.1 connections per origin. An SSE stream whose
 * fetch is never aborted stays open forever (the server keeps sending
 * heartbeats), so every teardown path that skips the abort leaks one
 * connection. Session switches tear streams down constantly; five leaked
 * streams exhaust the pool and then every request - session switch, file
 * open, terminal create, even the page reload - queues forever. That is the
 * "web UI stuck after a while" bug, so every exit path below must abort the
 * fetch (see the finally block).
 */
export async function streamEvents(
  sessionId: string,
  token: string | null,
  signal: AbortSignal,
  onEvent: (event: RuntimeEvent) => void,
  onOpen?: () => void,
) {
  // One controller owns the fetch for the entire lifetime of this stream
  // (connect phase AND read phase). The outer signal is bridged onto it and
  // the listener stays attached until the stream ends - removing it after the
  // connect phase (as an earlier revision did) left established streams
  // unabortable, so session switches leaked their connection.
  const CONNECT_TIMEOUT_MS = 15_000;
  const controller = new AbortController();
  const onOuterAbort = () => controller.abort(signal.reason);
  if (signal.aborted) {
    controller.abort(signal.reason);
  } else {
    signal.addEventListener("abort", onOuterAbort, { once: true });
  }

  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let connectTimer: ReturnType<typeof setTimeout> | undefined;
  try {
    // Bound the connection phase: if the server accepts the TCP connection
    // but never answers the request (wedged handler, half-open proxy), a bare
    // fetch() would hang forever and the caller's reconnect loop would stall.
    connectTimer = setTimeout(() => {
      controller.abort(new DOMException("SSE connect timed out", "TimeoutError"));
    }, CONNECT_TIMEOUT_MS);

    const response = await fetch(apiURL(`/sessions/${encodeURIComponent(sessionId)}/events?replay=active`), {
      method: "GET",
      headers: {
        Accept: "text/event-stream",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      signal: controller.signal,
    });
    // Headers arrived: the connect timeout must never fire during the read
    // phase, so clear it before consuming the body.
    clearTimeout(connectTimer);
    connectTimer = undefined;

    if (!response.ok || !response.body) {
      throw new Error(`SSE request failed with status ${response.status}`);
    }
    onOpen?.();

    reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    // If the underlying connection drops silently (no FIN/RST), reader.read()
    // can hang forever. A read timeout aborts the fetch - which closes the
    // socket instead of leaking it - and throws so the caller's reconnect
    // path runs on a fresh connection rather than leaving the UI stuck.
    const READ_TIMEOUT_MS = 90_000;
    const readWithTimeout = async () => {
      let timer: ReturnType<typeof setTimeout> | undefined;
      // If the timeout wins the race, the underlying read() may reject later
      // (when the abort below errors the stream); mark it handled so it does
      // not surface as an unhandled rejection in the console.
      const readPromise = reader!.read();
      readPromise.catch(() => undefined);
      try {
        return await Promise.race([
          readPromise,
          new Promise<never>((_, reject) => {
            timer = setTimeout(() => {
              controller.abort(new DOMException("SSE read timed out", "TimeoutError"));
              reject(new Error("SSE read timed out"));
            }, READ_TIMEOUT_MS);
          }),
        ]);
      } finally {
        if (timer) clearTimeout(timer);
      }
    };

    while (!signal.aborted) {
      const { done, value } = await readWithTimeout();
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
  } finally {
    if (connectTimer) clearTimeout(connectTimer);
    signal.removeEventListener("abort", onOuterAbort);
    // Tear the fetch down on EVERY exit path: normal return, thrown error,
    // outer abort (session switch / unmount), read timeout, or server close.
    // Aborting a settled fetch is a no-op; aborting a live one closes the
    // HTTP connection immediately so the slot returns to the browser pool.
    controller.abort();
    if (reader) {
      try {
        await reader.cancel();
      } catch {
        // The stream may already be errored or closed; nothing to do.
      }
    }
  }
}
