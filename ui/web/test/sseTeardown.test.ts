import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { streamEvents } from "../src/lib/sse";

/**
 * Regression tests for the SSE connection-leak bug.
 *
 * Background: browsers cap HTTP/1.1 at 6 concurrent connections per origin.
 * streamEvents used to exit without aborting its fetch, so every session
 * switch leaked one live SSE connection (the server keeps heartbeating it).
 * After five switches the connection pool was exhausted and the whole web UI
 * stalled: no session switch, no file open, no terminal create, and even a
 * page reload hung forever. These tests pin the contract that every teardown
 * path aborts the very AbortSignal that was handed to fetch().
 */

type FetchCall = { url: string; signal: AbortSignal };

const calls: FetchCall[] = [];
const originalFetch = globalThis.fetch;

beforeEach(() => {
  calls.length = 0;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
});

/**
 * Installs a fetch mock that behaves like the browser for abort semantics:
 * - the returned Response body is backed by a controllable ReadableStream;
 * - aborting the request signal errors the body stream (rejecting pending
 *   reads) and rejects the fetch promise if headers were not sent yet.
 */
function installSSEFetch() {
  const encoder = new TextEncoder();
  const api = {
    send(chunk: string) {
      api.controller?.enqueue(encoder.encode(chunk));
    },
    end() {
      api.controller?.close();
    },
    controller: undefined as ReadableStreamDefaultController<Uint8Array> | undefined,
  };
  globalThis.fetch = vi.fn((url: string | URL | Request, init?: RequestInit) => {
    const signal = (init?.signal ?? new AbortController().signal) as AbortSignal;
    calls.push({ url: String(url), signal });
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        api.controller = controller;
      },
    });
    signal.addEventListener(
      "abort",
      () => {
        // Browser behavior: an aborted fetch errors the body stream, which
        // rejects any pending reader.read().
        try {
          api.controller?.error(signal.reason ?? new DOMException("Aborted", "AbortError"));
        } catch {
          // Already closed/errored.
        }
      },
      { once: true },
    );
    return Promise.resolve(new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } }));
  }) as unknown as typeof fetch;
  return api;
}

describe("streamEvents connection teardown", () => {
  it("aborts the fetch when the outer signal aborts mid-stream (session switch)", async () => {
    const sse = installSSEFetch();

    const outer = new AbortController();
    const opened = vi.fn();
    const promise = streamEvents("s-1", null, outer.signal, () => undefined, opened);

    // Let the fetch resolve and the reader attach before switching sessions.
    await vi.waitFor(() => expect(opened).toHaveBeenCalled());
    expect(calls).toHaveLength(1);
    expect(calls[0].signal.aborted).toBe(false);

    // What ChatPage's effect cleanup does on a session switch:
    outer.abort();

    // The stream must settle promptly (via the aborted fetch), not hang
    // waiting for the next keepalive.
    await expect(promise).rejects.toThrow();
    // THE regression: the signal handed to fetch() must be aborted so the
    // browser closes the HTTP connection instead of leaking it.
    expect(calls[0].signal.aborted).toBe(true);
  });

  it("aborts the fetch when the read times out on a silent connection", async () => {
    vi.useFakeTimers();
    try {
      installSSEFetch();
      const outer = new AbortController();
      const promise = streamEvents("s-1", null, outer.signal, () => undefined);
      // Attach a handler immediately so the rejection (which fires inside the
      // fake-timer tick below) is never observed as unhandled.
      const assertion = expect(promise).rejects.toThrow(/read timed out/i);

      // No data ever arrives; advance past the 90s read timeout.
      await vi.advanceTimersByTimeAsync(91_000);

      await assertion;
      expect(calls[0].signal.aborted).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("parses data events and ends cleanly when the server closes the stream", async () => {
    const sse = installSSEFetch();

    const outer = new AbortController();
    const events: unknown[] = [];
    const opened = vi.fn();
    const promise = streamEvents("s-1", null, outer.signal, (event) => events.push(event), opened);

    await vi.waitFor(() => expect(opened).toHaveBeenCalled());
    const payload = { type: "snapshot_ready", session_id: "s-1", timestamp: "2026-08-20T00:00:00Z" };
    sse.send(`data: ${JSON.stringify(payload)}\n\n`);
    sse.end();

    await promise;
    expect(events).toHaveLength(1);
    expect((events[0] as { type: string }).type).toBe("snapshot_ready");
    // A clean server close still releases the connection slot.
    expect(calls[0].signal.aborted).toBe(true);
  });

  it("aborts the fetch when the server never answers the connect phase", async () => {
    vi.useFakeTimers();
    try {
      // A fetch that never settles, like a wedged server.
      globalThis.fetch = vi.fn((url: string | URL | Request, init?: RequestInit) => {
        const signal = (init?.signal ?? new AbortController().signal) as AbortSignal;
        calls.push({ url: String(url), signal });
        return new Promise<Response>((_, reject) => {
          signal.addEventListener(
            "abort",
            () => reject(signal.reason ?? new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      }) as unknown as typeof fetch;

      const outer = new AbortController();
      const promise = streamEvents("s-1", null, outer.signal, () => undefined);
      // Attach the rejection handler before firing the fake timer so the
      // rejection is never observed as unhandled.
      const assertion = expect(promise).rejects.toThrow();
      await vi.advanceTimersByTimeAsync(16_000);

      await assertion;
      expect(calls[0].signal.aborted).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
