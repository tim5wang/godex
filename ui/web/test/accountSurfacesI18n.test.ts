import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { messages } from "../src/i18n/messages";

// The user-centre / member-management surface (design §3.7): every key it renders
// must resolve in BOTH locales, and the lists here must keep up with the components.
//
// Why this file lives in test/ rather than beside src/i18n/messages.ts (where the
// sibling access-surface test sits): it reads component sources from disk, and the
// app tsconfig typechecks `src/` without @types/node. Putting node:fs imports in
// src/ fails `pnpm typecheck`, which is a gate — so the node-flavoured test belongs
// in the node-flavoured test directory, and the key list stays in ONE place instead
// of being split across two files.
//
// useI18n falls back to the raw key when a lookup fails, so a missing translation
// renders "members.inviteStateUnused" inside the UI rather than failing loudly —
// the same silent defect that once shipped `work_fact` as a raw key in the Chinese UI.

function resolve(locale: "en" | "zh", key: string): string | null {
  let current: unknown = (messages as Record<string, unknown>)[locale];
  for (const part of key.split(".")) {
    if (!current || typeof current !== "object" || !(part in (current as Record<string, unknown>))) {
      return null;
    }
    current = (current as Record<string, unknown>)[part];
  }
  return typeof current === "string" ? current : null;
}

const ACCOUNT_KEYS = [
  "account.createdAt",
  "account.currentPassword",
  "account.currentPasswordRequired",
  "account.email",
  "account.eventAt",
  "account.eventIP",
  "account.eventJoinedTenant",
  "account.eventKind",
  "account.eventLogin",
  "account.eventLogout",
  "account.eventPasswordChanged",
  "account.eventRegister",
  "account.eventSessionRevoked",
  "account.eventSessionsRevoked",
  "account.eventsEmpty",
  "account.eventsSubtitle",
  "account.eventsTitle",
  "account.loadFailed",
  "account.newPassword",
  "account.newPasswordRequired",
  "account.passwordChangeFailed",
  "account.passwordChanged",
  "account.passwordHint",
  "account.passwordTitle",
  "account.platformRole",
  "account.profileTitle",
  "account.revokeOthers",
  "account.revokeOthersConfirm",
  "account.revokeOthersDone",
  "account.revokeOthersFailed",
  "account.role",
  "account.sessionCreated",
  "account.sessionCurrent",
  "account.sessionExpires",
  "account.sessionRevoke",
  "account.sessionRevokeConfirm",
  "account.sessionRevokeFailed",
  "account.sessionRevoked",
  "account.sessionTenant",
  "account.sessionsEmpty",
  "account.sessionsSubtitle",
  "account.sessionsTitle",
  "account.sharedTokenNotice",
  "account.subtitle",
  "account.tenant",
  "account.title",
  "account.userID",
];

const MEMBERS_KEYS = [
  "members.adminScopeNotice",
  "members.colActions",
  "members.colEmail",
  "members.colInviteCreated",
  "members.colInviteExpires",
  "members.colInviteRole",
  "members.colInviteState",
  "members.colJoined",
  "members.colRole",
  "members.colUserID",
  "members.createInvite",
  "members.gotoSettings",
  "members.inviteCodeOnce",
  "members.inviteCreateFailed",
  "members.inviteCreated",
  "members.inviteRevokeFailed",
  "members.inviteRevoked",
  "members.inviteStateExpired",
  "members.inviteStateRevoked",
  "members.inviteStateUnused",
  "members.inviteStateUsed",
  "members.invitesEmpty",
  "members.invitesSubtitle",
  "members.invitesTitle",
  "members.listTitle",
  "members.memberRemoved",
  "members.membersEmpty",
  "members.ownerLocked",
  "members.platformAdminPickTenant",
  "members.removeConfirm",
  "members.removeConfirmText",
  "members.removeFailed",
  "members.removeMember",
  "members.revokeInvite",
  "members.revokeInviteConfirm",
  "members.roleAdmin",
  "members.roleChangeFailed",
  "members.roleChanged",
  "members.roleMember",
  "members.roleOwner",
  "members.subtitle",
  "members.tenantAdminOnly",
  "members.title",
  "members.ttlHours",
];

const SESSION_GATE_KEYS = [
  "sessionGate.body",
  "sessionGate.pickNode",
  "sessionGate.title",
  "sessionGate.useSharedToken",
];

// The invite-code sign-in mode. Without it a second person can never join an
// existing tenant, because registering always creates a new one (design §3.5).
const AUTH_INVITE_KEYS = [
  "auth.acceptInvite",
  "auth.inviteCode",
  "auth.inviteCodeHint",
  "auth.inviteCodeRequired",
  "auth.inviteFailed",
  "auth.inviteScopeNote",
  "auth.inviteSuccess",
  "auth.modeInvite",
];

// Navigation labels and page subtitles are looked up indirectly (labelKey /
// headerSubtitleKey), so a typo there is invisible to a component-level grep.
const SHELL_KEYS = ["app.nav.account", "app.nav.members", "account.subtitle", "members.subtitle"];

const ALL_KEYS = [
  ...ACCOUNT_KEYS,
  ...MEMBERS_KEYS,
  ...SESSION_GATE_KEYS,
  ...AUTH_INVITE_KEYS,
  ...SHELL_KEYS,
];

describe("account & member surface i18n", () => {
  it.each(["en", "zh"] as const)("resolves every key in %s", (locale) => {
    const missing = ALL_KEYS.filter((key) => resolve(locale, key) === null);
    expect(missing).toEqual([]);
  });

  it("keeps interpolation placeholders in both locales", () => {
    // A notice reading "Signed out  other session(s)." is worse than an
    // untranslated string, so the placeholder is asserted, not just the key.
    const placeholders: Record<string, string[]> = {
      "account.revokeOthersDone": ["count"],
    };
    for (const [key, names] of Object.entries(placeholders)) {
      for (const locale of ["en", "zh"] as const) {
        const text = resolve(locale, key) ?? "";
        for (const name of names) {
          expect(text, `${locale}:${key} is missing {${name}}`).toContain(`{${name}}`);
        }
      }
    }
  });

  // The lists are spelled out so a typo is a failure rather than something both
  // sides agree on. The cost is staleness, so they are checked against the
  // components themselves: a newly used key must be added here, and a component
  // that asks for a key the message tree lacks is caught by the resolution test.
  it("covers every key the components actually ask for", () => {
    const files = [
      "../src/features/account/AccountPage.tsx",
      "../src/features/members/MembersPage.tsx",
      "../src/lib/accountLabels.ts",
      "../src/features/chat/layout/SessionsRail.tsx",
      "../src/pages/LoginPage.tsx",
    ];
    const families = new Set(["account", "members", "sessionGate"]);
    const known = new Set(ALL_KEYS);
    const missing: string[] = [];

    for (const file of files) {
      const source = readFileSync(fileURLToPath(new URL(file, import.meta.url)), "utf8");
      for (const match of source.matchAll(/["`]([a-zA-Z]+\.[A-Za-z0-9_.]+)["`]/g)) {
        const key = match[1];
        if (!families.has(key.split(".")[0])) continue;
        if (!known.has(key) && !missing.includes(key)) missing.push(key);
      }
    }

    expect([...new Set(missing)].sort()).toEqual([]);
  });

  it("keeps the two surfaces' copy distinct", () => {
    // A copy-paste that left the member page titled "Account" would still resolve
    // and still be wrong, so the distinction is asserted explicitly.
    for (const locale of ["en", "zh"] as const) {
      expect(resolve(locale, "account.title")).not.toBe(resolve(locale, "members.title"));
      expect(resolve(locale, "app.nav.account")).not.toBe(resolve(locale, "app.nav.members"));
    }
  });
});
