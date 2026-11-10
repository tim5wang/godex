import { request } from "./apiClient";

// Platform administration (design §3.6). Kept in its own module rather than
// appended to api.ts, which is already at 861 lines against a 900-line budget.

/** AdminTenant is one row of the superadmin tenant list. */
export interface AdminTenant {
  id: string;
  name: string;
  plan?: string;
  /** The superadmin's sharing switch: whether this tenant's members may expose
   *  their nodes to each other. */
  sharing_enabled: boolean;
  member_count: number;
  created_at?: string;
  /** How many people may be members of this tenant. 0 means unlimited. */
  max_users?: number;
  /** How many devices EACH MEMBER may own; pc / cloud / phone / watch all count
   *  against this same number. 0 means unlimited.
   *
   *  Per USER, not per tenant: a tenant-wide device cap cannot express "2 devices
   *  each" — it would shrink for everyone each time somebody joined. */
  max_devices_per_user?: number;
  /** How many devices the tenant owns ACROSS ALL MEMBERS. Reported for the estate at
   *  a glance; deliberately NOT compared against the per-user limit, because that
   *  ratio is per member and this row does not model members. */
  node_count?: number;
  /** The tenant's daily forwarded-byte allowance. 0 means unlimited. */
  max_daily_bytes?: number;
  /** How much of that allowance is spent in the CURRENT quota day, from the same
   *  counters and the same window the quota enforces — so this is the number the
   *  refusal is based on, not a differently-measured approximation of it. */
  daily_used_bytes?: number;
}

/** AdminMember is a user's membership inside a tenant. Carries the email but
 *  never any credential material — the server strips password hashes. */
export interface AdminMember {
  user_id: string;
  email?: string;
  role: string;
  joined_at?: string;
}

export interface AdminPolicy {
  default_visibility: string;
  roles: string[];
  /** What a FRESH signup starts with (0 = unlimited for each), from the server so the
   *  console shows the real terms rather than guessing at config it cannot read. */
  free_tier_max_users?: number;
  free_tier_max_devices_per_user?: number;
  free_tier_max_daily_bytes?: number;
}

/** listAdminTenants returns every tenant on this instance. Crosses tenant
 *  boundaries by design, so the server answers 403 for a non-operator. */
export function listAdminTenants(token: string | null): Promise<{ tenants: AdminTenant[] }> {
  return request<{ tenants: AdminTenant[] }>("/admin/tenants", { method: "GET" }, token);
}

export function listAdminMembers(token: string | null, tenantID: string): Promise<{ members: AdminMember[] }> {
  return request<{ members: AdminMember[] }>(
    `/admin/tenants/${encodeURIComponent(tenantID)}/members`,
    { method: "GET" },
    token,
  );
}

/** setTenantSharing flips the superadmin's sharing switch for one tenant.
 *
 *  Turning it off does not rewrite node visibility: nodes keep whatever their
 *  owners chose, and the authorization seam simply stops honouring "tenant"
 *  while the switch is off. That makes it reversible without a migration. */
export function setTenantSharing(
  token: string | null,
  tenantID: string,
  enabled: boolean,
): Promise<{ tenant_id: string; sharing_enabled: boolean; updated_at: string }> {
  return request<{ tenant_id: string; sharing_enabled: boolean; updated_at: string }>(
    `/admin/tenants/${encodeURIComponent(tenantID)}/sharing`,
    { method: "PUT", body: JSON.stringify({ enabled }) },
    token,
  );
}

/** setTenantMaxDevicesPerUser sets how many devices EACH MEMBER of a tenant may own.
 *
 *  0 means unlimited, and lowering the number does NOT evict existing devices: it only
 *  refuses new enrolments, so a policy change never silently orphans machines that are
 *  already running. */
export function setTenantMaxDevicesPerUser(
  token: string | null,
  tenantID: string,
  maxDevicesPerUser: number,
): Promise<{ tenant_id: string; max_devices_per_user: number; updated_at: string }> {
  return request<{ tenant_id: string; max_devices_per_user: number; updated_at: string }>(
    `/admin/tenants/${encodeURIComponent(tenantID)}/max-devices-per-user`,
    { method: "PUT", body: JSON.stringify({ max_devices_per_user: maxDevicesPerUser }) },
    token,
  );
}

/** setTenantMaxUsers sets how many people may be members of a tenant. 0 = unlimited.
 *
 *  Separate from the device allowance because they are two different products of the
 *  plan (how many people vs how many machines each), and because writing one column per
 *  endpoint is what stops one limit from silently resetting the other. */
export function setTenantMaxUsers(
  token: string | null,
  tenantID: string,
  maxUsers: number,
): Promise<{ tenant_id: string; max_users: number; updated_at: string }> {
  return request<{ tenant_id: string; max_users: number; updated_at: string }>(
    `/admin/tenants/${encodeURIComponent(tenantID)}/max-users`,
    { method: "PUT", body: JSON.stringify({ max_users: maxUsers }) },
    token,
  );
}

/** setTenantMaxDailyBytes sets the tenant's daily forwarded-traffic allowance, in MB.
 *
 *  MB rather than bytes because that is the unit an operator reasons in ("100", not
 *  "104857600"), and because the config side already speaks MB for the free tier — two
 *  units for one number is how an operator ends up setting 1/1000th of what they meant.
 *  The server converts; this end stays human.
 *
 *  0 means unlimited, and lowering it does NOT disconnect anything: it only refuses new
 *  forwarded requests until the next daily reset. */
export function setTenantMaxDailyMB(
  token: string | null,
  tenantID: string,
  maxDailyMB: number,
): Promise<{ tenant_id: string; max_daily_bytes: number; updated_at: string }> {
  return request<{ tenant_id: string; max_daily_bytes: number; updated_at: string }>(
    `/admin/tenants/${encodeURIComponent(tenantID)}/max-daily-bytes`,
    { method: "PUT", body: JSON.stringify({ max_daily_bytes: mbToBytes(maxDailyMB) }) },
    token,
  );
}

/** bytesPerMB is the binary megabyte the server also uses, so a limit set here and the
 *  free-tier default in config mean the same thing. Using 10^6 here would make "100 MB"
 *  in this box 4.9% smaller than "100" in the config file. */
const bytesPerMB = 1024 * 1024;

/** mbToBytes converts a whole number of MB for the wire. Kept as one function so the
 *  rounding rule lives in one place. */
export function mbToBytes(mb: number): number {
  return Math.max(0, Math.round(mb)) * bytesPerMB;
}

/** bytesToMB is the inverse, for pre-filling the edit box from a stored byte value. */
export function bytesToMB(bytes?: number): number {
  if (!bytes || bytes <= 0) {
    return 0;
  }
  return Math.round(bytes / bytesPerMB);
}
/** getAdminPolicy returns the effective platform policy so the UI renders
 *  defaults from the same source the authorization seam reads. */
export function getAdminPolicy(token: string | null): Promise<AdminPolicy> {
  return request<AdminPolicy>("/admin/policy", { method: "GET" }, token);
}

/** listAdminNodes returns every node WITH its ownership, unfiltered.
 *
 *  Distinct from listControlNodes on purpose: that one applies the NodeAccess
 *  policy, which deliberately does not let a platform admin cross a tenant
 *  boundary, so it would hide exactly the nodes the operator needs to manage. */
export function listAdminNodes(token: string | null): Promise<{ nodes: AdminNode[] }> {
  return request<{ nodes: AdminNode[] }>("/admin/nodes", { method: "GET" }, token);
}

export interface AdminNode {
  id: string;
  name?: string;
  status?: string;
  tenant_id?: string;
  owner_user_id?: string;
  visibility?: string;
}

/** claimNode points an existing node at a tenant/owner (design §4.4). This is
 *  the only way ownership is ever set: nodes cannot self-report it. */
export function claimNode(
  token: string | null,
  nodeID: string,
  input: { tenantID: string; ownerUserID: string; visibility?: string },
): Promise<AdminNode> {
  return request<AdminNode>(
    `/admin/nodes/${encodeURIComponent(nodeID)}/claim`,
    {
      method: "POST",
      body: JSON.stringify({
        tenant_id: input.tenantID,
        owner_user_id: input.ownerUserID,
        visibility: input.visibility ?? "",
      }),
    },
    token,
  );
}

/** setNodeVisibility changes how far one node is exposed (design §4.2).
 *
 *  Owner-only on the server, and refused when the superadmin has sharing off for
 *  the node's tenant: storing "tenant" while peers cannot reach it would
 *  advertise a node nobody can use. */
export function setNodeVisibility(
  token: string | null,
  nodeID: string,
  visibility: "private" | "tenant",
): Promise<AdminNode> {
  return request<AdminNode>(
    `/control/nodes/${encodeURIComponent(nodeID)}/visibility`,
    { method: "POST", body: JSON.stringify({ visibility }) },
    token,
  );
}
