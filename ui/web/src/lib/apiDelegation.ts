import { request } from "./apiClient";

// Operating-access grants (design §3.6, decision ⑪).
//
// The grant belongs to the TENANT, so the tenant's own console creates, lists and
// withdraws it. There is deliberately no "grant myself access" call here: the
// operator surface only ever READS what it was given (`getMyDelegations`).

/** TenantDelegation is one grant of read access by a tenant to an operator.
 *
 *  `active` is computed server-side so both consoles agree on liveness — a client
 *  comparing timestamps itself would be a second implementation of the expiry rule,
 *  and the two would eventually disagree. */
export interface TenantDelegation {
  id: string;
  tenant_id: string;
  operator_user_id: string;
  granted_by?: string;
  scope?: string;
  reason?: string;
  created_at?: string;
  expires_at?: string;
  revoked_at?: string;
  active: boolean;
}

/** DelegationAuditEntry is one recorded USE of a grant. The rows exist whether or
 *  not anyone mentions the access, which is what makes the trail trustworthy. */
export interface DelegationAuditEntry {
  delegation_id?: string;
  tenant_id?: string;
  operator_user_id?: string;
  action?: string;
  target?: string;
  allowed: boolean;
  reason?: string;
  at?: string;
}

/** ConsentRecord is the user-agreement trail (design §3.6, 用户协议). */
export interface ConsentRecord {
  tenant_id: string;
  user_id?: string;
  terms_version: string;
  accepted_at: string;
  source_ip?: string;
}

export function listTenantDelegations(
  token: string | null,
): Promise<{ delegations: TenantDelegation[]; max_ttl_hours: number }> {
  return request<{ delegations: TenantDelegation[]; max_ttl_hours: number }>(
    "/tenant/delegations",
    { method: "GET" },
    token,
  );
}

/** createTenantDelegation authorizes ONE named operator for a bounded window.
 *
 *  `operatorUserID` is required by the server: a grant that names nobody would be
 *  "any operator, any time", which is exactly the silent bypass the design rejects. */
export function createTenantDelegation(
  token: string | null,
  input: { operatorUserID: string; ttlHours?: number; reason?: string },
): Promise<{ delegation: TenantDelegation }> {
  return request<{ delegation: TenantDelegation }>(
    "/tenant/delegations",
    {
      method: "POST",
      body: JSON.stringify({
        operator_user_id: input.operatorUserID,
        ttl_hours: input.ttlHours ?? 0,
        reason: input.reason ?? "",
      }),
    },
    token,
  );
}

export function revokeTenantDelegation(token: string | null, id: string): Promise<{ revoked: boolean }> {
  return request<{ revoked: boolean }>(
    `/tenant/delegations/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    token,
  );
}

export function listDelegationAudit(
  token: string | null,
  limit = 100,
): Promise<{ entries: DelegationAuditEntry[] }> {
  return request<{ entries: DelegationAuditEntry[] }>(
    `/tenant/delegations/audit?limit=${encodeURIComponent(String(limit))}`,
    { method: "GET" },
    token,
  );
}

export function listConsentRecords(
  token: string | null,
): Promise<{ records: ConsentRecord[]; current_terms_version: string }> {
  return request<{ records: ConsentRecord[]; current_terms_version: string }>(
    "/tenant/consent",
    { method: "GET" },
    token,
  );
}

/** recordConsent records a member's acceptance of a terms revision.
 *
 *  Signup already writes one; this covers agreeing later to a new revision. */
export function recordConsent(
  token: string | null,
  termsVersion: string,
): Promise<{ recorded: boolean; terms_version: string }> {
  return request<{ recorded: boolean; terms_version: string }>(
    "/tenant/consent",
    { method: "POST", body: JSON.stringify({ terms_version: termsVersion, accepted: true }) },
    token,
  );
}

/** getMyDelegations lists the grants issued TO the caller.
 *
 *  Scoped to the caller so the operator console shows what it may read without
 *  becoming a map of who else is inspecting whom. */
export function getMyDelegations(token: string | null): Promise<{ delegations: TenantDelegation[] }> {
  return request<{ delegations: TenantDelegation[] }>("/delegations/mine", { method: "GET" }, token);
}
