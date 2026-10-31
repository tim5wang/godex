import { Badge, Button, Card, Space, Tag, Typography } from "antd";
import { ThunderboltOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { InfoHint } from "./workDesktopHints";
import { formatTimestamp } from "./workDesktopView";
import type { AttentionItem } from "../../lib/apiWorkContext";

/**
 * AttentionInbox is the cross-stream "needs me" list.
 *
 * It exists so six or seven parallel streams can be triaged from one place: each
 * row names its stream and jumps straight to the tab where the item is resolved,
 * instead of making the user open desktops one by one to find out which moved.
 * The list is compact by design (one line per item) because it is a triage view,
 * not a place to work.
 */
export function AttentionInbox(props: {
  items: AttentionItem[];
  loading: boolean;
  onOpen: (contextId: string, tab: string) => void;
}) {
  const { items, loading, onOpen } = props;
  const { t } = useI18n();
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);

  const label = (item: AttentionItem) => {
    switch (item.kind) {
      case "delegation_review": return tr("attentionReview");
      case "todo_overdue": return tr("attentionOverdue");
      case "todo_soon": return tr("attentionSoon");
      default: return tr("attention");
    }
  };
  const color = (item: AttentionItem) => {
    switch (item.priority) {
      case "high": return "red";
      case "normal": return "orange";
      default: return "default";
    }
  };

  return (
    <Card
      size="small"
      className="wd-attention"
      title={
        <Space size={6}>
          <ThunderboltOutlined aria-hidden="true" />
          {tr("attention")}
          {!loading && items.length ? <Badge count={items.length} size="small" /> : null}
          <InfoHint title={tr("showHint")} text={tr("attentionHint")} />
        </Space>
      }
    >
      {items.length === 0 ? (
        <Typography.Text type="secondary">{tr("attentionEmpty")}</Typography.Text>
      ) : (
        <ul className="wd-attention-list">
          {items.map((item, index) => (
            <li key={`${item.kind}-${item.context_id}-${index}`} className={`wd-attention-item wd-attention-${item.priority}`}>
              <Tag color={color(item)}>{label(item)}</Tag>
              <button type="button" className="wd-attention-main" onClick={() => onOpen(item.context_id, item.tab)}>
                <strong>{item.title}</strong>
                <span className="wd-muted">{item.context_title}</span>
              </button>
              {item.due_at ? <span className="wd-muted wd-attention-due">{formatTimestamp(item.due_at)}</span> : null}
              <Button size="small" type="link" onClick={() => onOpen(item.context_id, item.tab)}>
                {tr("attentionOpen")}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
