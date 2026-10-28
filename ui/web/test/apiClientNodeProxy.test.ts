import { afterEach, describe, expect, it } from "vitest";
import { apiURL } from "../src/lib/apiClient";
import { useNodeContextStore } from "../src/store/nodeContext";

// Which paths stay on THIS instance while a remote node is active.
//
// Remote mode proxies almost everything to the active node so the UI reflects the
// node's own state. Some paths must NOT be proxied, because they are not the
// node's data at all:
//
//   - the account surface (/account): the caller's own profile, devices and
//     sign-in history live in the CENTER's account store. The node has its own
//     accounts database, so proxying answers from a different instance — and for
//     a tenant account on a node, from a database that has never heard of them.
//   - tenant administration (/tenant, /delegations): members, invites, delegations
//     and consent records belong to the center's account store (design §3.7/§3.6).
//   - credential operations (/auth): login/register/logout/me are how the shell
//     authenticates against THIS instance.
//   - relay metering (/usage/relay): recorded where the traffic is forwarded —
//     the center.
//
// This was a real defect: the blacklist held only /meta, /control, /admin, /push
// and /relay, so with a remote node active the user centre asked the NODE for the
// caller's account. Nothing failed loudly in the common case (a superadmin's node
// answers something), which is exactly why it needs pinning rather than eyeballing.

const REMOTE = "node-remote";
const PROXIED = (p: string) => `/api/control/nodes/${REMOTE}/proxy${p}`;

afterEach(() => useNodeContextStore.getState().clearNode());

describe("apiURL: center-local paths stay local while a remote node is active", () => {
  const CENTER_LOCAL = [
    "/meta",
    "/control/nodes",
    "/admin/tenants",
    "/push/subscriptions",
    "/relay",
    "/auth/login",
    "/auth/register",
    "/auth/logout",
    "/auth/me",
    "/auth/accept-invite",
    "/account",
    "/account/sessions",
    "/account/sessions/revoke-others",
    "/account/login-events",
    "/account/password",
    "/tenant/members",
    "/tenant/invites",
    "/tenant/delegations",
    "/tenant/consent",
    "/delegations/mine",
    "/usage/relay",
  ];

  it.each(CENTER_LOCAL)("%s stays on the center", (path) => {
    useNodeContextStore.getState().setNode(REMOTE, "Remote");
    expect(apiURL(path)).toBe(`/api${path}`);
  });

  it("keeps a query string when the path is center-local", () => {
    // /usage/relay is called with ?hours=..., and a comparison that included the
    // query would send the metering read to the node.
    useNodeContextStore.getState().setNode(REMOTE, "Remote");
    expect(apiURL("/usage/relay?hours=24")).toBe("/api/usage/relay?hours=24");
  });
});

describe("apiURL: node-scoped paths still proxy", () => {
  // The counterpart assertion: tightening the blacklist must not swallow the
  // paths that remote mode exists for.
  const NODE_SCOPED = [
    "/sessions",
    "/files/list",
    "/notes",
    "/skills/catalog",
    "/memory",
    "/config",
    "/agent-templates",
    "/automation",
    "/packages",
    "/providers",
    "/llm-capture",
    "/usage/calls",
    "/usage/keys",
    "/watch/devices",
  ];

  it.each(NODE_SCOPED)("%s proxies to the active node", (path) => {
    useNodeContextStore.getState().setNode(REMOTE, "Remote");
    expect(apiURL(path)).toBe(PROXIED(path));
  });

  it("keeps the node's own /usage/keys local to the node, not the center", () => {
    // Sibling of /usage/relay: one is center data, the other node data, so they
    // must not be lumped together by a prefix rule on /usage.
    useNodeContextStore.getState().setNode(REMOTE, "Remote");
    expect(apiURL("/usage/keys")).toBe(PROXIED("/usage/keys"));
    expect(apiURL("/usage/relay")).toBe("/api/usage/relay");
  });
});

describe("apiURL: with no remote node", () => {
  it("sends everything to the center", () => {
    useNodeContextStore.getState().clearNode();
    for (const path of ["/sessions", "/account", "/auth/me", "/usage/relay"]) {
      expect(apiURL(path)).toBe(`/api${path}`);
    }
  });
});
