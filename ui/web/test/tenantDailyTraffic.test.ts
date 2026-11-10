import { afterEach, describe, expect, it } from "vitest";
import { APIError } from "../src/lib/apiClient";
import { bytesToMB, mbToBytes, setTenantMaxDailyMB } from "../src/lib/apiAdmin";
import { quotaMessage } from "../src/lib/notifications";
import { useNodeContextStore } from "../src/store/nodeContext";
import { useLocaleStore } from "../src/store/locale";

// The daily-traffic allowance as the operator's console sees it.
//
// Three things here can go wrong silently, which is why they are pinned rather than
// eyeballed:
//   - the UNIT: the field is stored in bytes but edited in MB, and the free-tier config
//     also speaks MB. Getting the conversion wrong is a 1024x error that looks plausible
//     on screen ("100" either way) and would only surface as a user hitting the wall.
//   - the 429 rendering: `useI18n` falls back to the raw key, so a missing/half-wired quota
//     sentence shows "quota.exceeded" in the UI — the one error a self-service user is
//     supposed to act on.
//   - who may see it: an UNLIMITED tenant must never be told its allowance ran out.

const originalFetch = globalThis.fetch;
const originalLocale = useLocaleStore.getState().locale;
const ZERO = 0;

/** Set the locale through the STORE, not through setLocale: that setter writes
 *  window.localStorage, and vitest runs these tests in the node environment (no window).
 *  The store's value is what translate() reads, so this is the same input with none of
 *  the browser shim. */
function setLocale(locale: "en" | "zh") {
  useLocaleStore.setState({ locale });
}

afterEach(() => {
  globalThis.fetch = originalFetch;
  useNodeContextStore.getState().clearNode();
  setLocale(originalLocale);
});

describe("daily traffic allowance units", () => {
  it("treats MB as the binary megabyte both sides use", () => {
    // 1024*1024, not 10^6: the free-tier default in config is MB too, so "100" must mean
    // the same number in the console and in the config file.
    expect(mbToBytes(100)).toBe(104857600);
    expect(bytesToMB(104857600)).toBe(100);
  });

  it("round-trips a stored value back into the edit box", () => {
    for (const mb of [0, 1, 100, 1024]) {
      expect(bytesToMB(mbToBytes(mb))).toBe(mb);
    }
  });

  it("renders an unlimited (0) allowance as 0 MB rather than a negative or NaN", () => {
    expect(bytesToMB(undefined)).toBe(0);
    expect(bytesToMB(0)).toBe(0);
    expect(bytesToMB(-5)).toBe(0);
    expect(mbToBytes(-5)).toBe(0);
  });

  it("sends bytes, not the megabytes the operator typed", async () => {
    let sent: unknown = null;
    globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
      sent = typeof init?.body === "string" ? JSON.parse(init.body) : null;
      return new Response(JSON.stringify({ tenant_id: "tnt_x", max_daily_bytes: ZERO }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof globalThis.fetch;

    await setTenantMaxDailyMB("token", "tnt_x", 100);
    // The wire carries bytes; sending 100 here would set a 100-BYTE allowance, which the
    // server would accept and every user would hit within one request.
    expect(sent).toEqual({ max_daily_bytes: 104857600 });
  });
});

describe("quota 429 rendering", () => {
  it("names the usage, the limit and the reset — in the reader's language", () => {
    const err = new APIError(429, "daily forwarding allowance exhausted", {
      quota: "daily_bytes",
      used_bytes: 104857600,
      limit_bytes: 104857600,
      reset_at: "2026-11-12T00:00:00Z",
    });

    setLocale("zh");
    const zh = quotaMessage(err);
    expect(zh).not.toBeNull();
    expect(zh).toContain("100.0 MB");
    expect(zh).toContain("重置");
    // The raw key would mean the lookup failed, which renders literally in the UI.
    expect(zh).not.toContain("quota.exceeded");

    setLocale("en");
    const en = quotaMessage(err);
    expect(en).not.toBeNull();
    expect(en).toContain("100.0 MB");
    expect(en).not.toContain("quota.exceeded");
    // Same facts, different language: this is the whole point of not forwarding the
    // server's English sentence.
    expect(en).not.toBe(zh);
  });

  it("stays silent for a 429 that is not a quota refusal", () => {
    // A full mailbox answers 429 with no used/limit; there is nothing to report, so the
    // caller's own message must be used instead of this one.
    expect(quotaMessage(new APIError(429, "flow session mailbox full"))).toBeNull();
    expect(quotaMessage(new APIError(429, "rate limited", { error: "too many" }))).toBeNull();
  });

  it("never tells an unlimited tenant its allowance ran out", () => {
    // limit_bytes == 0 is how the wire spells "unlimited". Rendering "0 B of 0 B" inside a
    // refusal would be a contradiction, and exactly the sort of thing a user would report
    // as a bug.
    expect(
      quotaMessage(new APIError(429, "quota exceeded", { used_bytes: 5000, limit_bytes: ZERO })),
    ).toBeNull();
  });

  it("ignores errors that are not 429s at all", () => {
    expect(quotaMessage(new APIError(403, "not your node"))).toBeNull();
    expect(quotaMessage(new Error("network down"))).toBeNull();
    expect(quotaMessage(undefined)).toBeNull();
  });
});
