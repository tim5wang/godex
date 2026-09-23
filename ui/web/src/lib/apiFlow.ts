import { APIError, apiURL, authHeaders, request } from "./apiClient";

// ---- Flow Spec v1 types (mirrors agent.FlowVersionView / FlowRunView) ----

export interface FlowDefinition {
  flow_id: string;
  name?: string;
  description?: string;
  version: string;
  status?: string;
  template_id?: string;
  inputs?: { name: string; type?: string; desc?: string }[];
  outputs?: { name: string; type?: string; desc?: string }[];
  nodes: FlowNode[];
  edges: FlowEdge[];
}

export interface FlowNode {
  id: string;
  kind: string; // step | llm | decision | human | branch | loop | function
  title?: string;
  prompt?: string;
  agent_type?: string;
  write_scope?: string[];
  retry?: FlowRetryPolicy;
  decision?: FlowDecisionSpec;
  branch?: FlowBranchSpec;
  human?: FlowHumanSpec;
  loop?: FlowLoopSpec;
  function?: FlowFunctionSpec;
  outputs?: { name: string; type?: string; desc?: string }[];
  /** Editor-only canvas layout (x/y); stripped before compile. */
  canvas_pos?: { x: number; y: number };
  /** Pin this step node to an agent template / business key id. */
  agent_ref?: string;
  timeout_sec?: number;
}

export interface FlowFunctionSpec {
  runtime: string; // js | wasm
  source?: string; // js handler source (runtime=js)
  ref?: string; // node-library id (runtime=wasm)
  handler?: string; // entry function; default "handle"
  input_schema?: unknown;
  output_schema?: unknown;
}

export interface FlowLoopSpec {
  body?: string[];
  exit_when?: FlowCondition;
  max_iterations?: number;
  iteration_key?: string;
}

export interface FlowHumanSpec {
  queue: string;
  assignee_policy?: string;
  form?: unknown;
  prompt?: string;
  timeout_ms?: number;
  on_timeout?: string;
  result_var?: string;
}

export interface FlowRetryPolicy {
  max_attempts?: number;
  initial_interval_ms?: number;
  backoff_coefficient?: number;
  max_interval_ms?: number;
  jitter?: number;
  retry_on?: string[];
  non_retryable?: string[];
}

export interface FlowDecisionSpec {
  provider?: string;
  decision_type?: "choice" | "boolean" | "score";
  choices?: { id: string; label?: string }[];
  timeout_ms?: number;
  on_error?: "fail_closed" | "fail_open" | "fail";
  default_choice?: string;
}

export interface FlowBranchSpec {
  cases: { name?: string; to: string; condition: FlowCondition }[];
  default_to: string;
}

export interface FlowCondition {
  status?: string;
  verdict?: string;
  node?: string;
  choice?: string;
  confidence?: { op: string; value: number };
  output?: { path: string; op: string; value: unknown };
  all?: FlowCondition[];
  any?: FlowCondition[];
}

export interface FlowEdge {
  id?: string;
  from: string;
  to: string;
  edge_type?: "data_dependency" | "handoff" | "condition";
  when?: FlowCondition;
  max_iterations?: number;
  iteration_key?: string;
}

export interface FlowSummaryView {
  flow_id: string;
  draft?: string;
  gray?: string;
  published?: string;
}

export interface FlowVersionView {
  flow_id: string;
  version: string;
  status: string;
  name?: string;
  description?: string;
  nodes: number;
  edges: number;
  digest?: string;
  created_at: string;
  updated_at: string;
  definition?: FlowDefinition;
}

export interface FlowRunView {
  run_id: string;
  flow_id: string;
  version: string;
  digest: string;
  status: string;
  workflow_id?: string;
  inputs?: Record<string, unknown>;
  outputs?: Record<string, unknown>;
  error?: string;
  started_at: string;
  updated_at: string;
  finished_at?: string;
}

// ---- Flow API functions ----

export function listFlows(token: string | null) {
  return request<FlowSummaryView[]>("/v1/flows", { method: "GET" }, token);
}

export function createFlow(token: string | null, args: {
  flow_id?: string;
  version?: string;
  status?: string;
  definition?: FlowDefinition;
}) {
  return request<FlowVersionView>("/v1/flows", { method: "POST", body: JSON.stringify(args) }, token);
}

export function listFlowVersions(token: string | null, flowId: string) {
  return request<FlowVersionView[]>(`/v1/flows/${encodeURIComponent(flowId)}`, { method: "GET" }, token);
}

export function validateFlow(token: string | null, flowId: string, version: string, def: FlowDefinition) {
  return request<{ digest: string }>(
    `/v1/flows/${encodeURIComponent(flowId)}/versions/${encodeURIComponent(version)}/validate`,
    { method: "POST", body: JSON.stringify(def) },
    token,
  );
}

export function publishFlow(token: string | null, flowId: string, version: string) {
  return request<FlowVersionView>(
    `/v1/flows/${encodeURIComponent(flowId)}/versions/${encodeURIComponent(version)}/publish`,
    { method: "POST" },
    token,
  );
}

export function createFlowRun(token: string | null, flowId: string, body: {
  version?: string;
  inputs?: Record<string, unknown>;
  wait_ms?: number;
  /** step_mode creates the run WITHOUT auto-start so the UI can single-step it. */
  step_mode?: boolean;
}) {
  return request<FlowRunView>(`/v1/flows/${encodeURIComponent(flowId)}/runs`, { method: "POST", body: JSON.stringify(body) }, token);
}

export function listFlowRuns(token: string | null, flowId: string) {
  return request<FlowRunView[]>(`/v1/flows/${encodeURIComponent(flowId)}/runs`, { method: "GET" }, token);
}

export function getFlowRun(token: string | null, runId: string, flowId: string) {
  return request<FlowRunView>(`/v1/flow-runs/${encodeURIComponent(runId)}?flow_id=${encodeURIComponent(flowId)}`, { method: "GET" }, token);
}

export function cancelFlowRun(token: string | null, runId: string, flowId: string) {
  return request<FlowRunView>(`/v1/flow-runs/${encodeURIComponent(runId)}/cancel?flow_id=${encodeURIComponent(flowId)}`, { method: "POST" }, token);
}

/** Flow-run workflow event (created/start/handoff/decision_made/...). */
export interface FlowRunEvent {
  event: string;
  node_id?: string;
  at?: string;
  choice?: string;
  confidence?: number;
  provider?: string;
  latency_ms?: number;
  error?: string;
  [key: string]: unknown;
}

/** Fetches the full append-only event log of a run (poll=1 snapshot). */
export function flowRunEvents(token: string | null, runId: string, flowId: string) {
  return request<FlowRunEvent[]>(
    `/v1/flow-runs/${encodeURIComponent(runId)}/events?flow_id=${encodeURIComponent(flowId)}&poll=1`,
    { method: "GET" },
    token,
  );
}

// ---- Flow single-step debug (调试面板单步运行) -----------------------------

export interface NodeStepView {
  id: string;
  kind: string;
  title?: string;
  status: string;
  outputs?: Record<string, unknown>;
  error?: string;
  decision?: { choice?: string; confidence?: number };
}

export interface StepFlowView {
  run_id: string;
  flow_id: string;
  status: string;
  started?: string;
  nodes: NodeStepView[];
  terminal: boolean;
}

/** Advances a debug run by exactly one node; returns per-node state with
 * outputs/context for the debug panel. */
export function stepFlowRun(token: string | null, runId: string, flowId: string) {
  return request<StepFlowView>(
    `/v1/flow-runs/${encodeURIComponent(runId)}/step?flow_id=${encodeURIComponent(flowId)}`,
    { method: "POST" },
    token,
  );
}

/** Streams run events over the SSE endpoint (no poll=1): calls onEvent per
 * incoming `data:` event; resolves when the server sends `event: done` or the
 * stream closes. Abort via the passed signal. (P3 余项 4 — SSE 实时增量高亮.) */
export function streamFlowRunEvents(
  token: string | null,
  runId: string,
  flowId: string,
  onEvent: (ev: FlowRunEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  return new Promise((resolve, reject) => {
    fetch(apiURL(`/v1/flow-runs/${encodeURIComponent(runId)}/events?flow_id=${encodeURIComponent(flowId)}`), {
      method: "GET",
      headers: authHeaders(token),
      signal,
    })
      .then((response) => {
        if (!response.ok) {
          throw new APIError(response.status, response.statusText);
        }
        const reader = response.body?.getReader();
        if (!reader) {
          return resolve();
        }
        const decoder = new TextDecoder();
        let buffer = "";
        let eventName = "";
        const pump = (): void => {
          reader
            .read()
            .then(({ done, value }) => {
              if (done) {
                return resolve();
              }
              buffer += decoder.decode(value, { stream: true });
              let idx: number;
              while ((idx = buffer.indexOf("\n\n")) >= 0) {
                const chunk = buffer.slice(0, idx);
                buffer = buffer.slice(idx + 2);
                for (const line of chunk.split("\n")) {
                  if (line.startsWith("event: ")) {
                    eventName = line.slice(7).trim();
                  } else if (line.startsWith("data: ")) {
                    const data = line.slice(6).trim();
                    if (!data) {
                      continue;
                    }
                    if (eventName === "done") {
                      return resolve();
                    }
                    try {
                      onEvent(JSON.parse(data) as FlowRunEvent);
                    } catch {
                      /* skip malformed frames */
                    }
                  }
                }
              }
              pump();
            })
            .catch(reject);
        };
        pump();
      })
      .catch(reject);
  });
}

// ---- Flow diagnosis (P3 Agent 闭环 §22.2) ---------------------------------

export interface FlowDiagnosis {
  run_id: string;
  flow_id: string;
  version: string;
  root_cause: string;
  summary?: string;
  suggestions: string[];
  fixed_definition?: FlowDefinition;
}

/** LLM diagnosis of a failed run: event log + definition → root cause +
 * suggestions + optional validated fixed definition (NOT saved; caller
 * persists it as a new version via createFlow). */
export function diagnoseFlowRun(token: string | null, runId: string, flowId: string) {
  return request<FlowDiagnosis>(
    `/v1/flow-runs/${encodeURIComponent(runId)}/diagnose?flow_id=${encodeURIComponent(flowId)}`,
    { method: "POST" },
    token,
  );
}

// ---- Flow inspection (P3 Agent 闭环 §22.2 定期巡检) -----------------------

export interface FlowInspectionSummary {
  flow_id: string;
  published_version?: string;
  total: number;
  completed: number;
  failed: number;
  canceled: number;
  waiting: number;
  running: number;
  failure_rate: number;
  last_run_at?: string;
  error_nodes: number;
  human_waiting: number;
  iteration_caps: number;
  latest_error_runs?: string[];
}

export interface FlowInspectionReport {
  generated_at: string;
  window_hours: number;
  total: number;
  failed: number;
  waiting: number;
  failure_rate: number;
  flows: FlowInspectionSummary[];
}

/** Aggregates run health across published flows over a window (hours). */
export function inspectFlows(token: string | null, windowHours = 24) {
  return request<FlowInspectionReport>(
    `/v1/flow-inspection?window_hours=${windowHours}`,
    { method: "GET" },
    token,
  );
}

/** Drafts a Flow Spec v1 definition from a natural-language description via
 * the LLM (P2.5). The result is validated but NOT saved; the caller previews
 * and persists it through createFlow. */
export function generateFlowSpec(token: string | null, description: string, definition?: FlowDefinition) {
  return request<FlowDefinition>(
    "/v1/flows/generate",
    { method: "POST", body: JSON.stringify({ description, definition }) },
    token,
  );
}

// ---- Node library (P3): reusable function-node definitions ----------------

export interface NodeLibraryEntry {
  id: string;
  name: string;
  description?: string;
  tags?: string[];
  source?: string; // builtin | user
  function: FlowFunctionSpec;
  created_at?: string;
  updated_at?: string;
}

/** Lists all node-library entries (builtin seeds + user-defined). */
export function listNodeLibrary(token: string | null) {
  return request<NodeLibraryEntry[]>("/v1/node-library", { method: "GET" }, token);
}

/** Creates or updates a user node-library entry. */
export function saveNodeLibrary(token: string | null, entry: NodeLibraryEntry) {
  return request<NodeLibraryEntry>(
    "/v1/node-library",
    { method: "POST", body: JSON.stringify(entry) },
    token,
  );
}

/** Deletes a user node-library entry (builtin entries are read-only). */
export function deleteNodeLibrary(token: string | null, id: string) {
  return request<{ deleted: string }>(
    `/v1/node-library/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    token,
  );
}
