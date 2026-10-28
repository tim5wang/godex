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
// This is a BLACKLIST: only the center's own paths stay local, so the shell keeps
// operating while a remote node is active. Every other interface/API (chat, files,
// terminal, taskboard, business agents, agent templates, notes, settings, memory,
// skills, automation, providers, ...) is node-scoped and proxies to the active
// remote node so remote mode reflects the node's own state.
//
// What stays local, and why it is NOT a detail of the node:
//   /meta         instance metadata (workspace dir, etc.) for the shell itself
//   /control..    node management / control plane (the "nodes" app)
//   /admin..      platform administration (tenants, sharing, metering). The
//                 superadmin administers THIS center's tenants, so proxying would
//                 answer from the wrong instance (or 403 there)
//   /push..       center web-push subscriptions (VAPID keys, browser subs)
//   /relay        relay control websocket
//   /auth..       credential operations (login/register/logout/me/accept-invite).
//                 These authenticate the CALLER to THIS instance; the node has its
//                 own account store and has never heard of this principal
//   /account..    the caller's own profile, devices and sign-in history, which live
//                 in the center's account store (design §3.7)
//   /tenant..     tenant administration: members, invites, delegations, consent —
//                 all center-side account records (design §3.6/§3.7)
//   /delegations..the operator's own active grants (center-side, same store)
//   /usage/relay  relay metering, recorded where the traffic is forwarded (here)
//
// The account/tenant/auth/metering entries were a real defect: they were missing,
// so with a remote node active the user centre asked the NODE for the caller's own
// account. Nothing failed loudly in the common case, which is why it went unnoticed.
//
// NOTE on granularity: /usage is split. Relay metering is center data, but
// /usage/calls, /usage/keys, /usage/models and /usage/sessions describe the node's
// own model traffic, so a blanket "/usage" rule would be wrong either way.
const CENTER_LOCAL_EXACT = new Set([
  "/meta",
  "/relay",
  "/usage/relay",
]);

const CENTER_LOCAL_PREFIXES = [
  "/control",
  "/admin",
  "/push",
  "/auth",
  "/account",
  "/tenant",
  "/delegations",
];

/**
 * True when the path is answered by this instance rather than a remote node.
 *
 * Matched on a path boundary rather than a bare prefix, so a future "/controls" or
 * "/adminx" route cannot be silently swallowed by this list.
 */
function isCenterLocal(pathOnly: string): boolean {
  if (CENTER_LOCAL_EXACT.has(pathOnly)) {
    return true;
  }
  return CENTER_LOCAL_PREFIXES.some(
    (prefix) => pathOnly === prefix || pathOnly.startsWith(`${prefix}/`),
  );
}

function nodeProxyPath(path: string): string | null {
  const nodeID = useNodeContextStore.getState().nodeID;
  if (!nodeID) {
    return null;
  }
  const p = path.startsWith("/") ? path : `/${path}`;
  // Compare without the query/fragment: /usage/relay is called as
  // /usage/relay?hours=24, and matching on the raw string would send the metering
  // read to the node.
  const pathOnly = p.split(/[?#]/)[0];
  if (isCenterLocal(pathOnly)) {
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
