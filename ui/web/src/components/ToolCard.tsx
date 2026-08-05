import { Button, Space, Tag, Tooltip, Typography } from "antd";
import { DownOutlined, RightOutlined } from "@ant-design/icons";
import { ToolDetails } from "./ToolDetails";
import type { FeedItem } from "../lib/types";

interface ToolCardProps {
  item: FeedItem;
  onToggle: () => void;
}

export function ToolCard({ item, onToggle }: ToolCardProps) {
  const open = Boolean(item.expanded);
  const statusColor = item.status === "failed" ? "red" : item.status === "running" ? "processing" : "green";
  const hasDetails = Boolean(item.input || item.output || item.error);

  return (
    <div className="tool-card">
      <div className="tool-card-header">
        <Space className="tool-card-title" size={8} wrap>
          <Typography.Text className="tool-card-name" strong ellipsis={{ tooltip: item.title }}>
            {item.title}
          </Typography.Text>
          <Tag color={statusColor}>{item.status || "tool"}</Tag>
        </Space>
        {hasDetails ? (
          <Button
            className="tool-card-toggle"
            size="small"
            type="text"
            icon={open ? <DownOutlined /> : <RightOutlined />}
            onClick={onToggle}
            aria-expanded={open}
          >
            {open ? "Hide" : "Details"}
          </Button>
        ) : null}
      </div>

      {item.summary ? (
        <Tooltip title={item.summary}>
          <Typography.Paragraph className="tool-card-summary" type="secondary" ellipsis={{ rows: 2, expandable: true, symbol: "more" }}>
            {item.summary}
          </Typography.Paragraph>
        </Tooltip>
      ) : null}

      {open && hasDetails ? <ToolDetails item={item} /> : null}
    </div>
  );
}
