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
} from "./types";
import { useNodeContextStore } from "../store/nodeContext";

export class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export function apiURL(path: string) {
  if (/^(?:[a-z][a-z\d+\-.]*:)?\/\//i.test(path) || path.startsWith("blob:") || path.startsWith("data:")) {
    return path;
  }
  if (path.startsWith("/api/") || path === "/api") {
    return path;
  }
  const proxied = nodeProxyPath(path);
  if (proxied) {
    return `/api${proxied}`;
  }
  if (path.startsWith("/")) {
    return `/api${path}`;
  }
  return `/api/${path}`;
}

// nodeProxyPath returns the center-proxy URL for a node-scoped path when a
// remote node is active, or null when the request should hit the local center.
// Node-scoped paths are the ones the Chat/Terminal/Files pages use against a
// remote node; center management paths (/meta, /config, /control/...) stay
// local so the shell itself keeps working.
function nodeProxyPath(path: string): string | null {
  const nodeID = useNodeContextStore.getState().nodeID;
  if (!nodeID) {
    return null;
  }
  const p = path.startsWith("/") ? path : `/${path}`;
  if (
    p.startsWith("/sessions") ||
    p.startsWith("/v1/terminal") ||
    p.startsWith("/files") ||
    p.startsWith("/commands") ||
    p.startsWith("/providers")
  ) {
    return `/control/nodes/${encodeURIComponent(nodeID)}/proxy${p}`;
  }
  return null;
}

function authHeaders(token: string | null): HeadersInit {
  if (!token) {
    return {};
  }
  return {
    Authorization: `Bearer ${token}`,
  };
}

async function request<T>(path: string, init: RequestInit = {}, token: string | null = null): Promise<T> {
  const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
  const response = await fetch(apiURL(path), {
    ...init,
    headers: {
      ...(isFormData ? {} : { "Content-Type": "application/json" }),
      ...authHeaders(token),
      ...(init.headers ?? {}),
    },
  });

  if (!response.ok) {
    const data = await response.json().catch(() => ({ error: response.statusText }));
    throw new APIError(response.status, data.error ?? response.statusText);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

async function parseAPIError(response: Response): Promise<APIError> {
  const data = await response.json().catch(() => ({ error: response.statusText }));
  return new APIError(response.status, data.error ?? response.statusText);
}

export function getMeta() {
  return request<MetaResponse>("/meta");
}

export function listMemory(
  token: string | null,
  params: { query?: string; memoryType?: MemoryType | ""; tag?: string; source?: string; limit?: number } = {},
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

export function getNodeOverview(nodeID: string, token: string | null) {
  return request<NodeOverviewResponse>(`/control/nodes/${encodeURIComponent(nodeID)}/overview`, { method: "GET" }, token);
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

export function openSession(token: string | null, locator: SessionLocator) {
  return request<{ session_id: string; locator: SessionLocator }>(
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

export function listSessionSkills(token: string | null, sessionId: string) {
  return request<SkillCatalogEntry[]>(`/sessions/${encodeURIComponent(sessionId)}/skills/catalog`, { method: "GET" }, token);
}

export function listSessionSkillSources(
  token: string | null,
  sessionId: string,
  query?: string,
  options: { mode?: "trending" } = {},
) {
  const search = new URLSearchParams();
  const trimmed = query?.trim();
  if (trimmed) {
    search.set("q", trimmed);
  }
  if (options.mode) {
    search.set("mode", options.mode);
  }
  const suffix = search.toString() ? `?${search.toString()}` : "";
  return request<SkillSourceEntry[]>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/sources${suffix}`,
    { method: "GET" },
    token,
  );
}

export function getSessionSkill(token: string | null, sessionId: string, name: string) {
  return request<SkillCatalogEntry>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/${encodeURIComponent(name)}`,
    { method: "GET" },
    token,
  );
}

export function getActiveSessionSkills(token: string | null, sessionId: string) {
  return request<SkillActivation[]>(`/sessions/${encodeURIComponent(sessionId)}/skills/active`, { method: "GET" }, token);
}

export function installSessionSkill(token: string | null, sessionId: string, source: string, name?: string) {
  return request<SkillInstallResult>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/install`,
    {
      method: "POST",
      body: JSON.stringify({ source, name }),
    },
    token,
  );
}

export function normalizeSessionSkill(token: string | null, sessionId: string, name: string) {
  return request<SkillCatalogEntry>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/normalize`,
    {
      method: "POST",
      body: JSON.stringify({ name }),
    },
    token,
  );
}

export function removeSessionSkill(token: string | null, sessionId: string, name: string) {
  return request<SkillRemoveResult>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/${encodeURIComponent(name)}`,
    { method: "DELETE" },
    token,
  );
}

export function loadSessionSkill(token: string | null, sessionId: string, name: string) {
  return request<SkillActivation>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/load`,
    {
      method: "POST",
      body: JSON.stringify({ name }),
    },
    token,
  );
}

export function expandSessionSkill(token: string | null, sessionId: string, name: string, sections: string[]) {
  return request<SkillExpansion>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/expand`,
    {
      method: "POST",
      body: JSON.stringify({ name, sections }),
    },
    token,
  );
}

export function unloadSessionSkill(token: string | null, sessionId: string, name: string) {
  return request<SkillActivation>(
    `/sessions/${encodeURIComponent(sessionId)}/skills/unload`,
    {
      method: "POST",
      body: JSON.stringify({ name }),
    },
    token,
  );
}

export interface SubmitEnvelopeInput {
  source?: string;
  sender?: string;
  text?: string;
  content?: string;
  attachments?: AttachmentRef[];
  metadata?: Record<string, string>;
}

export function submitMessage(
  token: string | null,
  sessionId: string,
  envelope: SubmitEnvelopeInput,
  options: { queueMode?: "follow_up" | "steering" } = {},
) {
  return request<{ session_id: string; turn_id: string; retry_of?: string; completed: boolean; status?: string }>(
    `/sessions/${encodeURIComponent(sessionId)}/messages`,
    {
      method: "POST",
      body: JSON.stringify({ envelope, queue_mode: options.queueMode }),
    },
    token,
  );
}

export function retrySessionTurn(token: string | null, sessionId: string, turnId: string) {
  return request<{ session_id: string; turn_id: string; retry_of?: string; completed: boolean; status?: string; updated_at: string }>(
    `/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turnId)}/retry`,
    {
      method: "POST",
    },
    token,
  );
}

export function resumeSessionTurn(token: string | null, sessionId: string, turnId: string) {
  return request<{ session_id: string; turn_id: string; completed: boolean; status?: string; updated_at: string }>(
    `/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turnId)}/resume`,
    {
      method: "POST",
    },
    token,
  );
}

export function cancelSessionTurn(token: string | null, sessionId: string, turnId: string) {
  return request<{ session_id: string; turn_id: string; status: string; updated_at: string }>(
    `/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turnId)}/cancel`,
    {
      method: "POST",
    },
    token,
  );
}

export function uploadAttachments(
  token: string | null,
  sessionId: string,
  files: File[],
  onProgress?: (progress: number) => void,
) {
  const form = new FormData();
  files.forEach((file) => {
    form.append("files", file, file.name);
  });

  return new Promise<AttachmentRef[]>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", apiURL(`/sessions/${encodeURIComponent(sessionId)}/attachments`));
    if (token) {
      xhr.setRequestHeader("Authorization", `Bearer ${token}`);
    }
    xhr.upload.onprogress = (event) => {
      if (!onProgress || !event.lengthComputable) {
        return;
      }
      onProgress(Math.round((event.loaded / event.total) * 100));
    };
    xhr.onload = () => {
      if (xhr.status < 200 || xhr.status >= 300) {
        try {
          const parsed = JSON.parse(xhr.responseText) as { error?: string };
          reject(new APIError(xhr.status, parsed.error ?? xhr.statusText));
        } catch {
          reject(new APIError(xhr.status, xhr.statusText));
        }
        return;
      }
      try {
        const parsed = JSON.parse(xhr.responseText) as { attachments: AttachmentRef[] };
        onProgress?.(100);
        resolve(parsed.attachments);
      } catch {
        reject(new APIError(xhr.status, "invalid attachment upload response"));
      }
    };
    xhr.onerror = () => {
      reject(new APIError(0, "attachment upload failed"));
    };
    xhr.send(form);
  });
}

export async function fetchAttachmentBlob(token: string | null, url: string) {
  const response = await fetch(apiURL(url), {
    headers: {
      ...authHeaders(token),
    },
  });
  if (!response.ok) {
    throw await parseAPIError(response);
  }
  return response.blob();
}

export function executeCommand(token: string | null, sessionId: string, command: string, metadata?: Record<string, string>) {
  return request<CommandResult>(
    `/sessions/${encodeURIComponent(sessionId)}/commands`,
    {
      method: "POST",
      body: JSON.stringify({ command, metadata }),
    },
    token,
  );
}

// ---- Usage Gateway API ----

export function listUsageKeys(token: string | null) {
  return request<UsageKey[]>("/usage/keys", { method: "GET" }, token);
}

export function createUsageKey(
  token: string | null,
  body: { name: string; budget_credits?: number; warning_threshold?: number; allowed_models?: string[] },
) {
  return request<UsageKeyCreateResponse>(
    "/usage/keys",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function updateUsageKey(
  token: string | null,
  id: string,
  body: { name?: string; enabled?: boolean; budget_credits?: number; warning_threshold?: number; allowed_models?: string[] },
) {
  return request<UsageKey>(
    `/usage/keys/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function resetUsageKey(token: string | null, id: string) {
  return request<UsageKeyCreateResponse>(
    `/usage/keys/${encodeURIComponent(id)}/reset`,
    { method: "POST" },
    token,
  );
}

export function listUsageModels(token: string | null) {
  return request<UsageModelMapping[]>("/usage/models", { method: "GET" }, token);
}

export function createUsageModel(
  token: string | null,
  body: { public_model: string; target_profile_id: string; target_model: string; credit_weight?: number },
) {
  return request<UsageModelMapping>(
    "/usage/models",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function updateUsageModel(
  token: string | null,
  id: string,
  body: { public_model?: string; target_profile_id?: string; target_model?: string; credit_weight?: number; enabled?: boolean },
) {
  return request<UsageModelMapping>(
    `/usage/models/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function getUsageSummary(token: string | null, range: "day" | "week" = "day", apiKeyId?: string) {
  const search = new URLSearchParams();
  search.set("range", range);
  if (apiKeyId?.trim()) {
    search.set("api_key_id", apiKeyId.trim());
  }
  return request<UsageSummary[]>(`/usage/summary?${search.toString()}`, { method: "GET" }, token);
}

export function listUsageCalls(token: string | null, date?: string, apiKeyId?: string) {
  const search = new URLSearchParams();
  if (date?.trim()) {
    search.set("date", date.trim());
  }
  if (apiKeyId?.trim()) {
    search.set("api_key_id", apiKeyId.trim());
  }
  const query = search.toString();
  return request<UsageCall[]>(`/usage/calls${query ? `?${query}` : ""}`, { method: "GET" }, token);
}

export function getUsageTimeSeries(
  token: string | null,
  params: {
    granularity: "hour" | "day";
    start_time?: string;
    end_time?: string;
    api_key_id?: string;
    session_id?: string;
    model?: string;
  },
) {
  const search = new URLSearchParams();
  search.set("granularity", params.granularity);
  if (params.start_time) search.set("start_time", params.start_time);
  if (params.end_time) search.set("end_time", params.end_time);
  if (params.api_key_id) search.set("api_key_id", params.api_key_id);
  if (params.session_id) search.set("session_id", params.session_id);
  if (params.model) search.set("model", params.model);
  return request<UsageTimeSeriesPoint[]>(
    `/usage/time-series?${search.toString()}`,
    { method: "GET" },
    token,
  );
}

export function listUsageSessions(token: string | null, params?: { api_key_id?: string; limit?: number; offset?: number }) {
  const search = new URLSearchParams();
  if (params?.api_key_id) search.set("api_key_id", params.api_key_id);
  if (params?.limit) search.set("limit", String(params.limit));
  if (params?.offset) search.set("offset", String(params.offset));
  const query = search.toString();
  return request<SessionUsageSummary[]>(
    `/usage/sessions${query ? `?${query}` : ""}`,
    { method: "GET" },
    token,
  );
}

export function getUsageSessionDetail(token: string | null, sessionId: string) {
  return request<SessionUsageSummary>(
    `/usage/sessions/${encodeURIComponent(sessionId)}`,
    { method: "GET" },
    token,
  );
}

export function getCacheStats(
  token: string | null,
  params?: { range?: string; model?: string; api_key_id?: string },
) {
  const search = new URLSearchParams();
  search.set("range", params?.range ?? "day");
  if (params?.model) search.set("model", params.model);
  if (params?.api_key_id) search.set("api_key_id", params.api_key_id);
  return request<CacheStats[]>(
    `/usage/cache-stats?${search.toString()}`,
    { method: "GET" },
    token,
  );
}

// ---- File API ----

export interface FileEntry {
  name: string;
  isDir: boolean;
  size: number;
  modTime: string;
}

export interface FileReadResponse {
  path: string;
  content: string;
  size: number;
}

export function listFiles(token: string | null, dir?: string, root?: string) {
  const search = new URLSearchParams();
  search.set("dir", dir ?? ".");
  if (root?.trim()) {
    search.set("root", root.trim());
  }
  return request<{ items: FileEntry[] }>(`/files/list?${search.toString()}`, { method: "GET" }, token);
}

export function readFile(token: string | null, path: string, root?: string) {
  const search = new URLSearchParams();
  search.set("path", path);
  if (root?.trim()) {
    search.set("root", root.trim());
  }
  return request<FileReadResponse>(`/files/read?${search.toString()}`, { method: "GET" }, token);
}

export interface GitDiffResponse {
  repo: boolean;
  diff?: string;
  truncated?: boolean;
  error?: string;
}

/** Working-tree diff for a local git repository (one file or whole tree). */
export function gitDiff(token: string | null, path: string, root?: string) {
  const search = new URLSearchParams();
  if (path) {
    search.set("path", path);
  }
  if (root?.trim()) {
    search.set("root", root.trim());
  }
  return request<GitDiffResponse>(`/git/diff?${search.toString()}`, { method: "GET" }, token);
}

export function writeFile(token: string | null, path: string, content: string, root?: string) {
  return request<{ path: string; size: number }>(
    "/files/write",
    {
      method: "PUT",
      body: JSON.stringify({ path, content, ...(root?.trim() ? { root: root.trim() } : {}) }),
    },
    token,
  );
}

export function deleteFile(token: string | null, path: string, root?: string) {
  const search = new URLSearchParams();
  search.set("path", path);
  if (root?.trim()) {
    search.set("root", root.trim());
  }
  return request<void>(`/files?${search.toString()}`, { method: "DELETE" }, token);
}

export function mkdirFile(token: string | null, path: string, root?: string) {
  return request<{ path: string }>(
    "/files/mkdir",
    {
      method: "POST",
      body: JSON.stringify({ path, ...(root?.trim() ? { root: root.trim() } : {}) }),
    },
    token,
  );
}

export function renameFile(token: string | null, from: string, to: string, root?: string) {
  return request<{ from: string; to: string }>(
    "/files/rename",
    {
      method: "POST",
      body: JSON.stringify({ from, to, ...(root?.trim() ? { root: root.trim() } : {}) }),
    },
    token,
  );
}

export interface FileSearchResult {
  path: string;
  isDir: boolean;
  size: number;
}

export function searchFiles(token: string | null, query: string, mode: "name" | "content", root?: string) {
  const params = new URLSearchParams();
  params.set("q", query);
  params.set("mode", mode);
  if (root?.trim()) params.set("root", root.trim());
  return request<{ items: FileSearchResult[] }>(`/files/search?${params.toString()}`, { method: "GET" }, token);
}
