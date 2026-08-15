import { useMemo, useState } from "react";
import type { SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { Button, Empty, Popover, Space, Tag, Tooltip, Typography } from "antd";
import { AuditOutlined, CaretRightOutlined, FlagOutlined, PlusCircleOutlined, SendOutlined, SyncOutlined } from "@ant-design/icons";
import { formatTimelineTime, shortTurnId } from "../../../lib/timelineUtils";

type TimelineEventKind = "spawn" | "send_input" | "review" | "iterate" | "phase";

interface LaneEvent {
  kind: TimelineEventKind;
  lane: string;
  laneLabel: string;
  time: number;
  label: string;
  detail: string;
  status?: string;
  rejected?: boolean;
}

interface TimelineLane {
  id: string;
  label: string;
  events: LaneEvent[];
  rejected: boolean;
}

const KIND_META: Record<TimelineEventKind, { color: string; icon: React.ReactNode }> = {
  spawn: { color: "green", icon: <PlusCircleOutlined /> },
  send_input: { color: "blue", icon: <SendOutlined /> },
  review: { color: "purple", icon: <AuditOutlined /> },
  iterate: { color: "orange", icon: <SyncOutlined /> },
  phase: { color: "default", icon: <CaretRightOutlined /> },
};

function classifyEvent(event: SessionTimelineEntry, actorJobMap: ReadonlyMap<string, string>): LaneEvent | null {
  const payload = (event.payload ?? {}) as Record<string, unknown>;
  const time = new Date(event.timestamp).getTime();
  if (Number.isNaN(time)) return null;

  const phase = String(payload.phase ?? "");
  const status = String(payload.status ?? "");
  const actorKind = String(payload.actor_kind ?? "");
  const actorID = String(payload.actor_id ?? "");
  const jobID = String(payload.job_id ?? "");
  const runnerID = String(payload.runner_id ?? "");
  const isSubagentEvent = actorKind === "subagent" || event.type === "subagent_job_updated";
  if (event.type === "runner_phase_changed" && !isSubagentEvent) return null;

  const lane = isSubagentEvent
    ? jobID || actorJobMap.get(actorID) || runnerID || actorID || "subagent"
    : String(payload.role_name || payload.role_id || actorKind || payload.agent_type || actorID || "orchestrator");
  const laneLabel = String(payload.role_name || payload.display_title || payload.agent_type || jobID || actorID || "subagent");
  const label = String(payload.message || payload.display_title || phase || status || payload.tool_name || "event");
  const detail = [payload.error, payload.result, payload.recovery_hint, payload.tool_name]
    .filter((value) => value !== undefined && value !== null && String(value).trim() !== "")
    .map(String)
    .join(" · ");
  const rejected = String(payload.verdict ?? "").toLowerCase() === "reject" || /reject|fail|error/.test(status.toLowerCase()) || Boolean(payload.error);

  switch (event.type) {
    case "message_injected":
      return { kind: "send_input", lane, laneLabel, time, label, detail, status, rejected };
    case "subagent_job_updated": {
      const lower = phase.toLowerCase();
      let kind: TimelineEventKind = lower.includes("spawn") || lower === "created" || lower === "started" ? "spawn" : "phase";
      if (lower.includes("review")) kind = "review";
      if (lower.includes("iterate") || lower.includes("retry")) kind = "iterate";
      return { kind, lane, laneLabel, time, label, detail, status, rejected };
    }
    case "runner_phase_changed": {
      const lower = phase.toLowerCase();
      let kind: TimelineEventKind = "phase";
      if (lower.includes("review")) kind = "review";
      else if (lower.includes("iterate") || lower.includes("retry") || Number(payload.iteration ?? 0) > 1) kind = "iterate";
      return { kind, lane, laneLabel, time, label, detail, status, rejected: rejected || lower.includes("reject") };
    }
    default:
      return null;
  }
}

export function SubagentTimelinePanel({ items, onlyIssues = false }: { items: SessionTimelineEntry[]; onlyIssues?: boolean }) {
  const { t } = useI18n();
  const [showIssuesOnly, setShowIssuesOnly] = useState(onlyIssues);

  const model = useMemo(() => {
    // Some early runner events only contain actor_id. Resolve those to the
    // durable job_id seen in later events so one job never splits into lanes.
    const actorJobMap = new Map<string, string>();
    for (const item of items) {
      const payload = (item.payload ?? {}) as Record<string, unknown>;
      const actorID = String(payload.actor_id ?? "");
      const jobID = String(payload.job_id ?? "");
      if (actorID && jobID) actorJobMap.set(actorID, jobID);
    }

    const rawEvents = items
      .map((item) => classifyEvent(item, actorJobMap))
      .filter((event): event is LaneEvent => event !== null)
      .sort((left, right) => left.time - right.time);
    const visibleEvents = rawEvents.filter((event) => !showIssuesOnly || event.rejected || event.kind === "iterate");
    const byLane = new Map<string, TimelineLane>();
    for (const event of visibleEvents) {
      const lane = byLane.get(event.lane) ?? { id: event.lane, label: event.laneLabel, events: [], rejected: false };
      lane.label = event.laneLabel || lane.label;
      lane.events.push(event);
      lane.rejected ||= Boolean(event.rejected);
      byLane.set(event.lane, lane);
    }
    const lanes = [...byLane.values()].sort((left, right) => (left.events[0]?.time ?? 0) - (right.events[0]?.time ?? 0));
    return {
      lanes,
      rawCount: rawEvents.length,
      eventCount: visibleEvents.length,
      min: visibleEvents[0]?.time ?? 0,
      max: visibleEvents[visibleEvents.length - 1]?.time ?? 0,
    };
  }, [items, showIssuesOnly]);

  if (model.eventCount === 0) {
    const filteredOut = showIssuesOnly && model.rawCount > 0;
    return (
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        <TimelineToolbar showIssuesOnly={showIssuesOnly} onChange={setShowIssuesOnly} />
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={filteredOut ? t("chat.noSubagentTimelineFiltered") : t("chat.noSubagentTimeline")} />
        {filteredOut ? (
          <Button size="small" onClick={() => setShowIssuesOnly(false)}>
            {t("chat.timelineShowAllEvents")}
          </Button>
        ) : null}
      </Space>
    );
  }

  return (
    <Space direction="vertical" size={10} style={{ width: "100%" }}>
      <TimelineToolbar showIssuesOnly={showIssuesOnly} onChange={setShowIssuesOnly} />
      <div style={{ display: "flex", justifyContent: "space-between", gap: 8, flexWrap: "wrap" }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {model.lanes.length} agents · {model.eventCount} events
        </Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {formatTimelineTime(new Date(model.min).toISOString())} → {formatTimelineTime(new Date(model.max).toISOString())}
        </Typography.Text>
      </div>

      <div style={{ display: "grid", gap: 10 }}>
        {model.lanes.map((lane) => (
          <div
            key={lane.id}
            style={{
              minWidth: 0,
              padding: "10px 10px 4px",
              border: `1px solid ${lane.rejected ? "#ffccc7" : "var(--ant-color-border-secondary, #f0f0f0)"}`,
              borderRadius: 8,
              background: lane.rejected ? "rgba(255,77,79,0.04)" : "var(--ant-color-bg-container, #fff)",
            }}
          >
            <Space size={6} style={{ width: "100%", justifyContent: "space-between" }}>
              <Tooltip title={lane.id}>
                <Typography.Text strong ellipsis style={{ minWidth: 0, maxWidth: "75%" }}>
                  {lane.label}
                </Typography.Text>
              </Tooltip>
              <Typography.Text type="secondary" style={{ fontSize: 11, flexShrink: 0 }}>
                {shortTurnId(lane.id)} · {lane.events.length}
              </Typography.Text>
            </Space>
            <div style={{ marginTop: 8 }}>
              {lane.events.map((event, index) => (
                <TimelineEventRow key={`${event.time}-${event.kind}-${index}`} event={event} last={index === lane.events.length - 1} />
              ))}
            </div>
          </div>
        ))}
      </div>
    </Space>
  );
}

function TimelineEventRow({ event, last }: { event: LaneEvent; last: boolean }) {
  const meta = KIND_META[event.kind];
  const content = (
    <Space direction="vertical" size={3} style={{ maxWidth: 320 }}>
      <Space size={5} wrap>
        <Tag color={meta.color} style={{ margin: 0 }}>{event.kind}</Tag>
        {event.status ? <Tag style={{ margin: 0 }}>{event.status}</Tag> : null}
        {event.rejected ? <Tag color="red" style={{ margin: 0 }}>issue</Tag> : null}
      </Space>
      <Typography.Text strong>{event.label}</Typography.Text>
      {event.detail ? <Typography.Text type="secondary">{event.detail}</Typography.Text> : null}
      <Typography.Text type="secondary">{new Date(event.time).toLocaleString()}</Typography.Text>
    </Space>
  );

  return (
    <Popover content={content} trigger="click" placement="left">
      <div style={{ position: "relative", display: "grid", gridTemplateColumns: "24px minmax(0, 1fr) auto", gap: 7, minHeight: 45, cursor: "pointer" }}>
        {!last ? <span style={{ position: "absolute", left: 11, top: 22, bottom: -1, width: 1, background: "var(--ant-color-border, #d9d9d9)" }} /> : null}
        <span
          style={{
            zIndex: 1,
            display: "inline-flex",
            alignItems: "center",
            justifyContent: "center",
            width: 23,
            height: 23,
            borderRadius: "50%",
            color: event.rejected ? "#cf1322" : meta.color === "default" ? "#666" : meta.color,
            border: `1px solid ${event.rejected ? "#ff7875" : "var(--ant-color-border, #d9d9d9)"}`,
            background: event.rejected ? "#fff1f0" : "var(--ant-color-bg-container, #fff)",
          }}
        >
          {meta.icon}
        </span>
        <div style={{ minWidth: 0, paddingBottom: 9 }}>
          <Typography.Text ellipsis style={{ display: "block", fontSize: 12 }}>
            {event.label}
          </Typography.Text>
          <Space size={4} wrap>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>{event.kind}</Typography.Text>
            {event.status ? <Typography.Text type={event.rejected ? "danger" : "secondary"} style={{ fontSize: 11 }}>· {event.status}</Typography.Text> : null}
          </Space>
          {event.detail ? (
            <Typography.Text type={event.rejected ? "danger" : "secondary"} ellipsis style={{ display: "block", fontSize: 11 }}>
              {event.detail}
            </Typography.Text>
          ) : null}
        </div>
        <Typography.Text type="secondary" style={{ fontSize: 11, whiteSpace: "nowrap" }}>
          {formatTimelineTime(new Date(event.time).toISOString())}
        </Typography.Text>
      </div>
    </Popover>
  );
}

function TimelineToolbar({ showIssuesOnly, onChange }: { showIssuesOnly: boolean; onChange: (value: boolean) => void }) {
  return (
    <Space size={6} wrap>
      <Button size="small" icon={<FlagOutlined />} type={showIssuesOnly ? "primary" : "default"} onClick={() => onChange(!showIssuesOnly)}>
        {showIssuesOnly ? "All events" : "Issues / retries only"}
      </Button>
      <Typography.Text type="secondary" style={{ fontSize: 11 }}>click an event for details</Typography.Text>
    </Space>
  );
}
