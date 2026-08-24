import { describe, expect, it, vi } from "vitest";
import { StepAPIError, StepClient, createStepClient } from "./client";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function mockFetch(handler: (url: string, init: RequestInit) => Response) {
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    return handler(String(input), init ?? {});
  });
  return fn as unknown as typeof fetch;
}

describe("StepClient.createStep", () => {
  it("POSTs the request with biz auth and returns the structured result", async () => {
    const fetchFn = mockFetch((url, init) => {
      expect(url).toBe("https://godex.example.com/v1/agent-steps");
      expect(init.method).toBe("POST");
      expect((init.headers as Record<string, string>).Authorization).toBe("Bearer biz_123");
      expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
      const body = JSON.parse(String(init.body));
      expect(body.prompt).toBe("分析订单");
      expect(body.inputs).toEqual({ order_id: "ORD-1" });
      return jsonResponse(200, {
        step_id: "stp_1",
        session_id: "ses_1",
        status: "completed",
        text: "done",
        tools_used: [{ name: "crm__get_order", kind: "mcp" }],
      });
    });

    const step = new StepClient({ baseUrl: "https://godex.example.com", apiKey: "biz_123", fetch: fetchFn });
    const result = await step.createStep({ prompt: "分析订单", inputs: { order_id: "ORD-1" } });
    expect(result.step_id).toBe("stp_1");
    expect(result.status).toBe("completed");
    expect(result.tools_used?.[0]?.kind).toBe("mcp");
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  it("polls GET after a 408 timeout until the step completes", async () => {
    let polls = 0;
    const fetchFn = mockFetch((url, init) => {
      if (init.method === "POST") {
        return jsonResponse(408, { error: { code: "step_timeout", step_id: "stp_slow" } });
      }
      // GET /v1/agent-steps/{id}: first poll still running, second completed.
      polls += 1;
      if (polls >= 2) {
        return jsonResponse(200, { step_id: "stp_slow", session_id: "ses_2", status: "completed", text: "finally" });
      }
      return jsonResponse(200, { step_id: "stp_slow", session_id: "ses_2", status: "running" });
    });

    const step = new StepClient({ baseUrl: "https://godex.example.com", apiKey: "biz_123", fetch: fetchFn, pollIntervalMs: 1 });
    const result = await step.createStep({ prompt: "slow" });
    expect(result.status).toBe("completed");
    expect(result.text).toBe("finally");
    expect(polls).toBe(2);
  });

  it("throws StepAPIError with the unified envelope on non-2xx", async () => {
    const fetchFn = mockFetch(() =>
      jsonResponse(422, { error: { code: "invalid_output", message: "output missing required field \"reason\"", step_id: "stp_bad" } }),
    );
    const step = new StepClient({ baseUrl: "https://godex.example.com", apiKey: "biz_123", fetch: fetchFn });
    await expect(step.createStep({ prompt: "x" })).rejects.toMatchObject({
      name: "StepAPIError",
      status: 422,
      code: "invalid_output",
      step_id: "stp_bad",
    });
  });
});

describe("StepClient.cancelStep", () => {
  it("POSTs the cancel endpoint with auth", async () => {
    const fetchFn = mockFetch((url, init) => {
      expect(url).toBe("https://godex.example.com/v1/agent-steps/stp_9/cancel");
      expect(init.method).toBe("POST");
      return jsonResponse(200, { status: "canceling" });
    });
    const step = new StepClient({ baseUrl: "https://godex.example.com", apiKey: "biz_123", fetch: fetchFn });
    await step.cancelStep("stp_9");
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  it("propagates 409 step_not_running", async () => {
    const fetchFn = mockFetch(() => jsonResponse(409, { error: { code: "step_not_running", message: "no active turn" } }));
    const step = new StepClient({ baseUrl: "https://godex.example.com/api", apiKey: "biz_123", fetch: fetchFn });
    await expect(step.cancelStep("stp_9")).rejects.toMatchObject({
      name: "StepAPIError",
      status: 409,
      code: "step_not_running",
    });
  });
});

describe("createStepClient factory", () => {
  it("returns a configured StepClient and normalizes trailing slashes", async () => {
    const fetchFn = mockFetch((url) => {
      expect(url).toBe("https://godex.example.com/v1/agent-steps/stp_5");
      return jsonResponse(200, { step_id: "stp_5", session_id: "ses_5", status: "completed" });
    });
    const step = createStepClient({ baseUrl: "https://godex.example.com/", apiKey: "biz_1", fetch: fetchFn });
    expect(step).toBeInstanceOf(StepClient);
    const r = await step.getStep("stp_5");
    expect(r.step_id).toBe("stp_5");
  });
});

// Ensure StepAPIError is reachable as a value export too.
describe("StepAPIError", () => {
  it("is a real Error subclass", () => {
    const err = new StepAPIError(500, { code: "boom", message: "x" });
    expect(err).toBeInstanceOf(Error);
    expect(err.message).toBe("x");
  });
});
