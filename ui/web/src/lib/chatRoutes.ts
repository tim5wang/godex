import type { ListedSession, SessionLocator } from "./types";

function encodeSegment(value: string) {
  return encodeURIComponent(value);
}

export function buildChatRoute(locator: SessionLocator) {
  const channel = locator.channel?.trim() || "web";
  const key = locator.key?.trim() || "";
  const base = `/chat/${encodeSegment(channel)}/${encodeSegment(key)}`;
  if (!locator.user_id?.trim()) {
    return base;
  }
  return `${base}?user_id=${encodeURIComponent(locator.user_id.trim())}`;
}

export function buildChatRouteForSession(session: Pick<ListedSession, "locator">) {
  return buildChatRoute(session.locator);
}

export function locatorMatchesRoute(
  locator: SessionLocator | undefined,
  channel: string | undefined,
  sessionKey: string | undefined,
  userId?: string | null,
) {
  if (!locator || !channel || !sessionKey) {
    return false;
  }
  if ((locator.channel || "web") !== channel) {
    return false;
  }
  if ((locator.key || "") !== sessionKey) {
    return false;
  }
  const routeUserId = userId?.trim() || "";
  const locatorUserId = locator.user_id?.trim() || "";
  if (routeUserId) {
    return routeUserId === locatorUserId;
  }
  return true;
}
