import { useMemo, useState } from "react";
import type { SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { Empty, Space, Typography, Tag, Tooltip, Button, Popover } from "antd";
import { PlusCircleOutlined, SendOutlined, SyncOutlined, AuditOutlined, FlagOutlined, CaretRightOutlined } from "@ant-design/icons";
import { formatTimelineTime } from "../../../lib/timelineUtils";

// ---------------------------------------------------------------------------
// SubagentTimelinePanel
//
// C1: swim-lane timeline of subagent lifecycle events (spawn / send_input /
// review / iterate). One lane per actor role, x-axis is time, each event is a
// clickable dot that opens a detail popover. Lightweight self-drawn layout
// (absolutely positioned divs) — no chart library.
// ---------------------------------------------------------------------------

type TimelineEventKind = "spawn" | "send_input" | "review" | "iterate" | "phase";

interface LaneEvent {
  kind: TimelineEventKind;
  /** Stable grouping key (unique per subagent job where possible). */
  lane: string;
  /** Human-readable lane name shown in the left gutter. */
  laneLabel: string;
  time: number; // epoch ms
  label: string;
  detail: string;
  rejected?: boolean;
  /** Vertical stagger (px) within a same-time cluster to avoid icon overlap. */
  offsetY?: number;
  /** Horizontal position (0-100) after rank-based layout. */
  xPct?: number;
}

const KIND_META: Record<TimelineEventKind, { color: string; icon: React.ReactNode }> = {
  spawn: { color: "green", icon: <PlusCircleOutlined /> },
  send_input: { color: "blue", icon: <SendOutlined /> },
  review: { color: "purple", icon: <AuditOutlined /> },
  iterate: { color: "orange", icon: <SyncOutlined /> },
  phase: { color: "default", icon: <CaretRightOutlined /> },
};

function classifyEvent(event: SessionTimelineEntry): LaneEvent | null {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  const time = new Date(event.timestamp).getTime();
  if (Number.isNaN(time)) return null;
  const phase = String(payload.phase ?? "");
  const label = String(payload.display_title || payload.job_id || payload.actor_id || phase || "event");
  const detail = [phase, payload.status, payload.message, payload.error, payload.result, payload.recovery_hint]
    .filter((v) => v !== undefined && v !== null && String(v) !== "")
    .map(String)
    .join(" · ");

  // Lane grouping: subagent events (job updates and runner phase changes)
  // are keyed by the subagent job id so every subagent gets its own lane;
  // main-agent events fall back to the actor kind, and anything else to
  // role / actor id. The gutter label prefers a readable role name.
  const actorKind = String(payload.actor_kind ?? "");
  const jobID = String(payload.job_id ?? "");
  const runnerID = String(payload.runner_id ?? "");
  const subagentKey = jobID || runnerID || String(payload.actor_id ?? "");
  const isSubagentEvent = actorKind === "subagent" || event.type === "subagent_job_updated";
  const lane = isSubagentEvent
    ? subagentKey || "subagent"
    : String(payload.role_name || payload.role_id || actorKind || payload.agent_type || payload.actor_id || jobID || "orchestrator");
  const laneLabel = String(payload.role_name || payload.display_title || payload.job_id || payload.actor_id || actorKind || "orchestrator");

  switch (event.type) {
    case "message_injected":
      return { kind: "send_input", lane, laneLabel, time, label, detail };
    case "subagent_job_updated": {
      const kind: TimelineEventKind = phase.includes("spawn") || phase === "created" || phase === "started" ? "spawn" : "phase";
      const rejected = String(payload.verdict ?? "") === "reject" || String(payload.status ?? "").includes("reject");
      return { kind, lane, laneLabel, time, label, detail, rejected };
    }
    case "runner_phase_changed": {
      // This panel is the subagent swim-lane timeline. Main-agent runner
      // phases (actor_kind defaults to "main" in the backend) are emitted
      // several times per turn and would all collapse into one "main" lane
      // with overlapping icons, so only subagent phase transitions qualify.
      if (actorKind !== "subagent") {
        return null;
      }
      const lower = phase.toLowerCase();
      let kind: TimelineEventKind = "phase";
      if (lower.includes("review")) kind = "review";
      else if (lower.includes("iterate") || lower.includes("retry") || (payload.iteration && Number(payload.iteration) > 1)) kind = "iterate";
      const rejected = lower.includes("reject");
      return { kind, lane, laneLabel, time, label, detail, rejected };
    }
    default:
      return null;
  }
}

export function SubagentTimelinePanel({ items, onlyIssues = false }: { items: SessionTimelineEntry[]; onlyIssues?: boolean }) {
  const { t } = useI18n();
  const [showIssuesOnly, setShowIssuesOnly] = useState(onlyIssues);

  const lanes = useMemo(() => {
    const events = items
      .map(classifyEvent)
      .filter((e): e is LaneEvent => e !== null)
      .filter((e) => !showIssuesOnly || e.rejected || e.kind === "iterate");
    if (events.length === 0) {
      return {
        lanes: [] as string[],
        laneLabels: {} as Record<string, string>,
        laneHeights: {} as Record<string, number>,
        events: [] as LaneEvent[],
        min: 0,
        max: 0,
      };
    }
    const laneSet = Array.from(new Set(events.map((e) => e.lane))).sort();
    const laneLabels: Record<string, string> = {};
    for (const event of events) {
      laneLabels[event.lane] = event.laneLabel;
    }

    // Stagger events that land in the same time cluster (events within one
    // cluster share a near-identical x position, e.g. a burst of job updates
    // within the same second). Each cluster member gets an alternating vertical
    // offset and the lane height grows to fit the widest cluster, so icons no
    // longer pile on top of each other in a single row.
    const CLUSTER_MS = 900;
    const STAGGER_PX = 11;
    const laneHeights: Record<string, number> = {};
    for (const lane of laneSet) {
      const laneEvents = events.filter((e) => e.lane === lane).sort((a, b) => a.time - b.time);
      let clusterStart = -Infinity;
      let clusterIndex = 0;
      let maxAbsOffset = 0;
      for (const event of laneEvents) {
        if (event.time - clusterStart >= CLUSTER_MS) {
          clusterStart = event.time;
          clusterIndex = 0;
        }
        const direction = clusterIndex % 2 === 0 ? 1 : -1;
        event.offsetY = direction * (1 + Math.floor(clusterIndex / 2)) * STAGGER_PX;
        maxAbsOffset = Math.max(maxAbsOffset, Math.abs(event.offsetY));
        clusterIndex++;
      }
      laneHeights[lane] = 40 + maxAbsOffset * 2 + 8;
    }

    const times = events.map((e) => e.time);
    const min = Math.min(...times);
    const max = Math.max(...times);

    // Rank-based horizontal layout: distribute events evenly in time order
    // instead of mapping linearly to the global time span. A session whose
    // events cluster in the first minutes but spans hours (e.g. 11:07-19:46)
    // would otherwise collapse every dot into the leftmost 1-3% and overlap.
    const sorted = [...events].sort((a, b) => a.time - b.time);
    const total = sorted.length;
    sorted.forEach((e, i) => {
      e.xPct = total > 1 ? (i / (total - 1)) * 100 : 50;
    });
    return { lanes: laneSet, laneLabels, laneHeights, events: sorted, min, max };
  }, [items, showIssuesOnly]);

  if (lanes.events.length === 0) {
    return (
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        <IssuesToggle showIssuesOnly={showIssuesOnly} onChange={setShowIssuesOnly} />
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noSubagentTimeline")} />
      </Space>
    );
  }

  return (
    <Space direction="vertical" size={8} style={{ width: "100%" }}>
      <IssuesToggle showIssuesOnly={showIssuesOnly} onChange={setShowIssuesOnly} />
      <div style={{ position: "relative", minHeight: lanes.lanes.reduce((sum, lane) => sum + (lanes.laneHeights[lane] ?? 40), 0), marginLeft: 8 }}>
        <div
          style={{
            position: "absolute",
            right: 0,
            top: -2,
            fontSize: 11,
            color: "rgba(0,0,0,0.45)",
            whiteSpace: "nowrap",
          }}
        >
          {formatTimelineTime(new Date(lanes.min).toISOString())} → {formatTimelineTime(new Date(lanes.max).toISOString())}
        </div>
        {lanes.lanes.map((lane) => {
          const laneEvents = lanes.events.filter((e) => e.lane === lane);
          const laneHeight = lanes.laneHeights[lane] ?? 40;
          return (
            <div key={lane} style={{ position: "relative", height: laneHeight, borderBottom: "1px solid rgba(128,128,128,0.15)", display: "flex", alignItems: "center" }}>
              <span style={{ position: "absolute", left: -8, transform: "translateX(-100%)", maxWidth: 110, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontSize: 12 }}>
                <Typography.Text type="secondary">{lanes.laneLabels[lane] ?? lane}</Typography.Text>
              </span>
              {/* Swim-lane baseline: a horizontal line every event sits on. */}
              <div style={{ position: "absolute", left: 0, right: 0, top: "50%", height: 1, background: "rgba(128,128,128,0.22)" }} />
              {laneEvents.map((event, i) => {
                const meta = KIND_META[event.kind];
                const offsetY = event.offsetY ?? 0;
                const xPct = event.xPct ?? 50;
                return (
                  <Popover
                    key={`${event.time}-${i}`}
                    content={
                      <Space direction="vertical" size={2} style={{ maxWidth: 280 }}>
                        <Space size={6}>
                          <Tag color={meta.color} style={{ margin: 0 }}>
                            {event.kind}
                          </Tag>
                          {event.rejected ? <Tag color="red">rejected</Tag> : null}
                          <Typography.Text type="secondary">{formatTimelineTime(new Date(event.time).toISOString())}</Typography.Text>
                        </Space>
                        <Typography.Text strong>{event.label}</Typography.Text>
                        {event.detail ? <Typography.Text type="secondary">{event.detail}</Typography.Text> : null}
                      </Space>
                    }
                    trigger="click"
                  >
                    <Tooltip title={event.label}>
                      {offsetY !== 0 ? (
                        <div
                          style={{
                            position: "absolute",
                            left: `${xPct}%`,
                            top: offsetY > 0 ? "calc(50% + 11px)" : "auto",
                            bottom: offsetY < 0 ? "calc(50% + 11px)" : "auto",
                            width: 1,
                            height: Math.max(2, Math.abs(offsetY) - 11),
                            background: "rgba(128,128,128,0.35)",
                          }}
                        />
                      ) : null}
                      <span
                        style={{
                          position: "absolute",
                          left: `${xPct}%`,
                          top: "50%",
                          transform: `translate(-50%, calc(-50% + ${offsetY}px))`,
                          cursor: "pointer",
                          display: "inline-flex",
                          alignItems: "center",
                          justifyContent: "center",
                          width: 22,
                          height: 22,
                          borderRadius: "50%",
                          background: event.rejected ? "#fff1f0" : "#f0f0f0",
                          color: meta.color === "default" ? "#666" : meta.color,
                          border: `1px solid ${event.rejected ? "#ff4d4f" : "#d9d9d9"}`,
                        }}
                      >
                        {meta.icon}
                      </span>
                    </Tooltip>
                  </Popover>
                );
              })}
            </div>
          );
        })}
      </div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {lanes.events.length} events · click a dot for details
      </Typography.Text>
    </Space>
  );
}

function IssuesToggle({ showIssuesOnly, onChange }: { showIssuesOnly: boolean; onChange: (v: boolean) => void }) {
  return (
    <Button size="small" icon={<FlagOutlined />} type={showIssuesOnly ? "primary" : "default"} onClick={() => onChange(!showIssuesOnly)}>
      {showIssuesOnly ? "All events" : "Issues / retries only"}
    </Button>
  );
}
