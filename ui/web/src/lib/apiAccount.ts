import { request } from "./apiClient";

// User centre, member management and person invites (design §3.6). Its own module
// rather than an append to api.ts, which is already against the source line budget.

/** AccountProfile is the caller's own view of their account.
 *
 *  `shared_token` marks the single web-token holder: that credential has no user
 *  row, so the surfaces that need one (sessions, login history) answer 400 rather
 *  than an empty list — an empty list would read as "nothing signed in", which is
 *  the opposite of the truth. */
export interface AccountProfile {
  user_id: string;
  email?: string;
  platform_role?: string;
  tenant_id?: string;
  tenant_name?: string;
  tenant_plan?: string;
  role?: string;
  created_at?: string;
  shared_token?: boolean;
}

/** AccountSession is one live login session of the caller. `current` is computed
 *  server-side so the UI never has to guess which device it is on. */
export interface AccountSession {
  id: number;
  tenant_id?: string;
  role?: string;
  created_at: string;
  expires_at: string;
  current: boolean;
}

/** LoginEvent is one entry of the caller's sign-in history. */
export interface LoginEvent {
  id: number;
  tenant_id?: string;
  at: string;
  event: string;
  source_ip?: string;
}

/** MemberView is a membership plus the email needed to render a row. */
export interface MemberView {
  user_id: string;
  email?: string;
  role: string;
  joined_at?: string;
}

/** MemberInvite is a one-time person-invite code. The plaintext `code` is NOT
 *  here: it exists only in the create response, and the store keeps a hash. */
export interface MemberInvite {
  id: string;
  tenant_id: string;
  role: string;
  created_by?: string;
  created_at?: string;
  expires_at?: string;
  used_at?: string;
  used_by_user?: string;
  revoked_at?: string;
}

export function getAccountProfile(token: string | null): Promise<AccountProfile> {
  return request<AccountProfile>("/account", { method: "GET" }, token);
}

export function listAccountSessions(token: string | null): Promise<{ sessions: AccountSession[] }> {
  return request<{ sessions: AccountSession[] }>("/account/sessions", { method: "GET" }, token);
}

export function revokeAccountSession(token: string | null, id: number): Promise<{ revoked: boolean }> {
  return request<{ revoked: boolean }>(`/account/sessions/${id}`, { method: "DELETE" }, token);
}

/** revokeOtherSessions signs out every other device, keeping this one: pressing
 *  "sign out everywhere else" must not sign the user out of the device they are on. */
export function revokeOtherSessions(token: string | null): Promise<{ revoked: number }> {
  return request<{ revoked: number }>(
    "/account/sessions/revoke-others",
    { method: "POST", body: JSON.stringify({}) },
    token,
  );
}

export function listLoginEvents(token: string | null, limit = 50): Promise<{ events: LoginEvent[] }> {
  return request<{ events: LoginEvent[] }>(
    `/account/login-events?limit=${encodeURIComponent(String(limit))}`,
    { method: "GET" },
    token,
  );
}

/** changePassword requires the CURRENT password: an authenticated session alone
 *  must not be enough to lock the real owner out of their account. */
export function changePassword(
  token: string | null,
  input: { currentPassword: string; newPassword: string },
): Promise<{ changed: boolean }> {
  return request<{ changed: boolean }>(
    "/account/password",
    {
      method: "POST",
      body: JSON.stringify({ current_password: input.currentPassword, new_password: input.newPassword }),
    },
    token,
  );
}

/** acceptMemberInvite redeems a person-invite: it signs the person in.
 *
 *  Unauthenticated by design — the one-time code IS the credential, and requiring a
 *  login as well would deadlock the invite for the person who has no account yet. */
export function acceptMemberInvite(input: {
  code: string;
  email: string;
  password: string;
}): Promise<{ token: string; principal: { user_id: string; tenant_id?: string; role?: string } }> {
  return request("/auth/accept-invite", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

/** MemberScope picks the route family: a tenant admin manages their own tenant,
 *  the platform superadmin manages any tenant by id. Same operations either way,
 *  so the UI and its ownership rules stay identical. */
export type MemberScope = { kind: "tenant" } | { kind: "admin"; tenantID: string };

function memberBase(scope: MemberScope): string {
  return scope.kind === "tenant"
    ? "/tenant/members"
    : `/admin/tenants/${encodeURIComponent(scope.tenantID)}/members`;
}

function inviteBase(scope: MemberScope): string {
  return scope.kind === "tenant"
    ? "/tenant/invites"
    : `/admin/tenants/${encodeURIComponent(scope.tenantID)}/invites`;
}

export function listMembers(token: string | null, scope: MemberScope): Promise<{ members: MemberView[] }> {
  return request<{ members: MemberView[] }>(memberBase(scope), { method: "GET" }, token);
}

export function setMemberRole(
  token: string | null,
  scope: MemberScope,
  userID: string,
  role: string,
): Promise<{ member: MemberView }> {
  return request<{ member: MemberView }>(
    `${memberBase(scope)}/${encodeURIComponent(userID)}/role`,
    { method: "PUT", body: JSON.stringify({ role }) },
    token,
  );
}

export function removeMember(
  token: string | null,
  scope: MemberScope,
  userID: string,
): Promise<{ removed: boolean }> {
  return request<{ removed: boolean }>(
    `${memberBase(scope)}/${encodeURIComponent(userID)}`,
    { method: "DELETE" },
    token,
  );
}

export function listMemberInvites(
  token: string | null,
  scope: MemberScope,
): Promise<{ invites: MemberInvite[] }> {
  return request<{ invites: MemberInvite[] }>(inviteBase(scope), { method: "GET" }, token);
}

/** createMemberInvite returns the plaintext code ONCE: the store keeps only a
 *  hash, so this response is the single moment it can be read or copied. */
export function createMemberInvite(
  token: string | null,
  scope: MemberScope,
  input: { role?: string; ttlHours?: number },
): Promise<{ invite: MemberInvite; code: string }> {
  return request<{ invite: MemberInvite; code: string }>(
    inviteBase(scope),
    {
      method: "POST",
      body: JSON.stringify({ role: input.role ?? "member", ttl_hours: input.ttlHours ?? 0 }),
    },
    token,
  );
}

export function revokeMemberInvite(
  token: string | null,
  scope: MemberScope,
  id: string,
): Promise<{ revoked: boolean }> {
  return request<{ revoked: boolean }>(
    `${inviteBase(scope)}/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    token,
  );
}
