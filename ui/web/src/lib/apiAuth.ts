import { request } from "./apiClient";

// Email + password sign-in (design §3.1/§3.5). Kept in its own module rather
// than appended to api.ts, which is already close to the source line budget.

export interface AuthTenant {
  id: string;
  name: string;
  plan?: string;
}

export interface AuthPrincipal {
  user_id: string;
  tenant_id?: string;
  platform_role?: string;
  role?: string;
  entitlements: string[];
  auth_method?: string;
}

export interface AuthSession {
  token: string;
  expires_at: string;
  principal: AuthPrincipal;
  tenant?: AuthTenant;
}

export interface AuthProfile {
  principal: AuthPrincipal;
  tenant?: AuthTenant;
}

/** registerAccount creates a NEW tenant together with its owner account (design
 *  §3.5).
 *
 *  Anonymous callers are refused while self-service signup is closed, so the
 *  superadmin surface passes the shared web token: that is the operator's
 *  documented way to create a tenant without opening signup to everyone. */
export function registerAccount(
  input: {
    email: string;
    password: string;
    tenantName?: string;
  },
  token: string | null = null,
): Promise<AuthSession> {
  return request<AuthSession>(
    "/auth/register",
    {
      method: "POST",
      body: JSON.stringify({
        email: input.email,
        password: input.password,
        tenant_name: input.tenantName ?? "",
      }),
    },
    token,
  );
}

export function loginAccount(input: { email: string; password: string }): Promise<AuthSession> {
  return request<AuthSession>("/auth/login", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

/** logoutSession revokes the current session. Requires a session token: the
 *  shared web token is not a revocable session and the server answers 400. */
export function logoutSession(token: string | null): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>("/auth/logout", { method: "POST", body: JSON.stringify({}) }, token);
}

/** getAuthProfile resolves the caller's identity. Used to detect whether a
 *  stored credential is still valid before rendering the workspace. */
export function getAuthProfile(token: string | null): Promise<AuthProfile> {
  return request<AuthProfile>("/auth/me", { method: "GET" }, token);
}
