import { memo, useEffect, useMemo, useRef, useState } from "react";
import { Tag, Tooltip, Typography } from "antd";
import { CaretDownOutlined, CaretRightOutlined } from "@ant-design/icons";
import type { SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import {
  type TimelineStepGroup,
  type TimelineTurnGroup,
  formatTimelineTime,
  stringFromPayload,
  timelineEventLabel,
  timelineEventSummary,
  timelineEventFullText,
  shortTurnId,
  formatDurationMs,
} from "../../../lib/timelineUtils";

const NO_TURN_KEY = "__no_turn__";

type GroupedRow =
  | { kind: "turn"; group: TimelineTurnGroup }
  | { kind: "turnSummary"; group: TimelineTurnGroup }
  | { kind: "step"; step: TimelineStepGroup }
  | { kind: "stepSummary"; step: TimelineStepGroup }
  | { kind: "event"; event: SessionTimelineEntry; chronoIndex: number };

const ROW_HEIGHT: Record<GroupedRow["kind"], number> = {
  turn: 34,
  turnSummary: 30,
  step: 30,
  stepSummary: 28,
  event: 62,
};

function rowHeight(row: GroupedRow): number {
  return ROW_HEIGHT[row.kind];
}

function buildRows(
  groups: TimelineTurnGroup[],
  collapsedTurns: ReadonlySet<string>,
  collapsedSteps: ReadonlySet<string>,
): { rows: GroupedRow[]; containers: Map<number, { turnKey: string; stepKey: string }> } {
  const rows: GroupedRow[] = [];
  const containers = new Map<number, { turnKey: string; stepKey: string }>();
  let chronoCounter = 0;
  const countHidden = (step: TimelineStepGroup, turnKey: string) => {
    for (const ev of step.events) {
      containers.set(chronoCounter, { turnKey, stepKey: step.key });
      chronoCounter += 1;
    }
  };
  for (let g = groups.length - 1; g >= 0; g--) {
    const group = groups[g];
    const turnKey = group.turnId ?? NO_TURN_KEY;
    const turnCollapsed = collapsedTurns.has(turnKey);
    rows.push({ kind: "turn", group });
    if (turnCollapsed) {
      rows.push({ kind: "turnSummary", group });
      for (const step of group.steps) {
        countHidden(step, turnKey);
      }
      continue;
    }
    for (const step of group.steps) {
      const stepCollapsed = collapsedSteps.has(step.key);
      rows.push({ kind: "step", step });
      if (stepCollapsed) {
        rows.push({ kind: "stepSummary", step });
        countHidden(step, turnKey);
        continue;
      }
      for (const ev of step.events) {
        containers.set(chronoCounter, { turnKey, stepKey: step.key });
        rows.push({ kind: "event", event: ev, chronoIndex: chronoCounter });
        chronoCounter += 1;
      }
    }
  }
  return { rows, containers };
}

function useWindowedRows(rows: GroupedRow[], threshold = 150, overscan = 6) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [range, setRange] = useState<[number, number]>([0, rows.length]);

  useEffect(() => {
    if (rows.length <= threshold) {
      setRange([0, rows.length]);
      return;
    }
    const el = containerRef.current;
    if (!el) {
      return;
    }
    const heights = rows.map(rowHeight);
    const prefix = new Array<number>(rows.length + 1);
    prefix[0] = 0;
    for (let i = 0; i < rows.length; i++) {
      prefix[i + 1] = prefix[i] + heights[i];
    }
    const findIndex = (offset: number) => {
      let lo = 0;
      let hi = rows.length;
      while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (prefix[mid] <= offset) {
          lo = mid;
        } else {
          hi = mid - 1;
        }
      }
      return lo;
    };
    const update = () => {
      const scrollTop = el.scrollTop;
      const viewport = el.clientHeight;
      const start = Math.max(0, findIndex(scrollTop) - overscan);
      const end = Math.min(rows.length, findIndex(scrollTop + viewport) + overscan);
      setRange([start, end]);
    };
    update();
    el.addEventListener("scroll", update, { passive: true });
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => {
      el.removeEventListener("scroll", update);
      ro.disconnect();
    };
  }, [rows, threshold, overscan]);

  const totalHeight = useMemo(() => rows.reduce((acc, row) => acc + rowHeight(row), 0), [rows]);
  return { containerRef, range, totalHeight };
}

function stepSummaryLine(step: TimelineStepGroup): string {
  const tools: string[] = [];
  const counts = new Map<string, number>();
  for (const ev of step.events) {
    if (ev.type === "tool_call_finished") {
      const payload = (ev.payload ?? {}) as Record<string, unknown>;
      const name = String(payload.name ?? "tool");
      counts.set(name, (counts.get(name) ?? 0) + 1);
    }
  }
  for (const [name, count] of counts) {
    tools.push(`${name}×${count}`);
  }
  const duration = step.events.length > 1 ? formatDurationMs(Date.parse(step.endedAt) - Date.parse(step.startedAt)) : "";
  return [duration, tools.join(" ")].filter(Boolean).join(" · ");
}

export const TimelineGroupedList = memo(function TimelineGroupedList({
  groups,
  selectedIndex,
  onSelect,
  onFilterTurn,
  onFilterJob,
}: {
  groups: TimelineTurnGroup[];
  selectedIndex: number | null;
  onSelect: (index: number) => void;
  onFilterTurn: (turnId: string) => void;
  onFilterJob: (jobId: string) => void;
}) {
  const { t } = useI18n();
  const [collapsedTurns, setCollapsedTurns] = useState<Set<string>>(new Set());
  const [collapsedSteps, setCollapsedSteps] = useState<Set<string>>(new Set());
  const eventRefs = useRef(new Map<number, HTMLDivElement>());

  const { rows, containers } = useMemo(() => buildRows(groups, collapsedTurns, collapsedSteps), [groups, collapsedTurns, collapsedSteps]);

  // Auto-expand the containing turn/step when a selection points at a hidden row.
  useEffect(() => {
    if (selectedIndex == null) {
      return;
    }
    const container = containers.get(selectedIndex);
    if (!container) {
      return;
    }
    let changed = false;
    if (collapsedTurns.has(container.turnKey)) {
      setCollapsedTurns((prev) => {
        const next = new Set(prev);
        next.delete(container.turnKey);
        return next;
      });
      changed = true;
    }
    if (collapsedSteps.has(container.stepKey)) {
      setCollapsedSteps((prev) => {
        const next = new Set(prev);
        next.delete(container.stepKey);
        return next;
      });
      changed = true;
    }
    if (changed) {
      return; // rows rebuild on next render; scroll after that
    }
    window.setTimeout(() => {
      eventRefs.current.get(selectedIndex)?.scrollIntoView({ block: "nearest" });
    }, 0);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedIndex, containers, collapsedTurns, collapsedSteps]);

  const toggleTurn = (group: TimelineTurnGroup) => {
    const key = group.turnId ?? NO_TURN_KEY;
    setCollapsedTurns((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  const toggleStep = (step: TimelineStepGroup) => {
    setCollapsedSteps((prev) => {
      const next = new Set(prev);
      if (next.has(step.key)) {
        next.delete(step.key);
      } else {
        next.add(step.key);
      }
      return next;
    });
  };

  const { containerRef, range, totalHeight } = useWindowedRows(rows);
  const windowed = rows.length > 150 ? rows.slice(range[0], range[1]) : rows;
  const offsetTop = rows.length > 150 ? rows.slice(0, range[0]).reduce((acc, row) => acc + rowHeight(row), 0) : 0;

  const renderRow = (row: GroupedRow) => {
    switch (row.kind) {
      case "turn": {
        const collapsed = collapsedTurns.has(row.group.turnId ?? NO_TURN_KEY);
        return (
          <div
            key={`turn-${row.group.turnId ?? NO_TURN_KEY}`}
            onClick={() => toggleTurn(row.group)}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              minHeight: ROW_HEIGHT.turn,
              padding: "4px 8px",
              background: "rgba(0,0,0,0.06)",
              borderRadius: 6,
              cursor: "pointer",
              marginTop: 4,
            }}
            data-testid="timeline-turn-header"
          >
            {collapsed ? <CaretRightOutlined style={{ fontSize: 10 }} /> : <CaretDownOutlined style={{ fontSize: 10 }} />}
            <Typography.Text strong style={{ fontSize: 12 }}>
              {row.group.label}
            </Typography.Text>
            <Tag>{row.group.eventCount} events</Tag>
            {row.group.tools.slice(0, 3).map((tool) => (
              <Tag key={tool.name} color="orange">
                {tool.name}×{tool.count}
              </Tag>
            ))}
            <Typography.Text type="secondary" style={{ fontSize: 11, marginLeft: "auto" }}>
              {formatTimelineTime(row.group.startedAt)} → {formatTimelineTime(row.group.endedAt)}
            </Typography.Text>
          </div>
        );
      }
      case "turnSummary": {
        const steps = row.group.steps.length;
        return (
          <div key={`turn-sum-${row.group.turnId ?? NO_TURN_KEY}`} style={{ minHeight: ROW_HEIGHT.turnSummary, padding: "2px 8px 2px 24px" }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {row.group.eventCount} events · {steps} {steps === 1 ? "step" : "steps"} (collapsed)
            </Typography.Text>
          </div>
        );
      }
      case "step": {
        const collapsed = collapsedSteps.has(row.step.key);
        const line = stepSummaryLine(row.step);
        return (
          <div
            key={`step-${row.step.key}`}
            onClick={() => toggleStep(row.step)}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              minHeight: ROW_HEIGHT.step,
              padding: "2px 8px 2px 16px",
              cursor: "pointer",
              borderBottom: "1px solid rgba(0,0,0,0.04)",
            }}
            data-testid="timeline-step-header"
          >
            {collapsed ? <CaretRightOutlined style={{ fontSize: 10 }} /> : <CaretDownOutlined style={{ fontSize: 10 }} />}
            <Typography.Text strong style={{ fontSize: 12 }}>
              {row.step.label}
            </Typography.Text>
            {line ? <Typography.Text type="secondary" style={{ fontSize: 11 }}>{line}</Typography.Text> : null}
            <Tag style={{ fontSize: 11 }}>{row.step.events.length}</Tag>
          </div>
        );
      }
      case "stepSummary": {
        return (
          <div key={`step-sum-${row.step.key}`} style={{ minHeight: ROW_HEIGHT.stepSummary, padding: "2px 8px 2px 32px" }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {row.step.events.length} events (collapsed)
            </Typography.Text>
          </div>
        );
      }
      case "event": {
        const event = row.event;
        const payload = (event.payload ?? {}) as Record<string, unknown>;
        const summary = timelineEventSummary(event);
        const fullText = timelineEventFullText(event, summary);
        const jobID = stringFromPayload(payload.job_id);
        const selected = row.chronoIndex === selectedIndex;
        return (
          <div
            key={`ev-${row.chronoIndex}`}
            ref={(el) => {
              if (el) {
                eventRefs.current.set(row.chronoIndex, el);
              } else {
                eventRefs.current.delete(row.chronoIndex);
              }
            }}
            onClick={() => {
              onSelect(row.chronoIndex);
            }}
            style={{
              display: "flex",
              gap: 8,
              minHeight: ROW_HEIGHT.event,
              padding: "4px 8px 4px 24px",
              cursor: "pointer",
              borderRadius: 6,
              background: selected ? "rgba(22,119,255,0.10)" : "transparent",
              outline: selected ? "1px solid rgba(22,119,255,0.4)" : "none",
            }}
            onMouseEnter={(e) => {
              if (!selected) {
                e.currentTarget.style.background = "rgba(0,0,0,0.03)";
              }
            }}
            onMouseLeave={(e) => {
              if (!selected) {
                e.currentTarget.style.background = "transparent";
              }
            }}
            data-testid="timeline-event-row"
          >
            <Typography.Text style={{ fontSize: 11, color: "#999", lineHeight: "20px" }}>#{row.chronoIndex}</Typography.Text>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
                <Typography.Text strong style={{ fontSize: 12 }}>
                  {timelineEventLabel(event)}
                </Typography.Text>
                <Tag style={{ fontSize: 10, lineHeight: "16px" }}>{event.type}</Tag>
                {event.turn_id ? (
                  <Tooltip title={`${t("chat.timelineFilterByTurn")}: ${event.turn_id}`}>
                    <Tag
                      color="blue"
                      style={{ fontSize: 10, lineHeight: "16px", cursor: "pointer" }}
                      onClick={(e) => {
                        e.stopPropagation();
                        onFilterTurn(event.turn_id ?? "");
                      }}
                    >
                      {shortTurnId(event.turn_id)}
                    </Tag>
                  </Tooltip>
                ) : null}
                {jobID ? (
                  <Tooltip title={`${t("chat.timelineFilterByJob")}: ${jobID}`}>
                    <Tag
                      color="purple"
                      style={{ fontSize: 10, lineHeight: "16px", cursor: "pointer" }}
                      onClick={(e) => {
                        e.stopPropagation();
                        onFilterJob(jobID);
                      }}
                    >
                      {shortTurnId(jobID)}
                    </Tag>
                  </Tooltip>
                ) : null}
                <Typography.Text type="secondary" style={{ fontSize: 11, marginLeft: "auto" }}>
                  {formatTimelineTime(event.timestamp)}
                </Typography.Text>
              </div>
              <Tooltip title={fullText}>
                <Typography.Paragraph
                  className="timeline-summary"
                  style={{ marginBottom: 0, fontSize: 12 }}
                  ellipsis={{ rows: 1, expandable: true, symbol: "more" }}
                >
                  {summary || fullText || "-"}
                </Typography.Paragraph>
              </Tooltip>
            </div>
          </div>
        );
      }
    }
  };

  if (rows.length === 0) {
    return null;
  }

  return (
    <div
      ref={containerRef}
      style={{ maxHeight: "60vh", overflow: "auto", position: "relative", width: "100%" }}
      data-testid="timeline-grouped-list"
    >
      {rows.length > 150 ? (
        <div style={{ height: totalHeight, position: "relative" }}>
          <div style={{ position: "absolute", top: 0, left: 0, right: 0, transform: `translateY(${offsetTop}px)` }}>
            {windowed.map(renderRow)}
          </div>
        </div>
      ) : (
        rows.map(renderRow)
      )}
    </div>
  );
});
