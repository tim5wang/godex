// ---- Usage Gateway Types ----

export interface UsageKey {
  id: string;
  name: string;
  key_prefix: string;
  key_hash?: string;
  enabled: boolean;
  budget_credits: number;
  warning_threshold: number;
  allowed_models: string[];
  created_at: string;
  updated_at: string;
}

export interface UsageKeyCreateResponse {
  key: UsageKey;
  secret: string;
}

export interface UsageModelMapping {
  id: string;
  public_model: string;
  target_profile_id: string;
  target_model: string;
  credit_weight: number;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface UsageSummary {
  period?: string;
  api_key_id?: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  billable_tokens: number;
  credits: number;
  call_count: number;
  error_count: number;
}

export interface UsageCall {
  id: string;
  timestamp: string;
  api_key_id: string;
  public_model: string;
  target_profile_id: string;
  target_model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  billable_tokens: number;
  credit_weight: number;
  credits: number;
  estimated: boolean;
  status: string;
  error?: string;
  source_channel?: string;
  session_id?: string;
  turn_id?: string;
  job_id?: string;
  error_code?: string;
  latency_ms: number;
}

export interface UsageTimeSeriesPoint {
  bucket: string;
  call_count: number;
  error_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  billable_tokens: number;
  credits: number;
  avg_latency_ms: number;
}

export interface ModelTokenUsage {
  model: string;
  call_count: number;
  input_tokens: number;
  output_tokens: number;
}

export interface SessionUsageSummary {
  session_id: string;
  call_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  billable_tokens: number;
  credits: number;
  first_call: string;
  last_call: string;
  model_usage: ModelTokenUsage[];
}

export interface CacheStats {
  period: string;
  model: string;
  total_calls: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  hit_rate: number;
  tokens_saved: number;
}

// ---- Agent Step Platform: Business Agents (BizKey) Types ----

export interface ProviderRef {
  name: string;
  url?: string;
  token_ref?: string;
}

export interface BizKey {
  id: string;
  name: string;
  description?: string;
  default_prompt?: string;
  template_id?: string;
  key_hash?: string;
  key_prefix: string;
  enabled: boolean;
  mcp_servers: string[];
  providers: ProviderRef[];
  sandbox_tools: string[];
  skills?: string[];
  packages?: string[];
  models: string[];
  project_dir?: string;
  budget_credits: number;
  warning_threshold: number;
  created_at: string;
  updated_at: string;
}

export interface BizKeyCreateResponse {
  key: BizKey;
  secret: string;
}

// ---- MCP Server Registry Types ----

export type MCPServerType = "filesystem" | "stdio" | "streamable-http";

export interface MCPServerConfig {
  name: string;
  type: MCPServerType;
  root?: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  session_required?: boolean;
}

export interface MCPRegistryResponse {
  servers: MCPServerConfig[];
}

export interface MCPServerStatus {
  name: string;
  type: MCPServerType;
  online: boolean;
  error?: string;
  tools?: number;
  checked_at: string;
}

export interface MCPStatusResponse {
  statuses: MCPServerStatus[];
}

// ---- Taskboard (需求池 #1) ----

export type TaskboardUrgency = "urgent" | "normal" | "low";
export type TaskboardStatus = "backlog" | "todo" | "in_progress" | "in_review" | "done";

export interface TaskboardChecklistItem {
  text: string;
  done: boolean;
  evidence?: string;
}

export interface TaskboardComment {
  author: string;
  text: string;
  created_at: string;
}

export interface TaskboardExecution {
  id: string;
  session_id: string;
  status: string;
  started_at: string;
  ended_at?: string;
  summary?: string;
  /** Isolated execution session's own id — the run's messages and timeline
   * live here; primary jump-to-progress target. */
  job_session_id?: string;
  /** Hosting session (jump-to-progress: subagent timeline in that chat). */
  host?: {
    session_id: string;
    channel?: string;
    key?: string;
    user_id?: string;
    /** Part of the session identity hash — required to reopen the session. */
    project_dir?: string;
  };
  /** Where the run is currently stuck / what it is doing (thinking / tool_call
   * / waiting_approval / error / idle). Written by the exec observability path. */
  stage?: string;
  /** Coarse failure bucket (provider / tool / cancelled / interrupted / unknown). */
  error_type?: string;
  /** Last failure detail text surfaced to the PJM without opening the session. */
  last_error?: string;
  /** Last tool the run invoked. */
  last_tool?: string;
  updated_at?: string;
}

export interface TaskboardExecutionObservation {
  stage?: string;
  error_type?: string;
  last_error?: string;
  last_tool?: string;
}

export interface TaskboardReconcileResult {
  card_id: string;
  card_title: string;
  execution_id: string;
  stage?: string;
  error_type?: string;
  last_tool?: string;
  last_error?: string;
  stall?: boolean;
  stall_reason?: string;
  action: string;
}

export interface TaskboardCardConsistency {
  card_id: string;
  card_title: string;
  field: string;
  problem: string;
  suggested: string;
}

export interface TaskboardReconcileReport {
  scanned: number;
  observed: number;
  finalized: number;
  stalled: number;
  started_at?: string;
  duration?: number;
  signals?: TaskboardCardConsistency[];
  results?: TaskboardReconcileResult[];
}

export interface TaskboardCard {
  id: string;
  project_id: string;
  title: string;
  description?: string;
  prompt?: string;
  urgency: TaskboardUrgency;
  status: TaskboardStatus;
  template_id?: string;
  touched_paths?: string[];
  observed_paths?: string[];
  research?: TaskboardResearch;
  merge_report?: { conflicts: TaskboardPathConflict[] };
  holder?: string;
  blocked?: boolean;
  checklist?: TaskboardChecklistItem[];
  comments?: TaskboardComment[];
  executions?: TaskboardExecution[];
  version: number;
  created_by: string;
  updated_by: string;
  created_at: string;
  updated_at: string;
  deleted?: boolean;
}

export interface TaskboardPathConflict {
  path: string;
  other_path: string;
  other_card: string;
  other_title: string;
}

export interface TaskboardResearch {
  facts?: string[];
  locations?: string[];
  excluded_paths?: string[];
  open_questions?: string[];
}

export interface TaskboardProject {
  id: string;
  name: string;
  work_dirs?: string[];
  root_dir?: string;
  built_in?: boolean;
}

export interface TaskboardCardCreateInput {
  project_id?: string;
  work_dir?: string;
  title: string;
  description?: string;
  prompt?: string;
  urgency?: TaskboardUrgency;
  template_id?: string;
  touched_paths?: string[];
  research?: TaskboardResearch;
  checklist?: string[];
}

export interface TaskboardCardPatchInput {
  action: "update" | "move" | "complete" | "reject" | "checklist" | "comment";
  version: number;
  actor?: string;
  title?: string;
  description?: string;
  prompt?: string;
  urgency?: TaskboardUrgency;
  blocked?: boolean;
  template_id?: string;
  touched_paths?: string[];
  checklist?: string[];
  research?: TaskboardResearch;
  to?: TaskboardStatus;
  force?: boolean;
  reason?: string;
  // checklist action fields
  check_action?: "add" | "check" | "uncheck";
  index?: number;
  text?: string;
  evidence?: string;
}

export interface TaskboardProjectCreateInput {
  name: string;
  root_dir?: string;
  work_dirs?: string[];
}

// ---- LLM Capture (request/response jsonl dump) ----

export interface LlmCaptureStatus {
  enabled: boolean;
  dump_path: string;
}

export interface LlmCaptureSummary {
  id: string;
  timestamp: string;
  session_id?: string;
  turn_id?: string;
  model?: string;
  stream: boolean;
  latency_ms: number;
  error?: string;
  has_response: boolean;
  input_tokens: number;
}

export interface LlmCaptureRecord {
  id: string;
  timestamp: string;
  session_id?: string;
  turn_id?: string;
  job_id?: string;
  channel?: string;
  model?: string;
  stream: boolean;
  latency_ms: number;
  error?: string;
  request: unknown;
  response?: unknown;
}

