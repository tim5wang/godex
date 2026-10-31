import { afterEach, describe, expect, it, vi } from "vitest";
import { ensurePushSubscriptionSilent } from "../src/lib/push";

/**
 * Regression tests for the silent push re-subscribe used by the work desktop.
 *
 * The desktop is where nudges arrive, so it re-registers on entry. The property
 * that matters is what it must NOT do: call Notification.requestPermission().
 * Permission requests are only legitimate from a deliberate gesture, so a
 * prompt on page load trains users to hit "block" — which would permanently
 * silence the reminders the user wanted.
 */

/** Minimal browser surface: just enough for pushSupported() to be true. */
function stubBrowser(permission: NotificationPermission, onPrompt?: () => void) {
  const subscribe = vi.fn(async () => ({ endpoint: "https://push.example.com/s/x", toJSON: () => ({ keys: {} }) }));
  vi.stubGlobal("window", { PushManager: function PushManager() {} });
  vi.stubGlobal("navigator", {
    serviceWorker: {
      register: vi.fn(async () => ({ pushManager: { getSubscription: async () => null, subscribe } })),
      ready: Promise.resolve(),
    },
  });
  vi.stubGlobal("Notification", {
    permission,
    requestPermission: vi.fn(async () => {
      onPrompt?.();
      return permission;
    }),
  });
  return { subscribe };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ensurePushSubscriptionSilent", () => {
  it("never prompts when permission was never granted", async () => {
    let prompted = false;
    const { subscribe } = stubBrowser("default", () => { prompted = true; });
    const state = await ensurePushSubscriptionSilent(null);
    expect(state).toBeNull();
    expect(prompted).toBe(false);
    expect(subscribe).not.toHaveBeenCalled();
  });

  it("stays silent (no throw) when permission was denied", async () => {
    stubBrowser("denied");
    await expect(ensurePushSubscriptionSilent(null)).resolves.toBeNull();
  });

  it("does nothing when the browser cannot do Web Push at all", async () => {
    vi.stubGlobal("window", {});
    vi.stubGlobal("navigator", {});
    // A capable-enough Notification is present; the browser check alone must stop it.
    vi.stubGlobal("Notification", { permission: "granted", requestPermission: vi.fn() });
    await expect(ensurePushSubscriptionSilent(null)).resolves.toBeNull();
  });
});
