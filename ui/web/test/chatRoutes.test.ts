import { describe, expect, it } from "vitest";
import { buildChatRoute, buildChatRouteForSession, locatorMatchesRoute } from "../src/lib/chatRoutes";
import type { ListedSession } from "../src/lib/types";

function session(locator: ListedSession["locator"]): ListedSession {
  return {
    session_id: "s-1",
    locator,
    created_at: "2026-08-02T00:00:00",
    updated_at: "2026-08-02T00:00:00",
    last_activity_at: "2026-08-02T00:00:00",
  };
}

describe("buildChatRoute", () => {
  it("keeps the original channel for cross-entry sessions (tui/acp/channel)", () => {
    expect(buildChatRoute({ channel: "tui", key: "default" })).toBe("/chat/tui/default");
    expect(buildChatRoute({ channel: "acp", key: "default" })).toBe("/chat/acp/default");
    expect(buildChatRoute({ channel: "telegram", key: "chat-42" })).toBe("/chat/telegram/chat-42");
  });

  it("appends user_id as a query param so channel sessions resolve to the same stable id", () => {
    expect(buildChatRoute({ channel: "telegram", key: "chat-42", user_id: "u-7" })).toBe(
      "/chat/telegram/chat-42?user_id=u-7",
    );
  });

  it("omits the user_id query param when empty", () => {
    expect(buildChatRoute({ channel: "web", key: "new-1", user_id: "  " })).toBe("/chat/web/new-1");
  });

  it("encodes channel, key and user_id segments", () => {
    expect(buildChatRoute({ channel: "web", key: "a/b c", user_id: "u?x" })).toBe(
      "/chat/web/a%2Fb%20c?user_id=u%3Fx",
    );
  });
});

describe("buildChatRouteForSession", () => {
  it("builds the route from the session locator verbatim", () => {
    const s = session({ channel: "acp", key: "default", user_id: "pi" });
    expect(buildChatRouteForSession(s)).toBe("/chat/acp/default?user_id=pi");
  });

  it("falls back to the web channel when the locator channel is missing", () => {
    const s = session({ channel: "", key: "new-1" });
    expect(buildChatRouteForSession(s)).toBe("/chat/web/new-1");
  });
});

describe("locatorMatchesRoute", () => {
  it("matches the session built by buildChatRouteForSession", () => {
    const s = session({ channel: "acp", key: "default", user_id: "pi" });
    const route = buildChatRouteForSession(s);
    // route = /chat/acp/default?user_id=pi
    expect(locatorMatchesRoute(s.locator, "acp", "default", "pi")).toBe(true);
    expect(route.startsWith("/chat/acp/default")).toBe(true);
  });

  it("rejects a different channel so a web route never hijacks a tui session", () => {
    const s = session({ channel: "tui", key: "default" });
    expect(locatorMatchesRoute(s.locator, "web", "default", null)).toBe(false);
  });
});
