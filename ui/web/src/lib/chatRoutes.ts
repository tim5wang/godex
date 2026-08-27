import type { ListedSession, SessionLocator } from "./types";

function encodeSegment(value: string) {
  return encodeURIComponent(value);
}

export function buildChatRoute(locator: SessionLocator) {
  const channel = locator.channel?.trim() || "web";
  const key = locator.key?.trim() || "";
  const base = `/chat/${encodeSegment(channel)}/${encodeSegment(key)}`;
  // Session identity is hashed from the FULL locator (channel + key + user_id
  // + metadata). Every identity-bearing field must survive the URL, or
  // ChatPage will re-hash a different session id, OpenSession will miss, and
  // the page falls back to creating a new chat. ChatPage reads these params
  // back into locator metadata (project_dir/mode/requested_skills).
  const query: string[] = [];
  const userId = locator.user_id?.trim();
  if (userId) {
    query.push(`user_id=${encodeURIComponent(userId)}`);
  }
  const projectDir = locator.metadata?.project_dir?.trim();
  if (projectDir) {
    query.push(`workspace_dir=${encodeURIComponent(projectDir)}`);
  }
  const mode = locator.metadata?.mode?.trim();
  if (mode) {
    query.push(`mode=${encodeURIComponent(mode)}`);
  }
  const skills = locator.metadata?.requested_skills?.trim();
  if (skills) {
    query.push(`skills=${encodeURIComponent(skills)}`);
  }
  if (query.length === 0) {
    return base;
  }
  return `${base}?${query.join("&")}`;
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
