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
//
// This is a BLACKLIST: only the center shell's own control-plane paths stay
// local, so the shell keeps operating while a remote node is active. Every
// other interface/API (chat, files, terminal, taskboard, business agents,
// agent templates, usage, notes, settings, memory, skills, automation,
// providers, ...) is node-scoped and proxies to the active remote node so
// remote mode reflects the node's own state.
//
// Center shell paths kept local:
//   /meta       instance metadata (workspace dir, etc.) for the shell itself
//   /control..  node management / control plane (the "nodes" app)
//   /push..     center web-push subscriptions (VAPID keys, browser subs)
//   /relay      relay control websocket
function nodeProxyPath(path: string): string | null {
  const nodeID = useNodeContextStore.getState().nodeID;
  if (!nodeID) {
    return null;
  }
  const p = path.startsWith("/") ? path : `/${path}`;
  if (
    p === "/meta" ||
    p.startsWith("/control") ||
    p.startsWith("/push") ||
    p.startsWith("/relay")
  ) {
    return null;
  }
  return `/control/nodes/${encodeURIComponent(nodeID)}/proxy${p}`;
}

export function authHeaders(token: string | null): HeadersInit {
  if (!token) {
    return {};
  }
  return {
    Authorization: `Bearer ${token}`,
  };
}

/** Bound on regular API requests so a silent relay/node failure surfaces as an
 *  error instead of hanging the UI (stuck spinner, refresh never completing). */
const DEFAULT_REQUEST_TIMEOUT_MS = 60_000;

export async function request<T>(path: string, init: RequestInit = {}, token: string | null = null): Promise<T> {
  const isFormData = typeof FormData !== "undefined" && init.body instanceof FormData;
  // Use the caller's signal when provided (e.g. long uploads); otherwise bound
  // the request so it cannot hang forever.
  const signal = init.signal ?? AbortSignal.timeout(DEFAULT_REQUEST_TIMEOUT_MS);
  const response = await fetch(apiURL(path), {
    ...init,
    signal,
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

export async function parseAPIError(response: Response): Promise<APIError> {
  const data = await response.json().catch(() => ({ error: response.statusText }));
  return new APIError(response.status, data.error ?? response.statusText);
}
