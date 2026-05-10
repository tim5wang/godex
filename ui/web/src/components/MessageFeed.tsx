import { App as AntApp, Avatar, Button, Empty, Space, Tag, Tooltip, Typography } from "antd";
import { CheckSquareOutlined, CopyOutlined, RobotOutlined, SaveOutlined, ToolOutlined, UserOutlined, WarningOutlined } from "@ant-design/icons";
import { Bubble, type BubbleItemType } from "@ant-design/x";
import { AttachmentList } from "./AttachmentList";
import { MarkdownContent } from "./MarkdownContent";
import { SubagentCard } from "./SubagentCard";
import { TodoCard } from "./TodoCard";
import { ToolCard } from "./ToolCard";
import { useI18n } from "../i18n";
import { writeClipboardText } from "../lib/clipboard";
import type { FeedItem } from "../lib/types";

interface MessageFeedProps {
  items: FeedItem[];
  onToggleTool: (id: string) => void;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote?: boolean;
  hasNoteContext?: boolean;
}

export function MessageFeed({ items, onToggleTool, onSaveToNote, savingToNote = false, hasNoteContext = false }: MessageFeedProps) {
  const { message } = AntApp.useApp();
  const { t } = useI18n();

  if (items.length === 0) {
    return <Empty description="No messages yet" image={Empty.PRESENTED_IMAGE_SIMPLE} />;
  }

  const copyItem = async (item: FeedItem) => {
    const text = copyTextForItem(item);
    if (!text) {
      return;
    }
    try {
      await writeClipboardText(text);
      void message.success(t("chat.copied"));
    } catch {
      void message.error(t("chat.copyFailed"));
    }
  };

  const bubbleItems: BubbleItemType[] = items.map((item) => ({
    key: item.id,
    role: item.kind === "user" ? "user" : item.kind === "assistant" ? "ai" : item.kind === "tool" || item.kind === "todo" || item.kind === "subagent" ? "tool" : "system",
    content: renderContent({
      item,
      onToggleTool,
      onCopy: () => void copyItem(item),
      copyLabel: t("chat.copyMessage"),
      saveLabel: hasNoteContext ? t("chat.saveToCurrentNote") : t("chat.saveAsNote"),
      onSaveToNote,
      savingToNote,
    }),
    header: item.kind === "tool" || item.kind === "todo" || item.kind === "subagent" ? undefined : renderHeader(item),
    avatar: renderAvatar(item),
    rootClassName: `chat-feed-bubble chat-feed-bubble-${item.kind}`,
    variant: item.kind === "user" ? "filled" : item.kind === "tool" || item.kind === "todo" || item.kind === "subagent" ? "borderless" : "outlined",
    shape: "corner",
  }));

  return (
    <Bubble.List
      items={bubbleItems}
      autoScroll
      role={{
        user: { placement: "end", styles: { content: { maxWidth: "min(760px, 86vw)" } } },
        ai: { placement: "start", styles: { content: { maxWidth: "min(820px, 90vw)" } } },
        tool: { placement: "start", variant: "borderless" },
        system: { placement: "start", styles: { content: { maxWidth: "min(820px, 90vw)" } } },
      }}
    />
  );
}

function renderContent({
  item,
  onToggleTool,
  onCopy,
  copyLabel,
  saveLabel,
  onSaveToNote,
  savingToNote,
}: {
  item: FeedItem;
  onToggleTool: (id: string) => void;
  onCopy: () => void;
  copyLabel: string;
  saveLabel: string;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote: boolean;
}) {
  const copyable = Boolean(copyTextForItem(item));
  const canSaveToNote = Boolean(onSaveToNote && item.kind === "assistant" && item.body.trim());
  const content =
    item.kind === "tool" ? (
      <ToolCard item={item} onToggle={() => onToggleTool(item.id)} />
    ) : item.kind === "todo" ? (
      <TodoCard item={item} />
    ) : item.kind === "subagent" ? (
      <SubagentCard item={item} onToggle={() => onToggleTool(item.id)} />
    ) : (
      <Space direction="vertical" size={10} style={{ width: "100%" }}>
        {item.body ? <MarkdownContent content={item.body} forceMarkdown={item.kind === "user"} /> : null}
        {item.attachments?.length ? <AttachmentList attachments={item.attachments} /> : null}
      </Space>
    );

  if (!copyable && !canSaveToNote) {
    return content;
  }
  return (
    <div className="message-copy-frame">
      {content}
      <Space className="message-action-buttons" size={2}>
        {canSaveToNote ? (
          <Tooltip title={saveLabel}>
            <Button
              aria-label={saveLabel}
              icon={<SaveOutlined />}
              loading={savingToNote}
              onClick={(event) => {
                event.stopPropagation();
                onSaveToNote?.(item);
              }}
              shape="circle"
              size="small"
              type="text"
            />
          </Tooltip>
        ) : null}
        {copyable ? (
          <Tooltip title={copyLabel}>
            <Button
              aria-label={copyLabel}
              icon={<CopyOutlined />}
              onClick={(event) => {
                event.stopPropagation();
                onCopy();
              }}
              shape="circle"
              size="small"
              type="text"
            />
          </Tooltip>
        ) : null}
      </Space>
    </div>
  );
}

function copyTextForItem(item: FeedItem) {
  if (item.kind === "tool") {
    const parts = [
      item.title ? `Tool: ${item.title}` : "",
      item.status ? `Status: ${item.status}` : "",
      item.input ? `Input:\n${JSON.stringify(item.input, null, 2)}` : "",
      item.output ? `Output:\n${item.output}` : "",
      item.error ? `Error:\n${item.error}` : "",
      !item.input && !item.output && !item.error && item.summary ? item.summary : "",
    ].filter(Boolean);
    return parts.join("\n\n").trim();
  }
  if (item.kind === "todo") {
    return item.body;
  }
  if (item.kind === "subagent") {
    const parts = [
      item.title ? `Subagent: ${item.title}` : "",
      item.jobId ? `Job: ${item.jobId}` : "",
      item.status ? `Status: ${item.status}` : "",
      item.phase ? `Phase: ${item.phase}` : "",
      item.body ? `Latest:\n${item.body}` : "",
      item.error ? `Error:\n${item.error}` : "",
      item.progress?.length ? `Progress:\n${item.progress.map((entry) => [entry.timestamp, entry.phase, entry.status, entry.toolName, entry.message || entry.error || entry.result].filter(Boolean).join(" · ")).join("\n")}` : "",
    ].filter(Boolean);
    return parts.join("\n\n").trim();
  }
  const attachments = item.attachments?.map((attachment) => attachment.name || attachment.path || attachment.url).filter(Boolean) ?? [];
  return [item.body, attachments.length ? attachments.join("\n") : ""].filter(Boolean).join("\n\n").trim();
}

function renderHeader(item: FeedItem) {
  const color =
    item.kind === "error" ? "red" : item.kind === "warning" ? "gold" : item.kind === "background" ? "blue" : undefined;
  return (
    <Space size={8} wrap>
      <Typography.Text strong>{item.title}</Typography.Text>
      {item.status ? <Tag color={item.status === "failed" ? "red" : item.status === "running" ? "processing" : "default"}>{item.status}</Tag> : null}
      {color ? <Tag color={color}>{item.kind}</Tag> : null}
    </Space>
  );
}

function renderAvatar(item: FeedItem) {
  if (item.kind === "user") {
    return <Avatar icon={<UserOutlined />} style={{ background: "#0f766e" }} />;
  }
  if (item.kind === "tool") {
    return <Avatar icon={<ToolOutlined />} />;
  }
  if (item.kind === "todo") {
    return <Avatar icon={<CheckSquareOutlined />} style={{ background: "#0f766e" }} />;
  }
  if (item.kind === "subagent") {
    return <Avatar icon={<RobotOutlined />} style={{ background: "#475569" }} />;
  }
  if (item.kind === "warning" || item.kind === "error") {
    return <Avatar icon={<WarningOutlined />} style={{ background: item.kind === "error" ? "#b42318" : "#b45309" }} />;
  }
  return <Avatar icon={<RobotOutlined />} />;
}
