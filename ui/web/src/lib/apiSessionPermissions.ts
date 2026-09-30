import type {
  LlmCaptureRecord,
  LlmCaptureStatus,
  LlmCaptureSummary,
  PendingPermission,
  PermissionResolution,
} from "./types";
import { request } from "./apiClient";

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

// Node-scoped approvals are proxied through the center to the owning node.
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

export function getLlmCaptureStatus(token: string | null) {
  return request<LlmCaptureStatus>("/llm-capture/status", { method: "GET" }, token);
}

export function setLlmCaptureEnabled(token: string | null, enabled: boolean) {
  return request<{ enabled: boolean }>(
    `/llm-capture/${enabled ? "enable" : "disable"}`,
    { method: "POST" },
    token,
  );
}

export function listLlmCaptureRecords(token: string | null, limit = 100) {
  return request<LlmCaptureSummary[]>(
    `/llm-capture/records?limit=${encodeURIComponent(String(limit))}`,
    { method: "GET" },
    token,
  );
}

export function getLlmCaptureRecord(token: string | null, id: string) {
  return request<LlmCaptureRecord>(
    `/llm-capture/records/${encodeURIComponent(id)}`,
    { method: "GET" },
    token,
  );
}

export function clearLlmCaptureRecords(token: string | null) {
  return request<{ cleared: boolean }>("/llm-capture/clear", { method: "POST" }, token);
}
