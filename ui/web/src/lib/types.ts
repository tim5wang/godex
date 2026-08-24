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

export interface AttachmentRef {
  id?: string;
  name?: string;
  mime_type?: string;
  path?: string;
  url?: string;
  size_bytes?: number;
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

export interface ChannelStatus {
  name: string;
  enabled: boolean;
  running: boolean;
  state?: "disabled" | "starting" | "running" | "restarting" | "stopped" | "error";
  detail?: string;
  updated_at: string;
  last_start_at?: string;
  last_stop_at?: string;
  last_poll_at?: string;
  last_inbound_at?: string;
  last_ack_at?: string;
  last_reply_at?: string;
  last_duplicate_at?: string;
  last_error?: string;
  last_event?: string;
  last_delivery?: DeliveryRecord;
  last_access?: AccessDecision;
  capabilities?: ChannelCapabilities;
}

export interface ChannelStatusReport {
  generated_at: string;
  channels: ChannelStatus[];
  deliveries?: DeliveryRecord[];
}

export interface ChannelCapabilities {
  delivery?: boolean;
  auth_login?: boolean;
  media?: boolean;
  streaming?: boolean;
  typing?: boolean;
  status?: boolean;
  allow_from?: boolean;
  session_modes?: string[];
}

export interface DeliveryRecord {
  id: string;
  target_kind?: string;
  channel?: string;
  session_id?: string;
  status: string;
  attempts: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
  delivered_at?: string;
  failed_at?: string;
}

export interface AccessDecision {
  action: "allow" | "deny" | "approval_required" | string;
  reason?: string;
  channel?: string;
  sender_id?: string;
  platform_id?: string;
  thread_id?: string;
  decided_at: string;
}

export interface WeixinAuthAccount {
  base_url?: string;
  cdn_base_url?: string;
  ilink_bot_id?: string;
  ilink_user_id?: string;
  updated_at?: string;
}

export interface WeixinAuthLogin {
  active: boolean;
  state: string;
  raw_status?: string;
  message?: string;
  qr_code?: string;
  qr_code_img_url?: string;
  qr_code_img_value?: string;
  started_at?: string;
  last_checked_at?: string;
  updated_at?: string;
}

export interface WeixinAuthStatus {
  account_id: string;
  enabled: boolean;
  configured: boolean;
  state_dir: string;
  account?: WeixinAuthAccount;
  login?: WeixinAuthLogin;
}

export interface DeliveryTarget {
  kind?: string;
  session_id?: string;
  channel?: string;
  session_key?: string;
  recipient?: string;
  metadata?: Record<string, string>;
}

export interface CronSchedule {
  type: string;
  at?: string;
  every_seconds?: number;
  cron_expr?: string;
}

export interface CronJob {
  id: string;
  name?: string;
  message: string;
  timezone?: string;
  schedule: CronSchedule;
  session_mode?: string;
  delivery_target?: DeliveryTarget;
  enabled: boolean;
  created_by?: string;
  created_from_session?: string;
  created_at?: string;
  updated_at?: string;
  last_run_at?: string;
  next_run_at?: string;
  last_status?: string;
  last_error?: string;
}

export interface CronRunLog {
  id: string;
  job_id: string;
  session_id?: string;
  turn_id?: string;
  status: string;
  error?: string;
  delivery_target?: DeliveryTarget;
  started_at?: string;
  finished_at?: string;
}

export interface HeartbeatRule {
  id: string;
  enabled: boolean;
  interval_seconds: number;
  timezone?: string;
  active_hours_start?: string;
  active_hours_end?: string;
  session_mode?: string;
  delivery_target?: DeliveryTarget;
  prompt_override?: string;
  created_by?: string;
  created_from_session?: string;
  created_at?: string;
  updated_at?: string;
  last_run_at?: string;
  next_run_at?: string;
  last_status?: string;
  last_error?: string;
}

export interface HeartbeatRunLog {
  id: string;
  rule_id: string;
  session_id?: string;
  turn_id?: string;
  status: string;
  error?: string;
  suppressed?: boolean;
  delivery_target?: DeliveryTarget;
  started_at?: string;
  finished_at?: string;
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

export interface AgentIdentity {
  id: string;
  name?: string;
  kind?: string;
  role?: string;
  parent_id?: string;
  session_id?: string;
  source?: string;
  created_at?: string;
  updated_at?: string;
  capability_summary?: string[];
  model_hint?: string;
  budget_hint?: string;
  display?: Record<string, string>;
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

export interface PackageEntry {
  name: string;
  version: string;
  description?: string;
  source: string;
  digest: string;
  path: string;
  installed_at: string;
  resources?: Record<string, string[]>;
  app?: PackageAppManifest;
  permissions?: string[];
  recommended_bundles?: string[];
  capabilities?: string[];
  provides?: string[];
  requires?: string[];
  runtime?: PackageRuntimeDecl;
  tool_policy?: string[];
  smoke_tests?: PackageSmokeTest[];
  trust: string;
}

export interface PackageRuntimeDecl {
  kind?: string;
  module?: string;
  abi?: string;
}

export interface PackageAppManifest {
  kind?: string;
  id?: string;
  label?: string;
  config?: Record<string, unknown>;
}

export interface PackageSmokeTest {
  name: string;
  command: string;
  working_dir?: string;
  timeout_seconds?: number;
  required_permissions?: string[];
  expected_exit_code?: number;
}

export interface PackageSmokeRun {
  run_id: string;
  package_name: string;
  smoke_name: string;
  session_id?: string;
  status: string;
  output?: string;
  artifact_paths?: string[];
  pending_approval?: boolean;
  request_id?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
}

export interface PackageQualityReport {
  generated_at: string;
  package_count: number;
  skill_count: number;
  prompt_count: number;
  command_count: number;
  role_count: number;
  high_risk_packages: number;
  tool_health: ToolHealthSummary;
  failure_reasons?: FailureReason[];
  packages: PackageQuality[];
}

export interface PackageQuality {
  name: string;
  version?: string;
  source: string;
  trust: string;
  digest?: string;
  resource_counts: Record<string, number>;
  app?: PackageAppManifest;
  app_issues?: string[];
  permissions?: string[];
  recommended_bundles?: string[];
  capabilities?: string[];
  provides?: string[];
  requires?: string[];
  tool_policy?: string[];
  capability_issues?: string[];
  tool_policy_issues?: string[];
  dependency_issues?: string[];
  command_diagnostics?: PackageContractDiagnostic[];
  role_diagnostics?: PackageContractDiagnostic[];
  smoke_checks?: PackageSmokeCheck[];
  smoke_runs?: PackageSmokeRun[];
  install_health?: string;
  upgrade_hint?: string;
  reinstall_available_hint?: string;
  unknown_bundles?: string[];
  manifest_issues?: string[];
  resource_issues?: string[];
  permission_issues?: string[];
  risk_level: "low" | "medium" | "high";
  score: number;
}

export interface PackageContractDiagnostic {
  type: string;
  name?: string;
  path?: string;
  issues?: string[];
  summary?: string[];
}

export interface PackageSmokeCheck {
  name: string;
  command?: string;
  working_dir?: string;
  status: string;
  issues?: string[];
  last_run?: PackageSmokeRun;
}

export interface ToolHealthSummary {
  inspected_sessions: number;
  total_runs: number;
  success_runs: number;
  failure_runs: number;
  success_rate: number;
  by_tool?: ToolStat[];
}

export interface ToolStat {
  name: string;
  total: number;
  success: number;
  failure: number;
  success_rate: number;
  last_failure?: string;
}

export interface FailureReason {
  reason: string;
  count: number;
}

export interface PromptEntry {
  package_name: string;
  name: string;
  path: string;
  content?: string;
}

export interface PackageCommandEntry {
  package_name: string;
  name: string;
  namespace?: string;
  description?: string;
  mode?: string;
  prompt_path?: string;
  prompt?: string;
  aliases?: string[];
  roles?: string[];
  write_scope?: string[];
  permissions?: string[];
  recommended_bundles?: string[];
  capabilities?: string[];
  tool_policy?: string[];
  path: string;
}

export interface PackageCommandDispatch {
  mode: string;
  prompt: string;
  package_name: string;
  namespace?: string;
  command_name: string;
  invocation?: string;
  args?: string[];
  agent_type?: string;
  write_scope?: string[];
  roles?: string[];
  permissions?: string[];
  recommended_bundles?: string[];
  capabilities?: string[];
  tool_policy?: string[];
}

// CommandMetadata mirrors commands.CommandMetadata on the backend
// (GET /commands) — the built-in slash-command list shared by TUI,
// ACP, and the web composer palette.
export interface CommandMetadata {
  name: string;
  description: string;
  input_hint?: string;
}

export interface CommandResult {
  name: string;
  output?: string;
  artifact_path?: string;
  refresh_snapshot?: boolean;
  dispatch?: PackageCommandDispatch;
  dispatch_status?: string;
  dispatch_error?: string;
  diagnostics?: string[];
  dispatched_turn_id?: string;
  dispatched_job_id?: string;
}

export interface PackageRoleEntry {
  package_name: string;
  id: string;
  name: string;
  description?: string;
  base_prompt?: string;
  default_bundles?: string[];
  tools?: string[];
  write_enabled?: boolean;
  capabilities?: string[];
  tool_policy?: string[];
  model_hint?: string;
  budget_hint?: string;
  display?: Record<string, string>;
  path: string;
}

export interface DurableSubagentProgress {
  timestamp?: string;
  phase?: string;
  message?: string;
  tool_id?: string;
  tool_name?: string;
  error?: string;
  result?: string;
  iteration?: number;
  max_turns?: number;
  model?: string;
  recovery_hint?: string;
}

export interface DurableSubagentFileChange {
  path: string;
  status: string;
  bytes?: number;
  binary?: boolean;
}

export interface DurableSubagentJob {
  job_id: string;
  session_id?: string;
  parent_turn_id?: string;
  identity?: AgentIdentity;
  identity_id?: string;
  agent_type?: string;
  role_id?: string;
  role_name?: string;
  package_name?: string;
  sequence?: number;
  objective?: string;
  display_title?: string;
  prompt?: string;
  status?: string;
  result?: string;
  error?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  merged_at?: string;
  write_scope?: string[];
  default_bundles?: string[];
  tool_policy?: string[];
  tool_names?: string[];
  worktree_dir?: string;
  isolation?: string;
  workspace_origin?: string;
  git_branch?: string;
  cleanup_state?: string;
  merge_status?: string;
  last_phase?: string;
  max_turns?: number;
  model_request_count?: number;
  tool_call_count?: number;
  last_runner_phase?: string;
  last_iteration?: number;
  last_recovery_hint?: string;
  context_budget?: number;
  last_message?: string;
  last_tool_id?: string;
  last_tool_name?: string;
  progress?: DurableSubagentProgress[];
}

export interface DurableSubagentReview {
  job_id: string;
  worktree_dir?: string;
  write_scope?: string[];
  changes: DurableSubagentFileChange[];
  diff?: string;
  diff_truncated?: boolean;
  conflicts?: string[];
}

export interface DurableSubagentMerge {
  job_id: string;
  status: string;
  applied?: DurableSubagentFileChange[];
  conflicts?: string[];
  worktree_dir?: string;
}

export interface LongTaskStory {
  id: string;
  node_id?: string;
  repair_attempts?: number;
  title?: string;
  description?: string;
  acceptance_criteria?: string[];
  priority?: number;
  status: string;
  passes: boolean;
  verdict?: string;
  job_id?: string;
  handoff_ref?: string;
  result_preview?: string;
  error?: string;
  validation_status?: string;
  validation_ref?: string;
  merge_status?: string;
  commit_status?: string;
  commit_hash?: string;
  commit_ref?: string;
  updated_at?: string;
}

export interface LongTaskRunSummary {
  status: string;
  iterations: number;
  max_iterations?: number;
  started?: string[];
  finalized?: string[];
  repaired?: Array<{ story_id: string; failed_node_id: string; repair_node_id: string; attempt: number; reason?: string }>;
  blocked_by?: string;
  message?: string;
}

export interface AgentGraphNode {
  id: string;
  node_type?: string;
  title?: string;
  status: string;
  agent_type?: string;
  write_scope?: string[];
  job_id?: string;
  attempt?: number;
  verdict?: string;
  handoff_ref?: string;
  error?: string;
}

export interface AgentGraphEdge {
  id: string;
  edge_type?: string;
  from: string;
  to: string;
  status?: string;
  verdict?: string;
}

export interface AgentGraphView {
  workflow_id: string;
  status: string;
  total: number;
  pending: number;
  running: number;
  completed: number;
  failed: number;
  nodes: AgentGraphNode[];
  edges: AgentGraphEdge[];
  started?: string[];
  appended?: string[];
}

export interface LongTaskView {
  longtask_id: string;
  workflow_id: string;
  project?: string;
  branch_name?: string;
  description?: string;
  quality_checks?: string[];
  status: string;
  total: number;
  pending: number;
  running: number;
  completed: number;
  failed: number;
  stories: LongTaskStory[];
  graph?: AgentGraphView;
  started?: string[];
  run?: LongTaskRunSummary;
}

export interface TurnRecord {
  id: string;
  status: string;
  source?: string;
  sender?: string;
  summary?: string;
  started_at: string;
  updated_at: string;
  completed_at?: string;
  pending_request_id?: string;
  error?: string;
  retry_of?: string;
  can_retry?: boolean;
  can_resume?: boolean;
  phase?: string;
  recovery_hint?: string;
  injection_count?: number;
  last_tool_name?: string;
}

export interface ProtocolBlock {
  type: "text" | "tool_use" | "tool_result";
  text?: string;
  id?: string;
  name?: string;
  input?: Record<string, unknown>;
  tool_use_id?: string;
  content?: string;
  is_error?: boolean;
}

export interface ProtocolMessage {
  role: string;
  content: ProtocolBlock[];
  metadata?: {
    kind?: string;
    ephemeral?: boolean;
    transcript?: string;
    source?: string;
    sender?: string;
    timestamp?: string;
    text?: string;
    attachments?: AttachmentRef[];
    app_object_type?: string;
    app_object_id?: string;
    app_object_title?: string;
  };
}

export type FeedItemKind = "user" | "assistant" | "background" | "tool" | "todo" | "subagent" | "command" | "warning" | "error";

export type FeedSegmentType = "text" | "tool" | "todo" | "subagent";

/**
 * A single ordered piece inside a grouped assistant turn. `text` segments carry
 * markdown in `text`; other kinds reference the original FeedItem via `item`.
 */
export interface FeedSegment {
  type: FeedSegmentType;
  text?: string;
  item?: FeedItem;
}

export interface SubagentProgressItem {
  timestamp?: string;
  phase?: string;
  status?: string;
  message?: string;
  toolName?: string;
  error?: string;
  result?: string;
  iteration?: number;
  maxTurns?: number;
  model?: string;
  recoveryHint?: string;
}

export interface FeedItem {
  id: string;
  sessionId?: string;
  kind: FeedItemKind;
  title: string;
  body: string;
  timestamp?: string;
  attachments?: AttachmentRef[];
  summary?: string;
  status?: string;
  turnId?: string;
  input?: Record<string, unknown>;
  output?: string;
  error?: string;
  todoItems?: TodoFeedItem[];
  todoStats?: TodoFeedStats;
  expanded?: boolean;
  jobId?: string;
  parentTurnId?: string;
  agentType?: string;
  roleId?: string;
  roleName?: string;
  identity?: AgentIdentity;
  identityId?: string;
  capabilitySummary?: string[];
  modelHint?: string;
  budgetHint?: string;
  contextBudget?: number;
  packageName?: string;
  sequence?: number;
  objective?: string;
  displayTitle?: string;
  prompt?: string;
  phase?: string;
  lastToolName?: string;
  lastMessage?: string;
  toolNames?: string[];
  createdAt?: string;
  updatedAt?: string;
  finishedAt?: string;
  worktreeDir?: string;
  isolation?: string;
  workspaceOrigin?: string;
  gitBranch?: string;
  cleanupState?: string;
  writeScope?: string[];
  mergeStatus?: string;
  maxTurns?: number;
  modelRequestCount?: number;
  toolCallCount?: number;
  lastRunnerPhase?: string;
  lastIteration?: number;
  lastRecoveryHint?: string;
  progress?: SubagentProgressItem[];
  /**
   * V2 grouped rendering: ordered segments for a merged assistant turn.
   * Only present on items produced by groupFeedItemsIntoTurns().
   */
  segments?: FeedSegment[];
  /**
   * V2 grouped rendering: the FINAL assistant text of the turn (the result),
   * used by copy/save-to-note so only the answer is captured, not the process.
   */
  finalBody?: string;
}

export interface TodoFeedItem {
  id?: number;
  content: string;
  status: string;
  activeForm?: string;
}

export interface TodoFeedStats {
  total: number;
  completed: number;
  inProgress: number;
  pending: number;
}

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
