import { App as AntApp, Avatar, Button, Empty, Space, Tag, Tooltip, Typography } from "antd";
import { Fragment } from "react";
import {
  CheckCircleFilled,
  CheckSquareOutlined,
  CloseCircleFilled,
  CopyOutlined,
  DownOutlined,
  LoadingOutlined,
  RightOutlined,
  RobotOutlined,
  SaveOutlined,
  UserOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import { Bubble, type BubbleItemType } from "@ant-design/x";
import { AttachmentList } from "./AttachmentList";
import { MarkdownContent } from "./MarkdownContent";
import { SubagentCard } from "./SubagentCard";
import { TodoCard } from "./TodoCard";
import { useI18n } from "../i18n";
import { writeClipboardText } from "../lib/clipboard";
import type { FeedItem, FeedSegment } from "../lib/types";

interface MessageFeedV2Props {
  items: FeedItem[];
  onToggleTool: (id: string) => void;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote?: boolean;
  hasNoteContext?: boolean;
}

/**
 * Chat V2 message feed: assistant turns are pre-grouped (see
 * groupFeedItemsIntoTurns) so a single bubble interleaves text, compact tool
 * rows and todo cards in chronological order. Tool calls render as single-line
 * rows that expand in place, keeping the conversation scannable.
 */
export function MessageFeedV2({ items, onToggleTool, onSaveToNote, savingToNote = false, hasNoteContext = false }: MessageFeedV2Props) {
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
    role: item.kind === "user" ? "user" : item.kind === "subagent" || item.kind === "todo" || item.kind === "tool" ? "tool" : "ai",
    content: (
      <FeedItemBody
        item={item}
        onToggleTool={onToggleTool}
        onCopy={() => void copyItem(item)}
        copyLabel={t("chat.copyMessage")}
        saveLabel={hasNoteContext ? t("chat.saveToCurrentNote") : t("chat.saveAsNote")}
        onSaveToNote={onSaveToNote}
        savingToNote={savingToNote}
      />
    ),
    header: item.kind === "subagent" || item.kind === "todo" || item.kind === "tool" ? undefined : renderHeader(item),
    avatar: renderAvatar(item),
    rootClassName: `chat-feed-v2-bubble chat-feed-v2-bubble-${item.kind}${item.segments ? " chat-feed-v2-bubble-turn" : ""}`,
    variant: item.kind === "user" ? "filled" : "borderless",
    shape: "corner",
  }));

  return (
    <Bubble.List
      className="chat-feed-v2"
      items={bubbleItems}
      autoScroll
      role={{
        user: { placement: "end" },
        ai: { placement: "start" },
        tool: { placement: "start", variant: "borderless" },
        system: { placement: "start" },
      }}
    />
  );
}

function FeedItemBody({
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
  // Grouped assistant turn: render ordered segments, each block separated by a divider.
  if (item.segments?.length) {
    const visible = item.segments.filter((segment) => segment.type !== "text" || Boolean(segment.text?.trim()));
    return (
      <div className="chat-feed-v2-turn">
        {visible.map((segment, index) => (
          <Fragment key={segmentKey(segment, index)}>
            {index > 0 ? <hr className="chat-feed-v2-divider" /> : null}
            <TurnSegment segment={segment} onToggleTool={onToggleTool} />
          </Fragment>
        ))}
        {item.attachments?.length ? <AttachmentList attachments={item.attachments} /> : null}
        <TurnActions item={item} onCopy={onCopy} copyLabel={copyLabel} saveLabel={saveLabel} onSaveToNote={onSaveToNote} savingToNote={savingToNote} />
      </div>
    );
  }

  if (item.kind === "tool") {
    return <ToolCallRow item={item} onToggle={() => onToggleTool(item.id)} />;
  }
  if (item.kind === "todo") {
    return <TodoCard item={item} />;
  }
  if (item.kind === "subagent") {
    return <SubagentCard item={item} onToggle={() => onToggleTool(item.id)} />;
  }

  const copyable = Boolean(copyTextForItem(item));
  const canSaveToNote = Boolean(onSaveToNote && item.kind === "assistant" && item.body.trim());
  return (
    <div className="message-copy-frame chat-feed-v2-plain">
      <Space direction="vertical" size={10} style={{ width: "100%" }}>
        {item.body ? <MarkdownContent content={item.body} forceMarkdown={item.kind === "user"} /> : null}
        {item.attachments?.length ? <AttachmentList attachments={item.attachments} /> : null}
      </Space>
      {copyable || canSaveToNote ? (
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
      ) : null}
    </div>
  );
}

function TurnSegment({ segment, onToggleTool }: { segment: FeedSegment; onToggleTool: (id: string) => void }) {
  if (segment.type === "text") {
    return segment.text?.trim() ? (
      <div className="chat-feed-v2-text">
        <MarkdownContent content={segment.text} />
      </div>
    ) : null;
  }
  if (segment.type === "tool" && segment.item) {
    return <ToolCallRow item={segment.item} onToggle={() => onToggleTool(segment.item!.id)} />;
  }
  if (segment.type === "todo" && segment.item) {
    return (
      <div className="chat-feed-v2-todo">
        <TodoCard item={segment.item} />
      </div>
    );
  }
  if (segment.type === "subagent" && segment.item) {
    return <SubagentCard item={segment.item} onToggle={() => onToggleTool(segment.item!.id)} />;
  }
  return null;
}

function segmentKey(segment: FeedSegment, index: number) {
  return segment.item?.id ?? `text-${index}`;
}

/** Compact single-line tool call row that expands in place (URL-link style). */
export function ToolCallRow({ item, onToggle }: { item: FeedItem; onToggle: () => void }) {
  const open = Boolean(item.expanded);
  const hasDetails = Boolean(item.input || item.output || item.error);
  return (
    <div className={`tool-call-row${open ? " tool-call-row-open" : ""}`} data-status={item.status || "finished"}>
      <button aria-expanded={open} className="tool-call-row-header" onClick={hasDetails ? onToggle : undefined} type="button">
        <ToolStatusIcon status={item.status} />
        <span className="tool-call-row-name">{item.title}</span>
        {item.summary ? <span className="tool-call-row-summary">{item.summary}</span> : null}
        {hasDetails ? <span className="tool-call-row-chevron">{open ? <DownOutlined /> : <RightOutlined />}</span> : null}
      </button>
      {open && hasDetails ? (
        <div className="tool-call-row-details">
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

function ToolStatusIcon({ status }: { status?: string }) {
  if (status === "running") {
    return <LoadingOutlined className="tool-call-row-status tool-call-row-status-running" />;
  }
  if (status === "failed") {
    return <CloseCircleFilled className="tool-call-row-status tool-call-row-status-failed" />;
  }
  return <CheckCircleFilled className="tool-call-row-status tool-call-row-status-finished" />;
}

function TurnActions({
  item,
  onCopy,
  copyLabel,
  saveLabel,
  onSaveToNote,
  savingToNote,
}: {
  item: FeedItem;
  onCopy: () => void;
  copyLabel: string;
  saveLabel: string;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote: boolean;
}) {
  // Copy / save act only on the final result text, not the process.
  const hasResult = Boolean(item.finalBody?.trim());
  if (!hasResult) {
    return null;
  }
  const canSaveToNote = Boolean(onSaveToNote);
  return (
    <div className="chat-feed-v2-turn-actions">
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
    </div>
  );
}

function copyTextForItem(item: FeedItem) {
  // Grouped turn: copy only the final result, not the process.
  if (item.segments?.length) {
    return (item.finalBody ?? "").trim();
  }
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
      item.body ? `Latest:\n${item.body}` : "",
      item.error ? `Error:\n${item.error}` : "",
    ].filter(Boolean);
    return parts.join("\n\n").trim();
  }
  const attachments = item.attachments?.map((attachment) => attachment.name || attachment.path || attachment.url).filter(Boolean) ?? [];
  return [item.body, attachments.length ? attachments.join("\n") : ""].filter(Boolean).join("\n\n").trim();
}

function renderHeader(item: FeedItem) {
  const color = item.kind === "error" ? "red" : item.kind === "warning" ? "gold" : item.kind === "background" ? "blue" : undefined;
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
