import { Button, Card, Popconfirm, Space, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import type { ControlNode } from "../../lib/types";
import { VISUAL_TAG_COLOR, formatRelativeTime, nodeVisualStatus, type NodeVisualStatus } from "./nodeStatus";

const STATE_KEY: Record<NodeVisualStatus, string> = {
  healthy: "nodes.stateHealthy",
  degraded: "nodes.stateDegraded",
  offline: "nodes.stateOffline",
};

// The tag shows one word; the tooltip says what that word is derived from, so
// "degraded" is actionable instead of a mystery.
const HINT_KEY: Record<NodeVisualStatus, string> = {
  healthy: "nodes.stateHealthyHint",
  degraded: "nodes.stateDegradedHint",
  offline: "nodes.stateOfflineHint",
};

interface Props {
  node: ControlNode;
  /** Reference instant for the relative "last seen"; passed in so every card on
   *  a render agrees and no card runs its own timer. */
  now: number;
  onOpen: () => void;
  onDelete: () => void;
}

/**
 * One compact, status-coloured node card.
 *
 * The row is deliberately narrow: name, ONE status colour, and the five fields a
 * person filters by. The previous layout spread the same data across seven table
 * columns plus a second card markup for narrow screens; here a single markup
 * serves both, so the wide/narrow branches cannot drift apart.
 */
export function NodeCard({ node, now, onOpen, onDelete }: Props) {
  const { t, locale } = useI18n();
  const visual = nodeVisualStatus(node);
  const relativeSeen = formatRelativeTime(node.last_seen, now, locale);
  const absoluteSeen = node.last_seen ? new Date(node.last_seen).toLocaleString() : null;
  const capabilities = node.capabilities ?? [];

  return (
    <Card
      size="small"
      hoverable
      className={`node-card node-card--${visual}`}
      onClick={onOpen}
      styles={{ body: { padding: 12 } }}
    >
      <div className="node-card-head">
        <Space size={6} wrap>
          <Typography.Text strong ellipsis={{ tooltip: node.name || node.id }}>
            {node.name || node.id}
          </Typography.Text>
          <Tooltip
            title={`${t(HINT_KEY[visual])} — ${node.status || "offline"} · ${t("nodes.relayStatus")}: ${
              node.relay_status || "disconnected"
            }`}
          >
            <Tag color={VISUAL_TAG_COLOR[visual]}>{t(STATE_KEY[visual])}</Tag>
          </Tooltip>
          {node.source === "center" ? <Tag color="purple">{t("nodes.sourceCenter")}</Tag> : null}
          {node.kind ? <Tag>{node.kind}</Tag> : null}
        </Space>
        <Popconfirm
          title={t("nodes.deleteTitle")}
          description={t("nodes.deleteConfirm")}
          okText={t("nodes.deleteTitle")}
          cancelText={t("common.cancel")}
          onConfirm={onDelete}
          onPopupClick={(e) => e.stopPropagation()}
        >
          <Button
            size="small"
            type="text"
            danger
            aria-label={t("nodes.deleteTitle")}
            icon={<DeleteOutlined />}
            onClick={(e) => e.stopPropagation()}
          />
        </Popconfirm>
      </div>

      <Typography.Text type="secondary" className="node-card-id" copyable={{ text: node.id }}>
        {node.id}
      </Typography.Text>

      <div className="node-card-meta">
        <div className="node-card-meta-item">
          <span className="node-card-meta-label">{t("nodes.workspace")}</span>
          <span className="node-card-meta-value" title={node.workspace_dir}>
            {node.workspace_dir || "-"}
          </span>
        </div>
        <div className="node-card-meta-item">
          <span className="node-card-meta-label">{t("nodes.endpoint")}</span>
          <span className="node-card-meta-value" title={node.endpoint}>
            {node.endpoint || "-"}
          </span>
        </div>
        <div className="node-card-meta-item">
          <span className="node-card-meta-label">{t("nodes.version")}</span>
          <span className="node-card-meta-value">{node.version || "-"}</span>
        </div>
        <div className="node-card-meta-item">
          <span className="node-card-meta-label">{t("nodes.lastSeen")}</span>
          <span className="node-card-meta-value" title={absoluteSeen ?? undefined}>
            {relativeSeen || "-"}
          </span>
        </div>
      </div>

      {capabilities.length > 0 ? (
        <div className="node-card-caps">
          {capabilities.slice(0, 3).map((item) => (
            <Tag key={item} className="node-card-cap">
              {item}
            </Tag>
          ))}
          {capabilities.length > 3 ? <Tag className="node-card-cap">+{capabilities.length - 3}</Tag> : null}
        </div>
      ) : null}
    </Card>
  );
}
