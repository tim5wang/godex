import { Badge, Button, Card, Segmented, Space, Tag, Typography } from "antd";
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
  /** Which streams the list covers: everything, or only the open desktop. */
  scope: "current" | "all";
  onScopeChange: (scope: "current" | "all") => void;
}) {
  const { items, loading, onOpen, scope, onScopeChange } = props;
  const { t } = useI18n();
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);

  const label = (item: AttentionItem) => {
    switch (item.kind) {
      case "delegation_review": return tr("attentionReview");
      case "delegation_missing": return tr("attentionMissing");
      case "fact_review": return tr("attentionFactReview");
      case "todo_overdue": return tr("attentionOverdue");
      case "todo_soon": return tr("attentionSoon");
      case "todo_blocked": return tr("attentionBlocked");
      case "checkpoint_blocker": return tr("attentionBlocker");
      case "checkpoint_question": return tr("attentionQuestion");
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
      extra={
        <Segmented
          size="small"
          aria-label={tr("attentionScope")}
          value={scope}
          onChange={(value) => onScopeChange(value as "current" | "all")}
          options={[
            { value: "current", label: tr("attentionScopeCurrent") },
            { value: "all", label: tr("attentionScopeAll") },
          ]}
        />
      }
    >
      {items.length === 0 ? (
        <Typography.Text type="secondary">{tr("attentionEmpty")}</Typography.Text>
      ) : (
        <ul className="wd-attention-list">
          {items.map((item, index) => (
            <li key={`${item.kind}-${item.context_id}-${index}`} className={`wd-attention-item wd-attention-${item.priority}`}>
              <Tag color={color(item)}>{label(item)}</Tag>
              {/* A grouped row must SAY it stands for several entries, otherwise the
                  collapsed line silently hides work. */}
              {item.count && item.count > 1 ? <Tag>{tr("attentionGrouped", { count: item.count })}</Tag> : null}
              {/* Leverage: other streams waiting on this one. It is why this row sorts
                  above an equally urgent row nobody depends on. */}
              {item.blocked_count ? <Tag color="geekblue">{tr("attentionLeverage", { count: item.blocked_count })}</Tag> : null}
              <button type="button" className="wd-attention-main" onClick={() => onOpen(item.context_id, item.tab)}>
                <strong>{item.title}</strong>
                <span className="wd-muted">{item.context_title}</span>
                {/* The collapsed titles stay readable without opening the stream. */}
                {item.also?.length ? <span className="wd-muted wd-wrap">{item.also.join(" · ")}</span> : null}
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
