import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createTerminal,
  destroyTerminal,
  pollTerminal,
  writeTerminalInput,
} from "../src/lib/terminalClient";

async function waitFor(predicate: () => boolean) {
  for (let i = 0; i < 20; i += 1) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

describe("terminalClient real backend id mapping", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("uses the backend terminalId for output, input, and cleanup", async () => {
    const calls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      calls.push(`${init?.method ?? "GET"} ${url}`);
      if (url === "/v1/terminal/create") {
        return new Response(JSON.stringify({ terminalId: "term-server", initialCursor: 0 }), { status: 201 });
      }
      if (url === "/v1/terminal/term-server/output?cursor=0") {
        return new Response(JSON.stringify({ terminalId: "term-server", cursor: 12, data: "server ready", exited: false }), { status: 200 });
      }
      if (url === "/v1/terminal/term-server/input") {
        return new Response(JSON.stringify({ terminalId: "term-server", accepted: true }), { status: 200 });
      }
      if (url === "/v1/terminal/term-server") {
        return new Response(JSON.stringify({ terminalId: "term-server", exited: true }), { status: 200 });
      }
      return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
    });

    const created = createTerminal(".");
    await waitFor(() => calls.includes("GET /v1/terminal/term-server/output?cursor=0"));

    expect(pollTerminal(created.terminalId, 0, 0)).toMatchObject({
      cursor: 12,
      data: "server ready",
      exited: false,
    });

    writeTerminalInput({ terminalId: created.terminalId, data: "pwd\n" }, 1);
    await waitFor(() => calls.includes("POST /v1/terminal/term-server/input"));

    destroyTerminal(created.terminalId);
    await waitFor(() => calls.includes("DELETE /v1/terminal/term-server"));

    expect(calls).not.toContain(`GET /v1/terminal/${created.terminalId}/output?cursor=0`);
  });
});
