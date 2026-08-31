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
} from "./types";
import type { AgentTemplate } from "./apiAgent";
import { request } from "./apiClient";

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

// ---- Agent Step Platform: Business Agents (biz keys) API ----

export function listBizKeys(token: string | null) {
  return request<BizKey[]>("/v1/biz/keys", { method: "GET" }, token);
}

export function createBizKey(
  token: string | null,
  body: {
    name: string;
    description?: string;
    default_prompt?: string;
    template_id?: string;
    mcp_servers?: string[];
    providers?: ProviderRef[];
    sandbox_tools?: string[];
    skills?: string[];
    packages?: string[];
    models?: string[];
    project_dir?: string;
    budget_credits?: number;
    warning_threshold?: number;
  },
) {
  return request<BizKeyCreateResponse>(
    "/v1/biz/keys",
    {
      method: "POST",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function updateBizKey(
  token: string | null,
  id: string,
  body: {
    name?: string;
    description?: string;
    default_prompt?: string;
    enabled?: boolean;
    template_id?: string;
    mcp_servers?: string[];
    providers?: ProviderRef[];
    sandbox_tools?: string[];
    skills?: string[];
    packages?: string[];
    models?: string[];
    project_dir?: string;
    budget_credits?: number;
    warning_threshold?: number;
  },
) {
  return request<BizKey>(
    `/v1/biz/keys/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      body: JSON.stringify(body),
    },
    token,
  );
}

export function resetBizKey(token: string | null, id: string) {
  return request<BizKeyCreateResponse>(
    `/v1/biz/keys/${encodeURIComponent(id)}/reset`,
    { method: "POST" },
    token,
  );
}

export function revealBizKey(token: string | null, id: string, pin: string) {
  return request<BizKeyCreateResponse>(
    `/v1/biz/keys/${encodeURIComponent(id)}/reveal`,
    {
      method: "POST",
      body: JSON.stringify({ pin }),
    },
    token,
  );
}

export function migrateBizKeyTemplate(token: string | null, id: string) {
  return request<{ template: AgentTemplate; key: BizKey }>(
    `/v1/biz/keys/${encodeURIComponent(id)}/migrate-template`,
    { method: "POST" },
    token,
  );
}

export function deleteBizKey(token: string | null, id: string) {
  return request<void>(`/v1/biz/keys/${encodeURIComponent(id)}`, { method: "DELETE" }, token);
}

// ---- MCP Server Registry API ----

export function listMCPServers(token: string | null) {
  return request<MCPRegistryResponse>("/v1/mcp/servers", { method: "GET" }, token);
}

export function createMCPServer(token: string | null, body: MCPServerConfig) {
  return request<MCPServerConfig>("/v1/mcp/servers", { method: "POST", body: JSON.stringify(body) }, token);
}

export function updateMCPServer(token: string | null, name: string, body: MCPServerConfig) {
  return request<MCPServerConfig>(
    `/v1/mcp/servers/${encodeURIComponent(name)}`,
    { method: "PUT", body: JSON.stringify(body) },
    token,
  );
}

export function deleteMCPServer(token: string | null, name: string) {
  return request<void>(`/v1/mcp/servers/${encodeURIComponent(name)}`, { method: "DELETE" }, token);
}

export function testMCPServer(token: string | null, name: string) {
  return request<MCPServerStatus>(`/v1/mcp/servers/${encodeURIComponent(name)}/test`, { method: "POST" }, token);
}

export function getMCPStatuses(token: string | null) {
  return request<MCPStatusResponse>("/v1/mcp/status", { method: "GET" }, token);
}

// ---- Taskboard (需求池 #1) ----

export function listTaskboardProjects(token: string | null) {
  return request<{ projects: TaskboardProject[] }>("/v1/taskboard/projects", { method: "GET" }, token);
}

export function createTaskboardProject(token: string | null, input: TaskboardProjectCreateInput) {
  return request<{ project: TaskboardProject }>("/v1/taskboard/projects", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  }, token);
}

export function updateTaskboardProject(token: string | null, id: string, input: { name?: string; work_dirs?: string[] }) {
  return request<{ project: TaskboardProject }>(`/v1/taskboard/projects/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  }, token);
}

export function deleteTaskboardProject(token: string | null, id: string) {
  return request<{ deleted: boolean }>(`/v1/taskboard/projects/${encodeURIComponent(id)}`, { method: "DELETE" }, token);
}

export function listTaskboardCards(token: string | null, query?: { project?: string; status?: string; urgency?: string }) {
  const params = new URLSearchParams();
  if (query?.project) params.set("project", query.project);
  if (query?.status) params.set("status", query.status);
  if (query?.urgency) params.set("urgency", query.urgency);
  const qs = params.toString();
  return request<{ cards: TaskboardCard[]; count: number }>(`/v1/taskboard/cards${qs ? `?${qs}` : ""}`, { method: "GET" }, token);
}

export function createTaskboardCard(token: string | null, input: TaskboardCardCreateInput) {
  return request<{ card: TaskboardCard }>("/v1/taskboard/cards", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  }, token);
}

export function getTaskboardCard(token: string | null, id: string) {
  return request<{ card: TaskboardCard }>(`/v1/taskboard/cards/${encodeURIComponent(id)}`, { method: "GET" }, token);
}

export function patchTaskboardCard(token: string | null, id: string, input: TaskboardCardPatchInput) {
  return request<{ card: TaskboardCard }>(`/v1/taskboard/cards/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  }, token);
}

export function deleteTaskboardCard(token: string | null, id: string) {
  return request<{ card_id: string; deleted: boolean }>(`/v1/taskboard/cards/${encodeURIComponent(id)}`, { method: "DELETE" }, token);
}

export function executeTaskboardCard(token: string | null, id: string) {
  return request<{ execution_id: string; session_id: string }>(`/v1/taskboard/cards/${encodeURIComponent(id)}/execute`, { method: "POST" }, token);
}

export function observeTaskboardExecution(token: string | null, id: string, executionId: string) {
  return request<{ observation: TaskboardExecutionObservation; live: boolean }>(
    `/v1/taskboard/cards/${encodeURIComponent(id)}/executions/${encodeURIComponent(executionId)}/observe`,
    { method: "POST" },
    token,
  );
}

export function recoverTaskboardExecution(token: string | null, id: string, executionId: string, message: string) {
  return request<{ session_id: string; message: string }>(
    `/v1/taskboard/cards/${encodeURIComponent(id)}/executions/${encodeURIComponent(executionId)}/recover`,
    { method: "POST", body: JSON.stringify({ message }) },
    token,
  );
}

export function retryTaskboardExecution(token: string | null, id: string, executionId: string) {
  return request<{ turn_id: string; message: string }>(
    `/v1/taskboard/cards/${encodeURIComponent(id)}/executions/${encodeURIComponent(executionId)}/retry`,
    { method: "POST" },
    token,
  );
}

export function reconcileTaskboard(token: string | null) {
  return request<{ reconcile_report: TaskboardReconcileReport }>(
    `/v1/taskboard/reconcile`,
    { method: "POST" },
    token,
  );
}

