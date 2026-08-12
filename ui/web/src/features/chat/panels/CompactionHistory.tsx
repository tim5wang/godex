import type { SessionTimelineEntry, CompactionRecord } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { List, Empty, Space, Typography, Tag, Spin } from "antd";
import { formatTimelineTime } from "../../../lib/timelineUtils";

// ---------------------------------------------------------------------------
// CompactionHistoryPanel
//
// B2: lists every context compaction that happened in this session. When the
// dedicated compactions endpoint is available (records), it is used directly so
// the panel is not limited by the 80-item timeline window and early
// compactions recovered from session summary messages are included. Falls back
// to filtering the session timeline for snapshot_ready events with
// payload.compacted=true.
// ---------------------------------------------------------------------------

export function CompactionHistoryPanel({
  records,
  items,
  loading,
}: {
  records?: CompactionRecord[];
  items: SessionTimelineEntry[];
  loading?: boolean;
}) {
  const { t } = useI18n();

  const compactions: { timestamp: string; before: number; after: number; reasons: string; source?: string }[] = [];
  if (records && records.length > 0) {
    for (const record of records) {
      compactions.push({
        timestamp: record.timestamp,
        before: Number(record.before_tokens ?? 0),
        after: Number(record.after_tokens ?? 0),
        reasons: Array.isArray(record.reasons) ? record.reasons.join(", ") : "",
        source: record.source,
      });
    }
  } else {
    items
      .filter((event) => {
        if (event.type !== "snapshot_ready") return false;
        const payload = (event.payload ?? {}) as Record<string, unknown>;
        return payload.compacted === true;
      })
      .slice()
      .reverse() // newest first
      .forEach((event) => {
        const payload = (event.payload ?? {}) as Record<string, unknown>;
        compactions.push({
          timestamp: event.timestamp,
          before: Number(payload.token_estimate_before ?? 0),
          after: Number(payload.token_estimate_after ?? 0),
          reasons: Array.isArray(payload.compression_reasons)
            ? (payload.compression_reasons as unknown[]).map(String).join(", ")
            : "",
        });
      });
  }

  if (loading && compactions.length === 0) {
    return (
      <div style={{ textAlign: "center", padding: "24px 0" }}>
        <Spin size="small" />
      </div>
    );
  }
  if (compactions.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.compactionHistoryEmpty")} />;
  }
  return (
    <List
      size="small"
      dataSource={compactions}
      renderItem={(item) => (
        <List.Item>
          <Space direction="vertical" size={2} style={{ width: "100%" }}>
            <Space wrap size={6}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {formatTimelineTime(item.timestamp)}
              </Typography.Text>
              {item.before > 0 || item.after > 0 ? (
                <Tag color={item.after < item.before ? "green" : "default"}>
                  {t("chat.compactionHistoryTokens", { before: item.before, after: item.after })}
                </Tag>
              ) : null}
              {item.source ? (
                <Tag color="blue" style={{ marginInlineStart: 0 }}>
                  {item.source}
                </Tag>
              ) : null}
            </Space>
            {item.reasons ? (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t("chat.compactionHistoryReason", { reasons: item.reasons })}
              </Typography.Text>
            ) : null}
          </Space>
        </List.Item>
      )}
    />
  );
}
