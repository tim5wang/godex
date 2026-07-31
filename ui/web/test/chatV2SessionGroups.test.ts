import { describe, expect, it } from "vitest";
import { filterSessions, groupSessionsByDate } from "../src/features/chat-v2/sessionGroups";
import type { ListedSession } from "../src/lib/types";

function session(id: string, updatedAt: string, title = ""): ListedSession {
  return {
    session_id: id,
    locator: { channel: "web", key: id },
    title,
    created_at: updatedAt,
    updated_at: updatedAt,
    last_activity_at: updatedAt,
  };
}

describe("groupSessionsByDate", () => {
  const now = new Date("2026-07-30T12:00:00");

  it("groups sessions into Today / Yesterday / Previous 7 days / Older", () => {
    const sessions = [
      session("a", "2026-07-30T09:00:00"),
      session("b", "2026-07-29T23:00:00"),
      session("c", "2026-07-26T10:00:00"),
      session("d", "2026-06-01T10:00:00"),
    ];
    const groups = groupSessionsByDate(sessions, now);
    expect(groups.map((g) => g.label)).toEqual(["Today", "Yesterday", "Previous 7 days", "Older"]);
    expect(groups[0]!.sessions.map((s) => s.session_id)).toEqual(["a"]);
    expect(groups[1]!.sessions.map((s) => s.session_id)).toEqual(["b"]);
    expect(groups[2]!.sessions.map((s) => s.session_id)).toEqual(["c"]);
    expect(groups[3]!.sessions.map((s) => s.session_id)).toEqual(["d"]);
  });

  it("omits empty groups", () => {
    const groups = groupSessionsByDate([session("a", "2026-07-30T08:00:00")], now);
    expect(groups.map((g) => g.label)).toEqual(["Today"]);
  });

  it("sorts sessions by updated_at descending inside a group", () => {
    const sessions = [
      session("a", "2026-07-30T08:00:00"),
      session("b", "2026-07-30T11:00:00"),
    ];
    const groups = groupSessionsByDate(sessions, now);
    expect(groups[0]!.sessions.map((s) => s.session_id)).toEqual(["b", "a"]);
  });

  it("returns an empty list for no sessions", () => {
    expect(groupSessionsByDate([], now)).toEqual([]);
  });
});

describe("filterSessions", () => {
  const sessions = [
    session("alpha", "2026-07-30T09:00:00", "Fix login bug"),
    session("beta", "2026-07-29T09:00:00", "Write docs"),
  ];

  it("returns all sessions for an empty query", () => {
    expect(filterSessions(sessions, "")).toHaveLength(2);
    expect(filterSessions(sessions, "   ")).toHaveLength(2);
  });

  it("matches against title case-insensitively", () => {
    expect(filterSessions(sessions, "LOGIN").map((s) => s.session_id)).toEqual(["alpha"]);
  });

  it("matches against session key when title misses", () => {
    expect(filterSessions(sessions, "beta").map((s) => s.session_id)).toEqual(["beta"]);
  });

  it("returns empty when nothing matches", () => {
    expect(filterSessions(sessions, "zzz")).toEqual([]);
  });
});
