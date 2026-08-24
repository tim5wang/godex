/**
 * Agent Step Platform — TypeScript SDK (Phase B).
 *
 * Self-contained, zero-dependency client for godex's Agent Step API
 * (`POST /v1/agent-steps` and friends). Business UIs can embed godex as an
 * agent runtime by calling `createStep` and rendering `ui_card` results.
 *
 * Wire contract: docs/agent-step-platform-details.md §2.
 */

// ---------------------------------------------------------------------------
// Types (mirror of the HTTP contract; UiCardData is shared with the UI)
// ---------------------------------------------------------------------------

/** Structured interactive card emitted by the ui_card tool. */
export type UiCardData = {
  kind: "form" | "button_group" | "card";
  title?: string;
  content?: string;
  fields?: UiCardField[];
  actions?: UiCardAction[];
};

export type UiCardField = {
  name: string;
  label?: string;
  type?: "text" | "textarea" | "select" | "number";
  required?: boolean;
  placeholder?: string;
  options?: Array<{ label: string; value: string }>;
};

export type UiCardAction = {
  id?: string;
  label: string;
  kind?: "message" | "command" | "approve" | "url";
  value?: string;
};

export type StepTools = {
  /** MCP tool scope, e.g. ["crm/*", "!crm/delete_*"]. */
  mcp?: string[];
  /** Sandbox tool scope, e.g. ["read_file", "!bash"]. */
  sandbox?: string[];
};

export type StepContext = {
  /** Recall provider names, e.g. ["sales_crm", "godex://memory"]. */
  recall?: string[];
};

export type StructuredOutput = {
  /** JSON Schema the final output must satisfy. */
  schema: Record<string, unknown>;
};

export type StepRequest = {
  /** Client-supplied idempotency key; the server generates one when omitted. */
  step_id?: string;
  prompt: string;
  /** Business context, injected as an isolated (non-instruction) block. */
  inputs?: Record<string, unknown>;
  context?: StepContext;
  tools?: StepTools;
  model?: string;
  /** Synchronous deadline in seconds (default 60, max 600). */
  timeout_seconds?: number;
  structured_output?: StructuredOutput;
};

export type StepToolUse = {
  name: string;
  kind: "mcp" | "sandbox";
};

export type StepResult = {
  step_id: string;
  session_id: string;
  status: string;
  output?: unknown;
  text?: string;
  tools_used?: StepToolUse[];
  created_at: string;
};

export type StepError = {
  code: string;
  message: string;
  step_id?: string;
  session_id?: string;
};

/** Raised for non-2xx responses; carries the unified error envelope. */
export class StepAPIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly step_id?: string;
  readonly session_id?: string;

  constructor(status: number, detail: StepError, message?: string) {
    super(message ?? detail?.message ?? `step request failed (${status})`);
    this.name = "StepAPIError";
    this.status = status;
    this.code = detail?.code ?? "unknown";
    this.step_id = detail?.step_id;
    this.session_id = detail?.session_id;
  }
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export type StepClientOptions = {
  /** The godex server origin, e.g. "https://godex.claw.carc.top". */
  baseUrl: string;
  /** Business-system API key (biz_...). */
  apiKey: string;
  /** Optional fetch override (tests, proxies). */
  fetch?: typeof fetch;
  /** Poll interval used when the server replies 202. */
  pollIntervalMs?: number;
};

export class StepClient {
  private readonly baseUrl: string;
  private readonly apiKey: string;
  private readonly fetchFn: typeof fetch;
  private readonly pollIntervalMs: number;

  constructor(options: StepClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.apiKey = options.apiKey;
    this.fetchFn = options.fetch ?? globalThis.fetch;
    this.pollIntervalMs = options.pollIntervalMs ?? 1_000;
  }

  /**
   * Run one synchronous agent step. On 408 (sync timeout) the server keeps the
   * step running; this method starts polling GET /v1/agent-steps/{id} until the
   * step leaves the running state (use `signal` to bound the wait).
   */
  async createStep(req: StepRequest, signal?: AbortSignal): Promise<StepResult> {
    const init: RequestInit = {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
      },
      body: JSON.stringify(req),
    };
    const response = await this.fetchFn(this.url("/v1/agent-steps"), this.withSignal(init, signal));
    const body = await this.parseJson(response);

    if (response.ok) {
      return body as StepResult;
    }
    if (response.status === 408 && typeof body?.error?.step_id === "string") {
      return this.pollUntilDone(body.error.step_id, signal);
    }
    throw this.toAPIError(response.status, body);
  }

  /** Query the terminal state of a step session. */
  async getStep(stepId: string, signal?: AbortSignal): Promise<StepResult> {
    const response = await this.fetchFn(
      this.url(`/v1/agent-steps/${encodeURIComponent(stepId)}`),
      this.authInit({ method: "GET" }, signal),
    );
    const body = await this.parseJson(response);
    if (!response.ok) {
      throw this.toAPIError(response.status, body);
    }
    return body as StepResult;
  }

  /** Abort the active turn of a step session. */
  async cancelStep(stepId: string, signal?: AbortSignal): Promise<void> {
    const response = await this.fetchFn(
      this.url(`/v1/agent-steps/${encodeURIComponent(stepId)}/cancel`),
      this.authInit({ method: "POST" }, signal),
    );
    if (!response.ok) {
      const body = await this.parseJson(response);
      throw this.toAPIError(response.status, body);
    }
  }

  /**
   * Open an SSE stream of the underlying step session (same event shape as
   * GET /sessions/{id}/events). The returned controller aborts the stream.
   */
  async streamEvents(
    stepId: string,
    onEvent: (event: RuntimeStepEvent) => void,
    signal?: AbortSignal,
  ): Promise<AbortController> {
    const controller = new AbortController();
    const onOuterAbort = () => controller.abort();
    signal?.addEventListener("abort", onOuterAbort, { once: true });

    const response = await this.fetchFn(
      this.url(`/v1/agent-steps/${encodeURIComponent(stepId)}/events?replay=active`),
      this.authInit({ method: "GET", headers: { Accept: "text/event-stream" } }, controller.signal),
    );
    if (!response.ok || !response.body) {
      signal?.removeEventListener("abort", onOuterAbort);
      const body = await this.parseJson(response);
      throw this.toAPIError(response.status, body);
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    void (async () => {
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) {
            break;
          }
          buffer += decoder.decode(value, { stream: true });
          let sep: number;
          while ((sep = buffer.indexOf("\n\n")) !== -1) {
            const frame = buffer.slice(0, sep);
            buffer = buffer.slice(sep + 2);
            const line = frame
              .split("\n")
              .find((l) => l.startsWith("data:"))
              ?.slice(5)
              .trim();
            if (line) {
              try {
                onEvent(JSON.parse(line) as RuntimeStepEvent);
              } catch {
                // ignore malformed frames
              }
            }
          }
        }
      } finally {
        signal?.removeEventListener("abort", onOuterAbort);
      }
    })();
    return controller;
  }

  // -------------------------------------------------------------------------
  // Internals
  // -------------------------------------------------------------------------

  private async pollUntilDone(stepId: string, signal?: AbortSignal): Promise<StepResult> {
    for (;;) {
      if (signal?.aborted) {
        throw new StepAPIError(
          408,
          { code: "step_timeout", message: "step polling aborted", step_id: stepId },
          "step polling aborted",
        );
      }
      await sleep(this.pollIntervalMs, signal);
      const result = await this.getStep(stepId, signal);
      if (result.status !== "running") {
        return result;
      }
    }
  }

  private url(path: string): string {
    return `${this.baseUrl}${path.startsWith("/") ? path : `/${path}`}`;
  }

  private authInit(init: RequestInit, signal?: AbortSignal): RequestInit {
    return this.withSignal(
      {
        ...init,
        headers: {
          ...(init.headers ?? {}),
          Authorization: `Bearer ${this.apiKey}`,
        },
      },
      signal,
    );
  }

  private withSignal(init: RequestInit, signal?: AbortSignal): RequestInit {
    if (!signal) {
      return init;
    }
    const existing = init.signal as AbortSignal | undefined;
    if (!existing) {
      return { ...init, signal };
    }
    // Chain two signals: abort when either fires.
    const controller = new AbortController();
    const onAbort = () => controller.abort();
    existing.addEventListener("abort", onAbort, { once: true });
    signal.addEventListener("abort", onAbort, { once: true });
    return { ...init, signal: controller.signal };
  }

  private async parseJson(response: Response): Promise<any> {
    try {
      return await response.json();
    } catch {
      return undefined;
    }
  }

  private toAPIError(status: number, body: any): StepAPIError {
    const detail: StepError = body?.error ?? {};
    return new StepAPIError(status, detail);
  }
}

/** Runtime event type received on the SSE stream (subset of events.Event). */
export type RuntimeStepEvent = {
  type: string;
  turn_id?: string;
  payload?: Record<string, unknown>;
};

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/** Create a configured client (convenience factory). */
export function createStepClient(options: StepClientOptions): StepClient {
  return new StepClient(options);
}
