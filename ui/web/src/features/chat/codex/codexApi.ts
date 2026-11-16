import { APIError, apiURL, authHeaders, parseAPIError, request } from "../../../lib/apiClient";
import { translate } from "../../../i18n";
import { useLocaleStore } from "../../../store/locale";

export interface CodexItem { id: string; type: string; [key: string]: unknown }
export interface CodexTurn { id: string; status: string; items: CodexItem[]; startedAt?: number; error?: { message?: string } | null }
export interface CodexThread {
  id: string; name?: string; preview: string; cwd: string; model?: string | null; modelProvider: string; reasoningEffort?: string | null;
  status: { type: "idle" | "active" | "notLoaded" | "systemError"; activeFlags?: string[] };
  createdAt: number; updatedAt: number; canAcceptDirectInput?: boolean;
}
export interface CodexPage<T> { data: T[]; nextCursor: string | null }
export interface CodexApproval { id: string; method: string; params: Record<string, unknown> }
export interface CodexSnapshot { thread: CodexThread; turns: CodexPage<CodexTurn>; sequence: number; approvals: CodexApproval[] }
export interface CodexAccess { thread: CodexThread; loadedInRuntime: boolean }
export interface CodexEvent { method: string; params: Record<string, unknown>; sequence?: number }
export interface CodexStatus { ready: boolean; reason?: string; executable?: string; socket: string; workspaceRoots: string[] }
export interface CodexQueuedInput { id: string; clientUserMessageId: string; input: Array<{ type: string; text?: string }> }
export interface CodexModel { id: string; model: string; displayName: string; hidden: boolean; defaultReasoningEffort: string; supportedReasoningEfforts: Array<{ reasoningEffort: string; description: string }> }
export interface CodexModelSettings { model: string; effort: string }

const root = "/external/codex";
const threadPath = (id: string) => `${root}/threads/${encodeURIComponent(id)}`;
export const codexStatus = (token: string | null) => request<CodexStatus>(`${root}/status`, {}, token);
export const codexThreads = (token: string | null, cursor = "", search = "", view: "all" | "loaded" = "all") => request<CodexPage<CodexThread>>(`${root}/threads?${new URLSearchParams({ cursor, q: search, view })}`, {}, token);
export const codexAccess = (token: string | null, id: string, signal?: AbortSignal) => request<CodexAccess>(`${threadPath(id)}/access`, { signal }, token);
export const codexModels = (token: string | null, id: string, signal?: AbortSignal) => request<{ data: CodexModel[] }>(`${threadPath(id)}/models`, { signal }, token);
export const codexUpdateSettings = (token: string | null, id: string, body: CodexModelSettings) => request<CodexThread>(`${threadPath(id)}/settings`, { method: "PATCH", body: JSON.stringify(body) }, token);
export const codexHistory = (token: string | null, id: string, cursor = "", signal?: AbortSignal) => request<CodexSnapshot>(`${threadPath(id)}/history?${new URLSearchParams({ cursor })}`, { signal }, token);
export const codexQueue = (token: string | null, id: string) => request<CodexPage<CodexQueuedInput>>(`${threadPath(id)}/queue`, {}, token);
export const codexSubmit = (token: string | null, id: string, body: { requestId: string; text: string; mode: "queue" | "steer"; turnId?: string }) => request(`${threadPath(id)}/messages`, { method: "POST", body: JSON.stringify(body) }, token);
export const codexStartQueue = (token: string | null, id: string) => request(`${threadPath(id)}/queue/start`, { method: "POST", body: "{}" }, token);
export const codexInterrupt = (token: string | null, id: string, turnId: string) => request(`${threadPath(id)}/interrupt`, { method: "POST", body: JSON.stringify({ turnId }) }, token);
export const codexApprove = (token: string | null, id: string, requestId: string, decision: "accept" | "decline") => request(`${threadPath(id)}/approvals`, { method: "POST", body: JSON.stringify({ requestId, decision }) }, token);

export function isCodexActiveWriterError(error: unknown): boolean {
  if (!(error instanceof APIError)) return false;
  if ((error.body as { code?: string } | undefined)?.code === "codex_active_writer") return true;
  // Also handle an older Godex node during a rolling frontend/backend update.
  return [400, 409].includes(error.status) && /^thread \S+ already has an active writer$/.test(error.message);
}

/** One generic SSE transport, with the same teardown guarantees as native chat. */
export async function watchCodex(token: string | null, id: string, signal: AbortSignal, receive: (event: CodexEvent) => void): Promise<void> {
  const message = (key: string) => translate(useLocaleStore.getState().locale, `codex.${key}`);
  const controller = new AbortController();
  const abort = () => controller.abort(signal.reason);
  if (signal.aborted) abort(); else signal.addEventListener("abort", abort, { once: true });
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const arm = (ms: number) => { clearTimeout(timer); timer = setTimeout(() => controller.abort(new Error(message("streamTimeout"))), ms); };
  try {
    arm(25_000);
    const response = await fetch(apiURL(`${threadPath(id)}/events`), { headers: { ...authHeaders(token), Accept: "text/event-stream" }, signal: controller.signal });
    if (!response.ok) throw await parseAPIError(response);
    if (!response.body) throw new Error(message("streamUnavailable"));
    reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (!signal.aborted) {
      arm(45_000);
      const { done, value } = await reader.read();
      if (done) throw new Error(message("streamClosed"));
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, "\n");
      if (buffer.length > 32 * 1024 * 1024) throw new Error(message("eventTooLarge"));
      let boundary: number;
      while ((boundary = buffer.indexOf("\n\n")) >= 0) {
        const raw = buffer.slice(0, boundary); buffer = buffer.slice(boundary + 2);
        const data = raw.split("\n").filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
        if (data) receive(JSON.parse(data) as CodexEvent);
      }
    }
  } finally {
    clearTimeout(timer);
    signal.removeEventListener("abort", abort);
    controller.abort();
    try { await reader?.cancel(); } catch { /* already closed */ }
  }
}
