export interface AttachmentRef {
  id?: string;
  name?: string;
  mime_type?: string;
  path?: string;
  url?: string;
  size_bytes?: number;
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
  /**
   * Snapshot-source message index of this feed item (the N in the synthetic
   * `msg-N` turnId). Used to fork at a historical turn: the grouped turn
   * tracks the max index and forks with message_index = max+1 so the new
   * session ends at that turn's completed state.
   */
  messageIndex?: number;
  /** Fork point for a grouped historical turn (max messageIndex + 1). */
  forkMessageIndex?: number;
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

