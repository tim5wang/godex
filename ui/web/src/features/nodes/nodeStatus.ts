import type { ControlNode } from "../../lib/types";

/**
 * Node list presentation helpers, kept pure so they are unit-testable without a
 * DOM (the page itself is unreachable from vitest: no @testing-library here).
 *
 * The nodes page shows ONE compact status colour per card. "online" alone is not
 * enough: a node whose heartbeat is fine but whose relay channel is down cannot be
 * opened for remote chat/terminal/files, and that is exactly the state an operator
 * needs to spot when picking a node. So the three visual states are derived from
 * both signals rather than from `status` alone.
 */
export type NodeVisualStatus = "healthy" | "degraded" | "offline";

/** Tag colour per visual status (an antd Tag `color` value). */
export const VISUAL_TAG_COLOR: Record<NodeVisualStatus, string> = {
  healthy: "green",
  degraded: "gold",
  offline: "default",
};

export function nodeVisualStatus(node: Pick<ControlNode, "status" | "relay_status">): NodeVisualStatus {
  const online = node.status === "online";
  const relayed = node.relay_status === "connected";
  if (online && relayed) {
    return "healthy";
  }
  // Either half present, not both: reachable chatter but unusable remote control
  // (an inconsistent state worth surfacing rather than hiding as "offline").
  if (online || relayed) {
    return "degraded";
  }
  return "offline";
}

const RANK: Record<NodeVisualStatus, number> = { healthy: 0, degraded: 1, offline: 2 };

function safeTime(value?: string): number {
  if (!value) {
    return 0;
  }
  const at = Date.parse(value);
  return Number.isNaN(at) ? 0 : at;
}

/**
 * Healthy first, then degraded, then offline; freshest heartbeat within a group.
 * The order encodes "what can I pick right now", so the first card is the best
 * candidate instead of whatever order the registry happened to serialise.
 */
export function sortNodesByStatus(nodes: ControlNode[]): ControlNode[] {
  return [...nodes].sort((a, b) => {
    const byStatus = RANK[nodeVisualStatus(a)] - RANK[nodeVisualStatus(b)];
    if (byStatus !== 0) {
      return byStatus;
    }
    const bySeen = safeTime(b.last_seen) - safeTime(a.last_seen);
    if (bySeen !== 0) {
      return bySeen;
    }
    return (a.name || a.id).localeCompare(b.name || b.id);
  });
}

/** Free-text filter over the fields a human actually types to find a node. */
export function matchesNodeQuery(node: ControlNode, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) {
    return true;
  }
  return [node.name, node.id, node.endpoint, node.workspace_dir]
    .filter((value): value is string => typeof value === "string")
    .some((value) => value.toLowerCase().includes(needle));
}

/**
 * "3 分钟前" / "3 minutes ago" from an absolute timestamp.
 *
 * Intl.RelativeTimeFormat rather than hand-written keys: the duration unit set is
 * open-ended, and a localised formatter is already in the platform — adding one
 * message key per unit per locale would be pure maintenance drag.
 */
export function formatRelativeTime(iso: string | undefined, now: number, locale: string): string | null {
  if (!iso) {
    return null;
  }
  const at = Date.parse(iso);
  if (Number.isNaN(at)) {
    return iso;
  }
  const diff = at - now;
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(locale === "zh" ? "zh-CN" : "en", { numeric: "auto" });
  const MINUTE = 60_000;
  const HOUR = 60 * MINUTE;
  const DAY = 24 * HOUR;
  if (abs < MINUTE) {
    return rtf.format(0, "second");
  }
  if (abs < HOUR) {
    return rtf.format(Math.round(diff / MINUTE), "minute");
  }
  if (abs < DAY) {
    return rtf.format(Math.round(diff / HOUR), "hour");
  }
  return rtf.format(Math.round(diff / DAY), "day");
}
