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
