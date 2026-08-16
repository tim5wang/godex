import { useMemo, useState } from "react";
import type { TimelinePage, SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { Space, Empty, Typography, Button, Tag, Tooltip, Select, Input } from "antd";
import { type TimelineFilterState, timelineEventSummary, timelineEventFullText, stringFromPayload, timelineEventLabel, shortTurnId, formatTimelineTime, defaultTimelineFilters, defaultTimelineTypes, timelineEventTypeOptions, timelineEventTypeLabel, groupTimelineTurns, flattenTimelineEvents } from "../../../lib/timelineUtils";
import { EventDetailPanel } from "./EventDetailPanel";
import { TimelineOverview } from "./TimelineOverview";
import { TimelineGroupedList } from "./TimelineGroupedList";

export function TimelineList({
  page,
  fallbackItems,
  loading,
  filters,
  currentTurnId,
  canPrevious,
  pageIndex,
  onFiltersChange,
  onNextPage,
  onPreviousPage,
}: {
  page?: TimelinePage;
  fallbackItems: SessionTimelineEntry[];
  loading: boolean;
  filters: TimelineFilterState;
  currentTurnId: string;
  canPrevious: boolean;
  pageIndex?: number;
  onFiltersChange: (filters: TimelineFilterState) => void;
  onNextPage: () => void;
  onPreviousPage: () => void;
}) {
  const { t } = useI18n();
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null);
  const [selected, setSelected] = useState<SessionTimelineEntry | null>(null);
  const items = page?.items ?? fallbackItems.slice().reverse();
  const total = page?.total ?? fallbackItems.length;
  const hasMore = page?.has_more ?? false;
  const groups = useMemo(() => groupTimelineTurns(items), [items]);
  const chronoEvents = useMemo(() => flattenTimelineEvents(groups), [groups]);
  const filterActive =
    !(
      filters.types.length === defaultTimelineTypes.length &&
      defaultTimelineTypes.every((type) => filters.types.includes(type)) &&
      !filters.q &&
      !filters.jobId &&
      !filters.turnId &&
      !filters.currentTurnOnly &&
      filters.limit === 50
    );
  if (items.length === 0) {
    return (
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <TimelineFilters filters={filters} loading={loading} currentTurnId={currentTurnId} onChange={onFiltersChange} />
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={filterActive ? t("chat.noTimelineEventsMatch") : t("chat.noTimelineEvents")}>
          {filterActive ? (
            <Button size="small" onClick={() => onFiltersChange(defaultTimelineFilters())}>
              {t("chat.timelineResetFilters")}
            </Button>
          ) : null}
        </Empty>
      </Space>
    );
  }
  const handleSelect = (index: number) => {
    setSelectedIndex(index);
    const event = chronoEvents[index];
    if (event) {
      setSelected(event);
    }
  };
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <TimelineFilters filters={filters} loading={loading} currentTurnId={currentTurnId} onChange={onFiltersChange} />
      <Space style={{ width: "100%", justifyContent: "space-between" }} wrap>
        <Typography.Text type="secondary">{total} events{pageIndex !== undefined ? ` · ${t("chat.timelinePage", { page: pageIndex + 1 })}` : ""}</Typography.Text>
        <Space>
          <Button size="small" disabled={!canPrevious || loading} onClick={onPreviousPage}>
            Previous
          </Button>
          <Button size="small" disabled={!hasMore || loading} onClick={onNextPage}>
            Next
          </Button>
        </Space>
      </Space>
      <TimelineOverview groups={groups} selectedIndex={selectedIndex} onSelect={handleSelect} />
      <TimelineGroupedList
        groups={groups}
        selectedIndex={selectedIndex}
        onSelect={handleSelect}
        onFilterTurn={(turnId) => onFiltersChange({ ...filters, turnId, currentTurnOnly: false })}
        onFilterJob={(jobId) => onFiltersChange({ ...filters, jobId })}
      />
      <EventDetailPanel event={selected} onClose={() => setSelected(null)} />
      <Space style={{ width: "100%", justifyContent: "flex-end" }}>
        <Button size="small" disabled={!canPrevious || loading} onClick={onPreviousPage}>
          Previous
        </Button>
        <Button size="small" disabled={!hasMore || loading} onClick={onNextPage}>
          Next
        </Button>
      </Space>
    </Space>
  );
}

function TimelineFilters({
  filters,
  loading,
  currentTurnId,
  onChange,
}: {
  filters: TimelineFilterState;
  loading: boolean;
  currentTurnId: string;
  onChange: (filters: TimelineFilterState) => void;
}) {
  const update = (patch: Partial<TimelineFilterState>) => onChange({ ...filters, ...patch });
  const reset = () => onChange(defaultTimelineFilters());
  const effectiveTurnId = filters.currentTurnOnly ? currentTurnId : filters.turnId;
  const defaultActive =
    filters.types.length === defaultTimelineTypes.length &&
    defaultTimelineTypes.every((type) => filters.types.includes(type)) &&
    !filters.q &&
    !filters.jobId &&
    !filters.turnId &&
    !filters.currentTurnOnly &&
    filters.limit === 50;
  return (
    <Space direction="vertical" size={8} style={{ width: "100%" }}>
      <Space style={{ width: "100%", justifyContent: "space-between" }} wrap>
        <Space wrap size={6}>
          <Button
            size="small"
            type={filters.currentTurnOnly ? "primary" : "default"}
            disabled={loading || !currentTurnId}
            onClick={() => update({ currentTurnOnly: !filters.currentTurnOnly, turnId: filters.currentTurnOnly ? filters.turnId : "" })}
          >
            Current turn
          </Button>
          {effectiveTurnId ? (
            <Tooltip title={effectiveTurnId}>
              <Tag color="blue">{shortTurnId(effectiveTurnId)}</Tag>
            </Tooltip>
          ) : null}
        </Space>
        <Button size="small" disabled={loading || defaultActive} onClick={reset}>
          Reset
        </Button>
      </Space>
      <Select
        mode="multiple"
        size="small"
        allowClear
        maxTagCount={1}
        maxTagPlaceholder={(omitted) => `+${omitted.length}`}
        placeholder="Event types"
        popupMatchSelectWidth={false}
        style={{ width: "100%" }}
        value={filters.types}
        disabled={loading}
        onChange={(types) => update({ types })}
        options={timelineEventTypeOptions.map((type) => ({ value: type, label: timelineEventTypeLabel(type) }))}
      />
      <Input.Search
        size="small"
        allowClear
        placeholder="Search label / summary / payload"
        value={filters.q}
        disabled={loading}
        onChange={(event) => update({ q: event.target.value })}
      />
      <Space.Compact style={{ width: "100%" }}>
        <Input
          size="small"
          allowClear
          placeholder="job id"
          value={filters.jobId}
          disabled={loading}
          onChange={(event) => update({ jobId: event.target.value })}
        />
        <Input
          size="small"
          allowClear
          placeholder="turn id"
          value={filters.currentTurnOnly ? currentTurnId : filters.turnId}
          disabled={loading || filters.currentTurnOnly}
          onChange={(event) => update({ turnId: event.target.value })}
        />
      </Space.Compact>
      <Select
        size="small"
        value={filters.limit}
        disabled={loading}
        onChange={(limit) => update({ limit })}
        options={[25, 50, 100, 200].map((value) => ({ value, label: `${value} / page` }))}
      />
    </Space>
  );
}
