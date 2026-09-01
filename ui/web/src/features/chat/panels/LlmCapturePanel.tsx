import { useCallback, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Empty, List, Space, Spin, Switch, Tag, Tooltip, Typography, App as AntApp } from "antd";
import { BugOutlined, ClearOutlined, CopyOutlined, ReloadOutlined, EyeOutlined, ArrowLeftOutlined } from "@ant-design/icons";
import { useI18n } from "../../../i18n";
import {
  getLlmCaptureStatus,
  setLlmCaptureEnabled,
  listLlmCaptureRecords,
  getLlmCaptureRecord,
  clearLlmCaptureRecords,
} from "../../../lib/api";
import type { LlmCaptureRecord, LlmCaptureSummary } from "../../../lib/types";
import { formatTimelineTime } from "../../../lib/timelineUtils";
import { writeClipboardText } from "../../../lib/clipboard";
import CodeEditor from "../../files/CodeEditor";

export function prettyJSON(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value ?? "");
  }
}

function CapturedJsonViewer({ value }: { value: unknown }) {
  return (
    <div style={{ marginTop: 4, border: "1px solid var(--godex-border)", borderRadius: 8, overflow: "hidden", height: 360 }}>
      <CodeEditor content={prettyJSON(value)} filePath="capture.json" readOnly />
    </div>
  );
}

/** Self-contained panel hosted inside the Status dock (InspectorTabs). Detail
 *  views are shown inline with a back button — no full-screen Drawer, so the
 *  mobile mask / back-to-chat behaviour of the dock is preserved. */
export function LlmCapturePanel({ token }: { token: string | null }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const statusQuery = useQuery({
    queryKey: ["llm-capture", "status", token],
    queryFn: () => getLlmCaptureStatus(token),
    refetchInterval: 5_000,
  });

  const recordsQuery = useQuery({
    queryKey: ["llm-capture", "records", token],
    queryFn: () => listLlmCaptureRecords(token, 100),
    refetchInterval: 3_000,
  });

  const detailQuery = useQuery({
    queryKey: ["llm-capture", "record", token, selectedId],
    queryFn: () => (selectedId ? getLlmCaptureRecord(token, selectedId) : Promise.reject(new Error("no id"))),
    enabled: !!selectedId,
  });

  const toggleMutation = useMutation({
    mutationFn: (enabled: boolean) => setLlmCaptureEnabled(token, enabled),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["llm-capture"] });
    },
    onError: (err: Error) => {
      void message.error(`${t("chat.llmCaptureToggleFailed")}: ${err.message}`);
    },
  });

  const clearMutation = useMutation({
    mutationFn: () => clearLlmCaptureRecords(token),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["llm-capture", "records"] });
    },
  });

  const copyJSON = useCallback(
    (value: unknown, label: string) => {
      void writeClipboardText(prettyJSON(value)).then(() => message.success(`${label} ${t("chat.copied")}`));
    },
    [message, t],
  );

  const copyRecord = useCallback(
    (rec: LlmCaptureSummary) => {
      getLlmCaptureRecord(token, rec.id)
        .then((full) => copyJSON(full, t("chat.llmCaptureTitle")))
        .catch((err: Error) => void message.error(err.message));
    },
    [token, copyJSON, message, t],
  );

  const enabled = statusQuery.data?.enabled ?? false;
  const records = recordsQuery.data ?? [];

  // ---- Detail view (inline, with back) ----
  if (selectedId) {
    const record: LlmCaptureRecord | undefined = detailQuery.data;
    return (
      <div style={{ padding: "8px 12px 16px", display: "flex", flexDirection: "column", gap: 12 }}>
        <Space size={6}>
          <Button size="small" icon={<ArrowLeftOutlined />} onClick={() => setSelectedId(null)}>
            {t("chat.llmCaptureBack")}
          </Button>
          <Typography.Text strong style={{ fontSize: 13 }}>
            {t("chat.llmCaptureDetailTitle")}
          </Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }} ellipsis>
            {selectedId}
          </Typography.Text>
        </Space>
        {detailQuery.isLoading ? (
          <div style={{ textAlign: "center", padding: "24px 0" }}>
            <Spin size="small" />
          </div>
        ) : detailQuery.isError ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={(detailQuery.error as Error)?.message ?? "error"} />
        ) : record ? (
          <>
            <Space wrap size={6}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {formatTimelineTime(record.timestamp)}
              </Typography.Text>
              {record.model ? <Tag color="blue">{record.model}</Tag> : null}
              {record.stream ? <Tag>stream</Tag> : null}
              {record.error ? <Tag color="red">{record.error}</Tag> : null}
              {record.latency_ms ? <Tag>{record.latency_ms} ms</Tag> : null}
            </Space>
            <div>
              <Space size={4} style={{ width: "100%", justifyContent: "space-between" }}>
                <Typography.Text strong>{t("chat.llmCaptureRequest")}</Typography.Text>
                <Button size="small" icon={<CopyOutlined />} onClick={() => copyJSON(record.request, t("chat.llmCaptureRequest"))}>
                  {t("chat.copy")}
                </Button>
              </Space>
              <CapturedJsonViewer value={record.request} />
            </div>
            {record.response !== undefined ? (
              <div>
                <Space size={4} style={{ width: "100%", justifyContent: "space-between" }}>
                  <Typography.Text strong>{t("chat.llmCaptureResponse")}</Typography.Text>
                  <Button size="small" icon={<CopyOutlined />} onClick={() => copyJSON(record.response, t("chat.llmCaptureResponse"))}>
                    {t("chat.copy")}
                  </Button>
                </Space>
                <CapturedJsonViewer value={record.response} />
              </div>
            ) : null}
          </>
        ) : null}
      </div>
    );
  }

  // ---- List view ----
  return (
    <div style={{ padding: "8px 12px", display: "flex", flexDirection: "column", gap: 12 }}>
      <Space style={{ justifyContent: "space-between", width: "100%" }} wrap>
        <Space size={6}>
          <BugOutlined style={{ color: enabled ? "#fa541c" : "inherit" }} />
          <Typography.Text strong style={{ fontSize: 13 }}>
            {t("chat.llmCaptureTitle")}
          </Typography.Text>
        </Space>
        <Space size={4}>
          <Tooltip title={t("chat.llmCaptureClear")}>
            <Button size="small" icon={<ClearOutlined />} disabled={records.length === 0} loading={clearMutation.isPending} onClick={() => void clearMutation.mutate()} />
          </Tooltip>
          <Tooltip title={t("chat.llmCaptureRefresh")}>
            <Button size="small" icon={<ReloadOutlined />} loading={recordsQuery.isFetching} onClick={() => void queryClient.invalidateQueries({ queryKey: ["llm-capture", "records"] })} />
          </Tooltip>
          <Switch
            checked={enabled}
            loading={toggleMutation.isPending}
            checkedChildren={t("chat.llmCaptureOn")}
            unCheckedChildren={t("chat.llmCaptureOff")}
            onChange={(checked) => toggleMutation.mutate(checked)}
          />
        </Space>
      </Space>

      <div>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {enabled
            ? t("chat.llmCaptureDumpTo", { path: statusQuery.data?.dump_path ?? "" })
            : t("chat.llmCaptureDisabledHint")}
        </Typography.Text>
      </div>

      <div style={{ maxHeight: "min(60vh, 480px)", overflowY: "auto" }}>
        {recordsQuery.isLoading && records.length === 0 ? (
          <div style={{ textAlign: "center", padding: "24px 0" }}>
            <Spin size="small" />
          </div>
        ) : records.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.llmCaptureEmpty")} />
        ) : (
          <List
            size="small"
            dataSource={records}
            renderItem={(rec) => (
              <List.Item
                actions={[
                  <Tooltip key="copy" title={t("chat.llmCaptureCopy")}>
                    <Button type="text" size="small" icon={<CopyOutlined />} onClick={() => copyRecord(rec)} />
                  </Tooltip>,
                  <Tooltip key="view" title={t("chat.llmCaptureView")}>
                    <Button type="text" size="small" icon={<EyeOutlined />} onClick={() => setSelectedId(rec.id)} />
                  </Tooltip>,
                ]}
              >
                <Space direction="vertical" size={2} style={{ width: "100%", minWidth: 0 }}>
                  <Space wrap size={6}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {formatTimelineTime(rec.timestamp)}
                    </Typography.Text>
                    {rec.model ? (
                      <Tag color="blue" style={{ marginInlineEnd: 0 }}>
                        {rec.model}
                      </Tag>
                    ) : null}
                    {rec.stream ? <Tag style={{ marginInlineEnd: 0 }}>stream</Tag> : null}
                    {rec.input_tokens > 0 ? (
                      <Tag style={{ marginInlineEnd: 0 }}>{rec.input_tokens} tok</Tag>
                    ) : null}
                    {rec.error ? <Tag color="red" style={{ marginInlineEnd: 0 }}>err</Tag> : null}
                  </Space>
                  <Typography.Text type="secondary" ellipsis style={{ fontSize: 12 }}>
                    {rec.session_id ? `session: ${rec.session_id}` : ""}
                    {rec.session_id && rec.turn_id ? " · " : ""}
                    {rec.turn_id ? `turn: ${rec.turn_id}` : ""}
                  </Typography.Text>
                </Space>
              </List.Item>
            )}
          />
        )}
      </div>
    </div>
  );
}
