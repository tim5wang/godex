import { request } from "./apiClient";
import type { TaskboardCard } from "./typesProduct";

// Work Context Desktop — long-running work streams (design: docs/work-context-desktop-design.md,
// WD1 personal SQLite persistence). Types mirror internal/core/workcontext/types.go; all JSON is
// snake_case. Every access is scoped server-side by tenant+owner derived from the caller's
// principal, so the client never sends tenant/owner and never assumes cross-tenant visibility.

export type WorkContextStatus = "active" | "paused" | "archived";
export type RecallScope = "current" | "related" | "all";

export interface WorkCheckpoint {
  summary: string;
  completed: string[];
  in_progress: string[];
  next_actions: string[];
  blockers: string[];
  open_questions: string[];
  evidence_refs: string[];
  revision: number;
  created_at: string;
}

export interface WorkContext {
  id: string;
  title: string;
  background: string;
  goal: string;
  status: WorkContextStatus | string;
  version: number;
  checkpoint: WorkCheckpoint;
  created_at: string;
  updated_at: string;
}

export interface WorkResource {
  id: string;
  title: string;
  kind: string;
  locator: string;
  node_id: string;
  purpose: string;
  context_ids: string[];
  created_at: string;
}

export interface WorkSessionLink {
  id: string;
  session_id: string;
  topic: string;
  created_at: string;
}

export interface WorkRelation {
  id: string;
  from_id: string;
  to_id: string;
  kind: string;
  condition: string;
  created_at: string;
}

export interface WorkFact {
  valid_until?: string;
  id: string;
  context_id: string;
  title: string;
  content: string;
  kind: string;
  status: string;
  scope: string;
  subject: string;
  predicate: string;
  source: string;
  source_excerpt: string;
  source_revision: string;
  observed_at: string;
  verified_at: string;
  provenance: string;
  supersedes_id: string;
  version: number;
  created_at: string;
  memory_candidate_id: string;
}

export interface WorkEvent {
  id: string;
  context_id: string;
  kind: string;
  title: string;
  source: string;
  created_at: string;
}

export interface WorkDelegation {
  id: string;
  request_id: string;
  card_id: string;
  project_id: string;
  title: string;
  pack: string;
  state: string;
  created_at: string;
  // Enriched by the detail endpoint: the live taskboard card read from the ledger, and
  // an explicit flag when the card is gone so the desktop never fabricates progress.
  card?: TaskboardCard | null;
  source_missing?: boolean;
}

export interface WorkContextDetail {
  context: WorkContext;
  resources: WorkResource[];
  sessions: WorkSessionLink[];
  delegations: WorkDelegation[];
  facts: WorkFact[];
  relations: WorkRelation[];
  events: WorkEvent[];
  todos: WorkTodo[];
}

// WorkTodo is a first-class plan item: unlike the free-text checkpoint lists it
// carries status, an owner and a due time, and is edited in place under a version.
export type WorkTodoStatus = "todo" | "doing" | "done" | "blocked";
export interface WorkTodo {
  id: string;
  context_id: string;
  text: string;
  status: WorkTodoStatus;
  owner: string;
  due_at: string;
  source: string;
  position: number;
  version: number;
  created_at: string;
  updated_at: string;
}
export interface WorkTodoInput {
  text: string;
  status?: WorkTodoStatus;
  owner?: string;
  due_at?: string;
  source?: string;
}
export interface WorkTodoPatch {
  text?: string;
  status?: WorkTodoStatus;
  owner?: string;
  due_at?: string;
  position?: number;
  if_version: number;
}

// WorkFocus is what a viewer should notice: the plan items plus the events
// recorded since the stream was last seen (LastSeenAt empty means never).
export interface WorkFocus {
  last_seen_at: string;
  changes: WorkEvent[];
  todos: WorkTodo[];
}

export interface WorkContextBrief {
  context: WorkContext;
  events: WorkEvent[];
  facts: WorkFact[];
  delegations: WorkDelegation[];
  // Production state is not verified by this deployment; the flag is carried explicitly
  // rather than inferred so the UI can state it instead of pretending a live query ran.
  production_verified: boolean;
}

export interface WorkContextInput {
  title: string;
  background?: string;
  goal: string;
}

export interface WorkContextPatch {
  title?: string;
  background?: string;
  goal?: string;
  status?: WorkContextStatus;
  if_version: number;
}

export interface CheckpointInput {
  summary: string;
  completed?: string[];
  in_progress?: string[];
  next_actions?: string[];
  blockers?: string[];
  open_questions?: string[];
  evidence_refs?: string[];
}

export interface WorkResourceInput {
  // id is optional: set it to update an existing (possibly shared) resource in
  // place instead of creating a duplicate.
  id?: string;
  title: string;
  kind: string;
  locator: string;
  node_id?: string;
  purpose?: string;
}

export interface WorkRelationInput {
  to_id: string;
  kind: string;
  condition?: string;
}

export interface WorkFactInput {
  valid_until?: string;
  title: string;
  content: string;
  kind?: string;
  scope?: string;
  subject?: string;
  predicate?: string;
  source?: string;
  source_excerpt?: string;
  source_revision?: string;
  observed_at?: string;
  provenance?: string;
  supersedes_id?: string;
}

export interface WorkSessionInput {
  topic: string;
  workspace_dir?: string;
  session_id?: string;
}

export interface WorkDelegationInput {
  request_id: string;
  title: string;
  description?: string;
  acceptance?: string[];
  project_id?: string;
  work_dir?: string;
  template_id?: string;
}

export interface WorkDelegationResult {
  delegation: WorkDelegation;
  card: TaskboardCard;
}

// listWorkContexts returns the caller's desktop streams (empty on a fresh install — an
// empty database is a normal first-run state, never seeded with demo data).
export function listWorkContexts(token: string | null) {
  return request<WorkContext[]>("/v1/work-contexts", { method: "GET" }, token);
}

/**
 * WorkSessionIndex is every stream's linked conversations in ONE call, plus two things
 * the desktop list needs per stream but cannot get from the unread count: the newest
 * conversation turn (so a card can say WHAT moved) and the seen watermark (so it can tell
 * a NEW finished conversation from one already read).
 */
export interface WorkSessionIndex {
  sessions: Record<string, WorkSessionLink[]>;
  last_turn: Record<string, WorkEvent>;
  seen: Record<string, string>;
}

/**
 * getWorkContextSessionIndex answers, for every stream at once: which conversations are
 * linked, did one just finish, and has the owner seen it. One request instead of an
 * events read per stream, which is what makes "is one of my streams' conversations
 * running/finished?" answerable while the desktop sits in the background.
 */
export function getWorkContextSessionIndex(token: string | null) {
  return request<WorkSessionIndex>("/v1/work-context-sessions", { method: "GET" }, token);
}

export function createWorkContext(token: string | null, input: WorkContextInput) {
  return request<WorkContext>(
    "/v1/work-contexts",
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export function getWorkContext(token: string | null, id: string) {
  return request<WorkContextDetail>(
    `/v1/work-contexts/${encodeURIComponent(id)}`,
    { method: "GET" },
    token,
  );
}

export function patchWorkContext(token: string | null, id: string, patch: WorkContextPatch) {
  return request<WorkContext>(
    `/v1/work-contexts/${encodeURIComponent(id)}`,
    { method: "PATCH", body: JSON.stringify(patch) },
    token,
  );
}

export function listCheckpoints(token: string | null, id: string) {
  return request<WorkCheckpoint[]>(
    `/v1/work-contexts/${encodeURIComponent(id)}/checkpoints`,
    { method: "GET" },
    token,
  );
}

export function saveCheckpoint(
  token: string | null,
  id: string,
  ifVersion: number,
  checkpoint: CheckpointInput,
) {
  return request<WorkContext>(
    `/v1/work-contexts/${encodeURIComponent(id)}/checkpoints`,
    { method: "POST", body: JSON.stringify({ if_version: ifVersion, checkpoint }) },
    token,
  );
}

export function getWorkContextBrief(token: string | null, id: string) {
  return request<WorkContextBrief>(
    `/v1/work-contexts/${encodeURIComponent(id)}/brief`,
    { method: "GET" },
    token,
  );
}

export function listWorkResources(token: string | null) {
  return request<WorkResource[]>("/v1/work-context-resources", { method: "GET" }, token);
}

export function addWorkResource(token: string | null, contextId: string, input: WorkResourceInput) {
  return request<WorkResource>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/resources`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export function removeWorkResource(token: string | null, contextId: string, resourceId: string) {
  return request<{ deleted: boolean }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/resources/${encodeURIComponent(resourceId)}`,
    { method: "DELETE" },
    token,
  );
}

export function addWorkRelation(token: string | null, contextId: string, input: WorkRelationInput) {
  return request<WorkRelation>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/relations`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export function removeWorkRelation(token: string | null, contextId: string, relationId: string) {
  return request<{ deleted: boolean }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/relations/${encodeURIComponent(relationId)}`,
    { method: "DELETE" },
    token,
  );
}

export function createWorkFact(token: string | null, contextId: string, input: WorkFactInput) {
  return request<WorkFact>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/facts`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export function reviewWorkFact(
  token: string | null,
  contextId: string,
  factId: string,
  status: string,
  ifVersion: number,
  evidence?: { observed_at: string; source: string },
) {
  return request<WorkFact>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/facts/${encodeURIComponent(factId)}`,
    { method: "PATCH", body: JSON.stringify({ status, if_version: ifVersion, ...evidence }) },
    token,
  );
}

export function recallWorkFacts(
  token: string | null,
  contextId: string,
  scope: RecallScope,
  query: string,
) {
  const params = new URLSearchParams({ scope });
  if (query.trim()) params.set("q", query);
  return request<WorkFact[]>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/recall?${params.toString()}`,
    { method: "GET" },
    token,
  );
}

// promoteWorkFactMemoryCandidate submits an existing candidate to the durable-memory
// candidate inbox. It is an explicit human action and does not write durable memory
// directly (design §5.3).
export function promoteWorkFactMemoryCandidate(token: string | null, contextId: string, factId: string) {
  return request<WorkFact>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/facts/${encodeURIComponent(factId)}/memory-candidate`,
    { method: "POST" },
    token,
  );
}

export function linkWorkSession(token: string | null, contextId: string, input: WorkSessionInput) {
  return request<WorkSessionLink>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/sessions`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export function unlinkWorkSession(token: string | null, contextId: string, sessionId: string) {
  return request<{ deleted: boolean }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/sessions/${encodeURIComponent(sessionId)}`,
    { method: "DELETE" },
    token,
  );
}

export function createWorkDelegation(token: string | null, contextId: string, input: WorkDelegationInput) {
  return request<WorkDelegationResult>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/delegations`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export interface WorkMemoryState {
  source_seq: number;
  processed_seq: number;
  summary: string;
  enabled: boolean;
  interval_seconds: number;
  timeout_seconds?: number;
  next_run_at: string;
  status: string;
  last_error?: string;
  run_id: string;
  lease_until?: string;
  rescan_cursor: number;
}

export interface WorkMemoryRun {
  id: string;
  status: string;
  created_at: string;
  error?: string;
  result?: { summary?: string; duplicates?: string[][]; conflicts?: string[][]; candidates?: Array<{ title: string; content: string }>; references?: Array<{ title: string; content: string; source_fact_ids: string[] }>; mutations?: Array<{ before: WorkFact; after_status: string; reason: string; canonical_id?: string }>; metrics?: { model: string; model_ms: number; input_bytes: number; timeout_seconds: number }; generated_fact_ids?: string[]; skipped_candidates?: Array<{ title: string; content: string; source_fact_ids: string[]; reason: string }> };
}

export interface WorkMemoryIssue { id: string; facts: WorkFact[]; created_at: string }
export function getWorkMemoryIssues(token: string | null, contextId: string) {
  return request<WorkMemoryIssue[]>(`/v1/work-contexts/${encodeURIComponent(contextId)}/memory/issues`, { method: "GET" }, token);
}
export function resolveWorkMemoryIssue(token: string | null, contextId: string, issue: WorkMemoryIssue, keepId: string) {
  return request(`/v1/work-contexts/${encodeURIComponent(contextId)}/memory/issues/${encodeURIComponent(issue.id)}/resolve`, { method: "POST", body: JSON.stringify({ keep_id: keepId, versions: Object.fromEntries(issue.facts.map((f) => [f.id, f.version])) }) }, token);
}

// Maintenance is a scoped background job; it never creates a user chat turn.
export function organizeWorkContext(token: string | null, contextId: string, force = false) {
  return request<{ queued: boolean; reason?: string; job_id?: string; status: string }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/organize`,
    { method: "POST", body: JSON.stringify({ force }) }, token,
  );
}
export function getWorkMemory(token: string | null, contextId: string) {
  return request<WorkMemoryState>(`/v1/work-contexts/${encodeURIComponent(contextId)}/memory`, { method: "GET" }, token);
}
export function updateWorkMemorySchedule(token: string | null, contextId: string, enabled: boolean, interval_seconds: number, timeout_seconds?: number) {
  return request<WorkMemoryState>(`/v1/work-contexts/${encodeURIComponent(contextId)}/memory`, { method: "PATCH", body: JSON.stringify({ enabled, interval_seconds, timeout_seconds }) }, token);
}
export function getWorkMemoryRuns(token: string | null, contextId: string) {
  return request<WorkMemoryRun[]>(`/v1/work-contexts/${encodeURIComponent(contextId)}/memory/runs`, { method: "GET" }, token);
}
export function revertWorkMemoryRun(token: string | null, contextId: string, runId: string) {
  return request<WorkMemoryState>(`/v1/work-contexts/${encodeURIComponent(contextId)}/memory/runs/${encodeURIComponent(runId)}/revert`, { method: "POST" }, token);
}

// --- plan items (todos) ---

export function listWorkTodos(token: string | null, contextId: string) {
  return request<WorkTodo[]>(`/v1/work-contexts/${encodeURIComponent(contextId)}/todos`, { method: "GET" }, token);
}

export function createWorkTodo(token: string | null, contextId: string, input: WorkTodoInput) {
  return request<WorkTodo>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/todos`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

export function patchWorkTodo(token: string | null, contextId: string, todoId: string, patch: WorkTodoPatch) {
  return request<WorkTodo>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/todos/${encodeURIComponent(todoId)}`,
    { method: "PATCH", body: JSON.stringify(patch) },
    token,
  );
}

export function deleteWorkTodo(token: string | null, contextId: string, todoId: string) {
  return request<{ deleted: boolean }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/todos/${encodeURIComponent(todoId)}`,
    { method: "DELETE" },
    token,
  );
}

// --- focus / seen ---

// getWorkFocus returns the plan items and what changed since the stream was last
// seen. It is read-only: the watermark is only advanced by markWorkContextSeen.
export function getWorkFocus(token: string | null, contextId: string) {
  return request<WorkFocus>(`/v1/work-contexts/${encodeURIComponent(contextId)}/focus`, { method: "GET" }, token);
}

export function markWorkContextSeen(token: string | null, contextId: string) {
  return request<{ previous_last_seen_at: string }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/seen`,
    { method: "POST" },
    token,
  );
}

// AttentionItem is one actionable line in the cross-stream inbox. It carries the
// tab that resolves it, so the desktop can jump straight to where the work is
// done instead of to the stream's default tab.
export type AttentionPriority = "high" | "normal" | "low";
// AttentionEntry is ONE entry inside a grouped inbox row.
//
// It exists so a verdict can be scoped to a single entry ("ignore this one, not all
// three") without weakening the group-level verbs. Key is empty when the kind has no
// stable entity id — the stream's own recorded blockers/open questions are free text
// inside the checkpoint, so they can only be ruled on as a group.
export interface AttentionEntry {
  key?: string;
  title: string;
  detail?: string;
  due_at?: string;
  /** The verdict recorded on THIS entry (not on the row), when there is one. */
  action?: AttentionAction;
}

export interface AttentionItem {
  kind: string;
  priority: AttentionPriority;
  /** Group-scoped identity a verb is stored against, so the UI acts on exactly the
   *  row it renders. */
  item_key: string;
  /** The owner's recorded verb on this row, when there is one. A claim keeps the
   *  row actionable, so it stays listed carrying its state. */
  action?: AttentionAction;
  /** The individual entries a grouped row stands for. Present only when the row
   *  actually collapsed more than one, and what makes a per-entry verdict possible. */
  entries?: AttentionEntry[];
  context_id: string;
  context_title: string;
  title: string;
  detail: string;
  due_at?: string;
  tab: string;
  /** How many same-kind entries this row stands for; omitted means one. */
  count?: number;
  /** Other streams waiting on this one (dependency leverage). */
  blocked_count?: number;
  /** Collapsed titles of the grouped entries, capped by the server. */
  also?: string[];
}

// AttentionMetrics is the attention ledger: how much of today's notification budget
// has been spent and how much unread work is open. Every number is derived from data
// the inbox already reads, so it needs no second source of truth.
export interface AttentionMetrics {
  nudges_today: number;
  owner_cap: number;
  per_stream_cap: number;
  unread_total: number;
  aged_streams: number;
  /** Decisions the human made today (each verb counts once). The denominator of
   *  the attention-yield ratio. */
  actions_today: number;
  /** Plan items the owner finished today. The numerator of the yield ratio. */
  completed_today: number;
}

// Attention verbs the owner can apply to an inbox row. A row holds at most ONE of
// these at a time (the server replaces rather than stacks them).
export type AttentionVerb = "claim" | "snooze" | "dismiss";

export interface AttentionAction {
  /** Group-scoped identity of the row this verdict belongs to ("{kind}:{context_id}"). */
  item_key: string;
  context_id: string;
  kind: string;
  verb: AttentionVerb;
  /** When a snooze stops applying; empty for verbs that do not expire. */
  until_at?: string;
  /** Required for dismiss: a reason is what makes hiding a group undoable. */
  reason?: string;
  actor_id?: string;
  created_at: string;
  updated_at: string;
}

/** A row the owner already ruled on, reported so deferral stays visible and undoable. */
export interface DeferredAttention {
  item: AttentionItem;
  action: AttentionAction;
}

// WorkAttention is the inbox plus per-stream unread counts, so the desktop wall
// badges every stream from the same request that built the list.
export interface WorkAttention {
  items: AttentionItem[];
  unread: Record<string, number>;
  /** Streams whose unread changes have gone stale: their count is still reported,
   *  but the UI demotes it from an alert to a muted summary. */
  aged: Record<string, boolean>;
  /** Rows the owner already ruled on (snoozed or dismissed), kept so deferral is
   *  visible rather than looking like the item vanished. */
  deferred: DeferredAttention[];
  counts: Record<string, number>;
  metrics: AttentionMetrics;
  generated_at: string;
}

/**
 * Apply one verb to one inbox row.
 *
 * This is a write, but NOT an edit of the work: it never bumps the stream version
 * and never advances the seen watermark, so a row can be ruled on without
 * pretending the stream was read.
 */
export function recordWorkAttentionAction(
  token: string | null,
  contextId: string,
  input: { item_key: string; verb: AttentionVerb; until_at?: string; reason?: string; actor_id?: string },
) {
  return request<AttentionAction>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/attention-actions`,
    { method: "POST", body: JSON.stringify(input) },
    token,
  );
}

/** Undo a verb, restoring the row. Idempotent: undoing what is not recorded is fine. */
export function undoWorkAttentionAction(token: string | null, contextId: string, itemKey: string) {
  return request<{ ok: boolean }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/attention-actions/${encodeURIComponent(itemKey)}`,
    { method: "DELETE" },
    token,
  );
}

// getWorkAttention returns what needs the user across every stream. Read-only, so
// opening it never advances a stream's seen watermark.
export function getWorkAttention(token: string | null) {
  return request<WorkAttention>(`/v1/work-attention`, { method: "GET" }, token);
}

// workContextQueryKey keeps the token and active node in the key so switching the active
// remote node (or logging out) refetches instead of serving another node's desktop from
// cache — node-scoped data must not leak across the node context store.
export function workContextQueryKey(
  token: string | null | undefined,
  nodeID: string | null | undefined,
  ...parts: (string | undefined)[]
) {
  return ["work-context", token ?? "", nodeID ?? "", ...parts] as const;
}
