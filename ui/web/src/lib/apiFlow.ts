import { request } from "./apiClient";

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
  kind: string; // step | llm | decision | human | branch | loop
  title?: string;
  prompt?: string;
  agent_type?: string;
  write_scope?: string[];
  retry?: FlowRetryPolicy;
  decision?: FlowDecisionSpec;
  branch?: FlowBranchSpec;
  human?: FlowHumanSpec;
  outputs?: { name: string; type?: string; desc?: string }[];
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

/** Drafts a Flow Spec v1 definition from a natural-language description via
 * the LLM (P2.5). The result is validated but NOT saved; the caller previews
 * and persists it through createFlow. */
export function generateFlowSpec(token: string | null, description: string) {
  return request<FlowDefinition>(
    "/v1/flows/generate",
    { method: "POST", body: JSON.stringify({ description }) },
    token,
  );
}
