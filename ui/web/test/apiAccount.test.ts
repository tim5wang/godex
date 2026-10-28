import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  acceptMemberInvite,
  changePassword,
  createMemberInvite,
  getAccountProfile,
  listAccountSessions,
  listMemberInvites,
  listMembers,
  removeMember,
  revokeAccountSession,
  revokeMemberInvite,
  revokeOtherSessions,
  setMemberRole,
} from "../src/lib/apiAccount";
import { useNodeContextStore } from "../src/store/nodeContext";

// The account/member API contract (design §3.7).
//
// These pin the wire shape the UI depends on. They exist because the two route
// families are easy to get subtly wrong in a way no other gate catches:
//   - tenant scope and admin scope share one implementation, so a mix-up sends a
//     tenant admin to the platform-only route (403) or a superadmin to their own
//     tenant (wrong tenant entirely);
//   - revoking an invite from the superadmin console needs its OWN route: the
//     tenant route exists, so a typo'd path looks plausible and 404s at runtime;
//   - accepting an invite must NOT send a credential (the one-time code IS the
//     credential, and there is no session yet).
//
// TypeScript alone cannot catch any of these: every one of them is a valid string.

interface Call {
  url: string;
  method: string;
  body: unknown;
  headers: Record<string, string>;
}

const calls: Call[] = [];
const originalFetch = globalThis.fetch;

beforeEach(() => {
  calls.length = 0;
  useNodeContextStore.getState().clearNode();
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const headers: Record<string, string> = {};
    for (const [k, v] of Object.entries((init?.headers ?? {}) as Record<string, string>)) {
      headers[k.toLowerCase()] = v;
    }
    calls.push({
      url,
      method: init?.method ?? "GET",
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      headers,
    });
    return new Response(JSON.stringify({}), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof globalThis.fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

const last = (): Call => calls[calls.length - 1];

describe("member management route families", () => {
  // One component serves both surfaces; these two blocks are what keeps the
  // shared implementation from being pointed at the wrong route family.
  it("uses the tenant routes when managing your own tenant", async () => {
    await listMembers("tok", { kind: "tenant" });
    expect(last().url).toBe("/api/tenant/members");
    expect(last().method).toBe("GET");

    await setMemberRole("tok", { kind: "tenant" }, "usr_2", "admin");
    expect(last().url).toBe("/api/tenant/members/usr_2/role");
    expect(last().method).toBe("PUT");
    expect(last().body).toEqual({ role: "admin" });

    await removeMember("tok", { kind: "tenant" }, "usr_2");
    expect(last().url).toBe("/api/tenant/members/usr_2");
    expect(last().method).toBe("DELETE");

    await listMemberInvites("tok", { kind: "tenant" });
    expect(last().url).toBe("/api/tenant/invites");

    await createMemberInvite("tok", { kind: "tenant" }, { role: "member", ttlHours: 48 });
    expect(last().url).toBe("/api/tenant/invites");
    expect(last().method).toBe("POST");
    expect(last().body).toEqual({ role: "member", ttl_hours: 48 });
  });

  it("uses the platform routes when a superadmin manages a tenant by id", async () => {
    const scope = { kind: "admin", tenantID: "tnt_acme" } as const;

    await listMembers("tok", scope);
    expect(last().url).toBe("/api/admin/tenants/tnt_acme/members");

    await setMemberRole("tok", scope, "usr_2", "member");
    expect(last().url).toBe("/api/admin/tenants/tnt_acme/members/usr_2/role");

    await removeMember("tok", scope, "usr_2");
    expect(last().url).toBe("/api/admin/tenants/tnt_acme/members/usr_2");

    await listMemberInvites("tok", scope);
    expect(last().url).toBe("/api/admin/tenants/tnt_acme/invites");

    await createMemberInvite("tok", scope, { role: "admin", ttlHours: 0 });
    expect(last().url).toBe("/api/admin/tenants/tnt_acme/invites");
  });

  it("revokes an invite through the SAME scope it was created in", async () => {
    // The superadmin console's revoke button 404'd in production because the
    // tenant route existed and the admin route did not. Pinning both here keeps a
    // future edit from routing one scope's revoke at the other's path.
    await revokeMemberInvite("tok", { kind: "tenant" }, "inv_1");
    expect(last().url).toBe("/api/tenant/invites/inv_1");
    expect(last().method).toBe("DELETE");

    await revokeMemberInvite("tok", { kind: "admin", tenantID: "tnt_acme" }, "inv_1");
    expect(last().url).toBe("/api/admin/tenants/tnt_acme/invites/inv_1");
    expect(last().method).toBe("DELETE");
  });

  it("encodes ids so an id containing a slash cannot address another route", async () => {
    await removeMember("tok", { kind: "tenant" }, "usr/../../admin");
    expect(last().url).toBe("/api/tenant/members/usr%2F..%2F..%2Fadmin");

    await removeMember("tok", { kind: "admin", tenantID: "tnt/a" }, "usr_2");
    expect(last().url).toBe("/api/admin/tenants/tnt%2Fa/members/usr_2");
  });

  it("sends no credential when redeeming an invite", async () => {
    // The invitee has no session yet — requiring one would deadlock the invite.
    await acceptMemberInvite({ code: "gui_abc", email: "p@example.com", password: "correct-horse" });
    expect(last().url).toBe("/api/auth/accept-invite");
    expect(last().method).toBe("POST");
    expect(last().headers.authorization).toBeUndefined();
    expect(last().body).toEqual({ code: "gui_abc", email: "p@example.com", password: "correct-horse" });
  });
});

describe("user centre routes", () => {
  it("scopes every call to the caller (no id in the path)", async () => {
    // The server derives the user from the principal, so nothing here can address
    // another account — that property is the reason these routes are id-free.
    await getAccountProfile("tok");
    expect(last().url).toBe("/api/account");

    await listAccountSessions("tok");
    expect(last().url).toBe("/api/account/sessions");
  });

  it("revokes one session by id, and all others without an id", async () => {
    await revokeAccountSession("tok", 42);
    expect(last().url).toBe("/api/account/sessions/42");
    expect(last().method).toBe("DELETE");

    // "sign out everywhere else" must keep the current device, so the server keys
    // it on the presented token rather than on an id the client could choose.
    await revokeOtherSessions("tok");
    expect(last().url).toBe("/api/account/sessions/revoke-others");
    expect(last().method).toBe("POST");
  });

  it("sends the current password when changing it", async () => {
    // An authenticated session alone must not be enough to change the password:
    // that would make a stolen session equal to a stolen account.
    await changePassword("tok", { currentPassword: "old-secret", newPassword: "new-secret" });
    expect(last().url).toBe("/api/account/password");
    expect(last().method).toBe("POST");
    expect(last().body).toEqual({ current_password: "old-secret", new_password: "new-secret" });
  });

  it("attaches the credential to every scoped call", async () => {
    await getAccountProfile("tok-123");
    expect(last().headers.authorization).toBe("Bearer tok-123");
  });
});
