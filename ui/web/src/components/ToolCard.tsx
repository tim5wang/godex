import { Button, Space, Tag, Tooltip, Typography } from "antd";
import { DownOutlined, RightOutlined } from "@ant-design/icons";
import { MarkdownContent } from "./MarkdownContent";
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

      {open ? (
        <div className="tool-card-details">
          {item.input ? (
            <section className="tool-card-detail-block">
              <Typography.Text className="tool-card-detail-label" type="secondary">
                Input
              </Typography.Text>
              <pre className="tool-card-pre">{JSON.stringify(item.input, null, 2)}</pre>
            </section>
          ) : null}
          {item.output ? (
            <section className="tool-card-detail-block">
              <Typography.Text className="tool-card-detail-label" type="secondary">
                Output
              </Typography.Text>
              {looksLikeJSON(item.output) ? (
                <pre className="tool-card-pre">{formatJSONText(item.output)}</pre>
              ) : (
                <MarkdownContent className="tool-card-output" content={item.output} />
              )}
            </section>
          ) : null}
          {item.error ? (
            <section className="tool-card-detail-block">
              <Typography.Text className="tool-card-detail-label" type="secondary">
                Error
              </Typography.Text>
              <Typography.Text type="danger">{item.error}</Typography.Text>
            </section>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function looksLikeJSON(value: string) {
  const text = value.trim();
  return (text.startsWith("{") && text.endsWith("}")) || (text.startsWith("[") && text.endsWith("]"));
}

function formatJSONText(value: string) {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}
