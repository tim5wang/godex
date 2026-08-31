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

