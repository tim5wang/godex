import type {
  AttachmentRef,
  ChannelStatusReport,
  CommandMetadata,
  CommandResult,
  ConfigMetaResponse,
  ConfigSectionSchema,
  ControlNode,
  ConfigView,
  CronJob,
  CronRunLog,
  DoctorReport,
  DurableSubagentJob,
  DurableSubagentMerge,
  DurableSubagentReview,
  HeartbeatRule,
  HeartbeatRunLog,
  ListedSession,
  LongTaskView,
  MemoryCandidate,
  MemoryContextLayers,
  MemoryAuditLogEntry,
  MemoryDigestResult,
  MemoryRecord,
  MemorySuppression,
  MemoryType,
  MetaResponse,
  NodeOverviewResponse,
  ForwardStatus,
  ForwardCheckResult,
  Note,
  ModelsView,
  PackageCommandEntry,
  PackageEntry,
  PackageQualityReport,
  PackageRoleEntry,
  PackageSmokeRun,
  PendingPermission,
  PermissionResolution,
  ProviderListResponse,
  ProviderModelsResponse,
  ProviderTestResponse,
  PromptEntry,
  CIKSummary,
  ProtocolMessage,
  SecurityEvent,
  SkillActivation,
  SkillCatalogEntry,
  SkillExpansion,
  SkillInstallResult,
  SkillRemoveResult,
  SkillSourceEntry,
  RuntimeServiceStatus,
  SessionTimelineEntry,
  TimelinePage,
  CompactionRecord,
  SessionContextInspector,
  SessionLocator,
  Snapshot,
  UsageCall,
  UsageKey,
  UsageKeyCreateResponse,
  UsageModelMapping,
  UsageSummary,
  UsageTimeSeriesPoint,
  SessionUsageSummary,
  CacheStats,
  WeixinAuthStatus,
  BizKey,
  BizKeyCreateResponse,
  ProviderRef,
  MCPRegistryResponse,
  MCPStatusResponse,
  MCPServerConfig,
  MCPServerStatus,
  TaskboardProject,
  TaskboardProjectCreateInput,
  TaskboardCard,
  TaskboardCardCreateInput,
  TaskboardCardPatchInput,
  TaskboardExecutionObservation,
  TaskboardReconcileReport,
  LlmCaptureStatus,
  LlmCaptureSummary,
  LlmCaptureRecord,
} from "./types";
import { request } from "./apiClient";

export * from "./apiAgent";
export * from "./apiClient";
export * from "./apiProduct";

export function getMeta() {
  return request<MetaResponse>("/meta");
}

export function listMemory(
  token: string | null,
  params: { query?: string; memoryType?: MemoryType | ""; tag?: string; source?: string; status?: string; limit?: number } = {},
) {
  const search = new URLSearchParams();
  if (params.query?.trim()) {
    search.set("q", params.query.trim());
  }
  if (params.memoryType?.trim()) {
    search.set("memory_type", params.memoryType.trim());
  }
  if (params.tag?.trim()) {
    search.set("tag", params.tag.trim());
  }
  if (params.source?.trim()) {
    search.set("source", params.source.trim());
  }
  if (params.status?.trim()) {
    search.set("status", params.status.trim());
  }
  if (typeof params.limit === "number" && params.limit >= 0) {
    search.set("limit", String(params.limit));
  }
  const query = search.toString();
  return request<MemoryRecord[]>(`/memory${query ? `?${query}` : ""}`, { method: "GET" }, token);
}

export function listMemoryCandidates(token: string | null) {
  return request<MemoryCandidate[]>("/memory/candidates", { method: "GET" }, token);
}

export function mineProjectMemory(token: string | null) {
  return request<MemoryCandidate[]>("/memory/mine/project", { method: "POST" }, token);
}

export function listMemorySuppressions(token: string | null) {
  return request<MemorySuppression[]>("/memory/suppressions", { method: "GET" }, token);
}

export function listMemoryAudit(token: string | null, limit = 50) {
  return request<MemoryAuditLogEntry[]>(`/memory/audit?limit=${encodeURIComponent(String(limit))}`, { method: "GET" }, token);
}

export function digestMemory(token: string | null) {
  return request<MemoryDigestResult>("/memory/digest", { method: "POST" }, token);
}

export function restoreMemoryAudit(token: string | null, id: string, target: "before" | "after" = "before") {
  return request<MemoryAuditLogEntry>(
    `/memory/audit/${encodeURIComponent(id)}/restore`,
    {
      method: "POST",
      body: JSON.stringify({ target }),
    },
    token,
  );
}

export function previewMemoryContext(token: string | null, query: string) {
  const search = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : "";
  return request<MemoryContextLayers>(`/memory/context${search}`, { method: "GET" }, token);
}

export function listNotes(token: string | null, params: { query?: string; tag?: string } = {}) {
  const search = new URLSearchParams();
  if (params.query?.trim()) {
    search.set("q", params.query.trim());
  }
  if (params.tag?.trim()) {
    search.set("tag", params.tag.trim());
  }
  const query = search.toString();
  return request<Note[]>(`/notes${query ? `?${query}` : ""}`, { method: "GET" }, token);
}

export function getNote(token: string | null, id: string) {
  return request<Note>(`/notes/${encodeURIComponent(id)}`, { method: "GET" }, token);
}

export function saveNote(token: string | null, body: { id?: string; title: string; summary?: string; tags?: string[]; content: string }) {
  return request<Note>("/notes", { method: "POST", body: JSON.stringify(body) }, token);
}

export function deleteNote(token: string | null, id: string) {
  return request<Note>(`/notes/${encodeURIComponent(id)}`, { method: "DELETE" }, token);
}

export function getNoteRelatedMemories(token: string | null, noteID: string) {
  return request<MemoryRecord[]>(`/notes/${encodeURIComponent(noteID)}/related-memories`, { method: "GET" }, token);
}

export function rememberMemory(
  token: string | null,
  body: { title: string; summary: string; content: string; memory_type: MemoryType; source?: string; tags?: string[] },
) {
  return request<MemoryRecord>(
    "/memory/remember",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function updateMemory(
  token: string | null,
  body: {
    match_title?: string;
    match_file?: string;
    title: string;
    summary: string;
    content: string;
    memory_type: MemoryType;
    source?: string;
    tags?: string[];
  },
) {
  return request<MemoryRecord>(
    "/memory/update",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function forgetMemory(token: string | null, body: { title?: string; file?: string }) {
  return request<MemoryRecord>(
    "/memory/forget",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function acceptMemoryCandidate(
  token: string | null,
  fingerprint: string,
  body: { always_include?: boolean } = {},
) {
  return request<MemoryRecord>(
    `/memory/candidates/${encodeURIComponent(fingerprint)}/accept`,
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function dismissMemoryCandidate(token: string | null, fingerprint: string) {
  return request<MemoryCandidate>(
    `/memory/candidates/${encodeURIComponent(fingerprint)}/dismiss`,
    {
      method: "POST",
      body: JSON.stringify({}),
    },
    token,
  );
}

export function archiveMemory(token: string | null, body: { title?: string; file?: string }) {
  return request<MemoryRecord>(
    "/memory/archive",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function restoreMemoryStatus(token: string | null, body: { title?: string; file?: string }) {
  return request<MemoryRecord>(
    "/memory/restore",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function listMilestoneMemories(token: string | null) {
  return request<MemoryRecord[]>("/memory/milestones", { method: "GET" }, token);
}

export function archiveMilestoneMemories(token: string | null) {
  return request<{ archived: MemoryRecord[] }>("/memory/milestones/archive", { method: "POST" }, token);
}

export function removeMemorySuppression(token: string | null, key: string) {
  return request<{ removed: boolean }>(
    "/memory/suppressions/remove",
    {
      method: "POST",
      body: JSON.stringify({ key }),
    },
    token,
  );
}

export function getConfigMeta(token: string | null) {
  return request<ConfigMetaResponse>("/config/meta", { method: "GET" }, token);
}

export function getConfigSchema(token: string | null) {
  return request<ConfigSectionSchema[]>("/config/schema", { method: "GET" }, token);
}

export function getConfigView(token: string | null) {
  return request<ConfigView>("/config", { method: "GET" }, token);
}

export function updateConfig(
  token: string | null,
  body: { values: Record<string, unknown>; clear_secrets?: string[] },
) {
  return request<ConfigView>(
    "/config",
    {
      method: "PUT",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function reloadConfigFromDisk(token: string | null) {
  return request<ConfigView>("/config/reload", { method: "POST", body: JSON.stringify({}) }, token);
}

export function revealConfigSecret(token: string | null, path: string) {
  return request<{ path: string; value: string }>(
    "/config/reveal",
    {
      method: "POST",
      body: JSON.stringify({ path }),
    },
    token,
  );
}

export function getConfigDoctor(token: string | null) {
  return request<DoctorReport>("/config/doctor", { method: "GET" }, token);
}

export function getRuntimeServiceStatus(token: string | null) {
  return request<RuntimeServiceStatus>("/runtime/service", { method: "GET" }, token);
}

export function listControlNodes(token: string | null) {
  return request<ControlNode[]>("/control/nodes", { method: "GET" }, token);
}

export interface RegisterNodeInput {
  id: string;
  name?: string;
  trust_level?: string;
}

/** Register a new node on the center so it can later be issued a credential. */
export function registerControlNode(token: string | null, input: RegisterNodeInput) {
  return request<ControlNode>("/control/nodes/register", { method: "POST", body: JSON.stringify(input) }, token);
}

/** Issue (or rotate) a per-node credential for an already-registered node. */
export function issueNodeCredential(token: string | null, nodeID: string) {
  return request<{ node_id: string; credential: string }>(
    `/control/nodes/${encodeURIComponent(nodeID)}/credential`,
    { method: "POST", body: JSON.stringify({}) },
    token,
  );
}

/** Delete a node: removes it from the registry and drops its relay connection. */
export function deleteControlNode(token: string | null, nodeID: string) {
  return request<ControlNode>(
    `/control/nodes/${encodeURIComponent(nodeID)}`,
    { method: "DELETE", body: JSON.stringify({}) },
    token,
  );
}

export function getNodeOverview(nodeID: string, token: string | null) {
  return request<NodeOverviewResponse>(`/control/nodes/${encodeURIComponent(nodeID)}/overview`, { method: "GET" }, token);
}

export interface CreateForwardInput {
  name?: string;
  node_id: string;
  local_port: number;
  target: string;
}

/** List managed forward tunnels (with runtime status). */
export function listForwards(token: string | null) {
  return request<ForwardStatus[]>("/control/forwards", { method: "GET" }, token);
}

/** Create a managed forward tunnel on the center. */
export function createForward(token: string | null, input: CreateForwardInput) {
  return request<ForwardStatus>("/control/forwards", { method: "POST", body: JSON.stringify(input) }, token);
}

/** Remove a managed forward tunnel. */
export function deleteForward(token: string | null, id: string) {
  return request<{ removed: boolean }>(`/control/forwards/${encodeURIComponent(id)}`, { method: "DELETE", body: JSON.stringify({}) }, token);
}

/** Probe a forward tunnel end to end (listener → node relay → target). */
export function checkForward(token: string | null, id: string) {
  return request<ForwardCheckResult>(`/control/forwards/${encodeURIComponent(id)}/check`, { method: "POST", body: JSON.stringify({}) }, token);
}

export function restartRuntimeService(token: string | null) {
  return request<{ accepted: boolean; message?: string }>("/runtime/service/restart", { method: "POST", body: JSON.stringify({}) }, token);
}

export function listProviders(token: string | null) {
  return request<ProviderListResponse>("/providers", { method: "GET" }, token);
}

export function testProvider(token: string | null, id: string) {
  return request<ProviderTestResponse>(`/providers/${encodeURIComponent(id)}/test`, { method: "POST" }, token);
}

export function discoverProviderModels(token: string | null, id: string) {
  return request<ProviderModelsResponse>(`/providers/${encodeURIComponent(id)}/models`, { method: "POST" }, token);
}

export type ACPModelOption = { value: string; name: string };

export type ACPReasoningEffortOption = { value: string; name: string };

export function discoverACPAgentModels(token: string | null, id: string) {
  return request<{ models: ACPModelOption[] }>(`/acp/agents/${encodeURIComponent(id)}/models`, { method: "GET" }, token);
}

export function discoverACPAgentConfigOptions(token: string | null, id: string) {
  return request<{ models: ACPModelOption[]; reasoning_efforts: ACPReasoningEffortOption[] }>(
    `/acp/agents/${encodeURIComponent(id)}/config-options`,
    { method: "GET" },
    token,
  );
}

export function getChannelsStatus(token: string | null) {
  return request<ChannelStatusReport>("/channels", { method: "GET" }, token);
}

export function getModels(token: string | null, sessionId?: string) {
  const query = sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : "";
  return request<ModelsView>(`/models${query}`, { method: "GET" }, token);
}

export function setSessionModel(token: string | null, sessionId: string, profileId: string, reasoningEffort?: string) {
  return request<ModelsView>(
    `/sessions/${encodeURIComponent(sessionId)}/model`,
    {
      method: "POST",
      body: JSON.stringify({ profile_id: profileId, reasoning_effort: reasoningEffort || undefined }),
    },
    token,
  );
}

export function setSessionACPAgentModel(token: string | null, sessionId: string, model: string) {
  return request<ModelsView>(
    `/sessions/${encodeURIComponent(sessionId)}/acp-model`,
    {
      method: "POST",
      body: JSON.stringify({ model: model || undefined }),
    },
    token,
  );
}

export function setSessionACPAgentReasoningEffort(token: string | null, sessionId: string, effort: string) {
  return request<ModelsView>(
    `/sessions/${encodeURIComponent(sessionId)}/acp-reasoning-effort`,
    {
      method: "POST",
      body: JSON.stringify({ reasoning_effort: effort || undefined }),
    },
    token,
  );
}

export function getSecuritySummary(token: string | null) {
  return request<CIKSummary>("/security/summary", { method: "GET" }, token);
}

export function getSecurityAudit(token: string | null, limit = 50) {
  return request<SecurityEvent[]>(`/security/audit?limit=${encodeURIComponent(String(limit))}`, { method: "GET" }, token);
}

export function listPackages(token: string | null) {
  return request<PackageEntry[]>("/packages", { method: "GET" }, token);
}

export function getPackageQuality(token: string | null) {
  return request<PackageQualityReport>("/packages/quality", { method: "GET" }, token);
}

export function installPackage(token: string | null, source: string) {
  return request<PackageEntry>(
    "/packages/install",
    {
      method: "POST",
      body: JSON.stringify({ source }),
    },
    token,
  );
}

export function removePackage(token: string | null, name: string) {
  return request<PackageEntry>(
    "/packages/remove",
    {
      method: "POST",
      body: JSON.stringify({ name }),
    },
    token,
  );
}

export function reinstallPackage(token: string | null, name: string) {
  return request<PackageEntry>(
    `/packages/${encodeURIComponent(name)}/reinstall`,
    { method: "POST" },
    token,
  );
}

export function runPackageSmoke(token: string | null, packageName: string, smokeName: string, sessionId?: string) {
  return request<PackageSmokeRun>(
    `/packages/${encodeURIComponent(packageName)}/smoke/${encodeURIComponent(smokeName)}`,
    {
      method: "POST",
      body: JSON.stringify({ session_id: sessionId || undefined }),
    },
    token,
  );
}

export function listPrompts(token: string | null, includeContent = false) {
  return request<PromptEntry[]>(
    `/prompts?include_content=${includeContent ? "true" : "false"}`,
    { method: "GET" },
    token,
  );
}

export function listCommands(token: string | null) {
  return request<CommandMetadata[]>("/commands", { method: "GET" }, token);
}

export function listPackageCommands(token: string | null, includeContent = false) {
  return request<PackageCommandEntry[]>(
    `/packages/commands?include_content=${includeContent ? "true" : "false"}`,
    { method: "GET" },
    token,
  );
}

export function listPackageRoles(token: string | null, includeContent = false) {
  return request<PackageRoleEntry[]>(
    `/packages/roles?include_content=${includeContent ? "true" : "false"}`,
    { method: "GET" },
    token,
  );
}

export function getWeixinAuthStatus(token: string | null, accountId?: string) {
  const query = accountId ? `?account_id=${encodeURIComponent(accountId)}` : "";
  return request<WeixinAuthStatus>(`/channels/weixin/auth${query}`, { method: "GET" }, token);
}

export function startWeixinAuth(token: string | null, accountId?: string) {
  return request<WeixinAuthStatus>(
    "/channels/weixin/auth/start",
    {
      method: "POST",
      body: JSON.stringify(accountId ? { account_id: accountId } : {}),
    },
    token,
  );
}

export function logoutWeixinAuth(token: string | null, accountId?: string) {
  return request<WeixinAuthStatus>(
    "/channels/weixin/auth/logout",
    {
      method: "POST",
      body: JSON.stringify(accountId ? { account_id: accountId } : {}),
    },
    token,
  );
}

export function listCronJobs(token: string | null) {
  return request<CronJob[]>("/automation/cron/jobs", { method: "GET" }, token);
}

export function getCronJob(token: string | null, jobId: string) {
  return request<CronJob>(`/automation/cron/jobs/${encodeURIComponent(jobId)}`, { method: "GET" }, token);
}

export function createCronJob(token: string | null, body: Record<string, unknown>) {
  return request<CronJob>(
    "/automation/cron/jobs",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function updateCronJob(token: string | null, jobId: string, body: Record<string, unknown>) {
  return request<CronJob>(
    `/automation/cron/jobs/${encodeURIComponent(jobId)}`,
    {
      method: "PATCH",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function deleteCronJob(token: string | null, jobId: string) {
  return request<void>(`/automation/cron/jobs/${encodeURIComponent(jobId)}`, { method: "DELETE" }, token);
}

export function runCronJob(token: string | null, jobId: string) {
  return request<CronRunLog>(`/automation/cron/jobs/${encodeURIComponent(jobId)}/run`, { method: "POST" }, token);
}

export function getCronRunLogs(token: string | null, jobId: string) {
  return request<CronRunLog[]>(`/automation/cron/jobs/${encodeURIComponent(jobId)}/runs`, { method: "GET" }, token);
}

export function getHeartbeatRule(token: string | null) {
  return request<HeartbeatRule>("/automation/heartbeat", { method: "GET" }, token);
}

export function updateHeartbeatRule(token: string | null, body: Record<string, unknown>) {
  return request<HeartbeatRule>(
    "/automation/heartbeat",
    {
      method: "PUT",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function testHeartbeat(token: string | null) {
  return request<HeartbeatRunLog>("/automation/heartbeat/test", { method: "POST" }, token);
}

export function getHeartbeatLogs(token: string | null) {
  return request<HeartbeatRunLog[]>("/automation/heartbeat/logs", { method: "GET" }, token);
}

export function listSessions(token: string | null, channel?: string | null) {
  const trimmed = channel?.trim();
  const suffix = trimmed ? `?channel=${encodeURIComponent(trimmed)}` : "";
  return request<ListedSession[]>(`/sessions${suffix}`, { method: "GET" }, token);
}

export function deleteSession(token: string | null, sessionId: string) {
  return request<void>(`/sessions/${encodeURIComponent(sessionId)}`, { method: "DELETE" }, token);
}

export function renameSession(token: string | null, sessionId: string, title: string) {
  return request<ListedSession>(`/sessions/${encodeURIComponent(sessionId)}/title`, {
    method: "PATCH",
    body: JSON.stringify({ title }),
  }, token);
}

export function openSession(token: string | null, locator: SessionLocator) {
  return request<{ session_id: string; locator: SessionLocator; created_at?: string; updated_at?: string }>(
    "/sessions",
    {
      method: "POST",
      body: JSON.stringify({ locator }),
    },
    token,
  );
}

export function forkSession(token: string | null, sessionId: string, body: { turn_id?: string; message_index?: number; title?: string } = {}) {
  return request<{ session_id: string; locator: SessionLocator; model_profile_id?: string }>(
    `/sessions/${encodeURIComponent(sessionId)}/fork`,
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function getSnapshot(token: string | null, sessionId: string) {
  return request<Snapshot>(`/sessions/${encodeURIComponent(sessionId)}`, { method: "GET" }, token);
}

export function getSessionTimeline(token: string | null, sessionId: string, limit = 80) {
  return request<SessionTimelineEntry[]>(
    `/sessions/${encodeURIComponent(sessionId)}/timeline?limit=${encodeURIComponent(String(limit))}`,
    { method: "GET" },
    token,
  );
}

export function getSessionTimelinePage(
  token: string | null,
  sessionId: string,
  params: { limit?: number; cursor?: string; types?: string[]; q?: string; jobId?: string; turnId?: string } = {},
) {
  const search = new URLSearchParams();
  search.set("limit", String(params.limit ?? 50));
  if (params.cursor?.trim()) {
    search.set("cursor", params.cursor.trim());
  }
  if (params.types?.length) {
    search.set("type", params.types.join(","));
  }
  if (params.q?.trim()) {
    search.set("q", params.q.trim());
  }
  if (params.jobId?.trim()) {
    search.set("job_id", params.jobId.trim());
  }
  if (params.turnId?.trim()) {
    search.set("turn_id", params.turnId.trim());
  }
  return request<TimelinePage>(`/sessions/${encodeURIComponent(sessionId)}/timeline/page?${search.toString()}`, { method: "GET" }, token);
}

export function getSessionCompactions(token: string | null, sessionId: string) {
  return request<CompactionRecord[]>(`/sessions/${encodeURIComponent(sessionId)}/compactions`, { method: "GET" }, token);
}

export function listSessionSubagents(token: string | null, sessionId: string) {
  return request<DurableSubagentJob[]>(`/sessions/${encodeURIComponent(sessionId)}/subagents`, { method: "GET" }, token);
}

export function getSessionSubagent(token: string | null, sessionId: string, jobId: string) {
  return request<DurableSubagentJob>(
    `/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(jobId)}`,
    { method: "GET" },
    token,
  );
}

export function reviewSessionSubagent(token: string | null, sessionId: string, jobId: string) {
  return request<DurableSubagentReview>(
    `/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(jobId)}/review`,
    { method: "GET" },
    token,
  );
}

export function cancelSessionSubagent(token: string | null, sessionId: string, jobId: string) {
  return request<DurableSubagentJob>(
    `/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(jobId)}/cancel`,
    { method: "POST", body: JSON.stringify({}) },
    token,
  );
}

export function resumeSessionSubagent(token: string | null, sessionId: string, jobId: string) {
  return request<DurableSubagentJob>(
    `/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(jobId)}/resume`,
    { method: "POST", body: JSON.stringify({}) },
    token,
  );
}

export function mergeSessionSubagent(token: string | null, sessionId: string, jobId: string) {
  return request<DurableSubagentMerge>(
    `/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(jobId)}/merge`,
    { method: "POST", body: JSON.stringify({}) },
    token,
  );
}

export function listSessionLongTasks(token: string | null, sessionId: string) {
  return request<LongTaskView[]>(`/sessions/${encodeURIComponent(sessionId)}/longtasks`, { method: "GET" }, token);
}

export function runSessionLongTask(token: string | null, sessionId: string, workflowId: string, options: { auto_repair?: boolean } = {}) {
  return request<LongTaskView>(
    `/sessions/${encodeURIComponent(sessionId)}/longtasks/${encodeURIComponent(workflowId)}/run`,
    { method: "POST", body: JSON.stringify(options) },
    token,
  );
}

export function cancelSessionLongTask(token: string | null, sessionId: string, workflowId: string, nodeId: string) {
  return request<LongTaskView>(
    `/sessions/${encodeURIComponent(sessionId)}/longtasks/${encodeURIComponent(workflowId)}/cancel`,
    { method: "POST", body: JSON.stringify({ node_id: nodeId }) },
    token,
  );
}

export function finalizeSessionLongTaskStory(token: string | null, sessionId: string, workflowId: string, nodeId: string) {
  return request<LongTaskView>(
    `/sessions/${encodeURIComponent(sessionId)}/longtasks/${encodeURIComponent(workflowId)}/finalize`,
    { method: "POST", body: JSON.stringify({ node_id: nodeId }) },
    token,
  );
}

export function getSessionContextInspector(token: string | null, sessionId: string) {
  return request<SessionContextInspector>(
    `/sessions/${encodeURIComponent(sessionId)}/context-inspector`,
    { method: "GET" },
    token,
  );
}

export interface SessionTranscript {
  ref: string;
  messages: ProtocolMessage[];
}

export function getSessionTranscript(token: string | null, sessionId: string, ref: string) {
  return request<SessionTranscript>(
    `/sessions/${encodeURIComponent(sessionId)}/transcript/${encodeURIComponent(ref)}`,
    { method: "GET" },
    token,
  );
}

export function getSessionPermissions(token: string | null, sessionId: string) {
  return request<PendingPermission[]>(
    `/sessions/${encodeURIComponent(sessionId)}/permissions`,
    { method: "GET" },
    token,
  );
}

export function approveSessionPermission(
  token: string | null,
  sessionId: string,
  requestId: string,
  scope: "once" | "session",
) {
  return request<PermissionResolution>(
    `/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(requestId)}/approve`,
    {
      method: "POST",
      body: JSON.stringify({ scope }),
    },
    token,
  );
}

export function denySessionPermission(token: string | null, sessionId: string, requestId: string, reason?: string) {
  return request<PermissionResolution>(
    `/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(requestId)}/deny`,
    {
      method: "POST",
      body: JSON.stringify(reason ? { reason } : {}),
    },
    token,
  );
}

// Node-scoped approval endpoints: these go through the center proxy
// (/control/nodes/{id}/proxy/...) so the center web can approve or deny a
// pending permission that lives on a remote node.

export function approveNodePermission(
  nodeID: string,
  token: string | null,
  sessionId: string,
  requestId: string,
  scope: "once" | "session",
) {
  return request<PermissionResolution>(
    `/control/nodes/${encodeURIComponent(nodeID)}/proxy/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(requestId)}/approve`,
    {
      method: "POST",
      body: JSON.stringify({ scope }),
    },
    token,
  );
}

export function denyNodePermission(
  nodeID: string,
  token: string | null,
  sessionId: string,
  requestId: string,
  reason?: string,
) {
  return request<PermissionResolution>(
    `/control/nodes/${encodeURIComponent(nodeID)}/proxy/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(requestId)}/deny`,
    {
      method: "POST",
      body: JSON.stringify(reason ? { reason } : {}),
    },
    token,
  );
}

// ---- LLM Capture (request/response jsonl dump) ----

export function getLlmCaptureStatus(token: string | null) {
  return request<LlmCaptureStatus>(`/llm-capture/status`, { method: "GET" }, token);
}

export function setLlmCaptureEnabled(token: string | null, enabled: boolean) {
  return request<{ enabled: boolean }>(`/llm-capture/${enabled ? "enable" : "disable"}`, { method: "POST" }, token);
}

export function listLlmCaptureRecords(token: string | null, limit = 100) {
  return request<LlmCaptureSummary[]>(`/llm-capture/records?limit=${encodeURIComponent(String(limit))}`, { method: "GET" }, token);
}

export function getLlmCaptureRecord(token: string | null, id: string) {
  return request<LlmCaptureRecord>(`/llm-capture/records/${encodeURIComponent(id)}`, { method: "GET" }, token);
}

export function clearLlmCaptureRecords(token: string | null) {
  return request<{ cleared: boolean }>(`/llm-capture/clear`, { method: "POST" }, token);
}
