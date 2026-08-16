import { useMemo } from "react";
import { Drawer, Tabs, Descriptions, Collapse, Tag, Typography, Space } from "antd";
import type { SessionTimelineEntry } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import {
  timelineEventLabel,
  formatTimelineTime,
  shortTurnId,
  stringFromPayload,
  previewText,
  subagentTimelineSummary,
  modelRequestTimelineSummary,
} from "../../../lib/timelineUtils";
import { MarkdownRenderer } from "../../../components/MarkdownRenderer";

function jsonText(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value ?? "");
  }
}

function payloadOf(event: SessionTimelineEntry): Record<string, unknown> {
  return (event.payload ?? {}) as Record<string, unknown>;
}

function JsonBlock({ value }: { value: unknown }) {
  const text = useMemo(() => jsonText(value), [value]);
  return (
    <pre
      style={{
        margin: 0,
        padding: 12,
        background: "rgba(0,0,0,0.03)",
        borderRadius: 8,
        maxHeight: 420,
        overflow: "auto",
        fontSize: 12,
        lineHeight: 1.5,
        whiteSpace: "pre-wrap",
        wordBreak: "break-word",
      }}
    >
      {text}
    </pre>
  );
}

function DetailField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Space direction="vertical" size={4} style={{ width: "100%" }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {label}
      </Typography.Text>
      {children}
    </Space>
  );
}

function ToolCallDetails({ payload }: { payload: Record<string, unknown> }) {
  const input = payload.input;
  const output = stringFromPayload(payload.output);
  const error = stringFromPayload(payload.error);
  const durationMs = Number(payload.duration_ms ?? 0);
  const artifactPaths = Array.isArray(payload.artifact_paths) ? payload.artifact_paths.map(String) : [];
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <DetailField label="Name">
        <Typography.Text strong>{stringFromPayload(payload.name) || "-"}</Typography.Text>
      </DetailField>
      {input !== undefined ? (
        <DetailField label="Input (arguments)">
          <JsonBlock value={input} />
        </DetailField>
      ) : null}
      {output ? (
        <DetailField label="Output">
          <pre
            style={{
              margin: 0,
              padding: 12,
              background: "rgba(0,0,0,0.03)",
              borderRadius: 8,
              maxHeight: 420,
              overflow: "auto",
              fontSize: 12,
              lineHeight: 1.5,
              whiteSpace: "pre-wrap",
              wordBreak: "break-word",
            }}
          >
            {output}
          </pre>
        </DetailField>
      ) : null}
      {error ? (
        <DetailField label="Error">
          <Typography.Text type="danger">{error}</Typography.Text>
        </DetailField>
      ) : null}
      {durationMs > 0 ? (
        <DetailField label="Duration">
          <Typography.Text>{(durationMs / 1000).toFixed(1)} s</Typography.Text>
        </DetailField>
      ) : null}
      {payload.timed_out ? (
        <DetailField label="Timed out">
          <Tag color="orange">yes</Tag>
        </DetailField>
      ) : null}
      {artifactPaths.length > 0 ? (
        <DetailField label="Artifacts">
          {artifactPaths.map((path) => (
            <Typography.Text key={path} code>
              {path}
            </Typography.Text>
          ))}
        </DetailField>
      ) : null}
      {stringFromPayload(payload.recovery_hint) ? (
        <DetailField label="Recovery hint">
          <Typography.Text type="secondary">{stringFromPayload(payload.recovery_hint)}</Typography.Text>
        </DetailField>
      ) : null}
    </Space>
  );
}

function AssistantDetails({ payload }: { payload: Record<string, unknown> }) {
  const text = stringFromPayload(payload.text);
  const thinking = stringFromPayload(payload.thinking);
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      {text ? (
        <DetailField label="Answer">
          <div style={{ maxHeight: 420, overflow: "auto" }}>
            <MarkdownRenderer content={text} />
          </div>
        </DetailField>
      ) : null}
      {thinking ? (
        <Collapse
          size="small"
          items={[
            {
              key: "thinking",
              label: `Thinking (${previewText(thinking).length} chars)`,
              children: (
                <div style={{ maxHeight: 420, overflow: "auto", whiteSpace: "pre-wrap" }}>
                  <MarkdownRenderer content={thinking} />
                </div>
              ),
            },
          ]}
        />
      ) : null}
      {!text && !thinking ? <Typography.Text type="secondary">Empty assistant message.</Typography.Text> : null}
    </Space>
  );
}

function ModelRequestDetails({ payload }: { payload: Record<string, unknown> }) {
  const model = stringFromPayload(payload.model);
  const input = Number(payload.input_tokens ?? 0);
  const output = Number(payload.output_tokens ?? 0);
  const cacheRead = Number(payload.cache_read_tokens ?? 0);
  const cacheWrite = Number(payload.cache_write_tokens ?? 0);
  const durationMs = Number(payload.duration_ms ?? 0);
  const ttftMs = Number(payload.ttft_ms ?? 0);
  const hitRate = input + cacheRead > 0 ? Math.round((cacheRead / (input + cacheRead)) * 100) : 0;
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <Descriptions size="small" column={1} bordered>
        <Descriptions.Item label="Model">{model || "-"}</Descriptions.Item>
        <Descriptions.Item label="Input tokens">
          {input.toLocaleString()} uncached + {cacheRead.toLocaleString()} cached ({hitRate}% hit)
        </Descriptions.Item>
        <Descriptions.Item label="Output tokens">{output.toLocaleString()}</Descriptions.Item>
        <Descriptions.Item label="Cache write tokens">{cacheWrite.toLocaleString()}</Descriptions.Item>
        <Descriptions.Item label="TTFT">{ttftMs > 0 ? `${Math.round(ttftMs).toLocaleString()} ms` : "-"}</Descriptions.Item>
        <Descriptions.Item label="Total duration">{durationMs > 0 ? `${(durationMs / 1000).toFixed(1)} s` : "-"}</Descriptions.Item>
        <Descriptions.Item label="Stop reason">{stringFromPayload(payload.stop_reason) || "-"}</Descriptions.Item>
      </Descriptions>
      {stringFromPayload(payload.error) ? (
        <DetailField label="Error">
          <Typography.Text type="danger">{stringFromPayload(payload.error)}</Typography.Text>
        </DetailField>
      ) : null}
    </Space>
  );
}

function NoticeDetails({ payload }: { payload: Record<string, unknown> }) {
  const message = stringFromPayload(payload.message);
  const code = stringFromPayload(payload.code);
  const recoveryHint = stringFromPayload(payload.recovery_hint);
  const actor = [stringFromPayload(payload.actor_kind), stringFromPayload(payload.actor_id)].filter(Boolean).join(" ");
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      {message ? (
        <DetailField label="Message">
          <Typography.Text>{message}</Typography.Text>
        </DetailField>
      ) : null}
      {code ? (
        <DetailField label="Code">
          <Tag>{code}</Tag>
        </DetailField>
      ) : null}
      {actor ? (
        <DetailField label="Actor">
          <Typography.Text>{actor}</Typography.Text>
        </DetailField>
      ) : null}
      {recoveryHint ? (
        <DetailField label="Recovery hint">
          <Typography.Text type="secondary">{recoveryHint}</Typography.Text>
        </DetailField>
      ) : null}
    </Space>
  );
}

export function EventDetailPanel({
  event,
  onClose,
}: {
  event: SessionTimelineEntry | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const payload = event ? payloadOf(event) : {};
  const summary = event ? (event.type === "model_request_completed" ? modelRequestTimelineSummary(payload) : event.type === "subagent_job_updated" ? subagentTimelineSummary(payload) : previewText(stringFromPayload(payload.text)) || previewText(stringFromPayload(payload.message)) || "") : "";
  const turnId = stringFromPayload(event?.turn_id);
  const sessionId = stringFromPayload(event?.session_id);

  const summaryTab = useMemo(() => {
    if (!event) return null;
    switch (event.type) {
      case "tool_call_started":
      case "tool_call_finished":
        return <ToolCallDetails payload={payload} />;
      case "assistant_message_completed":
        return <AssistantDetails payload={payload} />;
      case "model_request_completed":
        return <ModelRequestDetails payload={payload} />;
      case "warning_raised":
      case "error_raised":
        return <NoticeDetails payload={payload} />;
      case "user_message_accepted":
        return (
          <DetailField label="Message">
            <div style={{ maxHeight: 420, overflow: "auto" }}>
              <MarkdownRenderer content={stringFromPayload(payload.text) || "-"} />
            </div>
          </DetailField>
        );
      case "subagent_job_updated":
        return <JsonBlock value={payload} />;
      default:
        return <JsonBlock value={payload} />;
    }
  }, [event, payload]);

  if (!event) {
    return null;
  }

  return (
    <Drawer
      title={
        <Space wrap size={6}>
          <Typography.Text strong>{timelineEventLabel(event)}</Typography.Text>
          <Tag>{event.type}</Tag>
          {turnId ? <Tag color="blue">{shortTurnId(turnId)}</Tag> : null}
        </Space>
      }
      width={520}
      open={true}
      onClose={onClose}
      destroyOnClose
      extra={
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {formatTimelineTime(event.timestamp)}
        </Typography.Text>
      }
    >
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        {sessionId ? (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            session: {sessionId}
          </Typography.Text>
        ) : null}
        {summary ? (
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }} ellipsis={{ rows: 2, expandable: true, symbol: "more" }}>
            {summary}
          </Typography.Paragraph>
        ) : null}
        <Tabs
          size="small"
          items={[
            {
              key: "summary",
              label: t("chat.timelineDetailSummary") || "Summary",
              children: summaryTab,
            },
            {
              key: "raw",
              label: t("chat.timelineDetailRaw") || "Raw payload",
              children: <JsonBlock value={event.payload ?? {}} />,
            },
          ]}
        />
      </Space>
    </Drawer>
  );
}
