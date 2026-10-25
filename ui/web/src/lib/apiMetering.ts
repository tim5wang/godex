import { request } from "./apiClient";

// Relay metering (design §7, basic tier). Two audiences with two scopes:
//
//   getRelayUsage      a tenant reads ITS OWN forwarded usage
//   getAdminRelayUsage the operator reconciles EVERY tenant
//
// The tenant call takes no tenant id: the server derives it from the principal,
// since accepting one would let any signed-in user read another tenant's traffic
// metadata.

/** RelayUsageSummary is one tenant's forwarded-traffic counters over a window. */
export interface RelayUsageSummary {
  tenant_id: string;
  requests: number;
  request_bytes: number;
  response_bytes: number;
  first_at?: string;
  last_at?: string;
}

export interface RelayUsageResponse {
  summary: RelayUsageSummary;
  window_from: string;
  window_to: string;
}

export interface AdminRelayUsageResponse {
  tenants: RelayUsageSummary[];
  total: RelayUsageSummary;
  window_from: string;
  window_to: string;
}

/** getRelayUsage reads the caller's own tenant usage. `hours` defaults to 24 on
 *  the server; an invalid window is a 400 there rather than a silently different
 *  period, which is why the value is passed through unmodified. */
export function getRelayUsage(token: string | null, hours = 24): Promise<RelayUsageResponse> {
  return request<RelayUsageResponse>(`/usage/relay?hours=${encodeURIComponent(String(hours))}`, { method: "GET" }, token);
}

/** getAdminRelayUsage reads every tenant's usage plus the grand total.
 *
 *  The total is returned alongside the rows on purpose: it must equal their sum,
 *  and a mismatch is how a row belonging to no tenant (the operator's own direct
 *  traffic) becomes visible instead of silently missing. */
export function getAdminRelayUsage(token: string | null, hours = 24): Promise<AdminRelayUsageResponse> {
  return request<AdminRelayUsageResponse>(
    `/admin/usage/relay?hours=${encodeURIComponent(String(hours))}`,
    { method: "GET" },
    token,
  );
}
