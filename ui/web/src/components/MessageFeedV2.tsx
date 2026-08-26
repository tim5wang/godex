import { App as AntApp, Avatar, Button, Empty, Space, Tag, Tooltip, Typography } from "antd";
import { Fragment, useRef, useState } from "react";
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
  SoundOutlined,
  UserOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import { Bubble, type BubbleItemType } from "@ant-design/x";
import { AttachmentList } from "./AttachmentList";
import { ChangesCard } from "./ChangesCard";
import { MarkdownContent } from "./MarkdownContent";
import { SubagentCard } from "./SubagentCard";
import { TodoCard } from "./TodoCard";
import { ToolDetails } from "./ToolDetails";
import { UiCardView, type UiCardData } from "../features/workflows/components/UiCardView";
import { useI18n } from "../i18n";
import { writeClipboardText } from "../lib/clipboard";
import { createPCMPlayer, type PCMPlayer } from "../lib/ttsPlayback";
import type { FeedItem, FeedSegment } from "../lib/types";

interface MessageFeedV2Props {
  items: FeedItem[];
  onToggleTool: (id: string) => void;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote?: boolean;
  hasNoteContext?: boolean;
  /** Session workspace root — enables the “Changed files” card + git diff. */
  workspaceDir?: string;
  /** Web token for authenticated git/file requests. */
  token?: string | null;
  /** Opens a changed file in the Files dock panel. */
  onOpenInFiles?: (path: string) => void;
  /** Submit a ui_card interaction value back to the running session. */
  onSubmitCard?: (value: string) => void;
  /** 语音已启用（meta.voice_enabled），控制消息 TTS 播放按钮显隐。 */
  voiceEnabled?: boolean;
}

/**
 * Chat V2 message feed: assistant turns are pre-grouped (see
 * groupFeedItemsIntoTurns) so a single bubble interleaves text, compact tool
 * rows and todo cards in chronological order. Tool calls render as single-line
 * rows that expand in place, keeping the conversation scannable.
 */
export function MessageFeedV2({ items, onToggleTool, onSaveToNote, savingToNote = false, hasNoteContext = false, workspaceDir, token, onOpenInFiles, onSubmitCard, voiceEnabled = false }: MessageFeedV2Props) {
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
        workspaceDir={workspaceDir}
        token={token}
        onOpenInFiles={onOpenInFiles}
        onSubmitCard={onSubmitCard}
        voiceEnabled={voiceEnabled}
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
  workspaceDir,
  token,
  onOpenInFiles,
  onSubmitCard,
  voiceEnabled,
}: {
  item: FeedItem;
  onToggleTool: (id: string) => void;
  onCopy: () => void;
  copyLabel: string;
  saveLabel: string;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote: boolean;
  workspaceDir?: string;
  token?: string | null;
  onOpenInFiles?: (path: string) => void;
  onSubmitCard?: (value: string) => void;
  voiceEnabled?: boolean;
}) {
  const { t } = useI18n();
  // Grouped assistant turn: render ordered segments, each block separated by a divider.
  if (item.segments?.length) {
    const visible = item.segments.filter((segment) => segment.type !== "text" || Boolean(segment.text?.trim()));
    return (
      <div className="chat-feed-v2-turn">
        {visible.map((segment, index) => (
          <Fragment key={segmentKey(segment, index)}>
            {shouldShowTurnDivider(visible, index) ? <hr className="chat-feed-v2-divider" /> : null}
            <TurnSegment segment={segment} onToggleTool={onToggleTool} onSubmitCard={onSubmitCard} />
          </Fragment>
        ))}
        {item.attachments?.length ? <AttachmentList attachments={item.attachments} /> : null}
        <ChangesCard segments={item.segments} workspaceDir={workspaceDir} token={token} onOpenInFiles={onOpenInFiles} />
        <TurnActions item={item} onCopy={onCopy} copyLabel={copyLabel} saveLabel={saveLabel} onSaveToNote={onSaveToNote} savingToNote={savingToNote} token={token} voiceEnabled={voiceEnabled} />
      </div>
    );
  }

  if (item.kind === "tool") {
    const card = parseUiCardOutput(item);
    if (card && onSubmitCard) {
      return (
        <div className="chat-feed-v2-uicard">
          <UiCardView card={card} onSubmitCard={onSubmitCard} />
        </div>
      );
    }
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
  const inFlight = item.status === "sending" || item.status === "running";
  return (
    <div className="message-copy-frame chat-feed-v2-plain">
      <Space direction="vertical" size={10} style={{ width: "100%" }}>
        {item.body ? <MarkdownContent content={item.body} forceMarkdown={item.kind === "user"} /> : null}
        {item.attachments?.length ? <AttachmentList attachments={item.attachments} /> : null}
        {inFlight ? (
          <Space size={6}>
            <LoadingOutlined spin />
            <Typography.Text type="secondary">
              {item.kind === "user" ? t("chat.sendingMessage") : t("chat.runningCommand", { name: item.title })}
            </Typography.Text>
          </Space>
        ) : null}
      </Space>
      {copyable || canSaveToNote ? (
        <Space className="message-action-buttons" size={2}>
          {voiceEnabled && item.kind === "assistant" ? <TTSPlayButton text={item.body} token={token} /> : null}
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

function TurnSegment({ segment, onToggleTool, onSubmitCard }: { segment: FeedSegment; onToggleTool: (id: string) => void; onSubmitCard?: (value: string) => void }) {
  if (segment.type === "text") {
    return segment.text?.trim() ? (
      <div className="chat-feed-v2-text">
        <MarkdownContent content={segment.text} />
      </div>
    ) : null;
  }
  if (segment.type === "tool" && segment.item) {
    const card = parseUiCardOutput(segment.item);
    if (card && onSubmitCard) {
      return (
        <div className="chat-feed-v2-uicard">
          <UiCardView card={card} onSubmitCard={onSubmitCard} />
        </div>
      );
    }
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

/**
 * Parse a ui_card tool item's structured output (the tool echoes its card
 * JSON as the output string). Returns null when the item isn't a ui_card call
 * or the output isn't a valid card.
 */
function parseUiCardOutput(item: FeedItem): UiCardData | null {
  if (item.title !== "ui_card" || typeof item.output !== "string" || !item.output.trim()) {
    return null;
  }
  try {
    const card = JSON.parse(item.output) as UiCardData;
    if (card && typeof card === "object" && (card.kind === "form" || card.kind === "button_group" || card.kind === "card")) {
      return card;
    }
  } catch {
    // not a card payload
  }
  return null;
}

/**
 * Decide whether a divider belongs BEFORE the segment at `index` within an
 * assistant turn. Only one divider is wanted: between the leading "process"
 * (thinking + tool calls + todos) and the final answer. The final answer is
 * the last text segment; every other boundary stays un-divided so consecutive
 * tool rows don't each carry a horizontal rule.
 */
export function shouldShowTurnDivider(segments: FeedSegment[], index: number): boolean {
  if (index <= 0 || index >= segments.length) {
    return false;
  }
  const segment = segments[index];
  if (segment.type !== "text" || !segment.text?.trim()) {
    return false;
  }
  // Only the LAST text segment is the final answer.
  const isFinalText = !segments.slice(index + 1).some((s) => s.type === "text" && Boolean(s.text?.trim()));
  if (!isFinalText) {
    return false;
  }
  // Need some preceding process/content to separate from.
  return segments.slice(0, index).some((s) => (s.type === "text" ? Boolean(s.text?.trim()) : true));
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
        <ToolDetails item={item} />
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
  token,
  voiceEnabled,
}: {
  item: FeedItem;
  onCopy: () => void;
  copyLabel: string;
  saveLabel: string;
  onSaveToNote?: (item: FeedItem) => void;
  savingToNote: boolean;
  token?: string | null;
  voiceEnabled?: boolean;
}) {
  // Copy / save act only on the final result text, not the process.
  const hasResult = Boolean(item.finalBody?.trim());
  if (!hasResult) {
    return null;
  }
  const canSaveToNote = Boolean(onSaveToNote);
  return (
    <div className="chat-feed-v2-turn-actions">
      {voiceEnabled && hasResult ? <TTSPlayButton text={item.finalBody ?? ""} token={token} /> : null}
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

function TTSPlayButton({ text, token, voiceEnabled = true }: { text: string; token?: string | null; voiceEnabled?: boolean }) {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const [playing, setPlaying] = useState(false);
  const playerRef = useRef<PCMPlayer | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const doneRef = useRef(false); // 本次播放是否已收尾（不再入队新帧）
  const pendingRef = useRef(0); // 尚未 enqueue 的 PCM 帧计数
  const playIdRef = useRef(0); // 播放代次：防旧代次异步帧串入新播放器

  const stop = () => {
    playIdRef.current += 1;
    doneRef.current = true;
    wsRef.current?.close();
    wsRef.current = null;
    playerRef.current?.close();
    playerRef.current = null;
    pendingRef.current = 0;
    setPlaying(false);
  };

  const play = () => {
    const trimmed = text.trim();
    if (playing || !trimmed || !voiceEnabled) return;
    const playId = ++playIdRef.current;
    setPlaying(true);
    doneRef.current = false;
    pendingRef.current = 0;
    // 流式端点：WS 发文本 → 收 PCM 帧边生成边播（首帧即播）→ tts_done 收尾。
    const base = window.location.origin.replace(/^http/, "ws");
    const params = new URLSearchParams();
    if (token) params.set("token", token);
    const ws = new WebSocket(`${base}/v1/tts/stream?${params.toString()}`);
    wsRef.current = ws;

    const enqueueFrame = (buf: ArrayBuffer) => {
      pendingRef.current -= 1;
      // 旧代次（已 stop/重新播放）的残留帧 → 丢弃，不重建播放器。
      if (playIdRef.current !== playId) return;
      // 已收尾且播放器已释放 → 丢弃残留帧。
      if (doneRef.current && !playerRef.current) return;
      if (!playerRef.current) {
        playerRef.current = createPCMPlayer(() => {
          playerRef.current = null;
          setPlaying(false);
        });
      }
      playerRef.current?.enqueue(new Uint8Array(buf));
      // 正常收尾且所有已接收帧都已入队 → end()（播完自动释放并复位状态）。
      if (doneRef.current && pendingRef.current <= 0) {
        playerRef.current?.end();
      }
    };

    const finish = (immediate: boolean) => {
      if (doneRef.current) return;
      doneRef.current = true;
      ws.close();
      wsRef.current = null;
      if (immediate) {
        // 错误/手动停止：丢弃排队音频。
        playerRef.current?.close();
        playerRef.current = null;
        pendingRef.current = 0;
        setPlaying(false);
      } else if (pendingRef.current <= 0) {
        // 正常收尾：帧已全部入队 → end() 播完自动复位；无帧 → 直接复位。
        if (playerRef.current) {
          playerRef.current.end();
        } else {
          setPlaying(false);
        }
      }
      // pendingRef > 0：等最后一帧 enqueueFrame 里触发 end()（避免尾部帧被拒）。
    };

    ws.onopen = () => {
      ws.send(JSON.stringify({ text: trimmed }));
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") {
        // 下行 PCM（binary）→ 共享播放器排队播放（首帧即播）。
        pendingRef.current += 1;
        void ev.data.arrayBuffer().then(enqueueFrame);
        return;
      }
      let msg: { type: string };
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (msg.type === "tts_done") {
        finish(false);
      } else if (msg.type === "error") {
        void message.error(t("chat.playSpeechFailed"));
        finish(true);
      }
    };
    ws.onerror = () => {
      void message.error(t("chat.playSpeechFailed"));
      finish(true);
    };
    ws.onclose = () => {
      // 未收到 tts_done 的连接关闭（如网络中断）→ 按正常收尾处理，别砍掉已排队音频。
      finish(false);
    };
  };

  return (
    <Tooltip title={t("chat.speakMessage")}>
      <Button
        aria-label={t("chat.speakMessage")}
        icon={playing ? <LoadingOutlined /> : <SoundOutlined />}
        onClick={(event) => {
          event.stopPropagation();
          if (playing) {
            stop();
          } else {
            play();
          }
        }}
        shape="circle"
        size="small"
        type="text"
      />
    </Tooltip>
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
