import type { TimelinePage, SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { Space, Empty, Typography, Button, List, Tag, Tooltip, Select, Input } from "antd";
import { SafetyCertificateOutlined } from "@ant-design/icons";
import { type TimelineFilterState, timelineEventSummary, timelineEventFullText, stringFromPayload, timelineEventLabel, shortTurnId, formatTimelineTime, defaultTimelineFilters, defaultTimelineTypes, timelineEventTypeOptions, timelineEventTypeLabel } from "../../../lib/timelineUtils";

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
  const items = page?.items ?? fallbackItems.slice().reverse();
  const total = page?.total ?? fallbackItems.length;
  const hasMore = page?.has_more ?? false;
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
      <List
        size="small"
        loading={loading}
        dataSource={items}
        renderItem={(event) => {
          const payload = (event.payload ?? {}) as Record<string, unknown>;
          const summary = timelineEventSummary(event);
          const fullText = timelineEventFullText(event, summary);
          const jobID = stringFromPayload(payload.job_id);
          return (
            <List.Item>
              <List.Item.Meta
                avatar={<SafetyCertificateOutlined />}
                title={
                  <Space wrap>
                    <Typography.Text strong>{timelineEventLabel(event)}</Typography.Text>
                    <Tag>{event.type}</Tag>
                    {event.turn_id ? (
                      <Tooltip title={`${t("chat.timelineFilterByTurn")}: ${event.turn_id}`}>
                        <Tag color="blue" style={{ cursor: "pointer" }} onClick={() => onFiltersChange({ ...filters, turnId: event.turn_id ?? "", currentTurnOnly: false })}>
                          {shortTurnId(event.turn_id)}
                        </Tag>
                      </Tooltip>
                    ) : null}
                    {jobID ? (
                      <Tooltip title={`${t("chat.timelineFilterByJob")}: ${jobID}`}>
                        <Tag color="purple" style={{ cursor: "pointer" }} onClick={() => onFiltersChange({ ...filters, jobId: jobID })}>
                          {shortTurnId(jobID)}
                        </Tag>
                      </Tooltip>
                    ) : null}
                    <Typography.Text type="secondary">{formatTimelineTime(event.timestamp)}</Typography.Text>
                  </Space>
                }
                description={
                  <Tooltip title={fullText}>
                    <Typography.Paragraph className="timeline-summary" copyable ellipsis={{ rows: 2, expandable: true, symbol: "more" }}>
                      {summary || fullText || "-"}
                    </Typography.Paragraph>
                  </Tooltip>
                }
              />
            </List.Item>
          );
        }}
      />
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
