import { request } from "./apiClient";

// ---- Watch client types (mirror internal/services/watchauth) ----

/**
 * One paired wrist device.
 *
 * `credential_hash` is returned by the node but deliberately not modelled here:
 * the UI never displays or needs it, and naming it would invite rendering a
 * credential identifier next to a "copy" affordance.
 */
export interface WatchDevice {
  device_id: string;
  name?: string;
  kind?: string;
  scopes: string[];
  created_at: string;
  last_seen?: string;
  expires_at?: string;
  revoked?: boolean;
}

/** The scopes a watch credential can hold. Mirrors watchauth.AllowedScopes. */
export const WATCH_SCOPES = ["watch:read", "watch:command", "watch:approve"] as const;
export type WatchScope = (typeof WATCH_SCOPES)[number];

/**
 * Approves a pairing code shown on the watch, minting the device credential.
 *
 * Only the trusted side (web/mobile) may call this; the watch itself cannot
 * approve its own pairing, which is what stops a stolen watch from granting
 * itself access to a node.
 *
 * An empty scope list makes the node fall back to its own default, so callers
 * that intend to narrow access must send the scopes explicitly.
 */
export function approveWatchPairing(
  token: string | null,
  userCode: string,
  scopes: WatchScope[] = [],
) {
  return request<WatchDevice>(
    "/watch/pair/approve",
    { method: "POST", body: JSON.stringify({ user_code: userCode, scopes }) },
    token,
  );
}

/** Lists paired wrist devices. */
export function listWatchDevices(token: string | null) {
  return request<WatchDevice[]>("/watch/devices", { method: "GET" }, token);
}

/**
 * Revokes a device, invalidating its credential immediately.
 *
 * The node keeps the record (marked revoked) rather than deleting it, so the
 * device id stays attributable in the list.
 */
export function revokeWatchDevice(token: string | null, deviceID: string) {
  return request<WatchDevice>(
    `/watch/devices/${encodeURIComponent(deviceID)}`,
    { method: "DELETE" },
    token,
  );
}
