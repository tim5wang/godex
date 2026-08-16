import { memo } from "react";
import { Tooltip } from "antd";
import type { SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import {
  type TimelineLane,
  type TimelineTurnGroup,
  flattenTimelineEvents,
  timelineEventLabel,
  timelineEventLane,
  timelineEventSummary,
} from "../../../lib/timelineUtils";

const LANE_COLORS: Record<TimelineLane, string> = {
  input: "#69b1ff",
  model: "#95de64",
  tool: "#ffa940",
  other: "#d9d9d9",
};

/**
 * Chrome-Network-style sequence overview: every event is one equal-width block
 * colored by lane (input / model / tool / other), turn boundaries as gaps.
 * Click a block to select the event (highlights + scrolls the ledger).
 */
export const TimelineOverview = memo(function TimelineOverview({
  groups,
  selectedIndex,
  onSelect,
}: {
  groups: TimelineTurnGroup[];
  selectedIndex: number | null;
  onSelect: (index: number) => void;
}) {
  const { t } = useI18n();
  const events = flattenTimelineEvents(groups);
  if (events.length < 2) {
    return null;
  }

  // Emit blocks in chronological order (groups are newest-first).
  let blockIndex = 0;
  const blocks: React.ReactNode[] = [];
  for (let g = groups.length - 1; g >= 0; g--) {
    const turn = groups[g];
    for (const step of turn.steps) {
      for (const event of step.events) {
        const idx = blockIndex++;
        const lane = timelineEventLane(event);
        const selected = idx === selectedIndex;
        blocks.push(
          <Tooltip
            key={`${turn.turnId}|${step.key}|${idx}`}
            title={
              <span style={{ fontSize: 11 }}>
                {timelineEventLabel(event)}
                {timelineEventSummary(event) ? ` · ${timelineEventSummary(event)}` : ""}
              </span>
            }
            mouseEnterDelay={0.4}
          >
            <div
              role="button"
              aria-label={`${timelineEventLabel(event)} ${idx}`}
              onClick={() => onSelect(idx)}
              style={{
                flex: "1 1 0",
                minWidth: 2,
                height: "100%",
                background: LANE_COLORS[lane],
                opacity: selected ? 1 : 0.72,
                outline: selected ? "1.5px solid #1677ff" : "none",
                outlineOffset: selected ? -1 : 0,
                cursor: "pointer",
                boxSizing: "border-box",
              }}
            />
          </Tooltip>,
        );
      }
    }
    if (g > 0) {
      blocks.push(
        <div key={`turn-gap-${turn.turnId}`} style={{ flex: "0 0 3px", background: "rgba(0,0,0,0.18)" }} />,
      );
    }
  }

  return (
    <div
      data-testid="timeline-overview"
      style={{
        display: "flex",
        alignItems: "stretch",
        height: 22,
        width: "100%",
        border: "1px solid rgba(0,0,0,0.12)",
        borderRadius: 4,
        overflow: "hidden",
        background: "rgba(0,0,0,0.04)",
      }}
      title={t("chat.timelineOverviewTitle") || "Event overview (click to jump)"}
    >
      {blocks}
    </div>
  );
});
