import type { AgentIdentity, AttachmentRef, ProtocolMessage, TurnRecord } from "./typesAgent";

export * from "./typesAgent";
export * from "./typesChannels";
export * from "./typesPackages";
export * from "./typesProduct";

export type EventType =
  | "user_message_accepted"
  | "assistant_text_delta"
  | "assistant_thinking_delta"
  | "assistant_message_completed"
  | "model_request_completed"
  | "tool_call_started"
  | "tool_call_finished"
  | "todo_list_updated"
  | "warning_raised"
  | "error_raised"
  | "command_completed"
  | "skill_state_changed"
  | "history_recall_decision"
  | "subagent_job_updated"
  | "runner_phase_changed"
  | "message_injected"
  | "agent_identity_updated"
  | "snapshot_ready"
  | "turn_completed";

export interface RuntimeEvent {
  session_id?: string;
  turn_id?: string;
  type: EventType;
  timestamp: string;
  payload?: unknown;
}

export type SessionTimelineEntry = RuntimeEvent;

/** Payload of `assistant_message_completed` (also used by text deltas). */
export interface TextEventPayload {
  role?: string;
  text?: string;
  /** Full accumulated reasoning text of the completed assistant message. */
  thinking?: string;
}

/** Payload of `model_request_completed`: per-request usage + timing facts. */
export interface ModelRequestPayload {
  model?: string;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  started_at?: string;
  first_token_at?: string;
  completed_at?: string;
  duration_ms?: number;
  ttft_ms?: number;
  stop_reason?: string;
  error?: string;
}

export interface CompactionRecord {
  timestamp: string;
  before_tokens?: number;
  after_tokens?: number;
  reasons?: string[];
  source?: string;
  transcript_ref?: string;
}

export interface TimelinePage {
  items: SessionTimelineEntry[];
  next_cursor?: string;
  has_more: boolean;
  total: number;
}

export type MemoryType = "identity" | "user" | "workflow" | "project" | "warning" | "work_method" | "work_fact";

export interface MemoryRecord {
  id: string;
  title: string;
  file: string;
  summary: string;
  type: MemoryType;
  source?: string;
  created_at: string;
  updated_at: string;
  tags?: string[];
  fingerprint?: string;
  status?: "active" | "archived" | string;
  last_referenced_at?: string;
  content: string;
}

export interface MemoryCandidate {
  fingerprint: string;
  title: string;
  summary: string;
  content: string;
  memory_type: MemoryType;
  source?: string;
  created_at: string;
}

export interface MemorySuppression {
  fingerprint?: string;
  key?: string;
  source?: string;
  created_at: string;
  expires_at?: string;
}

export interface MemoryAuditLogEntry {
  id: string;
  action: string;
  memory_id?: string;
  title?: string;
  memory_type?: MemoryType;
  source?: string;
  candidate_fingerprint?: string;
  created_at: string;
  before?: MemoryRecord;
  after?: MemoryRecord;
  message?: string;
}

export interface MemoryDigestResult {
  candidates: MemoryCandidate[];
  report: string;
  report_path?: string;
}

export interface Note {
  id: string;
  title: string;
  summary?: string;
  tags?: string[];
  content: string;
  path: string;
  created_at: string;
  updated_at: string;
}

export interface MemoryContextItem extends MemoryRecord {
  score?: number;
}

export interface MemoryContextLayers {
  identity: MemoryContextItem[];
  core: MemoryContextItem[];
  relevant: MemoryContextItem[];
}

export interface ContextInspection {
  session_id?: string;
  message_count: number;
  token_estimate: number;
  history_token_estimate?: number;
  total_token_estimate?: number;
  token_breakdown?: ContextTokenBreakdown;
  prefix_cache?: PrefixCacheInspection;
  cache_usage?: CacheUsageInspection;
  compress_threshold: number;
  context_window_tokens?: number;
  retain_tokens?: number;
  suggest_compact: boolean;
  compression_reasons?: string[];
  pre_compaction_total?: number;
  post_compaction_total?: number;
  compaction_mode?: string;
  compaction_latency_ms?: number;
  largest_context_sources?: ContextSourcePressure[];
  active_skill_count: number;
  pending_permission_count: number;
  large_tool_result_reference_count?: number;
  tool_result_references?: ToolResultReference[];
  cumulative_tokens?: number;
  cumulative_input_tokens?: number;
  cumulative_output_tokens?: number;
}

export interface ContextSourcePressure {
  source: string;
  tokens: number;
}

export interface PrefixCacheInspection {
  system_hash?: string;
  tool_schemas_hash?: string;
  stable_prefix_hash?: string;
  stable_system_tokens: number;
  stable_tool_schema_tokens?: number;
  stable_memory_index_tokens?: number;
  dynamic_runtime_tokens: number;
  dynamic_section_tokens?: Record<string, number>;
}

export interface CacheUsageInspection {
  calls: number;
  input_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  hit_rate_percent: number;
}

export interface ContextTokenBreakdown {
  system: number;
  history: number;
  memory: number;
  runtime: number;
  tool_schemas: number;
  attachments: number;
  tool_results: number;
  total: number;
}

export interface ToolResultReference {
  tool_name?: string;
  tool_use_id?: string;
  bytes?: number;
  sha256?: string;
  artifact_path?: string;
}

export interface HistoryRecallDecisionSummary {
  allow_tool: boolean;
  automatic: boolean;
  explicit_request: boolean;
  recommended_scope?: string;
  score: number;
  reasons?: string[];
  timestamp?: string;
}

export interface SessionContextInspector {
  context: ContextInspection;
  transcript_ref_count: number;
  transcript_refs?: string[];
  recall_query?: string;
  memory_preview: MemoryContextLayers;
  history_recall?: HistoryRecallDecisionSummary | null;
}

export interface VersionInfo {
  version: string;
  commit?: string;
  date?: string;
  go_version: string;
  os: string;
  arch: string;
}

export interface MetaResponse {
  lead_name: string;
  model: string;
  workspace_dir: string;
  auth_required: boolean;
  version: VersionInfo;
  execution_mode?: string;
  ssh_target?: string;
  ssh_workspace?: string;
  ssh_options?: string[];
  docker_image?: string;
  docker_network?: string;
  voice_enabled?: boolean;
}

export interface ConfigMetaResponse {
  file_path: string;
  env_file: string;
  home_dir?: string;
  project_dir?: string;
  home_config_file?: string;
  project_config_file?: string;
  home_env_file?: string;
  project_env_file?: string;
  revision: number;
  last_apply?: ApplyReport;
}

export interface ApplyReport {
  applied_at?: string;
  storage_status?: "saved" | "save_failed";
  runtime_status?: "applied" | "applied_with_warnings" | "failed" | "skipped";
  message?: string;
  warnings?: string[];
  errors?: string[];
}

export interface ConfigFieldSchema {
  path: string;
  label: string;
  description: string;
  type: "string" | "int" | "float" | "bool" | "string_list" | "json";
  secret?: boolean;
  live_apply?: boolean;
  env?: string;
  options?: string[];
}

export interface ConfigSectionSchema {
  id: string;
  label: string;
  description?: string;
  fields: ConfigFieldSchema[];
}

export interface ConfigFieldState {
  source: "default" | "yaml" | "dotenv" | "env";
  overridden_by?: "default" | "yaml" | "dotenv" | "env";
  secret?: boolean;
  masked?: boolean;
  configured?: boolean;
  live_apply?: boolean;
  env?: string;
  deprecated_env?: string;
}

export interface ConfigView {
  file_path: string;
  env_file: string;
  home_dir?: string;
  project_dir?: string;
  home_config_file?: string;
  project_config_file?: string;
  home_env_file?: string;
  project_env_file?: string;
  revision: number;
  stored_values: Record<string, unknown>;
  effective_values: Record<string, unknown>;
  fields: Record<string, ConfigFieldState>;
  last_apply?: ApplyReport;
}

export interface RuntimeServiceStatus {
  name?: string;
  scope?: "user" | "system";
  os?: string;
  managed: boolean;
  installed?: boolean;
  running?: boolean;
  service_file?: string;
  log_file?: string;
  detail?: string;
}

export interface ControlNode {
  id: string;
  name: string;
  endpoint?: string;
  workspace_dir?: string;
  godex_home?: string;
  status: "online" | "offline" | string;
  version?: string;
  capabilities?: string[];
  metadata?: Record<string, string>;
  last_seen?: string;
  registered_at?: string;
  updated_at?: string;
  source?: string;
  relay_status?: string;
  last_health?: string;
  trust_level?: string;
}

export interface NodeSessionInfo {
  id: string;
  title?: string;
  running?: boolean;
  updated_at?: string;
}

export interface NodeJobInfo {
  id: string;
  name?: string;
  status?: string;
  phase?: string;
  turn?: number;
  total_turns?: number;
}

export interface NodeApprovalInfo {
  id: string;
  session_id?: string;
  intent?: string;
  status?: string;
}

export interface NodeStoredEvent {
  kind: string;
  time: string;
  detail?: string;
}

export interface NodeOverview {
  node_id: string;
  version?: string;
  capabilities?: string[];
  sessions?: NodeSessionInfo[];
  jobs?: NodeJobInfo[];
  approvals?: NodeApprovalInfo[];
  recent_events?: NodeStoredEvent[];
  last_health?: string;
  updated_at?: string;
}

export interface NodeOverviewResponse {
  node: ControlNode;
  overview: NodeOverview;
}

/** A managed TCP forward tunnel running inside the center process. */
export interface ForwardSpec {
  id: string;
  name?: string;
  node_id: string;
  local_port: number;
  target: string;
}

export interface ForwardStatus extends ForwardSpec {
  state: "running" | "error" | "stopped" | string;
  error?: string;
  active_conns: number;
  last_checked_at?: string;
  last_latency_ms?: number;
}

/** One leg of the end-to-end forward connectivity check. */
export interface ForwardCheckStep {
  name: string;
  ok: boolean;
  detail: string;
  latency_ms?: number;
}

export interface ForwardCheckResult {
  ok: boolean;
  steps: ForwardCheckStep[];
}

export interface DoctorCheck {
  severity: "error" | "warning" | "info";
  code: string;
  path?: string;
  message: string;
  suggestion?: string;
}

export interface DoctorReport {
  generated_at: string;
  home_dir?: string;
  project_dir?: string;
  home_config_file?: string;
  project_config_file?: string;
  home_env_file?: string;
  project_env_file?: string;
  errors: number;
  warnings: number;
  infos: number;
  checks: DoctorCheck[];
  last_apply?: ApplyReport;
}

export interface ProviderStatus {
  id: string;
  name?: string;
  type: string;
  base_url?: string;
  api_key_env?: string;
  credential_kind?: string;
  oauth_provider?: string;
  account_id?: string;
  oauth_mode?: string;
  has_credential: boolean;
  token_present: boolean;
  masked_credential?: string;
  last_test_error?: string;
}

export interface ProviderListResponse {
  providers: ProviderStatus[];
}

export interface ProviderTestResponse {
  status: ProviderStatus;
  ok: boolean;
  error?: string;
}

export interface ProviderModelInfo {
  id: string;
  name?: string;
  model: string;
  supports_streaming?: boolean;
}

export interface ProviderModelsResponse {
  provider_id: string;
  models?: ProviderModelInfo[];
  ok: boolean;
  error?: string;
}

export interface SkillCompatibility {
  status: string;
  missing_capabilities?: string[];
  missing_dependencies?: string[];
  notes?: string[];
}

export interface SkillCatalogEntry {
  id: string;
  name: string;
  description: string;
  when_to_use?: string[];
  argument_hint?: string;
  version?: string;
  categories?: string[];
  paths?: string[];
  recommended_bundles?: string[];
  sections?: string[];
  warnings?: string[];
  compatibility: SkillCompatibility;
  path?: string;
  install_memory?: SkillInstallMemory;
  normalization_status?: "not_needed" | "suggested" | "normalized" | "unavailable" | string;
  normalization_source?: string;
  normalized?: boolean;
  needs_normalization?: boolean;
  can_normalize?: boolean;
  skill_kind?: "root_skill" | "suite_root" | "child_skill" | string;
  suite_id?: string;
  child_skill_count?: number;
  child_skill_ids?: string[];
  child_skill_hint?: string;
}

export interface SkillSourceEntry {
  id: string;
  name: string;
  summary: string;
  source: string;
  skill_name?: string;
  tags?: string[];
  categories?: string[];
  version?: string;
  trust?: string;
  origin?: string;
  installs?: number;
  warnings?: string[];
  install_supported: boolean;
  install_source?: string;
  install_name?: string;
  install_reason?: string;
  installed: boolean;
  installed_path?: string;
  install_memory?: SkillInstallMemory;
}

export interface SkillInstallMemory {
  source: string;
  source_entry_id?: string;
  source_origin?: string;
  trust?: string;
  version?: string;
  categories?: string[];
  installed_at?: string;
}

export interface SkillRemoveResult {
  id: string;
  name: string;
  status: string;
  removed_path: string;
  was_active?: boolean;
}

export interface SkillActivation {
  id: string;
  name: string;
  status: string;
  description?: string;
  loaded_sections?: string[];
  available_sections?: string[];
  recommended_bundles?: string[];
  compatibility: SkillCompatibility;
  skill_kind?: "root_skill" | "suite_root" | "child_skill" | string;
  suite_id?: string;
  child_skill_count?: number;
  child_skill_ids?: string[];
  child_skill_hint?: string;
}

export interface SkillExpansion {
  id: string;
  name: string;
  status: string;
  expanded_sections?: string[];
  loaded_sections?: string[];
  available_sections?: string[];
  recommended_bundles?: string[];
  compatibility: SkillCompatibility;
}

export interface SkillInstallResult {
  id: string;
  name: string;
  status: string;
  source: string;
  source_origin?: string;
  trust?: string;
  version?: string;
  categories?: string[];
  installed_path: string;
  description?: string;
  sections?: string[];
  recommended_bundles?: string[];
  warnings?: string[];
  compatibility: SkillCompatibility;
  install_memory?: SkillInstallMemory;
}

export interface SessionLocator {
  channel: string;
  key?: string;
  user_id?: string;
  metadata?: Record<string, string>;
}

export interface ListedSession {
  session_id: string;
  locator: SessionLocator;
  title?: string;
  model_profile_id?: string;
  parent_session_id?: string;
  forked_from_turn_id?: string;
  forked_from_message_index?: number;
  branch_title?: string;
  created_at: string;
  updated_at: string;
  last_activity_at: string;
  running?: boolean;
}

export interface PermissionRequest {
  session_id?: string;
  source?: string;
  sender?: string;
  tool_name: string;
  action?: string;
  paths?: string[];
  command?: string;
  mutation?: boolean;
  input?: Record<string, unknown>;
}

export interface PendingPermission {
  id: string;
  request: PermissionRequest;
  reason?: string;
  created_at: string;
}

export interface PermissionResolution {
  request_id: string;
  decision: "allow" | "deny" | "pending" | "abstain";
  scope?: "once" | "session";
  reason?: string;
  request: PermissionRequest;
  resolved_at: string;
  resumed?: boolean;
  resume_status?: string;
  resume_output?: string;
  resume_pending_request_id?: string;
  resume_error?: string;
}

export interface Snapshot {
  session_id: string;
  locator: SessionLocator;
  messages: ProtocolMessage[];
  display_messages?: ProtocolMessage[];
  timeline?: SessionTimelineEntry[];
  turns?: TurnRecord[];
  queued_turns?: QueuedTurn[];
  pending_permissions?: PendingPermission[];
  running: boolean;
  active_turn_id?: string;
  active_phase?: string;
  identity?: AgentIdentity;
  model_profile_id?: string;
  updated_at: string;
}

export interface QueuedTurn {
  id: string;
  mode: "follow_up" | "steering";
  status: string;
  source?: string;
  sender?: string;
  summary?: string;
  created_at: string;
  updated_at: string;
}

export interface ModelProfile {
  id: string;
  name: string;
  provider: string;
  provider_name?: string;
  model: string;
  base_url: string;
  max_tokens: number;
  timeout_seconds: number;
  supports_streaming: boolean;
  supports_vision: boolean;
  reasoning_effort?: string;
  default?: boolean;
  selected?: boolean;
}

export interface ModelsView {
  default_profile_id: string;
  session_profile_id?: string;
  reasoning_effort?: string;
  acp_model?: string;
  profiles: ModelProfile[];
}

export interface SecurityEvent {
  id: string;
  at: string;
  category: string;
  action: string;
  severity?: string;
  session_id?: string;
  source?: string;
  summary?: string;
  metadata?: Record<string, string>;
}

export interface RiskSummary {
  axis: string;
  level: string;
  score: number;
  items?: string[];
  advice?: string[];
}

export interface CIKSummary {
  generated_at: string;
  policy: Record<string, unknown>;
  capability: RiskSummary;
  identity: RiskSummary;
  knowledge: RiskSummary;
  recent?: SecurityEvent[];
}
