import { describe, expect, it } from "vitest";
import type { ControlNode } from "../src/lib/types";
import {
  formatRelativeTime,
  matchesNodeQuery,
  nodeVisualStatus,
  sortNodesByStatus,
} from "../src/features/nodes/nodeStatus";

function node(over: Partial<ControlNode>): ControlNode {
  return { id: "n1", name: "n1", status: "offline", ...over };
}

// The card colour is the whole point of the redesign, so the mapping from the
// two independent readiness signals to the single visual state is pinned here.
//
// "online" alone is NOT healthy: a node whose heartbeat is fine but whose relay
// channel is down cannot be opened for remote chat/terminal/files. Painting it
// green would tell the operator to click something that will not work.
describe("nodeVisualStatus", () => {
  it("is healthy only when heartbeat and relay are both up", () => {
    expect(nodeVisualStatus(node({ status: "online", relay_status: "connected" }))).toBe("healthy");
  });

  it("is degraded when only one signal is up", () => {
    expect(nodeVisualStatus(node({ status: "online", relay_status: "disconnected" }))).toBe("degraded");
    expect(nodeVisualStatus(node({ status: "offline", relay_status: "connected" }))).toBe("degraded");
  });

  it("is offline when neither signal is up", () => {
    expect(nodeVisualStatus(node({ status: "offline", relay_status: "disconnected" }))).toBe("offline");
    expect(nodeVisualStatus(node({}))).toBe("offline");
  });
});

describe("sortNodesByStatus", () => {
  it("puts pickable nodes first and freshest heartbeat first within a group", () => {
    const degraded = node({ id: "degraded", status: "online", relay_status: "disconnected" });
    const offline = node({ id: "offline" });
    const olderHealthy = node({
      id: "older",
      status: "online",
      relay_status: "connected",
      last_seen: "2026-11-07T10:00:00Z",
    });
    const newerHealthy = node({
      id: "newer",
      status: "online",
      relay_status: "connected",
      last_seen: "2026-11-07T12:00:00Z",
    });
    expect(sortNodesByStatus([offline, olderHealthy, degraded, newerHealthy]).map((n) => n.id)).toEqual([
      "newer",
      "older",
      "degraded",
      "offline",
    ]);
  });

  it("does not mutate the input array", () => {
    const list = [node({ id: "b" }), node({ id: "a", status: "online", relay_status: "connected" })];
    sortNodesByStatus(list);
    expect(list.map((n) => n.id)).toEqual(["b", "a"]);
  });
});

describe("matchesNodeQuery", () => {
  const target = node({
    id: "node-a",
    name: "内网机器",
    endpoint: "http://10.0.0.5:8080",
    workspace_dir: "/srv/godex",
  });

  it("matches nothing when the query is blank", () => {
    expect(matchesNodeQuery(target, "   ")).toBe(true);
  });

  it("matches name, id, endpoint and workspace, case-insensitively", () => {
    expect(matchesNodeQuery(target, "内网")).toBe(true);
    expect(matchesNodeQuery(target, "NODE-A")).toBe(true);
    expect(matchesNodeQuery(target, "10.0.0.5")).toBe(true);
    expect(matchesNodeQuery(target, "/SRV/")).toBe(true);
  });

  it("rejects a non-match instead of matching everything", () => {
    expect(matchesNodeQuery(target, "other-node")).toBe(false);
  });

  it("tolerates nodes with the optional fields missing", () => {
    expect(matchesNodeQuery(node({ id: "bare", name: "" }), "bare")).toBe(true);
    expect(matchesNodeQuery(node({ id: "bare" }), "/srv")).toBe(false);
  });
});

describe("formatRelativeTime", () => {
  const now = Date.parse("2026-11-07T12:00:00Z");

  it("returns null for a missing timestamp and echoes an unparsable one", () => {
    expect(formatRelativeTime(undefined, now, "en")).toBeNull();
    expect(formatRelativeTime("not-a-date", now, "en")).toBe("not-a-date");
  });

  it("renders whole minutes/hours/days in both locales", () => {
    expect(formatRelativeTime("2026-11-07T11:57:00Z", now, "en")).toBe("3 minutes ago");
    expect(formatRelativeTime("2026-11-07T11:57:00Z", now, "zh")).toContain("3");
    expect(formatRelativeTime("2026-11-07T09:00:00Z", now, "en")).toBe("3 hours ago");
    expect(formatRelativeTime("2026-11-04T12:00:00Z", now, "en")).toBe("3 days ago");
  });

  it("collapses anything under a minute to 'now' rather than rounding to 0 minutes", () => {
    expect(formatRelativeTime("2026-11-07T11:59:30Z", now, "en")).toBe("now");
  });
});
