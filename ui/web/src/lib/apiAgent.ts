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
import { APIError, apiURL, authHeaders, parseAPIError, request } from "./apiClient";

export function listSessionSkills(token: string | null, sessionId: string) {
  return request<SkillCatalogEntry[]>(`/sessions/${encodeURIComponent(sessionId)}/skills/catalog`, { method: "GET" }, token);
}

/** Installed-skill catalog independent of any session, used by the new-session
 *  flow to pick which skills a fresh session starts with. */
export function listSkillsCatalog(token: string | null) {
  return request<SkillCatalogEntry[]>("/skills/catalog", { method: "GET" }, token);
}

/** Agent template (talent market): a named preset of an agent's capability
 *  boundary (bundles/tools, skills, MCP servers, persona, write scope)
 *  selected at session creation time. See docs/agent-role-and-bundle-design.md. */
export interface AgentTemplate {
  id: string;
  name: string;
  description?: string;
  avatar?: string;
  color?: string;
  scenarios?: string[];
  bundles?: string[];
  tools?: string[];
  write_enabled?: boolean;
  write_scope?: string[];
  mcp_servers?: string[];
  skills?: string[];
  packages?: string[];
  persona?: string;
  profile?: string;
  base_prompt?: string;
  model_hint?: string;
  budget_hint?: string;
  trim_heavy_sections?: boolean;
  memory?: string;
  engine?: string;
  project_dir?: string;
  source?: string;
}

export function listAgentTemplates(token: string | null) {
  return request<AgentTemplate[]>("/agent-templates", { method: "GET" }, token);
}

export function getAgentTemplate(token: string | null, id: string) {
  return request<AgentTemplate>(`/agent-templates/${encodeURIComponent(id)}`, { method: "GET" }, token);
}

export function createAgentTemplate(token: string | null, tpl: Partial<AgentTemplate>) {
  return request<AgentTemplate>("/agent-templates", { method: "POST", body: JSON.stringify(tpl) }, token);
}

export function updateAgentTemplate(token: string | null, id: string, tpl: Partial<AgentTemplate>) {
  return request<AgentTemplate>(`/agent-templates/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(tpl) }, token);
}

export function deleteAgentTemplate(token: string | null, id: string) {
  return request<{ deleted: string }>(`/agent-templates/${encodeURIComponent(id)}`, { method: "DELETE" }, token);
}

export function validateAgentTemplate(token: string | null, id: string) {
  return request<{ template: AgentTemplate; warnings: string[] }>(`/agent-templates/${encodeURIComponent(id)}/validate`, { method: "POST" }, token);
}

export interface ToolBundleOption {
  name: string;
  summary?: string;
  tools?: string[];
}

export interface TemplateFormOptions {
  bundles: ToolBundleOption[];
  tools: string[];
  engines: string[];
}

export function getAgentTemplateOptions(token: string | null) {
  return request<TemplateFormOptions>("/agent-templates/options", { method: "GET" }, token);
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

export function cancelQueuedTurn(token: string | null, sessionId: string, queueId: string) {
  return request<{ session_id: string; turn_id: string; status: string; updated_at: string; text?: string; attachments?: AttachmentRef[] }>(
    `/sessions/${encodeURIComponent(sessionId)}/queued/${encodeURIComponent(queueId)}/cancel`,
    {
      method: "POST",
    },
    token,
  );
}

export function steerQueuedTurn(token: string | null, sessionId: string, queueId: string) {
  return request<{ session_id: string; turn_id: string; status: string; updated_at: string }>(
    `/sessions/${encodeURIComponent(sessionId)}/queued/${encodeURIComponent(queueId)}/steer`,
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

/** 文本 → voice-engine TTS 合成 → WAV 音频 Blob（消息旁播放按钮用）。 */
export async function synthesizeSpeech(token: string | null, text: string): Promise<Blob> {
  const response = await fetch(apiURL("/v1/tts"), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(token),
    },
    body: JSON.stringify({ text }),
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

