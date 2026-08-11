import type { SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { List, Empty, Space, Typography, Tag } from "antd";
import { formatTimelineTime } from "../../../lib/timelineUtils";

// ---------------------------------------------------------------------------
// CompactionHistoryPanel
//
// B2: lists every context compaction that happened in this session. Data comes
// straight from the session timeline (snapshot_ready events with
// payload.compacted=true) — no new API needed. Each entry shows the token
// reduction (before -> after) and the compression reasons.
// ---------------------------------------------------------------------------

export function CompactionHistoryPanel({ items }: { items: SessionTimelineEntry[] }) {
  const { t } = useI18n();
  const compactions = items
    .filter((event) => {
      if (event.type !== "snapshot_ready") return false;
      const payload = (event.payload ?? {}) as Record<string, unknown>;
      return payload.compacted === true;
    })
    .slice()
    .reverse(); // newest first

  if (compactions.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.compactionHistoryEmpty")} />;
  }
  return (
    <List
      size="small"
      dataSource={compactions}
      renderItem={(event) => {
        const payload = (event.payload ?? {}) as Record<string, unknown>;
        const before = Number(payload.token_estimate_before ?? 0);
        const after = Number(payload.token_estimate_after ?? 0);
        const reasons = Array.isArray(payload.compression_reasons)
          ? (payload.compression_reasons as unknown[]).map(String).join(", ")
          : "";
        return (
          <List.Item>
            <Space direction="vertical" size={2} style={{ width: "100%" }}>
              <Space wrap size={6}>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {formatTimelineTime(event.timestamp)}
                </Typography.Text>
                {before > 0 || after > 0 ? (
                  <Tag color={after < before ? "green" : "default"}>{t("chat.compactionHistoryTokens", { before, after })}</Tag>
                ) : null}
              </Space>
              {reasons ? (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t("chat.compactionHistoryReason", { reasons })}
                </Typography.Text>
              ) : null}
            </Space>
          </List.Item>
        );
      }}
    />
  );
}
