import { describe, expect, it } from "vitest";
import { messages } from "./messages";

// Every key the operating-access / relay-metering surfaces use must resolve in BOTH
// locales. useI18n falls back to the raw key when a lookup fails, so a missing
// translation renders "delegation.grantTitle" inside the UI instead of failing
// loudly — a silent defect that is otherwise only caught by looking at the screen.

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

// Spelled out rather than derived from the components, so a typo in a component is
// a test failure instead of a matching typo on both sides of the assertion.
const DELEGATION_KEYS = [
  "delegation.tenantSubtitle",
  "delegation.tenantNoTenant",
  "delegation.tenantUnavailable",
  "delegation.grantTitle",
  "delegation.grantCreate",
  "delegation.grantCreated",
  "delegation.grantCreateFailed",
  "delegation.grantRevoke",
  "delegation.grantRevoked",
  "delegation.grantRevokeFailed",
  "delegation.grantRevokeConfirm",
  "delegation.grantHint",
  "delegation.grantEmpty",
  "delegation.operatorLabel",
  "delegation.operatorRequired",
  "delegation.operatorCol",
  "delegation.ttlLabel",
  "delegation.reasonLabel",
  "delegation.reasonPlaceholder",
  "delegation.reasonCol",
  "delegation.stateCol",
  "delegation.stateActive",
  "delegation.stateExpired",
  "delegation.stateRevoked",
  "delegation.scopeCol",
  "delegation.expiresCol",
  "delegation.actionsCol",
  "delegation.auditTitle",
  "delegation.auditSubtitle",
  "delegation.auditEmpty",
  "delegation.auditAtCol",
  "delegation.auditActionCol",
  "delegation.auditTargetCol",
  "delegation.auditDelegationCol",
  "delegation.auditResultCol",
  "delegation.auditAllowed",
  "delegation.auditDenied",
  "delegation.consentTitle",
  "delegation.consentSubtitle",
  "delegation.consentNone",
  "delegation.consentVersionCol",
  "delegation.consentAcceptedCol",
  "delegation.consentSourceCol",
  "delegation.consentAgree",
  "delegation.consentAgreeHint",
  "delegation.consentRecorded",
  "delegation.consentRecordFailed",
  "delegation.mineTitle",
  "delegation.mineSubtitle",
  "delegation.mineEmpty",
  "delegation.mineTenantCol",
  // The banner's {count}/{operators}/{expires} placeholders must survive: a
  // translation that drops them renders a notice with holes in it.
  "delegation.bannerTitle",
  "delegation.bannerBody",
  "delegation.bannerManage",
];

const METERING_KEYS = [
  "usage.relayTitle",
  "usage.relaySubtitle",
  "usage.relayNoTenant",
  "usage.relayUnavailable",
  "usage.relayWindow",
  "usage.relayWindow1h",
  "usage.relayWindow24h",
  "usage.relayWindow7d",
  "settings.adminRelayTitle",
  "settings.adminRelaySubtitle",
  "settings.adminRelayReconciled",
  "settings.adminRelayMismatch",
  "settings.adminRelayEmpty",
  "settings.adminRelayTenantCol",
  "settings.adminRelayRequestsCol",
  "settings.adminRelayRequestBytesCol",
  "settings.adminRelayResponseBytesCol",
  "settings.adminRelayLastAtCol",
  "settings.adminRelayUnscoped",
];

const VISIBILITY_KEYS = [
  "nodes.visibility",
  "nodes.visibilityPrivate",
  "nodes.visibilityTenant",
  "nodes.visibilityHint",
  "nodes.visibilityUpdateSuccess",
  "nodes.visibilityUpdateFailed",
  "nodes.visibilityOwnerOnly",
  "nodes.visibilityPeerNote",
];

const SETTINGS_KEYS = ["settings.accessTabTitle", "settings.adminForbidden"];

const ALL_KEYS = [...DELEGATION_KEYS, ...METERING_KEYS, ...VISIBILITY_KEYS, ...SETTINGS_KEYS];

describe("operating-access surface i18n", () => {
  it.each(["en", "zh"] as const)("resolves every key in %s", (locale) => {
    const missing = ALL_KEYS.filter((key) => resolve(locale, key) === null);
    expect(missing).toEqual([]);
  });

  it("keeps interpolation placeholders in both locales", () => {
    // A notice that literally reads "Operators: ; earliest expiry:" is worse than an
    // untranslated string, so the placeholders are asserted, not just the keys.
    const placeholders: Record<string, string[]> = {
      "delegation.bannerTitle": ["count"],
      "delegation.bannerBody": ["operators", "expires"],
      "delegation.grantHint": ["hours"],
      "delegation.consentAgree": ["version"],
      "settings.adminRelayReconciled": ["count"],
      "settings.adminRelayMismatch": ["total", "summed"],
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
});
