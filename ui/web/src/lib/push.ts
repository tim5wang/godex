/**
 * GoDex Web Push client: registers the service worker, requests notification
 * permission, subscribes through the browser Push API (VAPID key from the
 * center), and reports the subscription to the center's /push/subscribe
 * endpoint. The center keeps subscriptions in memory only.
 */

import { apiURL } from "./api";

export interface PushState {
  supported: boolean;
  permission: NotificationPermission;
  subscribed: boolean;
}

/** True when the browser can do Web Push at all. */
export function pushSupported(): boolean {
  return typeof window !== "undefined" && "serviceWorker" in navigator && "PushManager" in window;
}

/** Register the service worker (idempotent). */
export async function registerPushWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!pushSupported()) return null;
  const registration = await navigator.serviceWorker.register("/sw.js");
  await navigator.serviceWorker.ready;
  return registration;
}

/**
 * Ensure the browser is subscribed and the center knows the subscription.
 * Returns the resulting subscription state; never throws for permission
 * denial (callers surface the outcome to the user).
 */
export async function ensurePushSubscription(token: string | null): Promise<PushState> {
  if (!pushSupported()) return { supported: false, permission: "denied", subscribed: false };
  const permission = await Notification.requestPermission();
  if (permission !== "granted") return { supported: true, permission, subscribed: false };

  const registration = await registerPushWorker();
  if (!registration) return { supported: true, permission, subscribed: false };

  const publicKeyResp = await fetch(apiURL("/push/public-key"), {
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
  });
  if (!publicKeyResp.ok) return { supported: true, permission, subscribed: false };
  const { public_key: publicKey } = (await publicKeyResp.json()) as { public_key: string };

  let subscription = await registration.pushManager.getSubscription();
  if (!subscription) {
    subscription = await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(publicKey),
    });
  }
  const raw = subscription.toJSON();
  const payload = {
    endpoint: subscription.endpoint,
    keys: {
      p256dh: (raw.keys as { p256dh?: string } | undefined)?.p256dh ?? "",
      auth: (raw.keys as { auth?: string } | undefined)?.auth ?? "",
    },
  };
  await fetch(apiURL("/push/subscribe"), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(payload),
  });
  return { supported: true, permission, subscribed: true };
}

/** Ask the center to send a test notification to all subscribers. */
export async function sendTestPush(token: string | null): Promise<number> {
  const resp = await fetch(apiURL("/push/test"), {
    method: "POST",
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
  });
  if (!resp.ok) throw new Error(`test push failed: ${resp.status}`);
  const { notified } = (await resp.json()) as { notified: number };
  return notified;
}

/** base64url → Uint8Array for PushManager.subscribe(applicationServerKey). */
function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  const output = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) {
    output[i] = raw.charCodeAt(i);
  }
  return output;
}
