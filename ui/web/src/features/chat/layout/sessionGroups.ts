import type { ListedSession } from "../../../lib/types";

// Session grouping helpers for the chat sessions rail. Pure functions so
// they can be unit-tested without a DOM.

export type SessionDateGroupLabel = "Today" | "Yesterday" | "Previous 7 days" | "Older";

export interface SessionDateGroup {
  label: SessionDateGroupLabel;
  sessions: ListedSession[];
}

/** WorkspaceGroup.type determines the leading icon and path label. */
export type WorkspaceGroupType = "directory" | "acp" | "cron" | "channel" | "other";

/** A workspace group groups sessions by a logical namespace:
 *  - project_dir for normal workspace sessions → "directory"
 *  - ACP sessions (channel=acp): by project_dir if available, else "acp"
 *  - cron sessions (channel=cron): by job name → "cron"
 *  - other channel sessions (weixin, feishu, etc.) → "channel"
 *  - fallback: "other" */
export interface WorkspaceGroup {
  /** Stable key for React rendering. */
  key: string;
  /** Display label. */
  label: string;
  /** Full detail string for popover display. */
  path: string;
  /** Icon / grouping category. */
  type: WorkspaceGroupType;
  /** Date groups within this workspace, sorted most-recent first. */
  dateGroups: SessionDateGroup[];
  /** Total session count in this workspace. */
  count: number;
}

function startOfDay(date: Date): number {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

const UNSPECIFIED_KEY = "__godex_unspecified__";

/**
 * TEMP_DIR_PATTERNS: paths that match these prefixes are transient (test
 * harnesses, sandboxes) and should be grouped as "Other" rather than
 * presented as a workspace directory.
 */
const TEMP_DIR_PATTERNS = [
  "/var/folders/",
  "/tmp/",
  "/private/tmp/",
  "/dev/shm/",
];

export function isTempDir(path: string): boolean {
  return TEMP_DIR_PATTERNS.some((p) => path.startsWith(p));
}

/** Resolve the grouping key, label, path and type for a single session. */
function sessionGroupMeta(session: ListedSession): { key: string; label: string; path: string; type: WorkspaceGroupType } {
  const channel = session.locator?.channel ?? "web";
  const projectDir = session.locator?.metadata?.project_dir?.trim() ?? "";

  // ACP sessions: prefer project_dir if available.
  if (channel === "acp") {
    if (projectDir !== "" && !isTempDir(projectDir)) {
      return {
        key: projectDir,
        label: projectDir.split("/").pop() ?? projectDir,
        path: projectDir,
        type: "directory",
      };
    }
    return { key: "acp", label: "ACP", path: "ACP sessions", type: "acp" };
  }

  // Cron sessions: group by job name.
  if (channel === "cron") {
    const jobID = session.locator?.metadata?.job_id ?? session.locator?.key ?? "cron";
    return { key: `cron:${jobID}`, label: jobID, path: `Cron: ${jobID}`, type: "cron" };
  }

  // Named channel sessions (weixin, feishu, slack, etc.) — no project dir.
  if (channel !== "web" && channel !== "local") {
    return { key: channel, label: channel, path: `Channel: ${channel}`, type: "channel" };
  }

  // Workspace sessions: group by project_dir.
  if (projectDir !== "" && !isTempDir(projectDir)) {
    return {
      key: projectDir,
      label: projectDir.split("/").pop() ?? projectDir,
      path: projectDir,
      type: "directory",
    };
  }

  return { key: UNSPECIFIED_KEY, label: "Other", path: "", type: "other" };
}

/** Build workspace groups from a list of sessions. Groups are sorted by
 *  most-recently-updated session, with "Other" always last. */
export function groupSessionsByWorkspace(sessions: ListedSession[], now: Date = new Date()): WorkspaceGroup[] {
  const dirMap = new Map<string, ListedSession[]>();
  for (const session of sessions) {
    const { key } = sessionGroupMeta(session);
    const bucket = dirMap.get(key) ?? [];
    bucket.push(session);
    dirMap.set(key, bucket);
  }

  const groups: WorkspaceGroup[] = [];
  for (const [key, groupSessions] of dirMap) {
    const meta = sessionGroupMeta(groupSessions[0]);
    const dateGroups = groupSessionsByDate(groupSessions, now);
    groups.push({
      key,
      label: meta.label,
      path: meta.path,
      type: meta.type,
      dateGroups,
      count: groupSessions.length,
    });
  }

  groups.sort((a, b) => {
    if (a.key === UNSPECIFIED_KEY) return 1;
    if (b.key === UNSPECIFIED_KEY) return -1;
    return maxUpdated(b.dateGroups) - maxUpdated(a.dateGroups);
  });

  return groups;
}

function maxUpdated(groups: SessionDateGroup[]): number {
  let max = 0;
  for (const g of groups) {
    for (const s of g.sessions) {
      const ts = new Date(s.updated_at).getTime();
      if (ts > max) max = ts;
    }
  }
  return max;
}

export function groupSessionsByDate(sessions: ListedSession[], now: Date = new Date()): SessionDateGroup[] {
  const dayStart = startOfDay(now);
  const dayMs = 24 * 60 * 60 * 1000;
  const buckets: Record<SessionDateGroupLabel, ListedSession[]> = {
    Today: [],
    Yesterday: [],
    "Previous 7 days": [],
    Older: [],
  };
  const sorted = [...sessions].sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());
  for (const session of sorted) {
    const updated = new Date(session.updated_at).getTime();
    if (Number.isNaN(updated)) { buckets.Older.push(session); continue; }
    if (updated >= dayStart) { buckets.Today.push(session); }
    else if (updated >= dayStart - dayMs) { buckets.Yesterday.push(session); }
    else if (updated >= dayStart - 7 * dayMs) { buckets["Previous 7 days"].push(session); }
    else { buckets.Older.push(session); }
  }
  const order: SessionDateGroupLabel[] = ["Today", "Yesterday", "Previous 7 days", "Older"];
  return order.filter((l) => buckets[l].length > 0).map((l) => ({ label: l, sessions: buckets[l] }));
}

export function filterSessions(sessions: ListedSession[], query: string): ListedSession[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return sessions;
  return sessions.filter((s) => {
    const haystack = [
      s.title, s.branch_title, s.locator.key, s.locator.channel,
      s.session_id, s.locator?.metadata?.project_dir ?? "",
      s.locator?.metadata?.job_id ?? "",
    ].filter(Boolean).join(" ").toLowerCase();
    return haystack.includes(needle);
  });
}
