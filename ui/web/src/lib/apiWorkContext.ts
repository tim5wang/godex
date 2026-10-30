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
) {
  return request<WorkFact>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/facts/${encodeURIComponent(factId)}`,
    { method: "PATCH", body: JSON.stringify({ status, if_version: ifVersion }) },
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

// organizeWorkContext asks the Agent to sweep the stream now (update the
// checkpoint, consolidate facts, flag stale/conflicting items). It enqueues one
// turn on the stream's linked session; the maintenance itself is real Agent work,
// not a client-side button that pretends to reorganize.
export function organizeWorkContext(token: string | null, contextId: string) {
  return request<{ queued: boolean; session_id?: string }>(
    `/v1/work-contexts/${encodeURIComponent(contextId)}/organize`,
    { method: "POST" },
    token,
  );
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
